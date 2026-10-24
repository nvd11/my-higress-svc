# Dora v3.1 方案复核报告

> **评审对象**：`docs/MODEL_GOVERNANCE_PLAN_V3.md`（"v3.1 · 生产就绪终极版"）
> **对象指纹**：`sha256:28d24ddc7e337c1a…` · **283 行** · **20,780 bytes** · mtime `2026-10-25 00:02:55`
> **评审时状态**：**未提交（`??`）**
> **上轮评审**：`docs/DORA_V3_REVIEW.md`（`0456b6c`），提出 R-1 ~ R-7
> **评审日期**：2026-10-25
>
> **总体结论：✅ 通过，可开工。**
> 上轮 7 条问题中 **5 条完全闭环、2 条部分闭环**；新识别 **6 条文档级细节**（2 中 4 小），
> 其中 2 条若不在开工前定死，会在 P4 变成无法兑现的空头承诺。
> 本文档同时给出 **6 段可直接粘贴的补丁文本**，不需要改动 v3.1 的既有结构。

---

## 0. 结论摘要

| 维度 | v3.0 | **v3.1** |
|---|---|---|
| 路线（自研 `ai-gatekeeper`） | ✅ 同意 | ✅ |
| 架构 / 失败语义隔离 | ✅ 同意 | ✅ |
| 技术可行性（2 个阻塞假设） | ⚠️ 未验证 | ✅ **已降级为 P0.0 验证项 + A/B/C 决策树** |
| 回滚设计（L2 时效） | ⚠️ 数字错误 | ✅ **已更正为 3~5 分钟** |
| 安全可观测（被拒事件） | ❌ 缺失 | ✅ **G7 补上** |
| 文档质量 / 可追溯 | ✅ | ✅（新增版本修订记录） |
| **综合** | 有条件通过 | **✅ 通过，可开工** |

---

## 1. 上轮 7 条问题的闭环对账

| 编号 | 上轮问题 | v3.1 处置 | 位置 | 判定 |
|---|---|---|---|---|
| **R-1** | 门卫未设 body 上限 → 1,097 条长请求 413 | 强制 `SetRequestBodyBufferLimit(50MB)`；新增 E8 生产数据（max 927,080 tokens / 1,097 条）；P0.0 V1 + P1 3.5MB 压测 | §3.1.1 / §4-E8 / §7 | ✅ **闭环** |
| **R-2** | `x-original-model` 注入相位属未验证假设 | 新增 §3.3 **A/B/C 决策树**（原生头注入 / 门卫自报 / 顺序微调），P0.0 V2 用 demo 插件定案 | §3.3 / §7-V2 | ✅ **闭环（含回退路径）** |
| **R-3** | 被拒请求不落库 → 安全事件不可观测 | 新增 **G7** 验收项 + §3.1.6"阻断留痕" | §8-G7 / §3.1.6 | ✅ **闭环** |
| **R-4** | L2 "<30 秒" 与实测不符 | 改为 **3~5 分钟**，标注"同日实测真实耗时" | §6-L2 | ✅ **闭环** |
| **R-5** | 明文 A6 Key 被固化为长期方案 | P4 排入"迁 Secret 引用" | §7-P4.1 | ⚠️ **方向对，机制未定义** → N-1 |
| **R-6** | `virtual_keys` 多副本漂移 | R6：过渡期以 `wasm-plugin.yaml` 为真理源，P3b 收敛 | §5-R6 | ⚠️ **只到文件级，未到副本级** → N-3 |
| **R-7** | 三个小尾巴（`fallback` / `model_used` / 不可验证性能数字） | `fallback` 清理入 P0.4；`model_used` 写入 G4；删除 "150 行 / 0.05ms" | §7-P0.4 / §8-G4 | ✅ **闭环** |

**额外采纳的两条**

- P1 第 6 项新增"**实跑演练 L1 回滚命令**" —— 回滚从纸面变成可验证动作；
- 删除门卫的定量性能承诺（仅 G1 残留一个不可验证的 `< 1ms`，见 N-5）。

---

## 2. 遗留问题（6 条）

### 🟡 N-1（中）P4 的"Key 迁 Secret 引用"没有机制 → 会成为空头承诺

**v3.1 原文（§7-P4.1）**：*"将 `ingress.yaml` 中的 A6 Key 彻底迁移至 Kubernetes Secret 引用"*

**事实约束**

1. **K8s Ingress 的 `annotations` 是 `map[string]string`（纯字符串）**，K8s 层面不存在 secret 插值机制 → `higress.io/request-header-control-update` 只能写死字面量；
2. Higress `WasmPlugin` 的 `defaultConfig` 同为内联配置，**同样不支持引用 Secret**。

实测印证：当前 annotation 的值就是一段明文 `Authorization Bearer sk-jjoqdnu…`。

**影响**：若开工时不定机制，P4 会像 v2 的 `secretRef` 一样，写下一个"看起来很美但无法执行"的动作。

**三个可选机制（P4 前必须择一）**

| 机制 | 做法 | 结果 |
|---|---|---|
| A. 门卫注入上游 Authorization | Key 放进 `ai-gatekeeper` 的 `defaultConfig` | 明文位置从 annotation 挪到 CR，**治标不治本** |
| B. EnvoyFilter + SDS | 真正引用 K8s Secret | 复杂度高，超出本方案范围，需单独立项 |
| C. 保留 annotation + 记录风险 | 明确决策人/日期/复审周期 | 不算"迁移"，但诚实 |

**建议**：把 P4.1 改为"**评估并在 A/B/C 中择一**"，不要写成既定动作。

---

### 🟡 N-2（中）`virtual_keys` 为空时语义未定义 → 配错一次就是全站 401

**事实**：现有 `finops-audit` 的语义是 `len(VirtualKeys) == 0 → 放行（consumer=default）`，属**宽松兜底**。

门卫是 `FAIL_CLOSED`，若它选择相反语义（空表=全拒），一次配置失误就会导致**全站 401**——比插件崩溃更难排查，因为它看起来"网关是好的"。

**建议**：在 §3.1 明确写死语义，并纳入 G1 的边界用例：

> 空 `virtual_keys` = **放行并打 `x-consumer: default`**，同时输出一条启动告警日志。

---

### 🟡 N-3（中）R6 只解决"文件级"单源，未解决"副本级"

`wasm-plugin.yaml` 将来会包含**两个 WasmPlugin 资源**（`ai-gatekeeper` + `finops-audit`），各自一份 `virtual_keys` 副本。

**YAML anchor 无法跨 `---` 文档生效**，所以"以 `wasm-plugin.yaml` 为真理源"在物理上仍是两份拷贝，P3a~P3b 期间需要人工同步 → 必然漂移。

**建议**：CI 增加一项**两份 Key 字典完全一致的校验**（十几行脚本即可），并在 R6 的应对措施里写明。

---

### 🟢 N-4（小）P0.0 工期自相矛盾，且日期已过期

- 正文标题：*"前置 **30 分钟**极简验证"*；
- gantt：V1 / V2 / V3 各 **1d**，合计 3 天；
- 日期：`2026-10-24` 起 —— 而**今天已是 2026-10-25**，日程已过期。

**建议**：gantt 改为 `合计 0.5d`（或用小时粒度），日期改为实际开工日。

---

### 🟢 N-5（小）G1 的"耗时 < 1ms"不可验证

端到端延迟包含 TLS 握手与网络往返，边缘拒绝也远超 1ms。该指标无法作为验收判据。

**建议**：改为可验证判据 —— **"上游 A6 调用次数 = 0"**（用 A6 调用日志或 mock 上游计数器验证）。

---

### 🟢 N-6（小）两个插件都要写审计，`request_id` 来源需统一

门卫（G7 拒绝留痕）与 finops（用量记账）都需要 `request_id`。若各自生成，则无法按 id 关联同一请求。

**建议**：两者统一读取网关下发的 `x-request-id`（实测该头由网关覆写为自身 UUID），**禁止各自实现 PRNG 生成**——我们刚为"固定种子 PRNG 重放导致主键冲突"出过事故（`d0d89e4` 修复）。

---

## 3. 可直接粘贴的补丁文本

> 以下 6 段可分别插入 v3.1 对应位置，**不改变既有结构与编号**

### 补丁 1（插入 §7-P4 细目 1，替换现有一句）

```markdown
1. **A6 Key 凭证收口（机制待择一，勿写成既定动作）**：
   - K8s Ingress `annotations` 为 `map[string]string`，**不支持 Secret 插值**；`WasmPlugin.defaultConfig` 亦为内联配置；
   - P4 开工前在以下三选中择一并记录理由：
     - **A** 由 `ai-gatekeeper` 注入上游 `Authorization`（明文位置从 annotation 移至 CR，治标）；
     - **B** EnvoyFilter + SDS 真正引用 K8s Secret（需单独立项）；
     - **C** 保留 annotation 并登记风险决策（决策人 / 日期 / 复审周期）。
```

### 补丁 2（插入 §3.1 核心职责，新增第 7 条）

```markdown
   7. **空配置语义（防全站 401）**：当 `virtual_keys` 为空或未配置时，
      **放行并注入 `x-consumer: default`**，同时输出启动告警日志；
      严禁采用"空表全拒"语义 —— 一次配置失误会造成全站 401，且难以与网关故障区分。
```

### 补丁 3（插入 §5-R6 应对措施，追加一句）

```markdown
   → 补充：`wasm-plugin.yaml` 内存在两个 WasmPlugin 资源（门卫 + 审计），各自持有 `virtual_keys` 副本；
     YAML anchor 不跨 `---` 生效，故需在 CI 增加"两份 Key 字典逐字一致"的校验，P3b 后再收敛为门卫单份。
```

### 补丁 4（替换 §7 的 gantt P0.0 段与日期）

```markdown
    section P0.0: 前置极简验证 (合计 0.5d)
    V1: 50MB 缓冲支撑 3.5MB 报文       :v1, 2026-10-25, 3h
    V2: Body 阶段请求头注入时机测试    :v2, after v1, 2h
    V3: 本地拒绝事件留痕链路测试       :v3, after v2, 1h
```

### 补丁 5（替换 §8-G1 判定标准）

```markdown
| **G1** | 未带 Key 或非法 Key 在边缘被立即阻断 | 用无 Token 请求打网关，返回标准 401；
        且**上游 A6 调用次数 = 0**（以 A6 调用日志或 mock 上游计数器证明），不以外网往返耗时为准。 |
```

### 补丁 6（插入 §3.2，新增第 5 条）

```markdown
   5. **request_id 来源统一**：门卫与审计**均读取网关下发的 `x-request-id`**（实测该头由网关覆写为自身 UUID），
      禁止各自实现随机数生成逻辑；历史事故 `d0d89e4` 已证明固定种子 PRNG 会重放序列并导致主键冲突。
```

---

## 4. 建议开工顺序

```
① 应用 6 段补丁（纯文档，约 10 分钟）
② P0.0 三项验证（真实执行，目标 0.5d）
     V1 门卫 50MB 缓冲 + 3.5MB 真实报文
     V2 body 阶段能否改请求头 → 定 §3.3 走 A / B / C
     V3 本地 400/401 的留痕链路是否通
③ 依据 V2 结论锁定 §3.3 方案，再开 P0 编码
     P0 编码顺序建议：状态码 bug（独立小改，先修先稳）→ 生成器双产物 → 门卫主体 → 单测
```

**V2 的判定表**

| V2 结果 | 锁定方案 | 后续影响 |
|---|---|---|
| echo 上游能收到 `x-original-model` | **方案 A** | 各组件零侵入，最干净 |
| 收不到 | **方案 B** | 后端需支持按 `request_id` 合并两条记录 |
| A/B 都不可行 | **方案 C** | 需重验"门卫本地拒绝时前置审计是否挂死" |

---

## 5. 裁决表

| 评审维度 | 裁决 | 说明 |
|---|---|---|
| 路线选择 | ✅ 通过 | 自研门卫是正确选择 |
| 架构与失败语义 | ✅ 通过 | `FAIL_CLOSED` / `FAIL_OPEN` 分离是本方案核心价值 |
| 技术可行性 | ✅ 通过 | 阻塞假设已转为 P0.0 验证项 + 决策树 |
| 回滚设计 | ✅ 通过 | L1/L2/L3 时效已校正，并加入回滚演练 |
| 安全可观测 | ✅ 通过 | G7 补齐 |
| 文档质量 | ✅ 通过 | 证据链、风险册、附录、版本修订记录齐备 |
| 遗留处理 | ⚠️ 建议 | N-1 / N-2 / N-3 属中等，开工前用补丁定死；N-4~N-6 属细节，可随 P0 处理 |

**一句话**：v3.1 已经是可执行文档；把 6 段补丁贴进去，就可以直接开 P0。

---

## 6. 附录：证据复现（全部只读）

### E1 — 评审对象指纹

```bash
sha256sum docs/MODEL_GOVERNANCE_PLAN_V3.md | cut -c1-16   # 28d24ddc7e337c1a
stat -c '%s bytes  %y' docs/MODEL_GOVERNANCE_PLAN_V3.md   # 20780 bytes  2026-10-25 00:02:55
git status --porcelain docs/MODEL_GOVERNANCE_PLAN_V3.md    # ?? （评审时未提交）
```

### E2 — annotation 为纯字符串（N-1 依据）

```bash
kubectl -n higress-system get ingress higress-ai-ingress \
  -o jsonpath='{.metadata.annotations.higress\.io/request-header-control-update}'
# 输出：Authorization Bearer sk-jjoqdnu…（明文）
# K8s 事实：Ingress.metadata.annotations 类型为 map[string]string，无 Secret 插值能力
```

### E3 — 长上下文真实体积（R-1 已闭环的原始数据）

```bash
grep -n "SetRequestBodyBufferLimit" plugins/finops-audit/main.go
# 68: ctx.SetRequestBodyBufferLimit(50 * 1024 * 1024)

# 生产库（近 7 天）
# max(prompt_tokens) = 927,080 → 约 3.54 MB
# count(prompt_tokens > 262,144) = 1,097
```

### E4 — 现有空 `virtual_keys` 语义（N-2 依据）

```bash
sed -n '/func ResolveVirtualKey/,/^}/p' plugins/finops-audit/pkg/finops/core.go
# 首行即：if len(virtualKeys) == 0 { return "default", true }  ← 宽松兜底
```

### E5 — gantt 日期与工期矛盾（N-4 依据）

```bash
grep -n "2026-10-24, 1d" docs/MODEL_GOVERNANCE_PLAN_V3.md   # 187/188/189 行
date +%F                                                    # 2026-10-25（已过期）
```

### E6 — PRNG 历史事故（N-6 依据）

```bash
git log --oneline -1 d0d89e4
# d0d89e4 fix(finops-audit): 请求熵播种 PRNG（修复固定种子重放导致 PRIMARY KEY 冲突）
```

---

## 7. 元数据与流程说明

| 项 | 说明 |
|---|---|
| **审查 Artifact 未找到** | 未发现名为 `model_governance_plan_v3.md` 的文件（仓库 / `~/.config/opencode` / `~/.local/share/opencode` 均 0 命中）。本报告仅基于 `docs/MODEL_GOVERNANCE_PLAN_V3.md`（v3.1）出具 |
| **署名** | v3.1 标注"Cindy & Dora 联合编制"，但该版本由 Dora 单方编写；Cindy 侧产出为 `docs/DORA_V2_REVIEW.md`、`docs/DORA_V3_REVIEW.md`、本文档，建议按 git 历史如实署名 |
| **本报告未修改 v3.1 正文** | 为避免再次发生"直接改动他人未提交文件"（历史事件：`674d87f`），6 处修订以补丁文本形式提供 |
| **文档定位** | v1 基线 `8c81855` / v2 评审 `ace65d1` / v3 评审 `0456b6c` / v3.1 复核（本文档） |
