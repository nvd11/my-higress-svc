# my-higress-svc 🚀

> **基于阿里开源 Higress (Envoy C++ 内核) 与 Wasm-Go 插件生态的次世代极致性能 AI Gateway & FinOps 平台**  
> 专为 **700k ~ 1M Tokens 超长上下文**、高频自主 Coding Agent 与私有大模型集群量身打造。

---

## 🌟 为什么从 LiteLLM 升级至 Higress？

在日常深度使用 OpenCode、Claude Code 及自主 Agent（Yui、Rin）进行系统架构设计与长文本分析时，会话上下文频繁达到 **350k ~ 700k+ Tokens**（单次 JSON 报文高达 3MB ~ 5MB）。

传统的 Python 解释型网关（如 LiteLLM）受制于 **GIL 全局解释器锁与 asyncio 单线程事件循环**，在深度遍历和重构巨型 JSON 对象时极易产生秒级事件循环阻塞（Event Loop Lag），进而导致 TCP 缓冲区积压和上游服务端强制断流（`Connection closed` / `Deadline exceeded`）。

**Higress（基于 Envoy C++ 代理内核）带来了颠覆性的降维打击**：

| 对比维度 | 原 Python LiteLLM 网关 | 次世代 Higress AI Gateway | 提升收益 |
| :--- | :---: | :---: | :---: |
| **底层内核语言** | Python 3.12 (解释执行 / GIL 瓶颈) | **C++ 20 (Envoy 原生内存池 / SIMD 优化)** | 运行效率提升一个数量级 |
| **扩展插件机制** | Python Callback 拦截器 | **WebAssembly (Go 1.24+ `wasip1` 编译的 `.wasm` 字节码)** | 高性能沙箱隔离，零 GC 停顿 |
| **首字延迟开销 (TTFT)** | 15ms ~ 50ms 代理损耗 | **< 1ms** | 降低 **95%+** |
| **超长上下文流式响应** | 易因阻塞接收窗口导致超时断线 | **零内存拷贝流式透传（Zero-Copy SSE）** | 彻底根治长上下文卡壳 |
| **常驻内存开销** | 400MB ~ 2.0GB (大报文极易爆仓) | **< 80MB** | 内存占用缩减 **90%+** |

---

## 🏗️ 全局架构拓扑图

```text
                  开发者 / OpenCode / Claude Code / 浏览器大屏用户
                                        │
                                        ▼ HTTPS (统一入口域名，前后端 100% 同域 0 CORS)
                    OCI ALB (Layer 7 永久免费负载均衡器)
                                        │
                                        ▼ 灰云直连 (无 100s 超时限制)
              ┌─────────────────────────────────────────────────────┐
              │       Higress AI Gateway (Envoy C++ 内核)           │
              │                                                     │
              │  • 原生 ai-proxy 插件 (Google Gemini 3.8 原生直连)   │
              │  • Fallback 降级至 A6 API (gemini-3.8-backup)       │
              │  • 本地自主 Agent 裸流直通 (/hermes/yui, /hermes/rin) │
              │  • 前后端同域路径分流:                               │
              │    - /dashboard/* ➔ 独立 React 18 前端 (Nginx <15MB)│
              │    - /api/v1/*    ➔ 纯 Go 后端数据引擎 (<30MB)      │
              │                                                     │
              │  ┌───────────────────────────────────────────────┐  │
              │  │  自研 Wasm-Go 财务审计旁路插件 (Cindy 编写)   │  │
              │  └───────────────────────┬───────────────────────┘  │
              └──────────────────────────┼──────────────────────────┘
                                         │ 异步 HTTP POST (单次极速派发, 0 毫秒主线程阻塞)
                                         ▼
                         ┌─────────────────────────────┐
                         │   Dashboard-API 纯 Go 后端  │
                         │ (POST /api/v1/internal/log) │
                         └──────────────┬──────────────┘
                                        │
         ┌──────────────────────────────┼──────────────────────────────┐
         ▼ 异步入库                     ▼ 3天 Base64+Gzip 热缓存        ▼ 1.3MB 自动切片分块 + Gzip
 ┌──────────────────────┐      ┌──────────────────────┐      ┌──────────────────────┐
 │  OCI MySQL HeatWave  │      │  K3s 业务集群 Redis  │      │   StarFive 星光板    │
 │ (`llm_request_logs`) │      │(`litellm:payload:*`) │      │     VictoriaLogs     │
 │ 实时财务流水与 Token │      │ 抽屉透视 <5ms 极速开 │      │  海量原始报文永久冷存│
 └──────────────────────┘      └──────────────────────┘      └──────────────────────┘
```

---

## ✨ 核心特性

1. **100% 协议全兼容**：
   - 提供标准化的 `POST /v1/chat/completions` 与 `GET /v1/models`，对客户端零侵入无感知。
2. **极速大报文吞吐**：
   - 承受 700k ~ 1M Tokens 巨型报文不产生事件循环延迟，首字延迟（TTFT）损耗接近物理极限。
3. **保留所有核心 FinOps 资产**：
   - 异步写入 MySQL 审计表，继承每日中行实时汇率对账（USD ➔ CNY）；
   - 原始报文自动折叠 Base64 图片，Gzip 极速压缩后异步送往 VictoriaLogs 全文检索。
4. **Dashboard 大屏工程完整内嵌迁移**：
   - 将现有 React 18 + Vite + Tailwind CSS 前端大屏与只读查询/透视 API 代码**完整迁移至本项目内建交付**，自闭环构建，继续享受抽屉式报文穿透。
5. **指定部署于 OCI ARM 旗舰节点 (`free-arm-vm`)**：
   - 生产环境硬性调度绑定甲骨文云新加坡机房的 **`free-arm-vm` (Ampere Altra ARM64 4C24G 内存)**；
   - 与公网入口 OCI ALB（`161.118.240.179`）同处新加坡同一 VCN 内网网段，内网转发延迟低于 0.2ms，24GB 充沛内存确保万级并发从容运转。

---

## 📂 目录结构与规划

```text
my-higress-svc/
├── README.md                   # 项目工程说明书（当前文件）
├── config.yaml                 # ★ 唯一真源：模型清单 / 别名 / 渠道 / 计费价目
├── wasm-plugin.yaml            # 双 Wasm 插件编排 (ai-gatekeeper + finops-audit)
├── ingress.yaml                # [生成物] 每个 provider 一条精确匹配路由, 无 catch-all
├── mcp-bridge.yaml             # [生成物] 上游 DNS registry 与连接池
├── key-sync-job.yaml           # PostSync Hook: Vault → Secret → WasmPlugin 回填 + 回读校验
├── secrets.yaml                # OCI Vault 自动化 ExternalSecret 凭证声明
├── values.yaml                 # 定制化 Helm Values (ARM64 调度与 Envoy 调优)
├── .github/
│   ├── workflows/
│   │   ├── wasm-plugin-ci-cd.yml   # 单测 + 漂移护栏 + 密钥扫描 + 编译签名 + GitOps 钉 SHA
│   │   ├── backend-ci-cd.yml       # Go vet/单测/覆盖率门禁 + 镜像签名 + Digest Dispatch
│   │   └── frontend-ci-cd.yml      # tsc + 构建 + 镜像签名 + Digest Dispatch
│   └── dependabot.yml          # gomod / npm / actions / docker 依赖自动升级
├── scripts/
│   ├── secret-scan.py          # 全仓明文密钥扫描 (CI 强制)
│   └── smoke.sh                # 部署后契约冒烟 (401 / 400 / models=11 / 200)
├── docs/
│   ├── REQUIREMENTS.md         # 需求规格说明书 (v1.0 历史基线)
│   ├── DELIVERY_GOALS.md       # 四阶段交付目标与里程碑 (v1.0 历史基线)
│   ├── ARCHITECTURE.md         # 核心技术架构设计书 (v1.0 历史基线)
│   ├── COMPATIBILITY.md        # 资产兼容契约手册 (MySQL DDL / VictoriaLogs / Redis)
│   ├── DEPLOYMENT_GUIDE.md     # 生产环境部署实操指南
│   ├── GITOPS_DEPLOYMENT_ARCHITECTURE.md  # 双集群 GitOps 与 CI/CD 规范
│   ├── MODEL_GOVERNANCE_PLAN.md           # 治理方案 v1 (路线 C, 已被 V3 取代)
│   └── MODEL_GOVERNANCE_PLAN_V3.md        # ★ 治理方案 v3.7 (路线乙 ai-gatekeeper, 现行)
├── plugins/
│   ├── ai-gatekeeper/          # 第一道硬门禁 (鉴权 / 准入 / 别名改写 / models 直出)
│   └── finops-audit/           # 第二道旁路审计 (真实状态码 / Token 计量 / 三路落库)
├── frontend/                   # React 18 + Vite + Tailwind (Nginx <15MB)
└── services/dashboard-api/     # 纯 Go RESTful 后端 (查询 + 三位一体落库)
```

---

## 📖 深入文档指南

- 📋 **[详细需求规格说明书 (`docs/REQUIREMENTS.md`)](./docs/REQUIREMENTS.md)**：包含业务痛点深度溯源、功能性/非功能性需求矩阵及性能 KPI；
- 🎯 **[交付目标与里程碑计划 (`docs/DELIVERY_GOALS.md`)](./docs/DELIVERY_GOALS.md)**：包含四阶段实施路径（Phase 1 极速试驾 ➔ Phase 2 Wasm 开发 ➔ Phase 3 看板联调 ➔ Phase 4 GitOps 上云）与验收准则。

---

## 🤝 贡献与演进

由 **Jason (Boss)** 战略主导架构设计，**Cindy (贴身大秘❤️)** 全力负责 Wasm-Go 编码、云原生部署与全链路调优。
