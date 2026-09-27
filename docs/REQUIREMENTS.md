# my-higress-svc 需求规格说明书 (Requirements Specification)

> **文档版本**：v1.0.0 · **创建日期**：2026-09-28 · **核心维护者**：Jason & Cindy ❤️  
> **所属领域**：云原生大模型接入网关 (AI Gateway) / FinOps 财务审计 / 高性能流量代理

---

## 1. 项目背景与痛点溯源

在当前的大模型基础设施中，我们长期依托 Python 体系的 **LiteLLM Proxy** 提供多模型路由、虚拟 Key 分发与 API 转发。然而随着 Agent 开发场景深入，上下文体量频繁达到 **350k ~ 1M Tokens**（如大型代码库扫描、全量日志诊断），Python 解释型架构暴露出了不可忽视的性能天花板：

1. **Python GIL 与事件循环卡顿（Event Loop Lag）**：
   - 700k+ Tokens 的单次请求 JSON 体积高达 3MB ~ 5MB。
   - LiteLLM 必须在 Python 单线程主事件循环中，递归遍历、深拷贝并重构整个庞大的消息树，将其从 OpenAI 格式翻译为 Vertex/Gemini 格式。
   - 这会导致事件循环产生秒级阻塞，极易引发从上游接收 SSE 数据流时的 TCP 接收窗口耗尽（Zero Window），诱发 Google 端主动掐线（`Connection closed` / `Deadline exceeded`）。
2. **多级代理级联导致故障面扩大**：
   - 现存拓扑链路：`Client ➔ OCI ALB ➔ Kong Gateway ➔ LiteLLM Pod ➔ Google Gemini`。
   - 中间经历 4 层网络跳数。Kong 在面对 >16KB 的大请求体时强制落盘缓冲临时文件（`/kong_prefix/client_body_temp`），进一步放大了磁盘 I/O 延迟和脆弱窗口。
3. **伴生缓存组件脆弱**：
   - 长上下文任务的单次 Payload 快照体积庞大，缓存至 Redis 时极易导致实例达到物理上限（如 2.0GB OOM），进而反噬 LiteLLM 导致级联熔断。

为从根本上解决超长上下文和高频 Agent 调用的高吞吐、零延迟损耗需求，本项目决定引入基于 **Envoy C++ 内核与 Wasm-Go 扩展生态的阿里开源旗舰级 AI 网关 —— Higress**，实现网关底座的高性能进化。

---

## 2. 系统定位与核心愿景

`my-higress-svc` 定位于**极速、轻量、高可用的次世代云原生 AI 网关**。其核心使命是：

> **“以 C++ 内核承载极限吞吐，以 Wasm-Go 插件驱动业务旁路，100% 继承并复活现有 FinOps 可观测资产。”**

```
                    开发者 / OpenCode / 业务终端
                               │
                               ▼
        ┌──────────────────────────────────────────────┐
        │       Higress AI Gateway (Envoy C++ 内核)     │
        │  - 原生 C++ / Go 高性能 ai-proxy              │
        │  - 零内存拷贝 SSE 流式透传                   │
        │  - 极速路由分发与 Fallback 容灾               │
        │  ┌────────────────────────────────────────┐  │
        │  │  自定义 Wasm-Go 旁路审计插件 (Cindy 编写) │  │
        │  └──────────────────┬─────────────────────┘  │
        └─────────────────────┼────────────────────────┘
                              │
               ┌──────────────┴──────────────┐
               ▼                             ▼
       [结构化审计入库]              [巨型报文冷归档]
     OCI MySQL HeatWave             StarFive 边缘节点
    (`llm_request_logs`)          VictoriaLogs (gzip 压缩)
               │                             │
               └──────────────┬──────────────┘
                              ▼
        ┌──────────────────────────────────────────────┐
        │  现有 React 18 / Tailwind 可观测看板 (保持 100% 兼容) │
        │  - 实时 Token 流水与调用趋势大屏               │
        │  - 抽屉式原始报文秒级透视 (Payload Drawer)     │
        │  - 每日中行实时汇率折算 (USD -> CNY)          │
        └──────────────────────────────────────────────┘
```

---

## 3. 功能性需求 (Functional Requirements)

### F1: 统一 OpenAI 兼容接入层
- 提供标准化的 OpenAI 补全协议端点：
  - `POST /v1/chat/completions` (支持非流式与流式 SSE)
  - `GET /v1/models` (列出当前授权的模型清单)
  - `GET /health/readiness` & `GET /health/liveliness` (探活与就绪探针)
- 完美兼容主流客户端：OpenCode, Claude Code, Codex, Cursor, LangChain 及本地自主 Agent。

### F2: 多模型路由与多提供商生态
- **主力原生组**：
  - `gemini-3.8-flash` & `gemini-3.7-flash`：直连 Google AI Studio / Gemini 原生 REST 接口，支持 1M 超长上下文与深度思考（Thinking/Reasoning）特性。
- **应急保底组**：
  - `gemini-3.8-backup`：接入 A6 API 渠道。
  - `kimi-k3`、`glm-5.3`、`gpt-5.6-luna`：接入高可用中转渠道。
- **本地 Agent 直通**：
  - `yui` & `rin`：私网直连本地 Hermes Agent，保留原生 `tool.progress` 流式状态。

### F3: 智能熔断降级与重试机制 (Fault-Tolerance & Failover)
- 支持基于模型故障（HTTP 429, 500, 502, 503, 504）的自动降级（Fallback List）；
- 支持极速熔断判定（失败 1 次即触发）与轻量冷却解禁（冷却期 5s）；
- 支持请求重试机制（默认 3 次退避重试）。

### F4: 自研 Wasm-Go 财务审计与汇率对账插件 (FinOps Extension)
- **非阻塞旁路设计**：主请求与流式数据流绝对优先，审计与落库逻辑在流结束后异步执行，绝不增加网关主流程延迟。
- **Token 精确统计**：准确抓取 Prompt Tokens、Completion Tokens 以及思考（Reasoning）Tokens。
- **实时汇率换算**：接入中国银行/实时外汇牌价缓存，自动将 `cost_usd` 折算为人民币 `cost_cny`。
- **数据结构兼容**：异步向 OCI MySQL HeatWave 的 `llm_request_logs` 数据表插入完备的记录，字段与现有系统 100% 保持一致。

### F5: 报文冷热解耦与 VictoriaLogs 归档
- 将单次请求的原始输入（Prompt）与最终响应（Response）进行 Gzip 传输压缩（`compresslevel=1`）；
- 异步批量投递至 StarFive 星光板上的 **VictoriaLogs** 实例进行全文检索与永久保存；
- 在存储前对图片进行正则穿透折叠（防止超大 Base64 污染索引），保留元数据标记。

### F6: Dashboard 可观测大屏代码完美迁移与自闭环构建 (Dashboard Native Migration)
- **工程资产全量迁移**：将原 `my-litellm-service` 项目中成熟的 Dashboard 资产整体搬迁至本项目（`my-higress-svc`），实现“高性能 AI 网关 + 专属可观测大屏”一体化自闭环交付，彻底脱离对老旧 Python 项目的依赖。
- **前端工程完美移植**：将完整的 React 18 + Vite + Tailwind CSS 前端代码迁移至 `frontend/` 目录，完整保留实时 Token 流水大屏、模型费用环形图、每日消耗趋势折线图以及**核心的抽屉式报文透视（Payload Drawer）**界面。
- **查询与透视后端 API 移植**：完整迁移提供看板数据源的后端服务模块，保持 `/api/v1/logs`、`/api/v1/metrics/summary` 以及 `/api/v1/logs/{request_id}/payload` 接口契约 100% 兼容，原生连通 OCI MySQL HeatWave 与 StarFive VictoriaLogs。
- **多阶段容器自包含构建**：在 Dockerfile 中通过多阶段构建（Stage 1: Node 20 编译 React SPA ➔ Stage 2: 运行时挂载），实现单镜像内置大屏静态资产与查询接口，开箱即用。

---

## 4. 非功能性需求 (Non-Functional Requirements)

| 指标维度 | 现存 LiteLLM 指标 | Higress 目标交付指标 | 优化收益 |
| :--- | :---: | :---: | :---: |
| **首字延迟开销 (TTFT Proxy Overhead)** | 15ms ~ 50ms | **< 1ms** | 提升 **15~50倍** |
| **700k 超长上下文处理延迟** | 容易发生 2~5s 事件循环阻塞 | **< 20ms** (纯 C 内存池拷贝) | 消除事件循环假死 |
| **常驻运行时内存** | 400MB ~ 2.0GB (易 OOM) | **< 80MB** | 内存开销缩减 **90%+** |
| **单机并发吞吐 (QPS)** | ~200 QPS (Python GIL 瓶颈) | **> 5,000 QPS** | 并发吞吐提升 **25倍** |
| **多级网络跳数** | 4 跳 (`ALB -> Kong -> LiteLLM -> Upstream`) | **2 跳** (`ALB -> Higress -> Upstream`) | 减少 50% 网络节点 |

---

## 5. 安全与运维红线

1. **凭证隔离与安全红线**：
   - 绝不硬编码任何真实的 API Key、数据库密码或服务账号凭据；
   - 统一遵循 Kubernetes Secret / 环境变量注入规范。
2. **读写分离与零影响过渡**：
   - 在新网关彻底完成验收前，不触动现有线上稳定的业务流量与主路由；
   - 采用灰度或独立子域并行部署验证。
