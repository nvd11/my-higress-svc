# my-higress-svc 核心技术架构设计书 (Architecture Design)

> **文档版本**：v1.0.0 · **编制时间**：2026-10-03 · **核心主笔**：Cindy (主人贴身大秘❤️)  
> **战略目标**：以 Envoy C++ 为高性能内核，构建微秒级延迟、兼具财务审计与大报文归档的高可用 AI Gateway。

---

## 1. 全局系统架构拓扑 (Overall Architecture)

```text
       ┌────────────────────────────────────────────────────────┐
       │     客户端生态 (OpenCode / Claude Code / Codex / Agents) │
       └───────────────────────────┬────────────────────────────┘
                                   │ HTTPS / HTTP/2
                                   ▼
       ┌────────────────────────────────────────────────────────┐
       │   OCI ALB (永久免费 Layer 7 负载均衡器 161.118.240.179) │
       └───────────────────────────┬────────────────────────────┘
                                   │ 灰云同网段内网穿透 (<0.2ms 延迟)
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│               Higress AI Gateway (运行于 OCI free-arm-vm 4C24G)             │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ 1. 协议转换与模型路由层 (ai-proxy 核心引擎)                             │  │
│  │    • POST /v1/chat/completions (OpenAI 协议 ➔ Google Gemini 原生格式)   │  │
│  │    • 熔断降级 (Fallback): gemini-3.8-flash ➔ gemini-3.8-backup (A6)    │  │
│  │    • 深度推理模型: kimi-k3 ⇄ glm-5.3 ➔ gpt-5.6-luna (A6 / 元衡通道)     │  │
│  │    • 零内存拷贝 SSE 流式透传 (Zero-Copy Transfer)                      │  │
│  └──────────────────────────────────┬────────────────────────────────────┘  │
│                                     │                                       │
│  ┌──────────────────────────────────▼────────────────────────────────────┐  │
│  │ 2. 裸流直通通道 (Direct Pass-through Route)                            │  │
│  │    • /hermes/yui ➔ 直通本地 100.115.214.26:8644 (保留 tool.progress)    │  │
│  │    • /hermes/rin ➔ 直通本地 100.115.214.26:8642                         │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                     │                                       │
│  ┌──────────────────────────────────▼────────────────────────────────────┐  │
│  │ 3. Wasm-Go 财务审计与冷归档插件 (finops-audit.wasm)                      │  │
│  │    • 挂载于 OnHttpResponseBody 阶段 (仅在响应流结束/EOS 时触发旁路逻辑)     │  │
│  │    • 解析 Usage: Prompt / Completion / Reasoning Tokens 准确提取       │  │
│  │    • 正则穿透折叠 Base64 图片，保护冷存储空间                             │  │
│  └─────────────────┬─────────────────────────────────┬───────────────────┘  │
└────────────────────┼─────────────────────────────────┼──────────────────────┘
                     │                                 │
     (A) 异步 HTTP POST JSONL (Gzip)    (B) 异步 HTTP POST 结构化日志
                     │                                 │
                     ▼                                 ▼
      ┌─────────────────────────────┐   ┌─────────────────────────────┐
      │ StarFive 星光板 VictoriaLogs │   │   Dashboard-API 内部写端点  │
      │   (巨型原始报文无损冷存储)   │   │  (POST /api/v1/internal/log)│
      └─────────────────────────────┘   └──────────────┬──────────────┘
                                                       │ SQLAlchemy 连接池
                                                       ▼
                                        ┌─────────────────────────────┐
                                        │     OCI MySQL HeatWave      │
                                        │     (`llm_request_logs`)    │
                                        └─────────────────────────────┘
```

---

## 2. 关键设计决策与技术选型 (Key Decisions)

| 决策点 | 选定方案 | 决策依据与架构收益 |
| :--- | :--- | :--- |
| **Reasoning Tokens 计量** | **在 MySQL 表中新增 `reasoning_tokens` 独立字段** | Gemini 3.8 等推理模型具备独立 `thoughts_token_count`。独立建列既保全了旧有统计逻辑，又能精准核算深度思考的财务消耗。 |
| **MySQL 落库中转** | **复用 `dashboard-api` 提供内部接收端点** | Wasm-Go 沙箱受限于底层 Envoy ABI，**只支持 HTTP 客户端调用**，无法直接驱动 MySQL 二进制 TCP 协议。复用已有的 Python/FastAPI 模块，无需额外维护常驻 Sink 微服务，最大化节约 4C24G 实例内存。 |
| **Wasm 编译工具链** | **标准 Go 1.24+ 配合 `GOOS=wasip1 GOARCH=wasm`** | 放弃 TinyGo，消除其 GC、反射受限及序列化第三方库报错的隐患；使用官方成熟的 Higress wasm-go SDK 构建跨平台标准字节码。 |
| **缓存层策略** | **暂时移除 Redis 响应缓存** | Agent 场景下的巨型 Prompt 几乎无命中率，且历史发生过 2.0GB 报文快照反噬 OOM 的惨痛教训。网关专注于“极致吞吐与轻量”，去除多余状态依赖。 |
| **多模型收敛** | **全系淘汰 3.7，主力与保底全部收敛为 Gemini 3.8** | 简化 Fallback 链路为一级快速降级：`gemini-3.8-flash (原生)` ➔ `gemini-3.8-backup (A6 API)`。 |
| **控制面持久化与 Virtual Key 存储** | **零外部数据库依赖 (Stateless) + 基于 K8s etcd 的 Consumer CRD** | 彻底抛弃旧版 LiteLLM 对 Neon PostgreSQL 的沉重依赖。网关配置与对外分发的 Virtual Key (如分发给 Jayden 等业务端) 统一通过 Kubernetes 原生 `Consumer` CRD 声明，数据稳固持久化于 K3s 内部 etcd，由 Envoy 在 C++ 内存建立 $O(1)$ 哈希表进行微秒级鉴权，鉴权元数据直通 FinOps 审计账本。 |
| **K8s 交付与 CRD 纳管** | **方案 A：基于官方 Helm Chart 的 ArgoCD 一键全托管** | 彻底摒弃手动维护 CRD 定义的繁重负担。由 ArgoCD 声明式拉取官方 Higress Helm 仓库，自动装配 Controller、数据面 Pod 与扩展 CRD (`WasmPlugin`, `McpBridge`)，业务仓库仅维护差异化 `values.yaml` 与路由清单。 |

---

## 3. Wasm 审计插件运行机制与生命周期 (Wasm Hook Lifecycle)

为了绝对不拖慢网关的首字延迟 (TTFT)，`finops-audit` 插件严格遵循**异步旁路 (Non-blocking Out-of-Band)** 原则：

```mermaid
sequenceDiagram
    autonumber
    actor Client as 客户端 (OpenCode)
    participant Higress as Higress Gateway (Envoy)
    participant Upstream as 上游 (Google Gemini)
    participant Wasm as Wasm 审计插件
    participant DashAPI as Dashboard API
    participant VLogs as VictoriaLogs

    Client->>Higress: POST /v1/chat/completions (Stream=True)
    Higress->>Upstream: 原生转译并建立 HTTP/2 SSE 连接
    
    loop 极速零拷贝流式透传
        Upstream-->>Higress: SSE Chunk (Delta Tokens)
        Higress-->>Client: 立即下发 Chunk (耗时 < 1ms)
        Note over Wasm: 流进行中：Wasm 不阻塞、不深拷贝、零延迟损耗
    end

    Upstream-->>Higress: Final Chunk (包含 Usage 统计 & EOS 终止符)
    Higress-->>Client: 下发终止符 (客户端通信完毕并关闭连接)
    
    Note over Higress,Wasm: 触发 OnHttpResponseBody 结束钩子
    rect rgb(240, 248, 255)
        Wasm->>Wasm: 提取 Tokens (Prompt, Completion, Reasoning)
        Wasm->>Wasm: 折叠 Base64 图片正则，Gzip 压缩原始报文
        par 异步派发外部存储
            Wasm-)VLogs: HTTP POST /insert/jsonl (异步冷归档)
            Wasm-)DashAPI: HTTP POST /api/v1/internal/audit-log (异步结构化入库)
        end
    end
    DashAPI-)MySQL: 写入 llm_request_logs 数据表
```

---

## 4. 路由状态机与故障熔断降级 (Routing & Failover)

网关由 Higress 内置的核心路由插件与策略引擎统一调度：

1. **健康检查与预检 (Pre-Call Checks)**：网关建立连接前校验后端状态；
2. **极速熔断判定**：当上游返回 `429 (Rate Limit)`、`500/502/503/504 (Server Error)` 或连接超时达 **1 次**，立即判定节点亚健康；
3. **退避重试 (Retry)**：针对瞬态网络波动支持最多 3 次指数退避重试；
4. **轻量冷却解禁 (Cooldown)**：隔离时间设为 **5 秒**，到期后允许 Canary 试探流量恢复。

---

## 5. 零控制面数据库架构与 Virtual Key (Consumer CRD) 鉴权机制

与旧版 LiteLLM 必须强依赖外部关系型数据库（如 Neon PostgreSQL）维护 `lite_llm_keys` 表不同，本项目彻底摒弃外部控制面数据库，实现**真正的云原生无状态（Stateless）与微秒级内存鉴权**：

### 5.1 数据存储宿主：K3s 原生 etcd
- **存储介质**：网关的全部模型映射、路由规则、以及给同事/业务端（如 Jayden）下发的 Virtual Key，**全部持久化于 Kubernetes 自身的 etcd**；
- **资源形态**：基于官方标准的 `Consumer` CRD（定义在 `higress-system` 命名空间下）；
- **生命周期**：天然享有集群原生备份、容灾与随集群漂移能力，0 外部 DB 运维开销。

### 5.2 鉴权与校验流：Envoy 纯 C++ 内存哈希表 ($O(1)$)
1. **热加载注入**：当在集群中创建或更新一个 `Consumer` 对象时，Higress Controller 监听该事件，并通过 xDS 动态将 Key 映射灌入 Envoy 内存；
2. **零网络 I/O 鉴权**：业务端发起带 `Authorization: Bearer <VIRTUAL_KEY>` 的请求到达网关时，Envoy 直接在本地 C++ 内存哈希表中比对，耗时 `< 0.05ms`（彻底消除查库带来的 15ms~50ms 延迟损耗）；
3. **审计元数据自动传递**：鉴权通过后，Envoy 自动在请求上下文注入该 Consumer 的标识（如 `team=jayden`），后续 Wasm 审计插件在结束流时直接提取此标识写入 MySQL `llm_request_logs.api_key_alias` 字段。

### 5.3 Virtual Key 声明规范样例 (如为 Jayden 团队配发)
```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: Consumer
metadata:
  name: jayden-team
  namespace: higress-system
spec:
  authConfig:
    keyAuth:
      # 分发给业务端的真实 Virtual Key
      key: "sk-jayden-risk-analytics-token"
  metadata:
    team: "jayden"
    cost_center: "risk-analytics"
```

---

## 6. 生产环境部署拓扑与 GitOps 交付 (Production Deployment & GitOps)

### 5.1 方案 A：基于官方 Helm Chart 的 ArgoCD 自动化纳管架构
网关采用 **方案 A（官方 Helm Chart 声明式交付）**。CRD 无需团队自行维护与编译，全部交由 ArgoCD 生命周期管理：
1. **控制面与 CRD 自动装配**：ArgoCD 订阅官方 Helm 仓库，部署 `higress-controller` 并自动注册官方 CRD（`WasmPlugin`, `McpBridge`）；
2. **轻量配置下发**：Controller 通过 Envoy 原生 xDS 动态接口向数据面网关毫秒级热推配置，无需重启 Pod 且连接零中断；
3. **差异化收敛**：我们仅需在仓库中维护针对 `free-arm-vm` ARM64 的 `values.yaml` 与业务插件 CRD。

```text
[甲骨文云新加坡机房 VCN 10.0.0.0/16]
 │
 ├── OCI ALB (161.118.240.179:80)
 │    │
 │    └── 内网转发至 Worker Node free-arm-vm:31850 (K3s LoadBalancer NodePort)
 │
 └── 宿主机: free-arm-vm (134.185.90.98 / TS: 100.105.130.0)
      │ 4 OCPU ARM64, 24 GB 内存
      │
      └── K3s Pod 编排 (绑定 nodeSelector: kubernetes.io/hostname: free-arm-vm)
           ├── Pod: higress-controller (监听 CRD 并下发 xDS 配置)
           ├── Pod: higress-gateway (C++ Envoy 内核 + Wasm 运行时)
           └── Pod: my-higress-dashboard (内嵌 React 18 SPA + FastAPI 后端)
```
