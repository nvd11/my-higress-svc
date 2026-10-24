# Dora v3.2 方案复核报告

> **评审对象**：`docs/MODEL_GOVERNANCE_PLAN_V3.md`（"v3.2 · 生产就绪终极完备版"）
> **对象指纹**：`sha256:87d808174af54411…` · **314 行** · **24,587 bytes** · mtime `2026-10-25 00:29:25`
> **评审时状态**：未提交（`??`）
> **前序文档**：`docs/MODEL_GOVERNANCE_PLAN.md`（v1, `8c81855`）→ `docs/DORA_V2_REVIEW.md`（`ace65d1`）→ `docs/DORA_V3_REVIEW.md`（`0456b6c`）→ `docs/DORA_V31_REVIEW.md`（`e0ebc06`）
> **评审日期**：2026-10-25
>
> **总体结论：✅ 通过，可开工 —— 但有 3 处必须先修，其中 M-1 涉及审计链路的核心语义。**

---

## 0. 结论摘要

| 维度 | 结果 |
|---|---|
| 六条补丁落地 | **5/6 实质落地**（②方向与本报告建议相反，但结论可接受；④半修） |
| 新增问题 | **M-1（阻塞）**、M-2（中）、M-3（小） |
| 立场更新 | 空 `virtual_keys` 语义：**接受"一票否决"**，附 2 项前置条件 |
| 建议 | 修 M-1 → 应用 §7 补丁 → P0.0 扩为 **V1~V4** → 再开 P0 |

---

## 1. 六条补丁的落地对账

| 补丁 | v3.2 位置 | 落地 | 判定 |
|---|---|---|---|
| **①** P4 凭证机制 A/B/C | §4（新增整章，`config.yaml` 之外的独立章节） | 形式具备 | ⚠️ **图与表自相矛盾，3 条中仅 1 条技术成立**（详见 M-2） |
| **②** 空 `virtual_keys` 语义 | §3.1 第 2 条 | 明确写入 | ⚠️ **语义与本报告建议相反**（全拒 vs 放行），但可接受（详见 §5） |
| **③** 两份 Key 的 CI 校验 | §6-R6 + §8-P2 | 明确写入 | ✅ 采纳（缺具体脚本，可接受） |
| **④** P0.0 工期/日期 | §8 gantt + 细目 | 部分修正 | ⚠️ 日期已改为 2026-10-25 ✓；**三处数字仍互相矛盾**（详见 M-3） |
| **⑤** G1 判据定量化 | §9-G1 / G2 | 明确写入："上游调用次数 = 0" | ✅ 采纳（建议再补两种 401 分支用例，见 §7 补丁 4） |
| **⑥** 全链路 `x-request-id` | §3.3（新增） | 明确写入 | ❌ **与实测冲突**，且幂等语义表述有误（详见 M-1） |

---

## 2. 🔴 M-1：`x-request-id` 与 WAL 幂等语义（阻塞，影响审计链路）

### 2.1 问题一：门卫"生成并注入 `x-request-id`"很可能被 Envoy 覆盖或拼接

**v3.2 §3.3 原文**：*"检查传入的 `x-request-id` 请求头；若客户端未提供，立即调用 `finops.GenerateRequestID()` 派发一个标准 UUID 并调用 `AddHttpRequestHeader` 注入请求"*

**实测反证**：经网关请求时，响应头中的 `x-request-id` 实际值为：

```
x-request-id: 917cf3d7-1142-4606-b1e0-cf27a2354489,c117d9302ee72226e6bc841308a07676
```

**两个 UUID 以逗号拼接** → 说明该头由 Envoy/上游逐跳写入，门卫自己注入的值可能被覆盖、或被追加成"脏值"，进而污染：
- 审计账本的 `request_id`
- WAL 幂等键 `md5(request_id + "|" + created_at)`
- VictoriaLogs 的检索主键

**修复方向**：改为"**只读优先**"——优先采用 Envoy 已下发的 `x-request-id`（默认一定存在）；仅当为空时才自行生成。并在 P0.0 增加 V4 实测（见 §6）。

### 2.2 问题二：`md5(request_id + created_at)` 是"防重复投递"，不是"串联/合并"

**v3.2 §3.3 原文**：*"后续的 `finops-audit` 与后端 `dashboard-api` 统一以此 `x-request-id` 作为全局追踪主键与 WAL 幂等哈希键（`md5(request_id + created_at)`），全链路单号 100% 串联对齐"*

**代码事实**（`services/dashboard-api/api/internal.go:178-183`）：

```go
func auditRowID(rec WasmAuditRecordInput) string {
    seed := rec.RequestID + "|" + rec.CreatedAt.UTC().Format(time.RFC3339Nano)
    sum := md5.Sum([]byte(seed))
    return hex.EncodeToString(sum[:])
}
```

注释原文即写明：*"同一 `request_id` 的不同次上报 → `createdAt` 参与哈希 → 不会互相覆盖"*。

**结论**：
- 该键的用途是**同一条记录重复投递时幂等**（WAL 重放、重试），**不是**把两个组件对同一请求的两条记录"合并"；
- `created_at` 由**各写入方各自产生**，因此门卫与审计对同一请求必然算出**不同的 id → 两条独立记录**；
- 文档中"100% 串联对齐"的表述会误导后续实现者以为可以自动合并。

**这在两种情形下会产生实际差异**：

| 情形 | 现状行为 | 与文档表述的差异 |
|---|---|---|
| G7 拒绝留痕（门卫单独写） | 独立成行，`request_id` 一致 | ✅ 可接受（本就不需要合并） |
| §3.4 方案 B（门卫自报准入事件 + 审计记用量，期望合并） | **两行，各不相同**，无法自动合并 | ❌ 文档称"自动合并"，与实现不符 |

**修复方向（择一，写进文档）**：

| 方案 | 做法 | 代价 |
|---|---|---|
| **B1 统一时间源** | 两方都从 Envoy 取同一时间（如 `x-request-id` 对应的 start_time 头，或统一取整到秒） | 需验证 Envoy 是否暴露稳定时间头 |
| **B2 幂等键加事件类型** | `md5(request_id + event_type + created_at)`，并把 `event_type` 写入账本；后端按 `request_id` 关联两条记录 | 需 DB 加列（一次迁移） |
| **B3 明确放弃合并** | 文档改为"两条独立记录，按 request_id 关联查询"，G7 只要求可检索 | 零成本，但方案 B 的"合并"收益消失 |

---

## 3. 🟡 M-2：P4 凭证机制的图/表矛盾，且 3 条中仅 1 条技术成立

### 3.1 图与表对"机制 B"的定义完全不同

| 位置 | 机制 B 的定义 |
|---|---|
| §4 流程图 | *"通过 Ingress **tls / auth secretRef** 原生对接 Envoy 上游凭证绑定"* |
| §4 表格 | *"WasmPlugin 的 `defaultConfig` 中以环境变量方式引用 `${A6_API_KEY}`"* |

同一个编号两种完全不同的方案 → 实现者无法判断该做哪一个。

### 3.2 逐条技术判定

| 机制 | 声明 | 判定 |
|---|---|---|
| 图-B | Ingress `tls` / `auth` secretRef 绑定上游凭证 | ❌ **概念错误**：`tls` secret 承载的是证书；且 Ingress `annotations` 为 `map[string]string`，K8s 层面不存在 secret 插值，无法据此注入 `Authorization` 头 |
| 表-A | ExternalSecret 生成 Secret → *"在网关配置中直接以 Secret 引用挂载"* | ⚠️ **未指明"网关配置"是哪个对象**：Ingress annotation 不支持；`WasmPlugin.defaultConfig` 亦不支持 `secretRef`（v2 曾因 `apiTokens.secretRef` 踩坑，见 `DORA_V2_REVIEW.md` A1/B4） |
| 表-B | WasmPlugin `defaultConfig` 中 `${A6_API_KEY}` 由 Pod 环境变量解析 | ⚠️ **未验证**：Higress WasmPlugin 配置是否支持 env 插值需实测，不能作为既定方案 |
| 表-C | 在 `my-argocd-manifests` 用 SealedSecret / AVP 在**部署期渲染**进 Ingress 注解 | ✅ **技术成立**：渲染发生在 apply 之前，最终对象仍是普通 Ingress |

**事实补充**：文中"复用集群现有的 `oci-litellm-vault-store`"属实（`secrets.yaml:23`）。

**修复方向**：把 **机制 C 定为唯一主选**，A/B 标注"待验证"，删除流程图中误导性的 B 描述。

---

## 4. 🟡 M-3：P0.0 工期三处数字仍不一致

| 位置 | 数字 |
|---|---|
| §8 gantt 标题（第 217 行） | *"前置 **3h** 极简验证"* |
| §8 gantt 任务（218-220 行） | V1 / V2 / V3 各 **3h** → 合计 **9h** |
| §8 细目（第 240 行） | *"工期 **0.5d**"*（≈4h） |

**修复方向**：统一为一处数字。建议按 §6 的 V1~V4 重新排：**2h + 3h + 1h + 1h = 7h（≈0.9d）**。

---

## 5. 立场更新：接受"空 `virtual_keys` 一票否决"

v3.2 §3.1 第 2 条将空配置语义定为 **拒绝所有请求（401 `Authentication unconfigured`）**，与本报告上一轮建议的"放行 + `x-consumer: default` + 告警"**相反**。

**经复核，本报告接受 v3.2 的取值**，理由：

1. 与门卫 `FAIL_CLOSED` 的整体安全姿态一致；
2. 错误文案明确标注 `Authentication unconfigured`，配置问题与业务问题可区分；
3. L1 回滚（删除门卫）15 秒内可回落至 `finops-audit` 的宽松鉴权，故障可逆。

**但必须补两项前置条件，否则"更安全"会变成"更脆弱"**：

- [ ] **CI 断言 `virtual_keys` 非空**（一次漏配 = P3a 全站 401）
- [ ] **P3a 前在 `ai-lab` 用与生产完全相同的配置预演**（门卫优先级 900 会覆盖 finops 的宽松语义，此点无法靠推理保证）

---

## 6. 建议 P0.0 扩为 V1 ~ V4

| # | 验证项 | 方法 | 通过判据 | 不通过则 |
|---|---|---|---|---|
| **V1** | 缓冲上限 | 门卫设 `SetRequestBodyBufferLimit(50MB)`，发 3.5MB 报文 | 200 且能读到 `model` | 调大上限或改由头部传模型 |
| **V2** | body 阶段能否改请求头 | 5 行 demo 插件在 `ProcessRequestBody` 中 `SetHttpRequestHeader("x-original-model", …)`，echo 上游观察 | echo 端能看到该头 | 锁定 §3.4 方案 B |
| **V3** | 本地拒绝留痕 | 门卫返回 400，观察留痕通道 | 拒绝事件可检索 | 门卫自行上报（G7 由门卫实现） |
| **V4（新增）** | `x-request-id` 归属与稳定性 | 门卫注入/读取 `x-request-id`，echo 上游打印实际收到的值 | **值未被覆盖、未被逗号拼接** | 改为"只读不改"，并把 §3.3 的实现与文档同步 |

**V4 必要性**：实测已出现 `uuid,uuid` 拼接现象（§2.1），若不先验，`request_id` 可能带着逗号进入账本与 VictoriaLogs 检索键。

---

## 7. 可直接粘贴的补丁

### 补丁 1（替换 §3.3 全文）

```markdown
### 3.3 全链路 `x-request-id` 追踪规范

1. **只读优先**：门卫在 `onHttpRequestHeaders` 阶段**优先采用 Envoy 已下发的 `x-request-id`**；
   仅当其确实为空时，才生成 UUID 并 `AddHttpRequestHeader` 注入。
   （实测该头会被多跳写入，形如 `uuid,uuid`，门卫不得覆盖或追加。）
2. **稳定性验证**：P0.0 增加 **V4**，确认注入值未被覆盖/拼接；若被拼接则改为纯只读。
3. **幂等语义澄清**：后端主键为 `md5(request_id + "|" + created_at)`（`internal.go:178`），
   其用途是**同一条记录重复投递时幂等**（WAL 重放/重试），**不承担跨组件合并职责**。
   若采用 §3.4 方案 B 需要"合并"，须先落地以下之一：
   - **B1** 两方统一时间源（同一请求取同一 `created_at`）；
   - **B2** 幂等键加入 `event_type`，并按 `request_id` 关联查询；
   - **B3** 明确放弃合并，G7 仅要求"可按 `request_id` 检索到拒绝记录"。
```

### 补丁 2（替换 §4 流程图与表格的机制 B 描述）

```markdown
**机制选定**：以 **机制 C（部署期渲染）为唯一主选**；A、B 标注"待验证"，不作为既定实施路径。

| 机制 | 实施路径 | 状态 |
|:---:|---|---|
| **C（主选）** | `my-argocd-manifests` 使用 SealedSecret / ArgoCD Vault Plugin（AVP），在**部署期**将 A6 Key 解密渲染进 Ingress 注解；Git 仓库保持零明文 | ✅ 可行（渲染先于 apply） |
| A（待验证） | ExternalSecret 已生成 `higress-ai-secrets`（`secrets.yaml:23` 复用 `oci-litellm-vault-store`），但**对象如何被网关引用尚未确定**（Ingress annotation 无 secret 插值；`WasmPlugin.defaultConfig` 不支持 `secretRef`） | ⚠️ 需先验证引用机制 |
| B（待验证） | WasmPlugin `defaultConfig` 是否支持 `${ENV}` 插值需实测 | ⚠️ 未验证 |

> 说明：Ingress `annotations` 类型为 `map[string]string`，K8s 不提供 secret 插值；
> 因此"SDS / EnvoyFilter"若要使用，须单独立项评估，不在本方案范围。
```

### 补丁 3（替换 §8 gantt 的 P0.0 段）

```markdown
    section P0.0: 前置验证 (合计约 7h)
    V1: 50MB 缓冲支撑 3.5MB 报文       :v1, 2026-10-25, 2h
    V2: Body 阶段请求头注入时机测试    :v2, after v1, 3h
    V3: 本地拒绝事件留痕链路测试       :v3, after v2, 1h
    V4: x-request-id 归属与稳定性测试  :v4, after v3, 1h
```
（同步修改 §8 细目首行"前置 3h"→"前置约 7h（0.9d）"）

### 补丁 4（在 §9-G1 后追加边界用例）

```markdown
| **G1-b** | 非法 Key 被拒 | 伪造 Token 请求，断言 401 且 `code=invalid_api_key`，上游调用次数 = 0 |
| **G1-c** | 空 `virtual_keys` 配置 | 门卫配置清空后请求，断言 401 且 `message` 含 `Authentication unconfigured`，上游调用次数 = 0 |
```

### 补丁 5（在 §8-P2 中补 CI 校验的具体判据）

```markdown
3. **CI 校验项（必须全部通过）**：
   - `virtual_keys` **非空**（防止 P3a 因漏配导致全站 401）；
   - `ai-gatekeeper` 与 `finops-audit` 两份 `virtual_keys` **逐字一致**（键集合与值均相同）；
   - `pricing_gen.go` 与门禁名单生成物 **指纹同源**。
```

---

## 8. 裁决表

| 维度 | 判定 | 说明 |
|---|---|---|
| 六补丁落地 | ✅ 5/6 | ②语义相反但可接受；④半修 |
| 路线 / 架构 / 失败语义 | ✅ 通过 | 无变化，保持正确 |
| 审计链路正确性 | ⚠️ **需修 M-1** | `x-request-id` 注入与幂等语义 |
| 凭证治理可行性 | ⚠️ **需修 M-2** | 收敛为机制 C，A/B 标"待验证" |
| 工期准确性 | ⚠️ 需修 M-3 | 三处数字不一致 |
| 可执行性 | ✅ 通过 | 修完 M-1~M-3 即可开 P0 |

**一句话**：v3.2 把上轮 6 条补丁落地了 5 条，是目前最好的一版；剩下的 M-1 是**唯一会影响"账本是否可信"的问题**，务必在 P0 编码前定死。

---

## 9. 附录：证据复现（只读）

### E1 — 评审对象指纹

```bash
sha256sum docs/MODEL_GOVERNANCE_PLAN_V3.md | cut -c1-16   # 87d808174af54411
wc -l < docs/MODEL_GOVERNANCE_PLAN_V3.md                  # 314
stat -c '%s bytes  %y' docs/MODEL_GOVERNANCE_PLAN_V3.md   # 24587 bytes  2026-10-25 00:29:25
```

### E2 — `x-request-id` 被多跳写入（M-1 依据）

```bash
curl -s -D - -o /dev/null https://higress.jppwl.asia/v1/models \
  -H "Authorization: Bearer <KEY>" | grep -i x-request-id
# 实测形如：x-request-id: 917cf3d7-…,c117d930…
```

### E3 — WAL 幂等键实现（M-1 依据）

```bash
sed -n '168,184p' services/dashboard-api/api/internal.go
# auditRowID = md5(request_id + "|" + created_at RFC3339Nano)
# 注释：同一 request_id 的不同次上报 → createdAt 参与哈希 → 不会互相覆盖
```

### E4 — 工期三处数字（M-3 依据）

```bash
grep -n "前置 3h\|, 3h$\|0.5d" docs/MODEL_GOVERNANCE_PLAN_V3.md
# 217 前置 3h / 218-220 各 3h / 240 工期 0.5d
```

### E5 — Vault store 事实（M-2 佐证）

```bash
grep -n "secretStoreRef\|oci-litellm-vault-store" secrets.yaml
# 22: secretStoreRef:  23: name: oci-litellm-vault-store
```

### E6 — Ingress annotation 为纯字符串（M-2 依据）

```bash
kubectl -n higress-system get ingress higress-ai-ingress \
  -o jsonpath='{.metadata.annotations.higress\.io/request-header-control-update}'
# 输出：Authorization Bearer sk-jjoqdnu…    ← 明文，K8s 无 secret 插值能力
```

---

## 10. 版本与评审链

| 文档 | 提交 | 结论 |
|---|---|---|
| `docs/MODEL_GOVERNANCE_PLAN.md`（v1） | `8c81855` | 原始方案 + 证据链基线 |
| `docs/DORA_V2_REVIEW.md` | `ace65d1` | v2 **不可执行**（4 阻塞 + 5 事实错误 + 证据清空） |
| `docs/DORA_V3_REVIEW.md` | `0456b6c` | v3 **有条件通过**（2 阻塞假设） |
| `docs/DORA_V31_REVIEW.md` | `e0ebc06` | v3.1 **通过**（6 条文档级补丁；含二次修订记录） |
| `docs/DORA_V32_REVIEW.md`（本文档） | — | v3.2 **通过**（5/6 补丁落地；需修 M-1/M-2/M-3） |
