#!/usr/bin/env bash
# ==============================================================================
# 从 config.yaml 重新生成全部衍生配置 (唯一真源 → 4 份生成物)
# ==============================================================================
# 生成物:
#   plugins/finops-audit/pkg/finops/pricing_gen.go   价目表 (Wasm 编译期烧入)
#   plugins/ai-gatekeeper/models_gen.go              主名 / 别名 / 准入清单
#   mcp-bridge.yaml                                  上游 DNS registry
#   ingress.yaml                                     每 provider 一条精确路由
#
# 用法: bash scripts/gen-pricing.sh
# CI 会先跑本脚本, 再 git diff --exit-code 校验产物是否已提交。
# ==============================================================================
set -euo pipefail

cd "$(dirname "$0")/../plugins/finops-audit"

exec env CGO_ENABLED=0 go run internal/pricinggen/main.go \
  -config ../../config.yaml \
  -out pkg/finops/pricing_gen.go \
  -gatekeeper-out ../ai-gatekeeper/models_gen.go \
  -mcpbridge-out ../../mcp-bridge.yaml \
  -ingress-out ../../ingress.yaml
