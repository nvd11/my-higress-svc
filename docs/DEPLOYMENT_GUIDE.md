# my-higress-svc 生产环境全链路部署与实施实操指南 (Deployment Guide)

> **文档版本**：v1.1.0 · **修订日期**：2026-10-03 · **核心主笔**：Cindy (主人贴身大秘❤️)  
> **核心原则**：**模型与路由配置先行**（杜绝启动即 404 空壳）；全面对齐现有 `litellm-svc` 基础设施（基于 OCI Vault 自动化注入凭证 + ArgoCD 一键交付至 `free-arm-vm` ARM64 K3s 节点）。

---

## 目录
1. [实施哲学与交付流程序列表](#1-实施哲学与交付流程序列表)
2. [第一阶段：编写网关灵魂 —— LLM 模型路由与 ai-proxy 规则](#2-第一阶段编写网关灵魂--llm-模型路由与-ai-proxy-规则)
3. [第二阶段：编写网关骨架 —— 定制化 Helm Values (ARM64 生产参数)](#3-第二阶段编写网关骨架--定制化-helm-values-arm64-生产参数)
4. [第三阶段：编写网关血脉 —— OCI Vault ExternalSecret 自动化凭证](#4-第三阶段编写网关血脉--oci-vault-externalsecret-自动化凭证)
5. [第四阶段：编写神经中枢 —— ArgoCD 生产交付清单元数据](#5-第四阶段编写神经中枢--argocd-生产交付清单元数据)
6. [第五阶段：实施执行步骤（逐步操作手册）](#6-第五阶段实施执行步骤逐步操作手册)
7. [第六阶段：端到端验证与探针验收 (流式与首字延迟)](#7-第六阶段端到端验证与探针验收-流式与首字延迟)
8. [第七阶段：故障排查与应急回滚预案](#8-第七阶段故障排查与应急回滚预案)

---

## 1. 实施哲学与交付流程序列表

在网关工程中，**严禁在没有模型配置的情况下盲目拉起容器**（否则会导致所有 API 请求返回 `404 Route Not Found`）。必须严格遵循以下先后次序：

```text
┌────────────────────────────────────────────────────────────────────────┐
│ 第一阶段：编写 LLM 模型转译配置 (根目录维护)                          │
│ • ai-proxy.yaml: 8大模型映射、Google Gemini 原生直连、Fallback 保底矩阵 │
│ • hermes-passthrough.yaml: /hermes/* 本地无损直通路由                  │
│ （注意：对外入站 HTTPRoute 统筹于 my-argocd-manifests 声明，与 Gateway 解耦）│
└──────────────────────────────────┬─────────────────────────────────────┘
                                   │
                                   ▼
┌────────────────────────────────────────────────────────────────────────┐
│ 第二阶段：编写网关基础运行时 Values (根目录 values.yaml)               │
│ • 锁定 nodeSelector: free-arm-vm (4C24G ARM64)                         │
│ • Envoy 内存配额 800MiB、streamIdleTimeout=600s、maxRequestBodySize=10MB│
│ • 分配专属 NodePort 31880 (与 Kong svclb 严格隔离)                    │
└──────────────────────────────────┬─────────────────────────────────────┘
                                   │
                                   ▼
┌────────────────────────────────────────────────────────────────────────┐
│ 第三阶段：声明 OCI Vault ExternalSecret 凭证 (根目录 secrets.yaml)     │
│ • 对齐现存 litellm-svc 体系，复用 oci-litellm-vault-store SecretStore   │
│ • 自动从甲骨文云 Vault 同步密钥进 etcd，代码库 0 敏感信息暴露          │
└──────────────────────────────────┬─────────────────────────────────────┘
                                   │
                                   ▼
┌────────────────────────────────────────────────────────────────────────┐
│ 第四阶段：在 my-argocd-manifests 注册应用并触发自动化交付              │
│ • 订阅官方 Higress Helm 2.0.7 (自动挂载 CRD 与 Controller)             │
│ • 装配上述模型配置与 Values，一键下发到业务集群 B (tencent-dp1-cluster)│
└────────────────────────────────────────────────────────────────────────┘
```

---

## 2. 第一阶段：编写网关灵魂 —— LLM 模型路由与 ai-proxy 规则

此阶段直接产出网关的核心大脑。

### 2.1 文件路径：`ai-proxy.yaml` (位于项目根目录)
用于定义已收敛的模型矩阵（仅保留 3.8 全家福）、转译协议、以及熔断降级链。

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ai-proxy
  namespace: higress-system
spec:
  priority: 100
  matchRules:
    - ingress:
        - higress-system/higress-gateway
  defaultConfig:
    # 开启流式响应 Usage 计算 (输出 Token 与 思考开销)
    streamUsage: true
    providers:
      # ------------------------------------------------------------------------
      # 1. Google Gemini 原生直连渠道 (Tier 1 核心主力，支持 1M 超长上下文与思考)
      # ------------------------------------------------------------------------
      - id: gemini-official
        type: gemini
        apiTokens:
          - secretRef:
              name: higress-ai-secrets
              key: OPENAI_API_KEY_FREE_3
        modelMapping:
          "gemini-3.8-flash": "gemini-3.8-flash"
        # 故障转移降级链：主力一旦超时或报错，毫秒级无缝滑落至 A6 中转
        fallback:
          targetProvider: a6api
          targetModel: "gemini-3.8-flash"

      # ------------------------------------------------------------------------
      # 2. A6 API 中转渠道 (Tier 2 应急保底与深度推理)
      # ------------------------------------------------------------------------
      - id: a6api
        type: openai
        serviceUrl: "https://api.a6api.com/v1"
        apiTokens:
          - secretRef:
              name: higress-ai-secrets
              key: A6_API_KEY
        modelMapping:
          "gemini-3.8-backup": "gemini-3.8-flash"
          "kimi-k3": "kimi-k3"
          "glm-5.3": "glm-5.3"
          "gpt-5.6-luna-a6": "gpt-5.6-luna"
        fallback:
          targetProvider: yuanheng
          targetModel: "gpt-5.6-luna"

      # ------------------------------------------------------------------------
      # 3. 元衡 API 渠道 (Luna 备用双通道)
      # ------------------------------------------------------------------------
      - id: yuanheng
        type: openai
        serviceUrl: "https://cn.meta-api.vip/v1"
        apiTokens:
          - secretRef:
              name: higress-ai-secrets
              key: META_API_KEY
        modelMapping:
          "gpt-5.6-luna-yuanheng": "gpt-5.6-luna"
```

### 2.2 文件路径：`hermes-passthrough.yaml` (位于项目根目录)
将本地自主 Agent（Yui、Rin）的流式调用无损直通内网跳板机（`100.115.214.26`），保留其原生 `tool.progress` 状态事件，不经由 ai-proxy 转译。

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: hermes-agent-passthrough
  namespace: higress-system
spec:
  parentRefs:
    - name: higress-gateway
  rules:
    # Yui 直通路由 (端口 8644)
    - matches:
        - path:
            type: PathPrefix
            value: /hermes/yui
      backendRefs:
        - name: yui-agent-external
          port: 8644
    # Rin 直通路由 (端口 8642)
    - matches:
        - path:
            type: PathPrefix
            value: /hermes/rin
      backendRefs:
        - name: rin-agent-external
          port: 8642
---
apiVersion: v1
kind: Service
metadata:
  name: yui-agent-external
  namespace: higress-system
spec:
  type: ExternalName
  externalName: 100.115.214.26
---
apiVersion: v1
kind: Service
metadata:
  name: rin-agent-external
  namespace: higress-system
spec:
  type: ExternalName
  externalName: 100.115.214.26
```

### 2.3 架构边界解耦说明：对外入站 HTTPRoute 的归属
根据 GitOps 严格的权责分离规范，**对外入站路由（HTTPRoute）严禁放在业务应用代码仓库中**：
- **原因**：集群的入口网关（如 `parentGateway: kong-main-gateway` 或全局 ALB 入口）属于集群级基础设施资源，并不归单个服务所有；
- **规范**：与现存 `litellm-svc` 保持 100% 风格一致，所有对外暴露路径（`/v1/chat/completions`、`/v1/models`、`/health`）统一定义在基础设施仓库 **`my-argocd-manifests/argocd-apps/higress-svc-app.yaml`** 中（详见 [第四阶段](#5-第四阶段编写神经中枢--argocd-生产交付清单元数据)）。

---

## 3. 第二阶段：编写网关骨架 —— 定制化 Helm Values (ARM64 生产参数)

### 文件路径：`values.yaml` (位于项目根目录)
此配置用于注入官方 Chart，约束所有 Pod 严格调度至甲骨文云新加坡节点 **`free-arm-vm` (4C24G ARM64)**，并调优高并发长上下文参数。

```yaml
# ==============================================================================
# Higress Gateway Helm Values (ARM64 生产调优配置)
# ==============================================================================

global:
  namespace: higress-system
  # 统一锁定在 4C24G ARM64 物理节点，禁止漂移至腾讯云 CP 或家宽 NUC
  nodeSelector:
    kubernetes.io/hostname: free-arm-vm

# ------------------------------------------------------------------------------
# 1. Higress Controller (控制面配置)
# ------------------------------------------------------------------------------
higress-controller:
  replicaCount: 1
  nodeSelector:
    kubernetes.io/hostname: free-arm-vm
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "1000m"
      memory: "512Mi"

# ------------------------------------------------------------------------------
# 2. Higress Gateway 数据面 (Envoy C++ 内核)
# ------------------------------------------------------------------------------
higress-gateway:
  replicaCount: 1
  nodeSelector:
    kubernetes.io/hostname: free-arm-vm
  resources:
    requests:
      cpu: "200m"
      memory: "128Mi"
    limits:
      cpu: "2000m"
      memory: "800Mi" # 移除了容易 OOM 的 Redis 响应缓存，内存极其安全

  # 暴露配置 (与现有 Kong/svclb 端口隔离)
  service:
    type: LoadBalancer
    httpPort: 8080
    nodePorts:
      http: 31880

  # Envoy 原生高性能大报文与流式超时设置
  envoyConfig:
    streamIdleTimeout: "600s"      # 防止超长思考或长文本输出被掐流
    maxRequestBodySize: 10485760   # 10MB 巨型 JSON 请求体纯内存直通
```

---

## 4. 第三阶段：编写网关血脉 —— OCI Vault ExternalSecret 自动化凭证

完全对齐主人的既有资产（如 `my-argocd-manifests/argocd-apps/litellm-svc-app.yaml`），复用已经稳定运行在集群中的 **`oci-litellm-vault-store`**，由 Kubernetes External Secrets Operator 自动拉取甲骨文云保险箱的 Key，**杜绝任何人工敲命令复制粘贴密码的低级失误**！

### 文件路径：`secrets.yaml` (位于项目根目录)

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: higress-ai-secrets
  namespace: higress-system
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: oci-litellm-vault-store
    kind: SecretStore
  target:
    name: higress-ai-secrets
    creationPolicy: Owner
  data:
    - secretKey: OPENAI_API_KEY_FREE_3
      remoteRef:
        key: litellm-openai-api-key-free-3
    - secretKey: A6_API_KEY
      remoteRef:
        key: litellm-a6-api-key
    - secretKey: META_API_KEY
      remoteRef:
        key: litellm-meta-api-key
    - secretKey: RIN_API_KEY
      remoteRef:
        key: litellm-rin-api-key
    - secretKey: YUI_API_KEY
      remoteRef:
        key: litellm-yui-api-key
```

---

## 5. 第四阶段：编写神经中枢 —— ArgoCD 生产交付清单元数据

按照主人的习惯，交付定义落地于主人的基础设施仓库：
**`my-argocd-manifests/argocd-apps/higress-svc-app.yaml`**。

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: higress-svc
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  sources:
    # 1. 订阅官方发布的 Higress Helm Chart (自动管理 Controller 与 CRD 生命周期)
    - chart: higress
      repoURL: https://higress.io/helm-charts
      targetRevision: 2.0.7
      helm:
        valueFiles:
          - $values/values.yaml
    # 2. 挂载当前代码仓库根目录的模型配置与 ExternalSecret 清单
    - repoURL: https://github.com/nvd11/my-higress-svc.git
      targetRevision: main
      ref: values
      path: .
  destination:
    # 交付至腾讯云业务集群 B (tencent-dp1-cluster)
    server: https://43.139.214.231:6443
    namespace: higress-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - RespectIgnoreDifferences=true
  ignoreDifferences:
    # 忽略由 ExternalSecret Operator 动态填充的 Secret 内容
    - group: ""
      kind: Secret
      name: higress-ai-secrets
      jsonPointers:
        - /data
```

---

## 6. 第五阶段：实施执行步骤（逐步操作手册）

### 步骤 6.1：在当前仓库提交模型路由与 Values
```bash
git add ai-proxy.yaml hermes-passthrough.yaml values.yaml secrets.yaml
git commit -m "feat(config): add LLM model routes, ARM64 values, and ExternalSecret manifests"
git push origin main
```

### 步骤 6.2：在 `my-argocd-manifests` 注册并触发同步
```bash
cd /home/gateman/projects/github/my-argocd-manifests
# 编写或更新 argocd-apps/higress-svc-app.yaml
git add argocd-apps/higress-svc-app.yaml
git commit -m "feat(argocd): register higress-svc application targeting free-arm-vm"
git push origin main

# 登录阿里云 ArgoCD 控制面触发同步
sshpass -p 'ga@32565624' ssh root@8.148.149.80 "argocd app sync higress-svc"
```

### 步骤 6.3：排查 Pod 调度位置与状态
```bash
ssh gateman@43.139.214.231 "sudo -n k3s kubectl get pods -n higress-system -o wide"
```
**合格标志**：
- `higress-controller-*` 与 `higress-gateway-*` 均显示 `1/1 Running`；
- `NODE` 列必须为 **`free-arm-vm`**。

---

## 7. 第六阶段：端到端验证与探针验收 (流式与首字延迟)

通过 `free-arm-vm` 节点的 Tailscale IP (`100.105.130.0`) 发起端到端验证：

### 7.1 查询已授权的模型清单
```bash
curl -s http://100.105.130.0:31880/v1/models | jq .
```
预期输出：包含 `gemini-3.8-flash`、`gemini-3.8-backup`、`kimi-k3`、`glm-5.3` 等。

### 7.2 验证流式推理与首字延迟 (TTFT)
```bash
curl -X POST "http://100.105.130.0:31880/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.8-flash",
    "messages": [{"role": "user", "content": "你好，请用一句话告诉我你的名字。"}],
    "stream": true
  }'
```
预期输出：毫秒级返回第一块 SSE chunk，并且在结尾返回包含 `prompt_tokens` 与 `completion_tokens` 的 usage 块。

---

## 8. 第七阶段：故障排查与应急回滚预案

1. **若网关返回 404**：
   - 检查 `HTTPRoute` 与 `ai-proxy` 是否成功绑定到 `higress-gateway`：
   `sudo -n k3s kubectl describe wasmplugin ai-proxy -n higress-system`
2. **若请求报凭证失效或 401**：
   - 查看 ExternalSecret 同步状态：
   `sudo -n k3s kubectl get externalsecret higress-ai-secrets -n higress-system`
3. **回滚预案**：
   - 新网关采用独立的 NodePort `31880` 旁路运行，对现有的 `litellm-svc` 零流量冲突；
   - 如需卸载，在 ArgoCD 控制面执行 `argocd app delete higress-svc` 即可一键安全移除。
