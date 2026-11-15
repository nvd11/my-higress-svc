#!/usr/bin/env bash
# ==============================================================================
# 网关部署后冒烟契约测试 (Post-Deploy Smoke Test)
# ==============================================================================
# 用途: 在 ArgoCD 完成同步后, 用外部视角验证网关的"硬契约"没有被破坏。
# 不碰集群、不读数据库 —— 只看客户端能看到的行为。
#
# 用法:
#   BASE_URL=https://higress.jppwl.asia SMOKE_API_KEY=sk-xxx ./scripts/smoke.sh
#
# 环境变量:
#   BASE_URL          必填, 网关根地址 (不带 /v1)
#   SMOKE_API_KEY     必填, 一把合法的 Virtual Key
#   EXPECTED_MODELS   可选, /v1/models 应返回的主名数量 (默认 11)
#   WAIT_ATTEMPTS     可选, 等待网关就绪的重试次数 (默认 6, 每次间隔 20s)
#
# 退出码: 0 = 全部通过; 非 0 = 有契约被破坏
# ==============================================================================
set -uo pipefail

: "${BASE_URL:?BASE_URL 未设置}"
: "${SMOKE_API_KEY:?SMOKE_API_KEY 未设置}"
EXPECTED_MODELS="${EXPECTED_MODELS:-11}"
WAIT_ATTEMPTS="${WAIT_ATTEMPTS:-6}"
WAIT_INTERVAL="${WAIT_INTERVAL:-20}"

pass=0
fail=0

ok()   { echo "  ✅ $*"; pass=$((pass + 1)); }
bad()  { echo "  ❌ $*"; fail=$((fail + 1)); }
note() { echo "::error::$*"; }

# 用状态码 + body 做断言 (通过全局变量回传, 避免子 shell 丢失状态)
req() { # method path body auth_header
  local method="$1" path="$2" body="${3:-}" auth="${4:-}"
  local out
  out="$(mktemp)"
  if [[ -n "$auth" ]]; then
    HTTP_CODE="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" \
      "${BASE_URL}${path}" -H "Content-Type: application/json" \
      -H "Authorization: ${auth}" ${body:+--data "$body"} 2>/dev/null || echo 000)"
  else
    HTTP_CODE="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" \
      "${BASE_URL}${path}" -H "Content-Type: application/json" \
      ${body:+--data "$body"} 2>/dev/null || echo 000)"
  fi
  BODY="$(cat "$out")"
  rm -f "$out"
}

echo "▶ 等待网关就绪: ${BASE_URL}"
ready=0
for i in $(seq 1 "$WAIT_ATTEMPTS"); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/v1/models" 2>/dev/null || echo 000)"
  if [[ "$code" != "000" ]]; then ready=1; break; fi
  echo "  第 ${i}/${WAIT_ATTEMPTS} 次未就绪 (code=${code}), ${WAIT_INTERVAL}s 后重试..."
  sleep "$WAIT_INTERVAL"
done
if [[ "$ready" != "1" ]]; then
  note "网关 ${BASE_URL} 在 $((WAIT_ATTEMPTS * WAIT_INTERVAL))s 内不可达"
  exit 1
fi

echo "▶ G1: 缺失 Key 必须在边缘 401 (上游调用次数应为 0)"
req POST /v1/chat/completions '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"ping"}]}'
if [[ "$HTTP_CODE" == "401" ]]; then ok "401 unauthorized"; else bad "期望 401, 实际 $HTTP_CODE: ${BODY:0:200}"; fi

echo "▶ G1-b: 伪造 Key 必须 401 且 code=invalid_api_key"
req POST /v1/chat/completions '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"ping"}]}' 'Bearer sk-forged-key-should-never-pass'
if [[ "$HTTP_CODE" == "401" && "$BODY" == *invalid_api_key* ]]; then
  ok "401 invalid_api_key"
else
  bad "期望 401 invalid_api_key, 实际 $HTTP_CODE: ${BODY:0:200}"
fi

echo "▶ G2: 未纳管模型必须在边缘 400 model_not_found"
req POST /v1/chat/completions '{"model":"definitely-not-a-model","messages":[{"role":"user","content":"ping"}]}' "Bearer ${SMOKE_API_KEY}"
if [[ "$HTTP_CODE" == "400" && "$BODY" == *model_not_found* ]]; then
  ok "400 model_not_found"
else
  bad "期望 400 model_not_found, 实际 $HTTP_CODE: ${BODY:0:200}"
fi

echo "▶ G5: /v1/models 只暴露 ${EXPECTED_MODELS} 个主名, 且不泄露别名"
req GET /v1/models '' "Bearer ${SMOKE_API_KEY}"
if [[ "$HTTP_CODE" != "200" ]]; then
  bad "期望 200, 实际 $HTTP_CODE"
else
  n="$(printf '%s' "$BODY" | jq -r '.data | length' 2>/dev/null || echo -1)"
  if [[ "$n" == "$EXPECTED_MODELS" ]]; then ok "data 长度 = ${n}"; else bad "data 长度 = ${n}, 期望 ${EXPECTED_MODELS}"; fi
  if printf '%s' "$BODY" | jq -r '.data[].id' 2>/dev/null | grep -qx 'kimi'; then
    bad "别名 kimi 出现在 /v1/models 中"
  else
    ok "别名未泄露"
  fi
fi

echo "▶ G6: 真实模型必须 200 且返回 usage"
req POST /v1/chat/completions '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"ping"}],"max_tokens":8}' "Bearer ${SMOKE_API_KEY}"
if [[ "$HTTP_CODE" == "200" ]]; then ok "200 正常推理"; else bad "期望 200, 实际 $HTTP_CODE: ${BODY:0:200}"; fi

echo
echo "──────── 结果: ${pass} 通过 / ${fail} 失败 ────────"
[[ "$fail" -eq 0 ]] || exit 1
