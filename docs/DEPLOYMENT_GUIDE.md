# my-higress-svc 生产环境全链路部署与实施实操指南 (Deployment Guide)

> **文档版本**：v1.0.0 · **编制日期**：2026-10-03 · **核心主笔**：Cindy (主人贴身大秘❤️)  
> **指导思想**：严格基于 GitOps 规范与方案 A（官方 Helm Chart + ArgoCD 自动化纳管），零手工漂移，严禁敏感信息进代码仓库。

---

## 目录
1. [前置环境与集群拓扑基准](#1-前置环境与集群拓扑基准)
2. [第一阶段：编写定制化 Helm Values 配置](#2-第一阶段编写定制化-helm-values-配置)
3. [第二阶段：编写凭证 Secret 规范与占位模版](#3-第二阶段编写凭证-secret-规范与占位模版)
4. [第三阶段：编写模型路由与 ai-proxy 插件配置](#4-第四阶段编写模型路由与-ai-proxy-插件配置)
5. [第四阶段：编写 ArgoCD Application 注册清单](#5-第四阶段编写-argocd-application-注册清单)
6. [第五阶段：实施执行步骤（逐步操作手册）](#6-第五阶段实施执行步骤逐步操作手册)
7. [第六阶段：端到端验证与探针验收](#7-第六阶段端到端验证与探针验收)
8. [第七阶段：故障排查与应急回滚预案](#8-第七阶段故障排查与应急回滚预案)

---

## 1. 前置环境与集群拓扑基准

部署前必须再次确认以下资产与网络上下文无误：

- **GitOps 控制面（集群 A）**：阿里云 `aliyun-k3s`（`8.148.149.80` / TS `100.114.103.101`），负责驱动 ArgoCD；
- **业务交付目标（集群 B）**：腾讯云 `tencent-dp1-cluster`（CP: `43.139.214.231`），以外部集群形态注册于 ArgoCD；
- **目标硬件宿主机**：甲骨文云新加坡节点 **`free-arm-vm`**
  - 公网 IP: `134.185.90.98` · Tailscale IP: `100.105.130.0`
  - 架构规格: **Ampere Altra ARM64 4C24G**
  - K8s 节点调度标签: `kubernetes.io/hostname=free-arm-vm`
- **入口负载均衡器**：OCI ALB（`161.118.240.179:80`），与 `free-arm-vm` 同处新加坡同一 VCN Subnet（`10.0.0.0/16`）。

---

## 2. 第一阶段：编写定制化 Helm Values 配置

在当前项目创建并配置 `deploy/k8s/values.yaml`。此文件用于覆盖 Higress 官方 Helm Chart 的默认行为，强制绑定 ARM64 宿主机并调优并发。

### 文件路径：`deploy/k8s/values.yaml`

```yaml
# ==============================================================================
# Higress Gateway Helm Values 生产定制配置 (专为 oci-free-arm-vm 调优)
# ==============================================================================

global:
  # 统一在集群 B 中创建并使用独立命名空间
  namespace: higress-system
  # 硬性调度约束：严禁调度至腾讯云 CP 或本地家宽 NUC，必须锁定在 4C24G ARM64 机器上
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
  # 开启原生 Gateway API 与 CRD 自动监听
  gateway:
    name: higress-gateway
    namespace: higress-system

# ------------------------------------------------------------------------------
# 2. Higress Gateway 数据面 (Envoy C++ 代理内核)
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
      # 给 700k+ 超长上下文与高并发留足内存余量，由于移除了 Redis 响应缓存，800Mi 极其充裕
      memory: "800Mi"

  # 暴露 Service 策略 (与 OCI ALB 对接入站)
  service:
    type: LoadBalancer
    # 保持与集群现存 Kong/svclb 端口隔离，分配专属 NodePort
    httpPort: 8080
    nodePorts:
      http: 31880

  # Envoy 原生性能与网络调优
  envoyConfig:
    # 针对超长流式 SSE 响应，调高下游与上游空闲超时，杜绝被掐流
    streamIdleTimeout: "600s"
    # 允许 5MB 以上的大报文请求体直通内存，不强制落盘
    maxRequestBodySize: 10485760 # 10MB
```

---

## 3. 第二阶段：编写凭证 Secret 规范与占位模版

遵循 Cindy 的绝对安全红线：**真实 API Key 绝对严禁进入任何 Git 提交**！

### 3.1 文件路径：`deploy/k8s/secrets.yaml.template`
（仅作版本库模版占位，以 `.template` 结尾提交）

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: higress-ai-secrets
  namespace: higress-system
type: Opaque
stringData:
  # Google Gemini 官方主力 Key 3
  GEMINI_API_KEY: "PLACEHOLDER_OPENAI_API_KEY_FREE_3"
  # A6 API 中转保底 Key
  A6_API_KEY: "PLACEHOLDER_A6_API_KEY"
  # 元衡 API 渠道 Key
  META_API_KEY: "PLACEHOLDER_META_API_KEY"
  # 自家 Hermes Agent 认证 Key
  RIN_API_KEY: "PLACEHOLDER_RIN_API_KEY"
  YUI_API_KEY: "PLACEHOLDER_YUI_API_KEY"
```

### 3.2 生产集群注入规范（运维操作命令）
真实凭证仅通过 SSH 登录集群 B 的控制节点，直接打补丁注入集群 etcd：

```bash
# 登录集群 B 控制节点
ssh gateman@43.139.214.231

# 先创建命名空间
sudo -n k3s kubectl create namespace higress-system --dry-run=client -o yaml | sudo -n k3s kubectl apply -f -

# 创建或更新真实 Secret (由主人从现有 litellm-svc 环境变量读取真实值并注入)
sudo -n k3s kubectl -n higress-system create secret generic higress-ai-secrets \
  --from-literal=GEMINI_API_KEY='<真实_OPENAI_API_KEY_FREE_3>' \
  --from-literal=A6_API_KEY='<真实_A6_API_KEY>' \
  --from-literal=META_API_KEY='<真实_META_API_KEY>' \
  --from-literal=RIN_API_KEY='<真实_RIN_API_KEY>' \
  --from-literal=YUI_API_KEY='<真实_YUI_API_KEY>' \
  --dry-run=client -o yaml | sudo -n k3s kubectl apply -f -
```

---

## 4. 第四阶段：编写模型路由与 ai-proxy 插件配置

将收敛后的模型矩阵（仅保留 3.8 全家福）与 Hermes 直通路由转化为声明式 CRD。

### 文件路径：`deploy/k8s/routes/ai-proxy.yaml`

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
    # 开启流式响应 Usage 计算
    streamUsage: true
    # 统一 OpenAI 标准补全路由
    providers:
      # ------------------------------------------------------------------------
      # 1. Google Gemini 原生直连渠道 (Tier 1 主力)
      # ------------------------------------------------------------------------
      - type: gemini
        apiTokens:
          - secretRef:
              name: higress-ai-secrets
              key: GEMINI_API_KEY
        modelMapping:
          "gemini-3.8-flash": "gemini-3.8-flash"
        # 故障转移降级链：主力失败时自动滑落至 A6 中转
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

      # ------------------------------------------------------------------------
      # 3. 元衡 API 渠道 (Luna 备用通道)
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

### 文件路径：`deploy/k8s/routes/hermes-passthrough.yaml`

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
    # Yui 直通路由 (保留原生 tool.progress 事件与流式状态)
    - matches:
        - path:
            type: PathPrefix
            value: /hermes/yui
      backendRefs:
        - name: yui-agent-external
          port: 8644
    # Rin 直通路由
    - matches:
        - path:
            type: PathPrefix
            value: /hermes/rin
      backendRefs:
        - name: rin-agent-external
          port: 8642
---
# 注册指向局域网 Jump Host (Moon 100.115.214.26) 的私网 Service
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

---

## 5. 第四阶段：编写 ArgoCD Application 注册清单

在本项目中维护 ArgoCD 注册定义，后续由主人引入 `my-argocd-manifests` 纳管。

### 文件路径：`deploy/k8s/argocd-app.yaml`

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: higress-gateway
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  sources:
    # 1. 订阅官方发布的 Higress Helm Chart (自动管理 Controller 与 CRD)
    - chart: higress
      repoURL: https://higress.io/helm-charts
      targetRevision: 2.0.7
      helm:
        valueFiles:
          - $values/deploy/k8s/values.yaml
    # 2. 挂载当前代码仓库的差异化 values 与模型路由清单
    - repoURL: https://github.com/nvd11/my-higress-svc.git
      targetRevision: main
      ref: values
      path: deploy/k8s/routes
  destination:
    # 严格交付至业务集群 B (tencent-dp1-cluster)
    server: https://43.139.214.231:6443
    namespace: higress-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      # 忽略动态注入的 Secret 真实内容，防止 Git 覆盖 etcd
      - RespectIgnoreDifferences=true
  ignoreDifferences:
    - group: ""
      kind: Secret
      name: higress-ai-secrets
      jsonPointers:
        - /data
```

---

## 6. 第五阶段：实施执行步骤（逐步操作手册）

当文件编写完毕并推送到 GitHub 后，执行以下标准实施流程：

### 步骤 6.1：本地配置提交并推送
```bash
git add deploy/k8s/
git commit -m "feat(deploy): add Helm values, model routes, and ArgoCD application manifest"
git push origin main
```

### 步骤 6.2：注入生产真实 Secret
登录集群 B（`43.139.214.231`），执行本文 [第 3.2 节](#32-生产集群注入规范运维操作命令) 的 `create secret` 命令，确保 Key 安全落入 etcd。

### 步骤 6.3：在 ArgoCD 中注册应用
通过 SSH 登录集群 A（阿里云 ArgoCD 控制面 `8.148.149.80`）：
```bash
sshpass -p 'ga@32565624' ssh root@8.148.149.80

# 拷贝并应用 argocd-app.yaml (或通过 root-bootstrap 自动拉取)
sudo -n k3s kubectl apply -f /path/to/my-higress-svc/deploy/k8s/argocd-app.yaml

# 触发主动同步
argocd app sync higress-gateway
```

### 步骤 6.4：观察 Pod 调度与就绪状态
在集群 B 节点排查运行实例是否正确落在 `free-arm-vm`：
```bash
ssh gateman@43.139.214.231
sudo -n k3s kubectl get pods -n higress-system -o wide
```
**合格标志**：
1. `higress-controller-*` 与 `higress-gateway-*` 均处于 `1/1 Running`；
2. 调度所在 `NODE` 列必须为 **`free-arm-vm`**。

---

## 7. 第六阶段：端到端验证与探针验收

### 7.1 本地免公网直连测试 (通过 free-arm-vm Tailscale IP)
在任意局域网机器发起测试请求：

```bash
# 测试 1: 查询模型清单
curl -X GET "http://100.105.130.0:31880/v1/models"

# 测试 2: 验证 Gemini 3.8 Flash 流式推理与首字延迟
curl -X POST "http://100.105.130.0:31880/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.8-flash",
    "messages": [{"role": "user", "content": "请用一句话证明你的极速！"}],
    "stream": true
  }'
```

### 7.2 降级链路验证 (Failover Test)
模拟给一个不存在或恶意的系统参数触发 Google 原生拦截，验证是否自动转由 A6 API 的 `gemini-3.8-backup` 响应。

---

## 8. 第七阶段：故障排查与应急回滚预案

1. **若 Pod 发生 CrashLoopBackOff**：
   - 检查架构兼容性：`sudo -n k3s kubectl logs -n higress-system -l app.kubernetes.io/name=higress-gateway -c gateway`；
   - 确认镜像是否原生支持 `linux/arm64`。
2. **若请求出现 404 Route Not Found**：
   - 查看 Controller 同步状态：`sudo -n k3s kubectl logs -n higress-system -l app.kubernetes.io/name=higress-controller`；
   - 验证 `WasmPlugin` 与 `HTTPRoute` 是否生效绑定。
3. **紧急一键回滚**：
   - 现有 OCI ALB 仍指向旧版 LiteLLM 网关，新网关目前属于**独立 NodePort (31880) 旁路验证**；
   - 即使新网关出现异常，对线上生产流量造成 **0 影响**，直接在 ArgoCD 执行 `argocd app delete higress-gateway` 即可安全卸载。
