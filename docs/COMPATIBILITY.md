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

## 2. 报文冷归档格式 (StarFive VictoriaLogs)

### 2.1 投递协议与 Endpoint
- **写入端点**：`POST http://10.0.1.227:9428/insert/jsonl`
- **传输压缩**：`Content-Encoding: gzip`
- **过滤预处理**：针对请求体或响应体中的 `data:image/...;base64,...` 正则匹配并折叠替换为 `[base64_image_collapsed: len=XXXXX]`，杜绝无效索引膨胀。

### 2.2 JSONL 结构定义

```json
{
  "_time": "2026-10-03T12:00:00.000Z",
  "_stream": "{app=\"higress-gateway\", env=\"production\"}",
  "request_id": "chatcmpl-xxxx-xxxx",
  "model_requested": "gemini-3.8-flash",
  "model_used": "gemini-3.8-flash",
  "api_key_alias": "opencode-cindy",
  "prompt": "[{\"role\": \"user\", \"content\": \"Hello\"}]",
  "response": "Hello! How can I assist you today?",
  "prompt_tokens": 12,
  "completion_tokens": 8,
  "reasoning_tokens": 0,
  "total_tokens": 20,
  "status_code": 200,
  "latency_ms": 352
}
```

---

## 3. 汇率折算与模型单价对账规范 (FinOps Pricing)

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

## 4. 可观测性看板后端 API 契约 (Dashboard API)

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

### 4.4 [新增] Wasm 审计内部接收端点
- **端点**：`POST /api/v1/internal/audit-log`
- **权限**：仅限 K8s Pod 内网或 localhost 访问
- **作用**：接收 Wasm 插件异步发送的结构化审计对象，并批量或单条写入 MySQL。
