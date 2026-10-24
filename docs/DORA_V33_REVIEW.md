# Dora v3.3 方案复核报告

> **评审对象**：`docs/MODEL_GOVERNANCE_PLAN_V3.md`（"v3.3 · 生产定稿可开工版"）
> **对象指纹**：`sha256:403004fb9c346efd…` · **317 行** · **25,054 bytes** · mtime `2026-10-25 00:37:56`
> **评审时状态**：未提交（`??`）
> **前序文档**：v1 `8c81855` → v2 评审 `ace65d1` → v3 评审 `0456b6c` → v3.1 复核 `e0ebc06` → v3.2 复核 `e1aa1c0`
> **评审日期**：2026-10-25
>
> **总体结论：✅ 通过 —— 我上一轮的 5 段补丁 100% 采纳。**
> **但本轮新发现 1 条阻塞级事实错误（N-0：`FAIL_CLOSED` 不是合法枚举值）、1 条中危（N-1）、2 条小问题（N-2/N-3）。**
> **N-0 必须改，否则门卫插件根本部署不上，而且会"静默失败"——看起来上线了，实际什么都没发生。**

---

## 0. 结论摘要

| 维度 | 结果 |
|---|---|
| 上轮 5 段补丁采纳 | **5/5 = 100%**（§3.3 只读优先 + B3、§4 机制 C 主选、§8 工期 7h、G1-b/c、P2 三项 CI） |
| 本轮新发现 | **N-0（阻塞，事实错误）/ N-1（中，P3a 全站中断风险）/ N-2（中，AVP 落点）/ N-3（小，G4 降级）** |
| 自查 | **N-0 前几轮评审（含我方 v3/v3.1/v3.2 三份报告）均未发现**，本报告首次识别并附服务端验证 |
| 综合 | **✅ 可开工，但 N-0 必须先改** |

---

## 1. 上轮补丁的采纳对账（5/5）

| 补丁 | v3.3 位置 | 落地 |
|---|---|---|
| **M-1 · `x-request-id` 只读优先** | §3.3 第 1 条 | ✅ 原文照收，且引用了实测的 `uuid,uuid` 现象 |
| **M-1 · 幂等语义澄清 + B1/B2/B3** | §3.3 第 3 条 | ✅ 明确"不承担跨组件合并职责"，并**选定 B3（放弃合并）** |
| **M-1 · 新增 V4 验证** | §8 gantt（1h）+ 细目 | ✅ 判据"未被覆盖、未被逗号拼接" |
| **M-2 · 机制 C 唯一主选** | §4 整章重写 | ✅ A/B 降为"待验证"，删除 tls/auth secretRef 表述 |
| **M-3 · 工期统一** | §8 | ✅ `2h + 3h + 1h + 1h = 7h`，gantt 与细目一致 |
| **G1 边界用例** | §9 | ✅ G1-b（非法 Key）、G1-c（空配置）均已补 |
| **P2 CI 三项** | §8-P2 | ✅ 非空 / 逐字一致 / 指纹同源 |

> 结论：v3.3 已达到"生产定稿"的文档质量。

---

## 2. 🔴 N-0（阻塞 · 事实错误）`failStrategy: FAIL_CLOSED` 不是合法值

### 2.1 问题

v3.3 全篇（含 §2 架构图、§3.1 运行属性、§8、验收描述）共 **4 处**写作 `failStrategy: FAIL_CLOSED`。

**但 Higress 的 CRD 枚举只接受 `FAIL_CLOSE` 和 `FAIL_OPEN`。**

### 2.2 证据（服务端校验实测，非推测）

```bash
kubectl get crd wasmplugins.extensions.higress.io \
  -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.failStrategy}'
# {"description":"Specifies the failure behavior for the plugin due to fatal errors.",
#  "enum":["FAIL_CLOSE","FAIL_OPEN"],"type":"string"}
```

服务端 dry-run（不落盘）验证：

```bash
kubectl apply --dry-run=server -f - <<'YAML'
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata: { name: failstrategy-probe, namespace: higress-system }
spec:
  priority: 900
  failStrategy: FAIL_CLOSED
  url: oci://ghcr.io/nvd11/higress-finops-audit:7b9acb5
  defaultConfig: {}
YAML
```

实际输出：

```
The WasmPlugin "failstrategy-probe" is invalid:
spec.failStrategy: Unsupported value: "FAIL_CLOSED": supported values: "FAIL_CLOSE", "FAIL_OPEN"
```

对照：现有生产插件 `wasm-plugin.yaml:14` 使用的是 `FAIL_OPEN`（合法值）。

### 2.3 后果（比"报错"更危险：静默不生效）

| 路径 | 结果 |
|---|---|
| `kubectl apply` | 直接被 API Server 拒绝（报错可见，尚可发现） |
| **ArgoCD 同步** | 该资源 apply 失败 → **同步状态变 Degraded/OutOfSync**，但**其余资源照常生效** → 门卫插件根本不存在 |
| 值班视角 | 「我推了配置、流水线绿、ArgoCD 有同步记录」→ 误判为已上线；实际**鉴权与白名单完全没生效**，且不会产生任何 401/400 日志 |

这正是我们历史上踩过的同类坑：**"看起来部署了，其实什么都没发生"**。

### 2.4 修复

- 全篇 `FAIL_CLOSED` → **`FAIL_CLOSE`**（4 处）
- P2 CI 增加一项：`kubectl apply --dry-run=server` 校验仓库中的 WasmPlugin 清单（把"枚举值写错"这类问题挡在合并前）

### 2.5 自查声明

本报告首次识别此问题；**此前三轮评审（`0456b6c` / `e0ebc06` / `e1aa1c0`，含我方出具的部分）均沿用了文档中的错误写法，未核对 CRD 枚举**。后续评审应把"所有 `spec` 字段的取值是否合法"纳入固定检查项。

---

## 3. 🟡 N-1（中）`FAIL_CLOSE` 的失败面远大于"代码 panic"

### 3.1 问题

方案只把 `failStrategy` 理解成"插件代码崩了就关门"。但按 CRD 定义，它描述的是 **"failure behavior for the plugin due to fatal errors"** —— 涵盖：

| 失败场景 | 结果 |
|---|---|
| 插件代码 panic | 全站拒绝 |
| **OCI 镜像拉取失败 / 超时**（GHCR 抖动、凭证过期） | **全站拒绝** |
| **`defaultConfig` 解析失败**（JSON 写错） | **全站拒绝** |
| wasm VM 初始化异常 | **全站拒绝** |

即：**P3a 只要"部署动作本身"有一点问题，就不是灰度，而是全站中断**。L1 回滚虽 15 秒，但需要有人及时发现。

### 3.2 建议：P3a 拆成两段

```text
P3a-1  门卫以 FAIL_OPEN 上线（白名单与鉴权逻辑本身不依赖 failStrategy）
       → 观察 10 分钟：镜像拉取成功、配置解析成功、401/400 正常、真实流量 200
P3a-2  切 FAIL_CLOSE（改一行 + ArgoCD 同步，分钟级）
P3a-3  观察 24 小时
```

这样把"**部署是否成功**"与"**逻辑是否正确**"两件事解耦，安全终态不变，风险大幅下降。

---

## 4. 🟡 N-2（中）机制 C 的渲染发生在**哪个 ArgoCD App** 未写明

### 4.1 问题

v3.3 §4 写"由 `my-argocd-manifests` 使用 AVP 渲染进 Ingress 注解"。但 `higress-ai-ingress` 这个对象**并不属于那个仓库**。

**实测 App 源**：

```bash
kubectl -n argocd get application higress-svc -o jsonpath='{range .spec.sources[*]}{.repoURL}{"  path="}{.path}{"\n"}{end}'
# https://higress.io/helm-charts        path=
# https://github.com/nvd11/my-higress-svc.git  path=.
```

即 Ingress 来自 `my-higress-svc`（path `.`），若把 AVP 配在 `my-argocd-manifests`，**渲染不会作用于本 Ingress**。

### 4.2 两个可选落点

| 方案 | 做法 | 评价 |
|---|---|---|
| **C-1（推荐）** | 在 `higress-svc` 这个 Application 上启用 AVP，`ingress.yaml` 中改用 AVP 占位符（如 `<path:secret/data/a6#token>`） | 改动最小，不动仓库结构 |
| C-2 | 把 `ingress.yaml` 迁到 `my-argocd-manifests` 统一管理 | 跨仓库搬迁，影响 ArgoCD 目录结构 |

**建议**：§4 明确写"C-1：在 `higress-svc` Application 上启用 AVP"。

---

## 5. 🟢 N-3（小）方案 B 下 G4 会静默退化

§3.2 写："`model_requested` 优先取 `x-original-model`（取不到则取 body）"。
但若 P0.0 V2 判定方案 A 不可行 → 采用方案 B → **不存在 `x-original-model` 头** → `model_requested` 只能取 body 中**改写后的真名** → G4"保留客户端原始名"实际无法达成。

**建议**：在 §3.4 或 G4 补一句降级声明：

> 若采用方案 B，`model_requested` 记录上游真名（无法回溯别名），G4 按"记录真名"验收；如需保留原始别名，另立二期。

---

## 6. 🟢 两条微调

| # | 项 | 建议 |
|---|---|---|
| 1 | P3a 前"**同配置预演**"未写入 | P1 增加："在 `ai-lab` 使用与生产**完全相同的 `virtual_keys`** 预演，验证 401/200 分支与生产预期一致"（门卫优先级 900 会覆盖 finops 的宽松语义，无法靠推理保证） |
| 2 | `/v1/models` 只回 10 个主名，不含别名 | 若客户端配置的是别名（如 `gemini-4`），下拉列表里看不到自己用的名字。可选把 12 个别名一并列出（`owned_by: alias`） |

---

## 7. 可直接粘贴的补丁

### 补丁 1（全局替换，4 处）

```diff
-  failStrategy: FAIL_CLOSED
+  failStrategy: FAIL_CLOSE
```
同步修改正文描述「`FAIL_CLOSED`」→「`FAIL_CLOSE`（CRD 合法枚举值）」。

### 补丁 2（替换 §8 的 P3a 条文）

```markdown
#### P3 生产零窗口交接（分两段，解耦"部署成功"与"逻辑正确"）
* **P3a-1（FAIL_OPEN 观察 10 分钟）**：部署 `ai-gatekeeper`（priority 900，failStrategy: FAIL_OPEN）。
  验证项：镜像拉取成功、defaultConfig 解析成功、401/400 分支正常、真实流量 200、无 5xx 激增。
* **P3a-2（切换 FAIL_CLOSE）**：确认 P3a-1 全绿后，切换 `failStrategy: FAIL_CLOSE`。此时两层鉴权并存，观察 24 小时。
* **P3b**：确认门卫稳定接管后，移除 `finops-audit` 冗余 Key 校验。
> 说明：`failStrategy` 描述的是"插件致命错误（含镜像拉取失败、配置解析失败）时的行为"，
> 若首日即以 FAIL_CLOSE 上线，"部署动作本身出问题"会直接表现为全站中断。
```

### 补丁 3（替换 §4 的落点句）

```markdown
**渲染落点（实测确认）**：`higress-ai-ingress` 由 ArgoCD Application **`higress-svc`** 管理，
其 source 为 `https://github.com/nvd11/my-higress-svc.git`（path `.`）。
因此 AVP/SealedSecret 必须在 **`higress-svc` 这个 Application** 上启用（方案 C-1），
而不是在 `my-argocd-manifests` —— 后者的派发链路只承载 `config.yaml`。
```

### 补丁 4（插入 §9-G4 之后或 §3.4 末尾）

```markdown
> **降级声明（方案 B 适用）**：若 P0.0 V2 判定"body 阶段无法注入请求头"，则采用方案 B，
> 此时 `model_requested` 记录上游真名（无法回溯客户端所用别名），G4 按"记录真名"验收。
```

### 补丁 5（插入 §8-P2 的 CI 校验项）

```markdown
   - **WasmPlugin 清单服务端校验**：`kubectl apply --dry-run=server -f wasm-plugin.yaml`，
     确保 `failStrategy` 等枚举字段取值合法（历史问题：`FAIL_CLOSED` 非法，应为 `FAIL_CLOSE`）。
```

---

## 8. 裁决表

| 维度 | v3.2 | **v3.3** |
|---|---|---|
| 上轮补丁采纳 | 5/6 | **5/5（100%）** |
| 审计链语义 | ⚠️ 有误 | ✅ 已澄清 + 选定 B3 |
| 凭证治理 | ⚠️ 图/表矛盾 | ✅ 收敛机制 C（落点待修 N-2） |
| 工期一致性 | ⚠️ 三处矛盾 | ✅ 统一 7h |
| **配置合法性** | 未检查 | ❌ **N-0：`FAIL_CLOSED` 非法，插件部署不上** |
| 上线安全性 | 未讨论 | ⚠️ **N-1：首日 FAIL_CLOSE = 全站中断风险** |
| 综合 | 通过待修 | **✅ 可开工（先改 N-0）** |

---

## 9. 附录：证据复现（全部只读）

### E1 — 评审对象指纹

```bash
sha256sum docs/MODEL_GOVERNANCE_PLAN_V3.md | cut -c1-16   # 403004fb9c346efd
wc -l < docs/MODEL_GOVERNANCE_PLAN_V3.md                  # 317
stat -c '%s bytes  %y' docs/MODEL_GOVERNANCE_PLAN_V3.md   # 25054 bytes  2026-10-25 00:37:56
```

### E2 — CRD 枚举（N-0 依据）

```bash
kubectl get crd wasmplugins.extensions.higress.io \
  -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.failStrategy}'
# enum: ["FAIL_CLOSE","FAIL_OPEN"]
```

### E3 — 服务端 dry-run 拒绝（N-0 依据）

```bash
# 见 §2.2；实际输出：
# spec.failStrategy: Unsupported value: "FAIL_CLOSED": supported values: "FAIL_CLOSE", "FAIL_OPEN"
```

### E4 — 现有生产插件取值（对照组）

```bash
grep -n "failStrategy" wasm-plugin.yaml     # 14:  failStrategy: FAIL_OPEN
```

### E5 — ArgoCD App 源（N-2 依据）

```bash
kubectl -n argocd get application higress-svc \
  -o jsonpath='{range .spec.sources[*]}{.repoURL}{"  path="}{.path}{"\n"}{end}'
# https://higress.io/helm-charts  path=
# https://github.com/nvd11/my-higress-svc.git  path=.
```

### E6 — 自查：前序评审同样遗漏 N-0

```bash
grep -c "FAIL_CLOSED" docs/DORA_V3_REVIEW.md docs/DORA_V31_REVIEW.md docs/DORA_V32_REVIEW.md
# 2 / 3 / 1  —— 三轮评审均沿用了文档写法，未核对枚举
```

---

## 10. 版本与评审链

| 文档 | 提交 | 结论 |
|---|---|---|
| `docs/MODEL_GOVERNANCE_PLAN.md`（v1） | `8c81855` | 原始方案 + 证据链基线 |
| `docs/DORA_V2_REVIEW.md` | `ace65d1` | v2 不可执行（4 阻塞 + 5 事实错误） |
| `docs/DORA_V3_REVIEW.md` | `0456b6c` | v3 有条件通过（2 阻塞假设） |
| `docs/DORA_V31_REVIEW.md` | `e0ebc06` | v3.1 通过（6 条文档级补丁） |
| `docs/DORA_V32_REVIEW.md` | `e1aa1c0` | v3.2 通过（5/6 补丁，M-1/M-2/M-3） |
| `docs/DORA_V33_REVIEW.md`（本文档） | — | v3.3 **通过**（5/5 补丁；新增 N-0 阻塞 + N-1/N-2/N-3） |
