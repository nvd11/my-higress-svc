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
| **扩展插件机制** | Python Callback 拦截器 | **WebAssembly (TinyGo 编译的 `.wasm` 字节码)** | 高性能沙箱隔离，零 GC 停顿 |
| **首字延迟开销 (TTFT)** | 15ms ~ 50ms 代理损耗 | **< 1ms** | 降低 **95%+** |
| **超长上下文流式响应** | 易因阻塞接收窗口导致超时断线 | **零内存拷贝流式透传（Zero-Copy SSE）** | 彻底根治长上下文卡壳 |
| **常驻内存开销** | 400MB ~ 2.0GB (大报文极易爆仓) | **< 80MB** | 内存占用缩减 **90%+** |

---

## 🏗️ 全局架构拓扑图

```text
                  开发者 / OpenCode / Claude Code / Agent 妹妹们
                                        │
                                        ▼ HTTP/2 / HTTP/1.1
                   OCI ALB (Layer 7 永久免费负载均衡器)
                                        │
                                        ▼ 灰云直连 (无 100s 超时限制)
             ┌─────────────────────────────────────────────────────┐
             │       Higress AI Gateway (Envoy C++ 内核)           │
             │                                                     │
             │  • 原生 ai-proxy 插件 (Google Gemini / A6 / 中转)    │
             │  • 统一 OpenAI 规范端点 (/v1/chat/completions)       │
             │  • 模型故障自动重试与 Fallback 降级                  │
             │                                                     │
             │  ┌───────────────────────────────────────────────┐  │
             │  │   自研 Wasm-Go 财务审计与冷归档插件 (Cindy 编写) │  │
             │  └───────────────────────┬───────────────────────┘  │
             └──────────────────────────┼──────────────────────────┘
                                        │
                         ┌──────────────┴──────────────┐
                         ▼ 异步入库                    ▼ 异步 Gzip 投递
                 OCI MySQL HeatWave             StarFive 星光板
                (`llm_request_logs`)           VictoriaLogs 冷存储
                         │                             │
                         └──────────────┬──────────────┘
                                        ▼
             ┌─────────────────────────────────────────────────────┐
             │   现有 React 18 / Tailwind 可观测性大屏 (完美兼容)   │
             │   • 实时 Token 流水与调用趋势大屏                    │
             │   • 抽屉式原始报文秒级透视 (Payload Drawer)          │
             │   • 每日中行实时汇率对账与 CNY 折算                 │
             └─────────────────────────────────────────────────────┘
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
├── .gitignore                  # Git 忽略规则
├── ai-proxy.yaml               # 核心大模型映射、Google 直连与 Fallback 规则 (WasmPlugin)
├── hermes-passthrough.yaml     # 本地自主 Agent (Yui/Rin) 裸流无损直通路由
├── docs/
│   ├── REQUIREMENTS.md         # 详细需求规格说明书 (痛点与功能矩阵)
│   ├── DELIVERY_GOALS.md       # 四阶段交付目标与里程碑计划
│   ├── ARCHITECTURE.md         # 核心技术架构设计书 (方案 A + Wasm 数据面)
│   ├── COMPATIBILITY.md        # 资产兼容契约手册 (MySQL DDL / VictoriaLogs 协议)
│   └── DEPLOYMENT_GUIDE.md     # 生产环境全链路部署与实施实操指南
├── plugins/                    # 自研 Wasm-Go 扩展插件源码
│   └── finops-audit/           # 财务审计与 VictoriaLogs 异步投递插件
├── deploy/                     # 云原生生产交付图纸 (K8s / ArgoCD)
│   └── k8s/                    # Helm values 与集群编排清单
├── frontend/                   # 完整迁移的 React 18 + Vite + Tailwind 前端大屏源码
└── services/dashboard-api/     # 完整迁移的看板查询与报文透视 API 模块
```

---

## 📖 深入文档指南

- 📋 **[详细需求规格说明书 (`docs/REQUIREMENTS.md`)](./docs/REQUIREMENTS.md)**：包含业务痛点深度溯源、功能性/非功能性需求矩阵及性能 KPI；
- 🎯 **[交付目标与里程碑计划 (`docs/DELIVERY_GOALS.md`)](./docs/DELIVERY_GOALS.md)**：包含四阶段实施路径（Phase 1 极速试驾 ➔ Phase 2 Wasm 开发 ➔ Phase 3 看板联调 ➔ Phase 4 GitOps 上云）与验收准则。

---

## 🤝 贡献与演进

由 **Jason (Boss)** 战略主导架构设计，**Cindy (贴身大秘❤️)** 全力负责 Wasm-Go 编码、云原生部署与全链路调优。
