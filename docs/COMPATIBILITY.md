# my-higress-svc 资产兼容契约手册 (Compatibility Contract)

> **文档版本**：v1.0.0 · **生效日期**：2026-10-03 · **核心维护者**：Jason & Cindy ❤️  
> **核心原则**：严格保证与旧版 `my-litellm-service` 数据库结构、报文格式及看板交互 100% 平滑兼容。

---

## 1. 数据库兼容契约 (MySQL HeatWave: `llm_request_logs`)

### 1.1 完整 DDL 结构 (含 `reasoning_tokens` 升级方案)

```sql
CREATE TABLE IF NOT EXISTS `llm_request_logs` (
  `id` VARCHAR(36) NOT NULL COMMENT '记录唯一标识符 (UUID4)',
  `request_id` VARCHAR(128) NOT NULL COMMENT 'API 请求 ID',
  `api_key_alias` VARCHAR(64) NOT NULL DEFAULT 'default' COMMENT '客户端 Key 别名 / 团队标识',
  `model_requested` VARCHAR(64) NOT NULL COMMENT '客户端请求的模型别名',
  `model_used` VARCHAR(64) NOT NULL COMMENT '实际命中的上游模型 ID (追踪降级轨迹)',
  `provider` VARCHAR(64) NOT NULL DEFAULT 'unknown' COMMENT '上游真实供应商标识 (如 google-gemini, a6api.com)',
  `provider_key_alias` VARCHAR(64) NOT NULL DEFAULT 'unknown' COMMENT '调用上游使用的 API Key 别名',
  `prompt_tokens` INT NOT NULL DEFAULT 0 COMMENT '输入/提示 Token 数',
  `completion_tokens` INT NOT NULL DEFAULT 0 COMMENT '输出/补全 Token 数',
  `reasoning_tokens` INT NOT NULL DEFAULT 0 COMMENT '深度思考/推理 Token 数 (本次新增兼容字段)',
  `total_tokens` INT NOT NULL DEFAULT 0 COMMENT '总 Token 消耗数 (prompt + completion + reasoning)',
  `cost_usd` DECIMAL(10, 6) NOT NULL DEFAULT 0.000000 COMMENT '美金开销 (USD)',
  `cost_cny` DECIMAL(10, 6) NOT NULL DEFAULT 0.000000 COMMENT '折合人民币开销 (RMB)',
  `fx_rate` DECIMAL(8, 4) NOT NULL DEFAULT 7.2300 COMMENT '结算时采用的当日 USD/CNY 汇率',
  `latency_ms` INT NOT NULL DEFAULT 0 COMMENT '请求响应耗时 (毫秒)',
  `status_code` INT NOT NULL DEFAULT 200 COMMENT 'HTTP 响应状态码',
  `error_msg` TEXT DEFAULT NULL COMMENT 'API 调用失败的具体异常信息',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '记录落库时间',
  PRIMARY KEY (`id`),
  INDEX `idx_logs_created_at` (`created_at`),
  INDEX `idx_logs_model_used` (`model_used`),
  INDEX `idx_logs_provider` (`provider`),
  INDEX `idx_logs_provider_key` (`provider_key_alias`),
  INDEX `idx_logs_status_code` (`status_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='大模型请求审计与计费流水表';
```

---

## 2. 多租户 Virtual Key 鉴权与 Consumer 契约 (Zero-DB Key Management)

### 2.1 鉴权机制与字段映射
- **旧系统映射**：在旧版 LiteLLM 中，每个 API Key 在 PostgreSQL `lite_llm_keys` 中拥有一条记录，对应的 `key_alias`（例如 `jayden`）在记录请求时写入 `llm_request_logs.api_key_alias`；
- **新系统规范**：Higress 采用 Kubernetes 声明式 `Consumer` CRD，在 etcd 维护租户身份，经由 Envoy 纯内存哈希鉴权：
  - 客户端携带：`Authorization: Bearer <CONSUMER_KEY>`；
  - 匹配 `Consumer.metadata.name` ➔ 自动注入为 `llm_request_logs.api_key_alias` 字段；
  - 0 查库网络 I/O，鉴权延迟压降至 `< 0.05ms`。

### 2.2 租户 Consumer 标准定义规范 (以同事 Jayden 为例)
```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: Consumer
metadata:
  # 对应 llm_request_logs 表的 api_key_alias 字段值
  name: jayden-team
  namespace: higress-system
spec:
  authConfig:
    keyAuth:
      # 分发给 Jayden 的真实 Virtual Key
      key: "sk-jayden-production-key"
  metadata:
    team: "jayden"
    cost_center: "risk-analytics"
```

---

## 3. 报文冷归档格式 (StarFive VictoriaLogs)

### 3.1 投递协议与 Endpoint
- **写入端点**：`POST http://10.0.1.227:9428/insert/jsonline`（注意：必须为 `jsonline` 规范端点）
- **传输压缩**：`Content-Encoding: gzip`
- **Stream 标签**：`_stream: '{env="prod",service="litellm",type="payload"}'`（100% 保持与老系统一致，确保 LogsQL 联合检索不脱节）
- **单块大小阈值**：单分片字符数限制在 `1,300,000` 字符（约 1.74MB），避开 VictoriaLogs 单行 1.9MB 硬上限。
- **过滤预处理**：针对请求体或响应体中的 `data:image/...;base64,...` 正则匹配并折叠替换为 `[base64_image_collapsed: len=XXXXX]`，杜绝无效索引膨胀。

### 3.2 JSONLine 结构定义 (含分块支持)

```json
{
  "_time": "2026-10-03T12:00:00.000Z",
  "_stream": "{env=\"prod\",service=\"litellm\",type=\"payload\"}",
  "_msg": "LLM 调用日志: request_id=chatcmpl-xxxx, model=gemini-3.8-flash, status=200, latency=352ms",
  "env": "prod",
  "service": "litellm",
  "type": "payload",
  "request_id": "chatcmpl-xxxx-xxxx",
  "model": "gemini-3.8-flash",
  "key_alias": "opencode-cindy",
  "status_code": 200,
  "latency_ms": 352,
  "prompt_tokens": 12,
  "completion_tokens": 8,
  "reasoning_tokens": 0,
  "total_tokens": 20,
  "spend": 0.000039,
  "shard_index": 1,
  "total_shards": 1,
  "prompt_chunk": "[{\"role\": \"user\", \"content\": \"Hello\"}]",
  "prompt": "[{\"role\": \"user\", \"content\": \"Hello\"}]",
  "response": "Hello! How can I assist you today?"
}
```

---

## 4. Redis L2 Payload 热缓存契约 (抽屉透视秒开引擎)

为了保证 React 18 看板在点击历史记录时享受 **< 5ms** 的抽屉（Payload Drawer）瞬间滑开体验，系统必须同步维护 Redis 热缓存：

### 4.1 缓存规范与生命周期
- **缓存 Key 格式**：`litellm:payload:{request_id}`
- **TTL 过期时间**：`259,200 秒`（严格对齐老系统的 3 天保存周期 `86400 * 3`）
- **压缩与编码协议**：
  1. 报文预处理：剔除/折叠图片超长 Base64；
  2. 格式装配：序列化为 JSON 字符串 `{"prompt": <dict>, "response": <dict>}`；
  3. 内存极速压缩：使用 `gzip.compress(..., compresslevel=1)`（相比裸 JSON 体积压减 80%~95%）；
  4. 文本转码：通过 `base64.b64encode` 转为 ASCII 字符串写入 Redis；
- **抽屉读取降级链路**：
  - 第一步：`GET litellm:payload:{request_id}` ➔ 若命中，内存 Base64 解码 + Gzip 解压，**<1ms 极速直出**；
  - 第二步：若未命中（缓存过期或重启），自动从 MySQL 获取 `created_at` 日期分区，向 VictoriaLogs 发起 LogsQL 聚合检索还原分片，作为兜底。

---

## 5. 汇率折算与模型单价对账规范 (FinOps Pricing)

### 3.1 结算汇率获取机制 (FX Rate Engine)
1. **优先获取**：从中国银行实时外汇牌价缓存拉取当日 `USD/CNY` 现汇卖出价；
2. **熔断兜底**：若网络超时或解析异常，强制采用保守安全汇率：`7.2300`；
3. **计算公式**：
   $$\text{cost\_usd} = (\text{prompt\_tokens} \times P_{\text{in}}) + (\text{completion\_tokens} \times P_{\text{out}}) + (\text{reasoning\_tokens} \times P_{\text{reasoning}})$$
   $$\text{cost\_cny} = \text{cost\_usd} \times \text{fx\_rate}$$

### 3.2 官方核算单价矩阵 (每 1M Tokens)

| 模型标识 | Prompt 单价 ($/1M) | Completion 单价 ($/1M) | Reasoning 单价 ($/1M) |
| :--- | :--- | :--- | :--- |
| `gemini-3.8-flash` (官方直连) | $0.75 | $3.75 | $3.75 |
| `gemini-3.8-backup` (A6 中转 5折) | $0.375 | $1.875 | $1.875 |
| `kimi-k3` (A6 BBGT 极低通道) | ¥0.0766 | ¥0.3830 | ¥0.3830 |
| `glm-5.3` (A6 VIP 推理通道) | $0.0102 | $0.032057 | $0.032057 |
| `gpt-5.6-luna-a6` | $0.0096 | $0.0096 | $0.0096 |
| `gpt-5.6-luna-yuanheng` | $0.0373 | $0.0373 | $0.0373 |
| `yui` / `rin` (自主 Agent) | $0.0000 | $0.0000 | $0.0000 |

---

## 6. 可观测性看板后端 API 契约 (Dashboard API)

迁移后的 `dashboard-api` 需保持以下 RESTful 端点契约 100% 不变：

### 4.1 查询近期调用流水
- **端点**：`GET /api/v1/logs`
- **参数**：`page` (默认 1), `page_size` (默认 20), `model`, `status_code`, `start_time`, `end_time`
- **响应**：
  ```json
  {
    "total": 1280,
    "page": 1,
    "page_size": 20,
    "items": [
      {
        "id": "uuid",
        "request_id": "chatcmpl-xxxx",
        "api_key_alias": "default",
        "model_requested": "gemini-3.8-flash",
        "model_used": "gemini-3.8-flash",
        "provider": "google-gemini",
        "prompt_tokens": 120,
        "completion_tokens": 45,
        "reasoning_tokens": 0,
        "total_tokens": 165,
        "cost_usd": 0.000258,
        "cost_cny": 0.001865,
        "fx_rate": 7.23,
        "latency_ms": 482,
        "status_code": 200,
        "created_at": "2026-10-03T12:00:00"
      }
    ]
  }
  ```

### 4.2 财务指标大屏统计
- **端点**：`GET /api/v1/metrics/summary`
- **参数**：`time_range` (24h, 7d, 30d)
- **响应**：按模型分类的总 Token 消耗、累计美金开销、折算人民币开销与平均延迟。

### 4.3 抽屉式原始报文秒级透视 (Payload Drawer)
- **端点**：`GET /api/v1/logs/{request_id}/payload`
- **逻辑**：后端通过 `request_id` 查询 StarFive VictoriaLogs 归档，解压缩后返回 `prompt` 和 `response` 的完整 JSON 结构，供给前端抽屉高亮展开。

### 6.4 [新增] Wasm 审计内部接收端点
- **端点**：`POST /api/v1/internal/audit-log`
- **权限**：仅限 K8s Pod 内网或 localhost 访问
- **作用**：接收 Wasm 插件异步发送的结构化审计对象，并发落地 MySQL 账本、Redis 3天热缓存与 VictoriaLogs 冷存储。

### 6.5 前端 SPA 静态资产契约 (Frontend Routing Contract)
- **挂载根路径**：`base: "/dashboard/"`（所有静态 CSS/JS 资源统一以 `/dashboard/` 前缀请求）；
- **SPA Fallback 规则**：由前端 Nginx 对 `/dashboard/*` 内部重定向至 `index.html`，支持页面在深层路径刷新不白屏；
- **同域相对调用**：所有前端 Ajax 请求统一采用相对路径 `/api/v1/*`，由上层网关无缝转发给 Go 后台引擎，杜绝 CORS 跨域。
