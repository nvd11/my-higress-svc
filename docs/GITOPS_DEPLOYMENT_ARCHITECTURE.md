# Higress AI Gateway 总体 GitOps 部署架构与 CI/CD 规范指南

> **更新时间**：2026-10-24  
> **文档维护者**：Jason (GA) & Dora 贴身秘书团队  
> **项目仓库**：[`my-higress-svc`](https://github.com/nvd11/my-higress-svc)  
> **GitOps 清单仓库**：[`my-argocd-manifests`](https://github.com/nvd11/my-argocd-manifests)

---

## 目录
1. [总体部署拓扑与集群架构](#1-总体部署拓扑与集群架构)
2. [GitOps 总体部署策略与同步机制](#2-gitops-总体部署策略与同步机制)
   - [2.1 架构分层：两地双集群协作](#21-架构分层两地双集群协作)
   - [2.2 三种核心 GitOps 部署模式](#22-三种核心-gitops-部署模式)
3. [项目 CI/CD 流水线深度解析](#3-项目-cicd-流水线深度解析)
   - [3.1 `wasm-plugin-ci-cd.yml` (FinOps Wasm 插件)](#31-wasm-plugin-ci-cdyml-finops-wasm-插件)
   - [3.2 `backend-ci-cd.yml` (Dashboard API 后端)](#32-backend-ci-cdyml-dashboard-api-后端)
   - [3.3 `frontend-ci-cd.yml` (Dashboard Web 前端)](#33-frontend-ci-cdyml-dashboard-web-前端)
   - [3.4 生成物漂移护栏与 GitOps 回写收敛](#34-生成物漂移护栏与-gitops-回写收敛)
   - [3.5 密钥扫描、镜像签名与部署后冒烟](#35-密钥扫描镜像签名与部署后冒烟)
4. [配置凭据与 ExternalSecrets 同步](#4-配置凭据与-externalsecrets-同步)
5. [常用运维与验证速查](#5-常用运维与验证速查)

---

## 1. 总体部署拓扑与集群架构

整个 Higress AI 网关系统横跨 **阿里云** 与 **腾讯云 / 甲骨文云** 两个 K3s Kubernetes 集群，借助 Tailscale 全互联隧道构建出高内聚、低延时的混合多云架构：

```mermaid
graph TB
    subgraph CLUSTER_A["集群 A: 阿里云 aliyun-k3s (ArgoCD GitOps 控制中心)"]
        ARGO_SERVER["ArgoCD Server<br/>(8.148.149.80:30080)"]
        ARGO_CTRL["ArgoCD Application Controller"]
        REPO_SERVER["ArgoCD Repo Server"]
        ROOT_APP["root-bootstrap Application"]
        HIGRESS_APP["higress-svc Application<br/>(Multi-Source)"]
        DASH_BE_APP["higress-dashboard-backend Application"]
        DASH_FE_APP["higress-dashboard-frontend Application"]
        
        ROOT_APP --> HIGRESS_APP
        ROOT_APP --> DASH_BE_APP
        ROOT_APP --> DASH_FE_APP
    end

    subgraph CLUSTER_B["集群 B: 腾讯云业务集群 tencent-dp1-cluster (数据面与业务面)"]
        CP_B["vm-0-2-debian (腾讯云 CP 节点 · AMD64)<br/>- higress-controller (2.2.5 控制面)"]
        W_ARM["free-arm-vm (OCI 新加坡 · 4C24G ARM64)<br/>- higress-gateway (2.2.5 数据面 + Envoy 内核)<br/>- finops-audit (Wasm 插件常驻)<br/>- redis (本地 Pod)"]
        W_NUC["nuc (广州本地高速节点 · AMD64)"]
    end

    subgraph OCI_DB["甲骨文云新加坡基础存储 (OCI Singapore)"]
        HEATWAVE["MySQL HeatWave (Always Free)<br/>`litellm_db.llm_request_logs` (账单明细)"]
        VAULT["OCI Vault (凭证中心)<br/>- Gemini API Key<br/>- A6 API Key<br/>- Virtual Keys"]
    end

    ARGO_CTRL -. "Tailscale 隧道同步 (Sync/Apply)" .-> CLUSTER_B
    W_ARM -. "内网安全写入" .-> HEATWAVE
    CLUSTER_B -. "ExternalSecrets 拉取密钥" .-> VAULT
```

---

## 2. GitOps 总体部署策略与同步机制

### 2.1 架构分层：两地双集群协作
- **控制中心**：部署在阿里云节点（集群 A），专门负责监听 GitHub 仓库、渲染 Helm 清单与下发资源。
- **业务载体**：腾讯云节点（集群 B），作为外部集群注册进集群 A 的 ArgoCD 控制面，接收网关与业务 Pod 的部署。

### 2.2 三种核心 GitOps 部署模式

在本项目中，根据不同组件的工程属性，精准采用了三种 GitOps 交付范式：

| 交付范式 | 适用对象 | 核心机制 | 优势与特点 |
| :--- | :--- | :--- | :--- |
| **模式 1：Multi-Source 纯声明式** | **Higress Core 网关底座** | ArgoCD 监听官方 Helm Chart + 本仓 `values.yaml` | 零镜像编译，修改 `values.yaml` 即可动态调优网关（副本数、nodeSelector、超时参数）。 |
| **模式 2：GitOps 自动回写 (Auto Write-Back)** | **FinOps Wasm 插件** (`plugins/finops-audit`) | CI 编译 Go 1.24 wasip1 ➔ 推送 GHCR ➔ Actions 自动提交更新 `wasm-plugin.yaml` 中的 Git SHA | 100% 仓内闭环，ArgoCD 感知到 SHA 变化后秒级让 Envoy 网关热重载，无需重启网关 Pod。 |
| **模式 3：跨仓库 Dispatch 派发 (Digest Dispatch)** | **Dashboard 后端与前端** | CI 构建 Docker 镜像 ➔ POST `repository_dispatch` ➔ 清单仓修改 SHA256 Digest ➔ ArgoCD 滚动更新 | 镜像内容完全锁定不可变 Digest，彻底杜绝 Tag 飘移风险，符合严格的金融 IT 规范。 |

---

## 3. 项目 CI/CD 流水线深度解析

项目的所有 GitHub Actions 流水线集中存放在 `.github/workflows/` 目录下，各司其职：

```
.github/workflows/
├── wasm-plugin-ci-cd.yml      # Wasm 插件: 单测 + 漂移护栏 + 密钥扫描 + 编译签名 + GitOps 钉 SHA
├── backend-ci-cd.yml          # Dashboard 后端: vet/单测/覆盖率门禁 + 镜像签名 + Digest Dispatch
├── frontend-ci-cd.yml         # Dashboard 前端: tsc 类型检查 + 构建 + 镜像签名 + Digest Dispatch
└── (已删除) sync-config.yml    # 历史遗留, 其职责已由 config.yaml + pricinggen 生成物取代
```

---

### 3.1 `wasm-plugin-ci-cd.yml` (FinOps Wasm 插件)

* **文件路径**：[`.github/workflows/wasm-plugin-ci-cd.yml`](file:///home/gateman/projects/github/my-higress-svc/.github/workflows/wasm-plugin-ci-cd.yml)
* **触发条件**：
  - `main` 分支变动，且命中路径：`plugins/finops-audit/**`、`wasm-plugin.yaml`、`.github/workflows/wasm-plugin-ci-cd.yml`；
  - `main` 分支的 Pull Request；
  - 手动调度（`workflow_dispatch`）。
* **技术底座**：**Go 1.24 原生 `wasip1 / c-shared` 编译**（脱离了历史 TinyGo 调度器依赖，由 Higress 2.2.5 Envoy 原生完全支持）。

#### 执行流程图：
```mermaid
flowchart TD
    A["Push 触发 (plugins/finops-audit/**)"] --> B["Job 1: test<br/>- 安装 Go 1.24<br/>- 执行 CGO_ENABLED=0 go test -v ./pkg/finops"]
    B --> C["Job 2: build-and-deploy<br/>- CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm ."]
    C --> D["上传 Wasm Artifact 到 GitHub Actions 存储"]
    D --> E["Docker Buildx 打包 Scratch 镜像<br/>- Tag 1: ghcr.io/nvd11/higress-finops-audit:latest<br/>- Tag 2: ghcr.io/nvd11/higress-finops-audit:${{ github.sha }}"]
    E --> F["GitOps 自动回写步骤<br/>- sed 替换 wasm-plugin.yaml 镜像为本次 SHA<br/>- git commit -m 'chore(gitops): bump finops-audit wasm image to ... [skip ci]'<br/>- git push origin main"]
    F --> G["ArgoCD 检测到 Commit 变动 ➔ 自动同步 WasmPlugin ➔ Envoy 热加载生效！"]
```

---

### 3.2 `backend-ci-cd.yml` (Dashboard API 后端)

* **文件路径**：[`.github/workflows/backend-ci-cd.yml`](file:///home/gateman/projects/github/my-higress-svc/.github/workflows/backend-ci-cd.yml)
* **触发条件**：`services/dashboard-api/**` 发生变更。
* **交付目标**：`ghcr.io/nvd11/my-higress-dashboard-backend`。
* **构建架构**：`linux/arm64`（调度在 `ubuntu-24.04-arm` 原生 ARM 运行器上，秒级编译）。
* **跨仓联动**：
  仅在 **main 分支 push** 时派发（tag `v*` 构建不改写生产清单，避免用旧 commit 的镜像覆盖线上 digest）。
  构建完成后捕获不可变摘要 `${{ steps.build.outputs.digest }}`，向 `my-argocd-manifests` 派发：
  ```bash
  curl -X POST https://api.github.com/repos/nvd11/my-argocd-manifests/dispatches \
    -H "Authorization: Bearer $DISPATCH_TOKEN" \
    -d '{
      "event_type": "update-app-image-digest",
      "client_payload": {
        "svc_name": "higress-dashboard-backend",
        "image_digest": "sha256:..."
      }
    }'
  ```

---

### 3.3 `frontend-ci-cd.yml` (Dashboard Web 前端)

* **文件路径**：[`.github/workflows/frontend-ci-cd.yml`](file:///home/gateman/projects/github/my-higress-svc/.github/workflows/frontend-ci-cd.yml)
* **触发条件**：`frontend/**` 发生变更。
* **交付目标**：`ghcr.io/nvd11/my-higress-dashboard-frontend`。
* **构建机制**：多阶段构建（Node.js Vite 打包编译静态页面 ➔ Nginx Alpine 极简镜像承载）。
* **跨仓联动**：同样通过 `repository_dispatch` 驱动 `my-argocd-manifests` 自动更新 `argocd-apps/higress-dashboard-frontend-app.yaml` 中的 digest。

---

### 3.4 生成物漂移护栏与 GitOps 回写收敛

`config.yaml` 是模型清单与价目的唯一真源，`pricinggen` 在构建期把它编译成 4 份生成物
（`pricing_gen.go`、`models_gen.go`、`mcp-bridge.yaml`、`ingress.yaml`）。

* **真护栏（G8）**：`test` job 先重新生成，再对生成物执行 `git diff --exit-code`。
  只要「改了配置没提交生成物」或「手改生成物」，流水线立刻飘红并提示本地重跑命令。
* **生成物必须随 PR 提交**：生成器不会再被 bot 悄悄回写，因此 ArgoCD 看到的每个 commit
  都是「配置 + 产物」原子的，不存在「新配置 + 旧产物」的同步窗口。
* **回写只钉镜像 SHA**：`build-and-deploy` 仅 `sed` 更新 `wasm-plugin.yaml` 中的镜像 tag，
  提交信息带 `[skip ci]`；push 失败时自动 `pull --rebase` 重试 3 次，仍失败则 `::error::` 告警，
  避免「镜像已推、清单未更新」的静默漂移。
* **单测补充漂移守卫**：`TestPricingGeneratedFromConfigYAML` 逐字段比对配置与价目表，
  `TestDeepSeekV41Flash1017Pricing` 独立重算人民币→美元折算，防止手抄错误。

---

### 3.5 密钥扫描、镜像签名与部署后冒烟

| 能力 | 实现 | 触发 |
| :--- | :--- | :--- |
| **全仓密钥扫描** | `scripts/secret-scan.py`：扫描 `git ls-files` 全部文本，覆盖 `sk-*`、`AIza*`、私钥块，带占位符白名单 | 每次 wasm 流水线 `test` job |
| **镜像签名** | `cosign sign --yes <image>@<digest>`（keyless / OIDC），需 `id-token: write` | main 分支三套流水线 |
| **部署后冒烟** | `scripts/smoke.sh`：无 Key→401、伪造 Key→401 `invalid_api_key`、未纳管模型→400 `model_not_found`、`/v1/models` 长度=11 且不含别名、真实模型→200 | 配置了 `SMOKE_BASE_URL` 变量时，在写回后自动执行 |
| **依赖更新** | `.github/dependabot.yml`：gomod ×3、npm、actions、docker | 每周/每月 |

> 历史 `sync-config.yml`（把 config.yaml 转 Base64 派发）已删除：该职责已被
> `config.yaml → pricinggen → 生成物` 的构建链取代，两套机制并存只会产生第二份真源。

---

### 3.6 历史适配器清理说明 (Gemini Adapter Cleaned)

* **状态说明**：✅ **源码与流水线已彻底删除**。
* **架构背景**：过去用于编译中间代理层 `gemini-adapter`，目前流量已完全由 Envoy 直连海外上游，该中转层已被 Envoy 原生 Wasm 彻底替代，相关的源码与 `adapter-ci-cd.yml` 已完成物理清理。

---

## 4. 配置凭据与 ExternalSecrets 同步

项目严格遵守安全红线：**绝不在 Git 仓库明文提交任何真实 API 密钥**。

* **清单配置**：[`secrets.yaml`](file:///home/gateman/projects/github/my-higress-svc/secrets.yaml)
* **驱动组件**：Kubernetes `external-secrets-operator`
* **同步机制**：
  1. 通过集群内的 `oci-litellm-vault-store` SecretStore 定期轮询（每 1 小时）甲骨文云 OCI Vault；
  2. 提取公共上游凭证（如 `OPENAI_API_KEY_FREE_3`、`A6_API_KEY`）注入到集群 `Secret/higress-ai-secrets`；
  3. **ArgoCD 健康保护准则**：`secrets.yaml` 中声明的 `remoteRef.key` 必须在 OCI Vault 中真实存在。若声明了不存在的键值（如历史残留的未建凭据），会导致 ExternalSecret 抛出 404 探针失败，从而引发 ArgoCD 应用状态 `Degraded`。

---

## 5. 常用运维与验证速查

### 5.1 查看 ArgoCD 核心状态 (阿里云 Master)
```bash
# 查询应用同步与健康状态（预期正常为 Synced / Healthy）
ssh root@8.148.149.80 "kubectl get app higress-svc -n argocd"

# 强制触发 ArgoCD 重新评估同步
ssh root@8.148.149.80 "kubectl annotate app higress-svc -n argocd argocd.argoproj.io/refresh=normal --overwrite"
```

### 5.2 查看网关与 Wasm 运行日志 (腾讯云 CP / OCI Node)
```bash
# 查看网关 Pod 运行状态
ssh gateman@43.139.214.231 "echo 32565624 | sudo -S kubectl get pods -n higress-system -o wide"

# 检查当前生效的 Wasm 插件版本
ssh gateman@43.139.214.231 "echo 32565624 | sudo -S kubectl get wasmplugin finops-audit -n higress-system -o yaml | grep url"

# 实时查看网关 Envoy 加载 Wasm 的日志
ssh gateman@43.139.214.231 "echo 32565624 | sudo -S kubectl logs deploy/higress-gateway -n higress-system --tail=50"
```

### 5.3 生产连通性与财务审计入库验证
```bash
# 发送测试流式请求
curl -N -i -X POST https://higress.jppwl.asia/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-REDACTED-see-OCI-Vault" \
  -d '{"model": "gemini-3.8-flash", "messages": [{"role": "user", "content": "ping"}], "stream": true}'

# 检查 Dashboard 后端入库日志
ssh gateman@43.139.214.231 "echo 32565624 | sudo -S kubectl logs deploy/higress-dashboard-backend -n higress-system --tail=10"
```
