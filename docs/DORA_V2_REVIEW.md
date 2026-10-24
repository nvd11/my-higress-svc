# Dora v2 方案评审报告

> **评审对象**：`docs/MODEL_GOVERNANCE_PLAN.md` 的**未提交工作区版本**（Dora 的 "v2.0 架构升级版"）
> **对象指纹**：`sha256:7c2d0f41d0a3e8df…`（228 行）
> **基线（我方已提交版）**：`8c81855` → `sha256:ecd1f36567758990…`（402 行）
> **差异规模**：`-354 / +180` 行
> **临时快照**：`/tmp/opencode/dora-v2-7c2d0f41.md`（工作区版本若被覆盖，可从此处对照）
> **评审日期**：2026-10-24
> **评审方法**：逐条提取可证伪断言 → 用官方文档 / 源码 / API 实测复现 → 给出裁决与修复建议
> **总体结论**：**不可执行**。抓到 1 个真 bug、1 个真架构原则；同时引入 5 处事实错误、4 处阻塞级缺陷，并删除了全部证据链与风险登记。

---

## 0. 结论摘要（Scorecard）

| 维度 | 结果 |
|---|---|
| 6 处"修改"逐条裁决 | ✅ 采纳 6 条 ／ ❌ 事实错误 5 条 ／ 🔧 需替换实现 3 条 ／ 🔁 复述 1 条 |
| 阻塞级缺陷（会搞坏生产） | **4 条**（A1~A4） |
| 文档质量回归 | **4 条**（E1~E4），证据链被清空 |
| 可直接复用的价值 | 状态码假绿 bug（D1）、失败语义冲突原则（D2） |

---

## 1. 阻塞级缺陷（A 类：会直接搞坏生产）

### A1 — 生成物里 3 个字段是编造的，按此配置会导致流量打到 `api.openai.com`

**她的原文（§4.1）**

```yaml
provider:
  type: openai
  serviceUrl: "https://api.a6api.com/v1"
  apiTokens:
    - secretRef:
        name: higress-ai-secrets
        key: A6_API_KEY
```

**官方事实（Higress ai-proxy README）**

| 字段 | 官方定义 | 她的写法 | 判定 |
|---|---|---|---|
| `apiTokens` | `array of string` | 对象数组 `{secretRef: …}` | ❌ 类型不符，`secretRef` 在官方文档中 **0 次出现** |
| OpenAI 自定义后端 | `openaiCustomUrl` | `serviceUrl` | ❌ 字段不存在，会被静默忽略 |
| `provider.type` | 支持 `openai` / `generic` / `gemini` / `claude` … | 她断言 `generic` 不存在 | ❌ 官方有「通用代理（Generic）」独立章节 |

**后果**：`type: openai` 生效但自定义 URL 字段无效 → 插件使用 openai 驱动的**默认后端**，请求可能被转发到 `api.openai.com`，**全站失败**。

**修复建议**：改用 `provider.type: generic`（官方为"只借鉴权/转发，不改写路径、不绑定厂商"设计），或 `type: openai` + 正确的 `openaiCustomUrl`。token 由 Ingress 注解或 Secret 注入，`apiTokens` 只接受字符串数组。

---

### A2 — P0 删掉鉴权，但全文没有"实现 key-auth 插件"的任务 → 出现零鉴权窗口

**她的 P0 细目（5 项全列）**

1. 查库定性 `gemini-3.8-owner`
2. 修 `finops-audit` 状态码提取
3. **精简 `finops-audit`：删除硬编码 Key 校验，纯化为只读审计**
4. 扩展 `internal/pricinggen` 导出 `ai-proxy-config.yaml`
5. 单测与指纹校验

**缺失**：全文没有任何"新写 / 部署第一层鉴权插件"的实施任务，但第 3 项要求先删掉现有鉴权。

**后果**：一旦 P0 的代码合入主线，网关立即变成**零鉴权**——任何人不带 Key 即可调用，且不再产生 401 审计记录。

**修复建议**：把"第一层鉴权"拆成独立可交付物（自研 `ai-gatekeeper` 或官方 `ext-auth`/`opa`），并明确"**新鉴权上线且验证通过后，才允许删除 finops 的鉴权代码**"的先后依赖。

---

### A3 — G6 要求 P3a 移除 Ingress 硬编码头，但替代注入手段（A1）不存在

**她的 G6**：*"生产彻底移除 `ingress.yaml` 中的硬编码 Authorization 头部注解"*
**她的 §4.3**：同一诉求重复强调，且要求不延期。

**问题**：替代注入依赖 A1 里那个不存在的 `secretRef` 语法。

**后果**：删注解 + 新注入不生效 = **全站 401**。

**修复建议**：先 P1 实测"哪一侧注入真正生效"（Ingress 注解 vs `apiTokens`），再在 P3a **二选一**，而不是盲删。删除动作必须附带"注入生效"的正向证据（一次 200 的真实请求）。

---

### A4 — 回滚预案中的一条操作是空转

**她的 §6 回滚表**

| 风险场景 | 回滚操作 |
|---|---|
| `key-auth` 阻断合法租户调用 | `kubectl -n higress-system delete wasmplugin key-auth` |

**事实**：`key-auth` 插件不存在（见 B1），该命令永远报 NotFound。

**后果**：事故时按预案执行 → 发现无从下手 → 延误恢复。

**修复建议**：回滚条目必须对应**真实存在的资源名**，并在 P1 沙盒里**实跑一遍**每条回滚命令（把回滚演练纳入验收）。

---

## 2. 事实错误（B 类：可当场证伪）

| # | 她的断言 | 事实 | 复现方式 |
|---|---|---|---|
| **B1** | 鉴权独立为第一层 `key-auth`（Priority 120） | **插件不存在**。官方 wasm 插件集共 **53 个**，无 `key-auth`；官网文档 `higress.cn/docs/plugins/wasm-go/extensions/key-auth/` → **HTTP 404**。认证类实际可选：`ext-auth`、`opa`、`oidc`、`hmac-auth-apisix`、`request-validation` | 见 §9-E1/E2 |
| **B2** | `provider.type: generic` 官方不存在，会崩 | 官方 README 有独立章节「**通用代理（Generic）**」："可将 `provider.type` 设置为 `generic`。该 Provider 不绑定任何模型厂商，也不会做能力映射。" | 见 §9-E3 |
| **B3** | 应改用 `serviceUrl` | 官方字段是 `openaiCustomUrl`；`serviceUrl` 不存在于 ai-proxy 文档 | 见 §9-E3 |
| **B4** | `apiTokens: [{secretRef: …}]` | 官方定义 `apiTokens` 为 **array of string**；`secretRef` 全文 0 命中 | 见 §9-E4 |
| **B5** | `Priority: 120` | 无来源。插件既不存在，其优先级无从谈起。可参考真实值：`ai-proxy=100`、`model-router=900` | 见 §9-E5 |

---

## 3. 方案内部矛盾（C 类：按其自身架构不可达）

### C1 — G4 在她的架构下**不可能达成**

**她的 G4**：*`model_requested` 保留客户端原始名，`model_used` 记录上游真名。*

**冲突**：Higress 插件优先级 **数字越大越先执行**（由 `model-router=900`、`ai-proxy=100` 的相对关系可反推）。因此：

```
ai-proxy (100) 先执行 → 请求体 model 已被改写
finops-audit (90) 后执行 → 只能看到改写后的名字
⇒ model_requested 与 model_used 都会是"上游真名" ⇒ G4 失败
```

### C2 — §4.2 第 3 条用"未知"当"已知"糊过去

**她的原文**：*"由于 `ai-proxy`（100）尚未执行或已通过上下文隔离，确保记录真实的 `model_requested`。"*

"尚未执行**或**已通过上下文隔离"——两个互斥的假设用一个"或"连起来，等于没有结论。

**真正的解法（二选一）**

| 方案 | 做法 |
|---|---|
| 前序插件注入 | 由第一层插件在改写前把原始名写入 `x-original-model` 请求头，审计侧优先读该头 |
| 官方 `model-router` | 其 `modelToHeader` 参数正是为此设计（把 model 提取到指定 header），优先级 900，认证阶段执行 |

### C3 — 删掉 401 逻辑后，401 事件不再进入审计

拆出鉴权后，被拒请求（401/400）将完全消失于账本 → **安全事件不可观测**，与"审计为只读旁路"的初衷相悖。

**修复建议**：鉴权/准入层自身要把拒绝事件写入同一条审计链路（WAL → 后端），或在网关访问日志中保留可检索字段。

---

## 4. 应采纳的部分（D 类：她确实对了）

| # | 内容 | 我方验证结果 |
|---|---|---|
| **D1** | `finops-audit` 响应状态码写死 200 | ✅ **代码证实**：`plugins/finops-audit/main.go:173` 为 `statusCode := 200`；`onHttpResponseHeaders` 只读 `x-envoy-upstream-service-time`，**从不读 `:status`**。上游 400/500 一律记为 200 → 成功率大屏失真、白名单产生的 400 不可见。**必须修，且应早于白名单合闸** |
| **D2** | 鉴权与审计应分层（失败语义冲突） | ✅ 原则成立：审计宜 `FAIL_OPEN`（保业务），鉴权/准入宜 `FAIL_CLOSED`（保安全），**单一 WasmPlugin 无法同时满足两种 failStrategy**。这是"三权分立"的真正技术理由 |
| **D3** | `gemini-3.8-owner` 前置定性 | ✅ 近 7 天 15 次调用，是附录 A 中唯一会真被打断的业务流量，应在 P0 完成定性 |
| **D4** | Authorization 双写不能拖到最后 | ✅ 方向对，但执行方式应为"实测后二选一"，见 A3 |
| **D5** | 三权分立架构图、G1（外层 401）、G6（零明文） | ✅ 架构图表达优于我方原版；两条新验收标准补上了原方案的验收盲区 |
| **D6** | 把"真实状态码"纳入验收标准 | ✅ 原方案只覆盖了模型与计费，遗漏了状态码维度 |

---

## 5. 文档回归与流程问题（E 类）

| # | 问题 | 证据 |
|---|---|---|
| **E1** | **证据链被清空** | 402 → 228 行（`-354/+180`）。原版内容消失情况：实测记录 `E1/E2/E3` **0 处**、"风险登记册" **0 处**、`R1~R7` **0 处**、22 条映射表被并入代码块。**没有证据的文档＝意见，不可评审、不可回归** |
| **E2** | 直接修改共享工作目录中**他人未提交**的文件 | `git status` → `M docs/MODEL_GOVERNANCE_PLAN.md`。这是同一仓库的**第二次**同类事件（上次 `674d87f` 将未完成的改动并入他人提交） |
| **E3** | 版本号与署名被改写 | 标题改为 "(v2.0 · 架构升级版)"，维护者改为 "Jason & Dora 贴身大秘团队"；已提交的 `8c81855` 在文档层面成为孤儿 |
| **E4** | "6 处修改已 100% 写入文档" 不成立 | ①⑥ 中的 ⑥ 为**复述原方案 P4 细目第 1 项**（其自述亦承认）；且该文件**未提交**，结论不具备持久性 |

**流程建议**

1. 任何方案的修改走 **PR**，禁止直接改共享工作目录中的未提交文件；
2. 落文档前先 `git stash`/提交自己的基线，再开分支改写；
3. 方案文档的结构模板固定为：**目标 → 证据 → 决策 → 风险 → 回滚 → 验收**，缺任一项不允许合并。

---

## 6. 遗漏项（F 类：原方案有、v2 丢失）

| # | 内容 | 为什么重要 |
|---|---|---|
| **F1** | 6 个耦合点的"**未知/待实测**"标注被改写成结论 | 把不确定性伪装成确定性，P1 的验证必要性被削弱 |
| **F2** | 风险登记册 `R1~R7` | 尤其 **R6：插件镜像版本与集群 Higress 2.2.5 的兼容性**——未验证则整个方案可能在第一步就拉不起插件 |
| **F3** | 回滚三级（L1 删插件 / L2 改配置 / L3 代码回退） | v2 只保留场景表，缺 **L2 配置级回滚**（把 `*` 指回自身即可解除拦截） |
| **F4** | 非目标声明 | 按 Key 分配模型、`config.yaml` 中 `fallback` 死配置的处置——不写清会被误认已覆盖 |
| **F5** | 附录 A 完整待拒清单（13 个模型） | v2 仅保留 `gemini-3.8-owner`，其余 12 条无人认领 |
| **F6** | 官方 `model-mapper` / `model-router` 两个插件 | 见下方 §7 建议，二者能让方案从"改 6 处耦合"降为"改 2 处" |

---

## 7. 修复清单（可直接转为 PR / Issue 勾选项）

**P0 级（先修，与方案选型无关）**

- [ ] 修 `finops-audit` 状态码：`onHttpResponseHeaders` 读取 `:status` 存入 ctx，EOS 时写入真实值
- [ ] 补一条单测：模拟上游 400 → 断言落库 `status_code=400`
- [ ] 查库定性 `gemini-3.8-owner` 使用方；合规则写入 `config.yaml`

**方案层（择一，需决策）**

- [ ] **路线甲（不写代码）**：用官方 `model-mapper`（`modelKey` + `modelMapping` + `*`）做映射闸门；鉴权暂留 finops
- [ ] **路线乙（自研）**：新写 `ai-gatekeeper`（鉴权 + 准入 + `/v1/models` 过滤 + 注入 `x-original-model`），`FAIL_CLOSED`；finops 退为纯审计
- [ ] 若仍用 `ai-proxy`：`type` 改 `generic`，删除 `serviceUrl` / `secretRef`，字段名改 `openaiCustomUrl`（如采用 openai 驱动）

**文档层**

- [ ] 恢复 `E1/E2/E3` 实测记录、`R1~R7` 风险登记、22 条映射表、三级回滚、非目标声明
- [ ] 所有"已确认/将执行"的表述必须附证据或标记 `待实测`
- [ ] 版本号与署名以 git 历史为准，不在文档内私自升级

---

## 8. 裁决汇总表

| 她的 6 条 | 裁决 | 我方处置 |
|---|---|---|
| ① 鉴权独立（key-auth 120，三权分立） | ⚠️ 原则对（D2），**组件不存在（B1/B5）** | 改为自研 `ai-gatekeeper` 或官方 `ext-auth`/`opa`；严禁在建新组件前删除 finops 鉴权（A2） |
| ② 状态码写死 200 | ✅ **成立** | 立即修（D1），前置于所有方案动作 |
| ③ `generic` 不存在，改 `openai` + `serviceUrl` | ❌ **双重事实错误** | 保留 `generic`，修正字段名（A1/B2/B3） |
| ④ `gemini-3.8-owner` 前置定性 | ✅ 成立 | 纳入 P0（D3） |
| ⑤ Authorization 双写不延期 | ⚠️ 方向对，方式错 | 改为"实测后二选一"，并在 P1 演练回滚（A3/A4） |
| ⑥ `/v1/models` 过滤 | 🔁 复述原 P4 项 | 保留在 P4，不计为新增贡献（E4） |

---

## 9. 附录：证据复现命令

> 全部命令只读，不修改任何生产资源

### E1 — 官方 wasm 插件清单（证明 `key-auth` 不存在、`ai-proxy` 存在）

```bash
curl -s https://api.github.com/repositories/558188628/contents/plugins/wasm-go/extensions \
  | python3 -c "import sys,json;n=[x['name'] for x in json.load(sys.stdin)];print(len(n));print('key-auth' in n, 'ai-proxy' in n, 'model-mapper' in n)"
# 输出：53 / False True True
```

### E2 — 官网 key-auth 文档

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://higress.cn/docs/plugins/wasm-go/extensions/key-auth/
# 输出：404
```

### E3 — `generic` 与 `openaiCustomUrl` 的官方出处

```bash
curl -s https://raw.githubusercontent.com/higress-group/higress/main/plugins/wasm-go/extensions/ai-proxy/README.md \
  | grep -nE "通用代理|genericHost|openaiCustomUrl|serviceUrl"
# 输出：
# 128:  | `openaiCustomUrl` | …（serviceUrl 无匹配）
# 258:  #### 通用代理（Generic）
# 264:  | `genericHost` | …
```

### E4 — `apiTokens` 类型与 `secretRef`

```bash
curl -s https://raw.githubusercontent.com/higress-group/higress/main/plugins/wasm-go/extensions/ai-proxy/README.md \
  | grep -cE "secretRef"     # 输出：0
curl -s https://raw.githubusercontent.com/higress-group/higress/main/plugins/wasm-go/extensions/ai-proxy/README.md \
  | grep -n "apiTokens" | head -2   # array of string
```

### E5 — 优先级参考值

```bash
curl -s https://raw.githubusercontent.com/higress-group/higress/main/plugins/wasm-go/extensions/model-router/README.md | grep -A2 "运行属性"
# 输出：插件执行阶段：认证阶段 / 插件执行优先级：900
# ai-proxy README 同类段落：插件执行优先级：100
```

### E6 — 状态码写死（本地源码）

```bash
cd plugins/finops-audit && grep -n "statusCode" main.go
# 173: statusCode := 200
# 231: StatusCode: statusCode,
sed -n '/func onHttpResponseHeaders/,/^}/p' main.go
# 仅读取 x-envoy-upstream-service-time，未读取 :status
```

### E7 — 文档被改写的规模

```bash
git diff --shortstat docs/MODEL_GOVERNANCE_PLAN.md   # 1 file changed, 180 insertions(+), 354 deletions(-)
for k in E1 E2 E3 风险登记册 R1 ___not_declared___; do printf '%s %s\n' "$k" "$(grep -c "$k" docs/MODEL_GOVERNANCE_PLAN.md)"; done
# E1 0 / E2 0 / E3 0 / 风险登记册 0 / R1 0 / ___not_declared___ 4
```

### E8 — 上游对未知模型的拒绝行为（方案成立的前提）

```bash
# model=___not_declared___  → HTTP 400（上游拒绝）
# model=claude-opus-5       → HTTP 200（真模型，必须由网关挡）
# model=gemini-4            → HTTP 400（别名今天失效，需 modelMapping 修复）
```

---

## 10. 附：评审所依据的官方文档章节

| 主题 | 出处 |
|---|---|
| `modelMapping` 语义（仅映射，不拒绝） | ai-proxy README ·「配置字段 → provider.modelMapping」 |
| 通用代理 `generic` | ai-proxy README ·「提供商特有配置 → 通用代理（Generic）」 |
| `openaiCustomUrl` | ai-proxy README ·「提供商特有配置 → OpenAI」 |
| `apiTokens`（array of string） | ai-proxy README ·「基本配置 → provider」 |
| `model-mapper`（纯 body 改写） | `plugins/wasm-go/extensions/model-mapper/README.md` |
| `model-router`（model → header，优先级 900） | `plugins/wasm-go/extensions/model-router/README.md` |
| 官方插件全集（53 个） | `api.github.com/repositories/558188628/contents/plugins/wasm-go/extensions` |
