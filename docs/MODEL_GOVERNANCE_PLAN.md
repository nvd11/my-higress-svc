# AI 网关模型治理改造方案

> **目标**：让 `config.yaml` 成为**模型清单（model list）与计费价目**的唯一标准（single source of truth），
> 通过 Higress 官方 `ai-proxy` 插件的 `modelMapping` 落地准入、别名与改写，废除"网关纯透传、上游 143 个模型全放行"的现状。
>
> **状态**：方案已评审，待 P0 启动
> **日期**：2026-10-24
> **备份点**：分支 `backup/pre-modelmapping-20261024` / 标签 `backup-pre-modelmapping-20261024`（`1cfa372`）
> **关联已完成工作**：`4fc6abd`（价目改由 config.yaml 构建期生成）、`7b9acb5`（deepseek 输出价/缓存价校准，已上线）

---

## 1. 背景

### 1.1 现状实测（证据）

| 事实 | 证据 |
|---|---|
| 网关是**纯 L7 透传**，没有任何模型层逻辑 | envoy `config_dump`：唯一路由 `*` → `outbound\|443\|\|a6api.dns`；`ai-proxy` 插件配置出现 **0 次** |
| 上游怎么答，网关就怎么转 | `GET /v1/models` 经网关与直连 `api.a6api.com` 均返回 **143 个模型、17744 字节**，id 列表**完全一致** |
| 模型清单只存在于文档层 | `config.yaml` 声明 **10 个模型 / 22 个键**（含 12 个别名），但全仓库**无任何代码加载它作为准入依据** |
| 别名（aliases）**从未生效** | 实测 `model: gemini-4`（config 中的别名）→ 上游直接 **HTTP 400**，因为上游只有 `gemini-4-argon` |
| 计费已收敛到 config.yaml | `pricing_gen.go` 由 `internal/pricinggen` 构建期生成，头部带 config.yaml sha256 指纹，CI 有漂移守卫 |
| 鉴权已在 wasm 插件内 | `plugins/finops-audit/main.go` 对无 token/非法 token 返回 401（`failStrategy: FAIL_OPEN`，`priority: 90`） |

### 1.2 需要解决的 5 个行为

| # | 行为 | 现状 |
|---|---|---|
| ① | **准入**：哪些模型允许调用 | ❌ 143 个全放行，含 `definitely-not-a-model`、`claude-opus-5` 等 |
| ② | **别名映射**：自定义名 → 上游真名 | ❌ 原样透传，`aliases` 是死配置 |
| ③ | **对外宣告**：`/v1/models` 返回什么 | ❌ 直通上游，吐 143 个 |
| ④ | **拒绝契约**：不在名单里回什么 | ❌ 无（由上游决定，且上游对未知模型返回的错误文案与模型无关） |
| ⑤ | **元数据**：渠道 / 密钥别名 / 价目 | ✅ 已由 config.yaml 派生并上线 |

---

## 2. 改造目标

### 2.1 总目标

> **凡不在 `config.yaml` 里声明的模型，一律不可通过网关调用；凡在别名表里的名字，一律正确路由到主名。
> 以上行为全部由 `config.yaml` 单向驱动，不允许存在第二份手工清单。**

### 2.2 可验收目标（5 条）

| # | 验收标准 | 判定方法 |
|---|---|---|
| G1 | 调用未声明模型返回 **4xx**，且不产生上游计费 | 用 `definitely-not-a-model` 打网关，观察状态码与 A6 账单 |
| G2 | 12 个别名全部可用（今天全部不可用） | 逐个用别名请求，断言 200 且响应 `model` 为主名 |
| G3 | `config.yaml` 新增/删除模型 → 网关行为随之改变，**不改 Go 代码** | 改一条 + push，观察生效 |
| G4 | `pricing_gen.go` 与 `ai-proxy-config.yaml` 指纹同源、CI 防漂移 | 故意改 config.yaml 不重新生成 → 流水线红 |
| G5 | 审计链路不退化（`model_requested` 仍为客户端原始名、流式 usage 可聚合） | 对比改造前后同一条请求的落库字段 |

### 2.3 非目标（本期不做）

- 按虚拟 Key 分配可用模型（`cindy` 只能调 deepseek 之类）—— 需要 `virtual_keys` 结构升级，列入二期
- 模型级 fallback（`config.yaml` 的 `fallback:` 字段当前为死配置，处置见 P4）
- 多上游/多云分流（同一模型多提供商比价路由）

---

## 3. 方案选型

### 3.1 三个候选

| 方案 | 落点 | 简述 |
|---|---|---|
| **A** | 现有 `finops-audit` wasm | 在 `onHttpRequestBody` 里查表，未声明返 400 |
| **B** | 新建 `model-guard` wasm | 独立插件，单一职责 |
| **C（选定）** | 官方 `ai-proxy` wasm + `modelMapping` | 纯声明式：别名映射 + `*` 兜底闸门 |

### 3.2 对比

| 维度 | A | B | **C** |
|---|---|---|---|
| 需要写 Go 代码 | 中 | 中 | **零**（纯配置） |
| 改 config.yaml 即生效 | 需重新构建 wasm | 需重新构建 wasm | **只需重新生成配置** |
| 别名映射 | 自研 | 自研 | **原生支持**（前缀/正则/兜底） |
| 拒绝的干净度 | 最好（自定 400） | 最好 | **差**（借上游 400） |
| 对现有审计链的冲击 | 小 | 中 | **大（需重验 6 个点）** |
| 回滚速度 | 需回滚代码 + 镜像 | 删插件 | **删插件，30 秒** |

### 3.3 决策与代价声明

选择 **C**，理由：它是唯一能做到"**改 config.yaml 即生效、零 Go 代码**"的路径，且回滚成本最低。

**必须同时接受的代价（不做隐藏）**：

1. `modelMapping` **不具备拒绝能力**，白名单只能"借上游的手"（见 §4）。
2. 被拒请求的错误文案由上游给出（不是标准 `model_not_found`），且**会进入 A6 的调用日志**。
3. `ai-proxy` 会介入转发链路，与我们刚修好的审计/整形链路存在 6 个耦合点，必须逐项实测（见 §5.6）。

---

## 4. 关键技术结论（均已实测，非推断）

### 4.1 `modelMapping` 的真实语义

> 官方文档（Higress 2.2.5 对应版本）：`modelMapping` —— "AI 模型映射表，**用于将请求中的模型名称映射为服务提供商支持模型名称**"

| 能力 | 是否具备 |
|---|---|
| 精确键映射 | ✅ |
| 前缀通配 `gpt-3-*` | ✅ |
| 全通配 `*` 兜底 | ✅ |
| 正则 `~gpt(.*)` + 捕获组 | ✅ |
| 目标写 `""` = 保留原名 | ✅ |
| **未映射则拒绝** | ❌ **不拒绝，原样转发** |
| 白名单 / 计价 / 渠道元数据 | ❌ 不做 |

### 4.2 三次真实探测（直连上游，2026-10-24）

| # | 请求 `model` | 上游响应 | 结论 |
|---|---|---|---|
| E1 | `___not_declared___` | **HTTP 400** | 上游会拒绝未知模型名 → `*` 闸门机制成立 |
| E2 | `claude-opus-5` | HTTP 200 | 它是真模型，**必须由我们挡** |
| E3 | `gemini-4`（config 中的别名） | **HTTP 400** | 别名今天完全失效，改造后将被映射修复 |

### 4.3 由结论推导出的三条设计约束

1. **`*` 兜底是唯一闸门** → 10 个主名**必须逐个写恒等映射**（`"x": "x"`），否则主名会被兜底改名后误杀。
2. **拒绝发生在上游** → P3b 合闸前必须先把"会被拒的真实流量"清点完（附录 A）。
3. **别名与白名单共用同一张表** → 生成器必须一次性输出，不能分两次生成（否则漂移）。

---

## 5. 详细设计

### 5.1 架构：现状 vs 目标

```
【现状】
client ──▶ Higress Ingress (path /) ──▶ [finops-audit wasm: 鉴权 + 审计]
                                        └─▶ a6api.dns（模型原样透传，143 个全通）

【目标】
client ──▶ Higress Ingress (path /) ──▶ [ai-proxy wasm: modelMapping 改写 + * 闸门]
                                        ├─ 未声明 → 改名 ___not_declared___ → 上游 400
                                        └─ 已声明 → 主名/别名 → 上游真名
                                        └─▶ [finops-audit wasm: 鉴权 + 审计]
                                        └─▶ a6api.dns
```

> **待 P1 实测**：两个 wasm 的执行顺序（`ai-proxy` 优先级 100，`finops` 90）决定审计插件
> 看到的是"客户端原始名"还是"改写后的名字"。若顺序不利，需在审计侧记录"改写前"的值。

### 5.2 `config.yaml` 仍是唯一源（生成链）

```
config.yaml  ──┬─▶ internal/pricinggen ──▶ pkg/finops/pricing_gen.go        （价目，已上线）
               └─▶ internal/pricinggen ──▶ ai-proxy-config.yaml             （映射 + 闸门，本次新增）
CI：两处均做 git diff --exit-code 漂移守卫，指纹均为 config.yaml 的 sha256
```

### 5.3 生成物规格：`ai-proxy-config.yaml`（由 config.yaml 自动生成，示例）

| 请求中的 `model` | 改写为（上游真名） | 类型 |
|---|---|---|
| `gemini-4-argon` | `gemini-4-argon` | 主名 |
| `gemini-4` | `gemini-4-argon` | 别名 |
| `gemini-4-flash` | `gemini-4-argon` | 别名 |
| `gemini-4-argon-search` | `gemini-4-argon` | 主名 |
| `gemini-4:search` | `gemini-4-argon` | 别名 |
| `gemini-4-search` | `gemini-4-argon` | 别名 |
| `gemini-3.8-flash` | `gemini-3.8-flash` | 主名 |
| `gemini-3.8` | `gemini-3.8-flash` | 别名 |
| `gemini-2.5-flash` | `gemini-3.8-flash` | 别名 |
| `gemini-3.8-flash-search` | `gemini-3.8-flash` | 主名 |
| `gemini-3.8-flash:search` | `gemini-3.8-flash` | 别名 |
| `kimi-k3` | `kimi-k3` | 主名 |
| `kimi` | `kimi-k3` | 别名 |
| `glm-5.3` | `glm-5.3` | 主名 |
| `glm` | `glm-5.3` | 别名 |
| `hy3` | `hy3` | 主名 |
| `hunyuan3` | `hy3` | 别名 |
| `hy4-preview` | `hy4-preview` | 主名 |
| `gpt-5.6-luna` | `gpt-5.6-luna` | 主名 |
| `luna` | `gpt-5.6-luna` | 别名 |
| `gpt-5.6-luna-a6` | `gpt-5.6-luna` | 别名 |
| `deepseek-v4-pro-0813` | `deepseek-v4-pro-0813` | 主名 |
| **`*`** | **`___not_declared___`** | **闸门** |

产物形态：

```yaml
provider:
  type: generic                      # generic = 只做鉴权/转发，不绑定厂商、不改协议
  apiTokens: ["${A6_API_KEY}"]       # P4 迁移为 Secret 引用，禁止明文入仓
  modelMapping:
    # 主名恒等映射（必须逐个显式声明）
    "gemini-4-argon": "gemini-4-argon"
    ...
    # 别名 → 上游真名
    "gemini-4": "gemini-4-argon"
    ...
    # 闸门
    "*": "___not_declared___"
```

### 5.4 白名单闸门机制

```
客户端 model=X
  ├─ X 在映射表（主名或别名） → 改写为上游真名 → 200
  └─ X 不在映射表            → 被 * 改写为 ___not_declared___ → 上游 400
```

**已知副作用**：被拒请求会出现在 A6 调用日志中（脏数据），且错误文案为上游的"请求参数不正确"。
**缓解**：P4 若引入窄插件，可由网关直接返回标准 `model_not_found`，并停止透传。

### 5.5 错误契约

| 来源 | 状态码 | 响应体 |
|---|---|---|
| 网关（鉴权，现状） | 401 | `{"error":{"message":"Invalid Virtual Key","type":"invalid_request_error","code":"invalid_api_key"}}` |
| 上游（未声明模型，C 方案） | 400 | 上游原文（非标准，含"目标接口不支持的字段"字样） |
| 网关（窄插件，P4 可选） | 400 | `{"error":{"message":"Model 'X' is not declared in gateway model_list","type":"invalid_request_error","code":"model_not_found"}}` |

### 5.6 与现有审计链的 6 个耦合点（P1 必须逐项实测）

| # | 耦合点 | 风险 | 缓解方向 |
|---|---|---|---|
| 1 | wasm 执行顺序（ai-proxy 100 / finops 90） | 审计记到的是改写后名字 → 账目失真 | 让审计记录"改写前"的值，或调整优先级 |
| 2 | 响应体 `model` 字段被改写 | 前端展示模型名变化 | 前端以 `model_requested` 为准 |
| 3 | 流式 SSE 结构 | `normalize.go` 的 usage 聚合可能失效（刚修复） | 实测 + 回归用例 |
| 4 | `/v1/models` 是否被 ai-proxy 接管 | 大概率仍直通 → 对外仍宣告 143 | 置为 P4 的窄插件职责 |
| 5 | `x-request-id` 是否被覆盖 | WAL 幂等键 `md5(request_id+created_at)` 依赖它 | 实测；必要时审计侧改用上游 id |
| 6 | Authorization 双写（Ingress annotation + `apiTokens`） | 重复头导致上游鉴权失败 | P4 移除 Ingress 明文注入 |

### 5.7 `/v1/models` 的处置

| 阶段 | 行为 |
|---|---|
| P3 | 保持现状（直通上游 143 个），仅内部白名单生效 |
| P4 | 二选一：① 窄插件过滤响应为 10 个主名；② 若 ai-proxy 原生支持则用原生能力 |

**理由**：宣告与准入不一致属于"体验问题"，不是"安全问题"，不应阻塞 P3 上线。

### 5.8 上游第二道防线（P4）

A6 的**令牌本身支持限制可用模型**。将网关使用的令牌在上游限定为这 10 个模型，
即形成一道**不依赖 wasm 的硬防线** —— wasm 崩溃/回滚期间白名单仍然有效。

---

## 6. 实施计划 P0 ~ P4

### P0 — 纯本地：把 config.yaml 编译成 modelMapping

| 项 | 内容 |
|---|---|
| 位置 | 本地 + 分支 `feat/ai-proxy-modelmapping`（建议配独立 `git worktree`） |
| 动作 | 扩展 `internal/pricinggen` 输出 `ai-proxy-config.yaml`；补单测（主名恒等、别名指向、闸门存在、指纹一致） |
| 产出 | 生成物 + 单测 + 生成器改造 |
| 验收 | `go test ./...` 全绿；改 config.yaml 后生成物随之变化；两处指纹一致 |
| 影响生产 | ❌ 无 |
| 回滚 | 删分支 |
| 估时 | 2 h |

### P1 — 隔离验证：`ai-lab` namespace 实测 6 个耦合点

| 项 | 内容 |
|---|---|
| 位置 | 集群 namespace `ai-lab`（**不触碰 `higress-system` 生产路由**） |
| 动作 | 部署 `ai-proxy` WasmPlugin + 影子 Ingress + **mock 上游（echo pod）**，用 mock 直接观察"上游收到的报文" |
| 必答 6 问 | §5.6 全线 |
| 产出 | 实测报告（含原始报文、状态码、执行顺序结论） |
| 验收 | 6/6 有明确结论；形成"是否需要窄插件"的决策依据 |
| 影响生产 | ❌ 无 |
| 回滚 | `kubectl delete ns ai-lab` |
| 估时 | 2 h |

### P2 — 代码化 + CI 护栏（仍未上线）

| 项 | 内容 |
|---|---|
| 位置 | feature 分支 → PR |
| 动作 | `ai-proxy-config.yaml` 入库；CI 加漂移校验；按 P1 结论决定是否加窄插件；`wasm-plugin.yaml` 保持不动 |
| 产出 | 可合并的 PR |
| 验收 | PR 全绿、review 通过 |
| 影响生产 | ❌ 无（镜像推送/GitOps 回写均有 `if: refs/heads/main` 保护） |
| 回滚 | 关闭 PR |
| 估时 | 2~3 h |

### P3 — 灰度上线（首次动生产，两个闸）

| 子阶段 | 动作 | 观察项 |
|---|---|---|
| **P3a** | 合并 main：挂 `ai-proxy`，`*` 指向自身（不拦截），仅让 12 个别名生效 | 24 h：`model_requested` 是否失真、流式 usage 正常否、`x-request-id` 稳定否、账单是否对齐 |
| **P3b** | `*` 切至 `___not_declared___` | 被拒清单是否符合附录 A 的预判 |

| 项 | 内容 |
|---|---|
| 影响生产 | P3a 仅别名复活（可用性不变）；P3b 未纳管模型立即 400 |
| 回滚 | 秒级：`kubectl -n higress-system delete wasmplugin ai-proxy` |
| 估时 | P3a 0.5 h + 观察 24 h；P3b 0.5 h |

### P4 — 硬化收尾

| # | 事项 | 理由 |
|---|---|---|
| 1 | `/v1/models` 过滤为 10 个主名 | 否则"唯一标准"只做了一半 |
| 2 | Ingress 明文 A6 Key → Secret 引用 | 已体检出的 L-2 风险 |
| 3 | A6 令牌限模型（上游第二道防线） | wasm 失效时的兜底 |
| 4 | `config.yaml` 的 `fallback:` 字段处置 | 当前为死配置（ai-proxy 的 retry 不换模型）：删除或重新定义 |
| 5 | 文档更新（COMPATIBILITY / ARCHITECTURE） | 消除过期描述 |
| 估时 | 3 h |

### 阶段总表

| 阶段 | 环境 | 动生产 | 交付物 | 回滚 | 估时 |
|---|---|---|---|---|---|
| P0 | 本地/分支 | ❌ | `ai-proxy-config.yaml` + 单测 | 删分支 | 2 h |
| P1 | ai-lab | ❌ | 6 问实测报告 | 删 ns | 2 h |
| P2 | 分支/PR | ❌ | PR + CI 护栏 | 关 PR | 2~3 h |
| P3a | main | ⚠️ 挂插件不拦截 | 别名生效 | 删 WasmPlugin | 0.5 h + 24 h |
| P3b | main | ✅ 白名单合闸 | 未纳管模型 400 | 删 WasmPlugin | 0.5 h |
| P4 | main | ✅ 逐步 | 过滤 / Secret / 上游限量 / 文档 | 逐项 revert | 3 h |

---

## 7. 风险登记册

| ID | 风险 | 等级 | 影响 | 应对 |
|---|---|---|---|---|
| R1 | 合闸误杀真实流量 | **高** | 业务中断 | 先 P3a 观察 24 h；附录 A 清单逐条确认；秒级回滚 |
| R2 | 审计链被 ai-proxy 改写破坏 | **高** | 账目/报表失真 | P1 全量实测；必要时审计改记"改写前"字段 |
| R3 | 上游错误文案不友好 | 中 | 客户端解析困难 | P4 窄插件返回标准 `model_not_found` |
| R4 | 被拒请求污染 A6 调用日志 | 低 | 账单侧噪声 | 接受；或用窄插件改为网关侧拒绝 |
| R5 | 明文 Key 双写冲突 | 中 | 上游 401 | P1 实测第 6 点；P4 迁 Secret |
| R6 | `ai-proxy` 插件镜像版本与本集群 2.2.5 不兼容 | 中 | 无法部署 | P1 先验证拉取与加载 |
| R7 | 多实例共用工作目录导致提交混杂 | 中 | 代码事故（历史已发生） | feature 分支 + 独立 worktree |

---

## 8. 回滚预案（三级）

| 级别 | 场景 | 动作 | 生效时间 |
|---|---|---|---|
| L1 | 白名单误杀 / 转发异常 | `kubectl -n higress-system delete wasmplugin ai-proxy` | ~30 s |
| L2 | 配置内容有问题（映射写错） | 改 `ai-proxy-config.yaml` 重新生成并合并；或临时把 `*` 指回自身 | 分钟级 |
| L3 | 代码/仓库层面整体回退 | `git reset --hard backup/pre-modelmapping-20261024 && git push -f` | 分钟级 |

---

## 9. 变更管理与分支策略

| 规则 | 说明 |
|---|---|
| `main` 一推即上线 | ArgoCD `higress-svc` 应用跟踪 `main`，`automated + prune + selfHeal`；`config.yaml` 变更还会额外触发 `sync-config` 向 `my-argocd-manifests` 派发 |
| 开发在 feature 分支 | `feat/ai-proxy-modelmapping`；PR 只跑测试与构建，不推镜像、不回写 GitOps |
| 独立 worktree | 仓库工作目录曾被其他实例并发提交（`674d87f` 事故），本次改造跨 5 阶段，必须物理隔离 |
| 提交粒度 | 改完即提交，不留脏工作区窗口 |
| 单一数据源 | 任何模型/价格变更只改 `config.yaml`，禁止直接改生成物 |

---

## 10. 附录

### A. 近 7 天出现的**未纳管**模型（P3b 后将被拒）

| 模型 | 7 天请求数 | 备注 |
|---|---|---|
| `gemini-3.8-owner` | 15 | ⚠️ **上线前必须确认使用方** |
| `qwen3.8-flash` | 2 | 疑似探测 |
| `qwen3.8-max` | 1 | 探测 |
| `grok-4.7` | 1 | 探测 |
| `claude-sonnet-5` | 1 | 探测 |
| `claude-opus-5` | 1 | 探测 |
| `minimax-m3` | 1 | 探测 |
| `deepseek-v4-flash` | 1 | 探测 |
| `doubao-seedance-2.5` | 1 | 探测 |
| `glm-5.3-flash` | 1 | 探测 |
| `gpt-6-luna` | 1 | 探测 |
| `definitely-not-a-model` | 1 | 垃圾数据（改造后将被天然拒绝） |
| `unknown` | 1 | 垃圾数据 |

### B. 实测原始输出（2026-10-24）

```
E1  model=___not_declared___  → HTTP 400  {"error":{"message":"请求参数不正确…"}}
E2  model=claude-opus-5       → HTTP 200  {"id":"msg_011CgMC42…","model":"claude-opus-5",…}
E3  model=gemini-4            → HTTP 400  {"error":{"message":"请求参数不正确…"}}

GET /v1/models  经网关   → HTTP 200, 143 个, 17744 字节
GET /v1/models  直连上游 → HTTP 200, 143 个, 17744 字节   （id 列表完全一致）
```

### C. 相关文件

| 文件 | 角色 |
|---|---|
| `config.yaml` | **唯一标准**：模型清单 + 别名 + 渠道 + 价目 |
| `plugins/finops-audit/internal/chargeconfig/spec.go` | config.yaml 严格解析器（零依赖） |
| `plugins/finops-audit/internal/pricinggen/main.go` | 构建期生成器（价目，将扩展到映射） |
| `plugins/finops-audit/pkg/finops/pricing_gen.go` | 生成物：价目 |
| `plugins/finops-audit/pkg/finops/pricing_sync_test.go` | 漂移守卫测试 |
| `plugins/finops-audit/main.go` | 鉴权 + 审计（401 / WAL 预写 / 上报） |
| `wasm-plugin.yaml` | `finops-audit` 插件挂载（priority 90, FAIL_OPEN） |
| `ingress.yaml` | 纯透传路由 + 明文 A6 Key 注入（P4 待迁 Secret） |
| `.github/workflows/wasm-plugin-ci-cd.yml` | 构建 + 漂移守卫 + GitOps 回写 |
