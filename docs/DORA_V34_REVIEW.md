# Dora v3.4 方案复核报告

> **评审对象**：`docs/MODEL_GOVERNANCE_PLAN_V3.md`（"v3.4 · 生产无懈可击可开工版"）
> **对象指纹**：`sha256:339e4e2ac988c14c…` · **351 行** · **28,010 bytes** · mtime `2026-10-25 01:08:44`
> **评审时状态**：未提交（`??`）
> **前序文档**：v1 `8c81855` → v2 评审 `ace65d1` → v3 评审 `0456b6c` → v3.1 复核 `e0ebc06` → v3.2 复核 `e1aa1c0` → v3.3 复核 `c949023`
> **评审日期**：2026-10-25
>
> **总体结论：✅ 通过，可开工。**
> 上一轮 4 条问题（N-0/N-1/N-2/N-3）**全部闭环**，我建议的 2 项加固（CI `--dry-run=server`、全真 `virtual_keys` 预演）**也全部采纳**。
> 仅剩 **2 个小问题**：L-1 是"验收覆盖回退"（G6 被替换，CI 漂移验收项丢失），L-2 是两处过度承诺措辞。改完即可进入 P0.0。

---

## 0. 结论摘要

| 维度 | v3.3 | **v3.4** |
|---|---|---|
| 上轮 4 条问题闭环 | 0/4 | **4/4 ✅** |
| 我建议的 2 项加固 | 未采纳 | **2/2 ✅** |
| 阻塞级问题 | N-0（`FAIL_CLOSED` 非法） | **已修** |
| 上线安全性 | N-1（首日全站中断风险） | **已拆两段式** |
| 验收覆盖完整性 | ✅ | ⚠️ **L-1：G6 被替换，CI 漂移验收项丢失** |
| 综合 | 可开工（先改 N-0） | **✅ 可开工（改 2 处即可）** |

---

## 1. 上轮问题的闭环对账（4/4 + 2/2）

| 上轮问题 | v3.4 处置 | 证据（行号） |
|---|---|---|
| **N-0** `failStrategy: FAIL_CLOSED` 为非法枚举（CRD 仅接受 `FAIL_CLOSE`/`FAIL_OPEN`） | ✅ 全篇改为 **`FAIL_CLOSE`**，并在修订记录里注明"非法枚举纠正" | L19、L56、L69、L108、L115、L254-255、L297 |
| **N-1** 首日 `FAIL_CLOSE` 会把"部署失败"放大成全站中断 | ✅ 拆为 **P3a-1（FAIL_OPEN 观察 10 分钟）→ P3a-2（切 FAIL_CLOSE）→ P3b**，并补了理由说明 | L254-255、L295-300 |
| **N-2** 机制 C 的渲染落点写错仓库（应为 `higress-svc` App，不是 `my-argocd-manifests`） | ✅ 改为 **机制 C-1 = 在 `higress-svc` Application 启用 AVP**，正文写明 source `my-higress-svc path .`，并配 mermaid 图 | L163-172 |
| **N-3** 方案 B 下 `model_requested` 无法保留原始别名，G4 会静默退化 | ✅ 正文 + G4 验收行均加降级声明 | L157、L317 |
| 我方建议：CI 增加 `kubectl apply --dry-run=server` 清单校验 | ✅ 已进 P2 CI 清单，并点名"严防 `FAIL_CLOSED` 等非法值" | L292 |
| 我方建议：P1 用生产**同一份** `virtual_keys` 预演 | ✅ 已进 P1 第 2 项（措辞为"全真预演"） | §8-P1 第 2 条 |

**结论：v3.4 是我方参与评审以来质量最高的一稿。**

---

## 2. 🟡 L-1（中 · 验收覆盖回退）G6 被替换，CI 漂移验收项消失

### 2.1 事实对比

| 编号 | v3.3 | **v3.4** | 变化 |
|---|---|---|---|
| G5 | `/v1/models` 纯净度 100% | `/v1/models` 仅暴露 10 个主名 | ≈ 等价 |
| **G6** | **CI 防漂移双产物指纹锁死** | **3.54MB 超长上下文平稳穿透** | ❌ **语义被替换** |
| G7 | 阻断留痕可审计 | 阻断留痕可审计 | ✅ 不变 |

### 2.2 影响

把"413 压测"升格为验收项本身是**改进**（v3.3 里它只存在于 P1 的执行项，没有验收契约）。但**不应挤掉原有的 G6**，因为：

- 现有 G 矩阵中**再无任何一项**覆盖"修改 `config.yaml` 却未重新生成 → 流水线必须红"；
- 这条护栏正是 `4fc6abd`（价目改由 config.yaml 构建期生成）的核心成果，也是防止"模型名单/价目漂移"的唯一自动闸门；
- P2 虽列了 CI 三项校验，但**验收契约（G）缺项**会导致上线验收时无据可依。

### 2.3 修复

**新增 G8，而不是替换 G6**：

```markdown
| **G8** | CI 防漂移双产物指纹锁死 | 修改 `config.yaml`（或任一门禁/价目输入）但**不重新生成**生成物 → 流水线必须立即飘红阻断；
      `pricing_gen.go` 与门禁名单生成物的 `config.yaml` sha256 指纹必须一致。 |
```

> 附带说明：P0.0 的 **V1**（50MB 缓冲 + 3.5MB 报文）与 G6（3.54MB 穿透）是**同一指标的两个时点**——V1 是编码前的前置验证，G6 是生产验收。建议在 §8-P0.0 或 §9 加一句注明，避免后续被误认为重复劳动。

---

## 3. 🟢 L-2（小）两处过度承诺措辞

| 位置 | 原文 | 建议 |
|---|---|---|
| L1（标题） | "v3.4 · 生产**无懈可击**可开工版" | 移除"无懈可击" —— 方案文档越朴素越可信 |
| L300 | "两段式部署将风险**降至绝对零点**" | 改为"**显著降低**部署阶段的全站中断风险" |

工程上不存在"绝对零点"；此类措辞在事故复盘中会直接变成把柄，也会削弱文档中其他判断的可信度。

---

## 4. 可直接粘贴的补丁

### 补丁 1（§9 G7 之后新增一行）

```markdown
| **G8** | CI 防漂移双产物指纹锁死 | 修改 `config.yaml` 但**不重新生成**生成物 → 流水线必须立即飘红阻断；
     `pricing_gen.go` 与门禁名单生成物的 sha256 指纹必须与当前 `config.yaml` 一致。 |
```

### 补丁 2（措辞修正）

```diff
- # AI 网关模型治理改造方案 (v3.4 · 生产无懈可击可开工版)
+ # AI 网关模型治理改造方案 (v3.4 · 生产就绪可开工版)

- 两段式部署将风险降至绝对零点。
+ 两段式部署显著降低部署阶段的全站中断风险。
```

### 补丁 3（§9 或 §8-P0.0 增加一句关系说明）

```markdown
> 说明：P0.0 的 **V1** 与验收项 **G6** 是同一指标的不同时点 ——
> V1 为编码前的前置验证（沙盒），G6 为生产验收（长上下文真实流量），二者不重复。
```

---

## 5. 裁决表

| 评审维度 | 裁决 | 说明 |
|---|---|---|
| 路线选择（自研 `ai-gatekeeper`） | ✅ 通过 | 保持正确 |
| 架构与失败语义隔离 | ✅ 通过 | `FAIL_CLOSE`（门卫）/ `FAIL_OPEN`（审计）取值已合法化 |
| 配置合法性 | ✅ 通过 | N-0 已修，并加了 `--dry-run=server` 前置护栏 |
| 上线安全性 | ✅ 通过 | P3a 两段式，解耦"部署成功"与"逻辑正确" |
| 审计链路语义 | ✅ 通过 | `x-request-id` 只读优先 + 幂等语义澄清 + 选定 B3 |
| 凭证治理 | ✅ 通过 | 机制 C-1（`higress-svc` App 上启用 AVP），A/B 标"待验证" |
| 验收覆盖完整性 | ⚠️ 待补 | **L-1：需新增 G8** |
| 文档严谨性 | ⚠️ 待修 | **L-2：两处措辞** |
| **综合** | **✅ 可开工** | 改 2 处后即可进入 P0.0 |

---

## 6. 附录：证据复现（全部只读）

### E1 — 评审对象指纹

```bash
sha256sum docs/MODEL_GOVERNANCE_PLAN_V3.md | cut -c1-16   # 339e4e2ac988c14c
wc -l < docs/MODEL_GOVERNANCE_PLAN_V3.md                  # 351
stat -c '%s bytes  %y' docs/MODEL_GOVERNANCE_PLAN_V3.md   # 28010 bytes  2026-10-25 01:08:44
```

### E2 — N-0 修复情况（合法枚举已全量替换）

```bash
grep -c "FAIL_CLOSED" docs/MODEL_GOVERNANCE_PLAN_V3.md      # 2（均为解释性引用，位置 L19 / L292）
grep -o "FAIL_CLOSE[^D]" docs/MODEL_GOVERNANCE_PLAN_V3.md | wc -l   # 10
grep -n "failStrategy" wasm-plugin.yaml                     # 14: failStrategy: FAIL_OPEN（现存合法值对照）
```

### E3 — CRD 枚举（复核 N-0 的判据未变）

```bash
kubectl get crd wasmplugins.extensions.higress.io \
  -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.failStrategy}'
# enum: ["FAIL_CLOSE","FAIL_OPEN"]
```

### E4 — N-1 两段式（行号）

```bash
grep -n "P3a-1\|P3a-2" docs/MODEL_GOVERNANCE_PLAN_V3.md
# 254 / 255（gantt）、295 / 297（细目）
```

### E5 — N-2 落点校准

```bash
grep -n "机制 C-1" docs/MODEL_GOVERNANCE_PLAN_V3.md        # 163（章节标题首句）
kubectl -n argocd get application higress-svc \
  -o jsonpath='{range .spec.sources[*]}{.repoURL}{"  path="}{.path}{"\n"}{end}'
# https://higress.io/helm-charts  path=
# https://github.com/nvd11/my-higress-svc.git  path=.
```

### E6 — L-1 依据：G 矩阵被替换

```bash
grep -n "G5\|G6\|G7" docs/MODEL_GOVERNANCE_PLAN_V3.md
# L318 G5 /v1/models；L319 G6 3.54MB 穿透；L320 G7 留痕
# 对比 docs/DORA_V33_REVIEW.md §9：v3.3 的 G6 为 "CI 防漂移双产物指纹锁死"
```

### E7 — L-2 措辞位置

```bash
grep -n "无懈可击\|绝对零点" docs/MODEL_GOVERNANCE_PLAN_V3.md
# 1: 标题；300: 两段式说明
```

---

## 7. 版本与评审链

| 文档 | 提交 | 结论 |
|---|---|---|
| `docs/MODEL_GOVERNANCE_PLAN.md`（v1） | `8c81855` | 原始方案 + 证据链基线 |
| `docs/DORA_V2_REVIEW.md` | `ace65d1` | v2 不可执行（4 阻塞 + 5 事实错误） |
| `docs/DORA_V3_REVIEW.md` | `0456b6c` | v3 有条件通过（2 阻塞假设） |
| `docs/DORA_V31_REVIEW.md` | `e0ebc06` | v3.1 通过（6 条文档级补丁） |
| `docs/DORA_V32_REVIEW.md` | `e1aa1c0` | v3.2 通过（5/6 补丁；M-1/M-2/M-3） |
| `docs/DORA_V33_REVIEW.md` | `c949023` | v3.3 通过（5/5 补丁；N-0 阻塞 + N-1/N-2/N-3） |
| `docs/DORA_V34_REVIEW.md`（本文档） | — | v3.4 **通过**（4/4 闭环 + 2/2 加固；仅剩 L-1/L-2） |

---

## 8. 建议的开工顺序（与 v3.4 一致）

```text
① 应用补丁 1~3（新增 G8 / 修正措辞 / 说明 V1 与 G6 关系）—— 3 分钟
② P0.0 前置验证 V1 ~ V4（约 7h，本地 + ai-lab，零生产影响）
     V1 门卫 50MB 缓冲 + 3.5MB 报文
     V2 body 阶段能否改请求头（决定 §3.4 走方案 A 还是 B）
     V3 本地 400/401 留痕链路
     V4 x-request-id 归属与稳定性
③ 修 finops-audit 状态码 bug（独立小提交，30 分钟）
④ P0 编码：门卫主体 → 生成器双产物 → 单测（覆盖率 ≥ 85%）
```
