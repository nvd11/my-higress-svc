"""Internal Audit Log Ingestion API for Higress Wasm Plugin.

接收自研 Wasm-Go 插件 (finops-audit) 异步派发的审计数据，三位一体同时落地:
1. MySQL HeatWave (llm_request_logs 结构化计费账本)
2. K3s 业务集群 Redis (litellm:payload:{request_id}, 3天 Base64+Gzip 热缓存, 支撑大屏抽屉 <5ms 秒开)
3. StarFive 星光板 VictoriaLogs (1.3MB 自动切片分块 + Gzip 传输, 永久全文冷归档)
"""

import asyncio
import base64
import gzip
import json
import logging
from datetime import UTC, datetime
from typing import Any

from fastapi import APIRouter, Depends, Header, HTTPException, status
from pydantic import BaseModel, Field
from sqlalchemy import insert

from app.core.backends.factory import get_payload_backend
from app.core.config import Settings, get_settings
from app.core.payload_backend import PayloadBackend
from app.core.payload_uploader import _fold_base64_for_cache
from app.core.redis_client import get_redis_client
from app.db import get_async_engine, llm_request_logs

logger = logging.getLogger(__name__)
router = APIRouter(prefix="/internal", tags=["Internal"])


class WasmAuditRecord(BaseModel):
    """Wasm 插件传递过来的审计结构体."""

    id: str
    request_id: str
    api_key_alias: str = "default"
    model_requested: str
    model_used: str
    provider: str = "google-gemini"
    provider_key_alias: str = "OPENAI_API_KEY_FREE_3"
    prompt_tokens: int = 0
    completion_tokens: int = 0
    reasoning_tokens: int = 0
    total_tokens: int = 0
    cost_usd: float = 0.0
    cost_cny: float = 0.0
    fx_rate: float = 7.2300
    latency_ms: int = 0
    status_code: int = 200
    error_msg: str | None = None
    created_at: datetime | None = None
    prompt: str | None = None
    response: str | None = None


@router.post("/audit-log", status_code=status.HTTP_200_OK)
async def ingest_audit_log(
    record: WasmAuditRecord,
    settings: Settings = Depends(get_settings),
    backend: PayloadBackend = Depends(get_payload_backend),
    x_internal_source: str | None = Header(None),
) -> dict[str, Any]:
    """处理 Wasm 旁路审计数据并并行派发三方存储."""
    created_time = record.created_at or datetime.now(UTC)

    # 1. 组装写入 MySQL 的数据字典 (严格匹配 llm_request_logs 字段)
    db_values = {
        "id": record.id,
        "request_id": record.request_id,
        "api_key_alias": record.api_key_alias,
        "model_requested": record.model_requested,
        "model_used": record.model_used,
        "provider": record.provider,
        "provider_key_alias": record.provider_key_alias,
        "prompt_tokens": record.prompt_tokens,
        "completion_tokens": record.completion_tokens,
        "reasoning_tokens": record.reasoning_tokens,
        "total_tokens": record.total_tokens,
        "cost_usd": record.cost_usd,
        "cost_cny": record.cost_cny,
        "fx_rate": record.fx_rate,
        "latency_ms": record.latency_ms,
        "status_code": record.status_code,
        "error_msg": record.error_msg,
        "created_at": created_time,
    }

    async def write_mysql() -> None:
        try:
            engine = get_async_engine(settings)
            stmt = insert(llm_request_logs).values(**db_values)
            async with engine.begin() as conn:
                await conn.execute(stmt)
            logger.info("Successfully recorded audit log in MySQL: %s", record.request_id)
        except Exception as e:
            logger.warning("Failed writing audit log to MySQL for %s: %s", record.request_id, e)

    async def write_redis() -> None:
        if not record.prompt and not record.response:
            return
        try:
            redis = get_redis_client(settings)
            cache_key = f"litellm:payload:{record.request_id}"

            # 解析 prompt/response 为 json 对象
            try:
                p_obj = json.loads(record.prompt) if record.prompt else {}
            except Exception:
                p_obj = {"content": record.prompt}

            try:
                r_obj = json.loads(record.response) if record.response else {}
            except Exception:
                r_obj = {"reply": record.response}

            cache_prompt = _fold_base64_for_cache(p_obj)
            cache_response = _fold_base64_for_cache(r_obj)

            raw_json = json.dumps(
                {"prompt": cache_prompt, "response": cache_response},
                ensure_ascii=False,
            )
            compressed_bytes = gzip.compress(raw_json.encode("utf-8"), compresslevel=1)
            cached_val = base64.b64encode(compressed_bytes).decode("ascii")

            # 3天过期 (259200 秒)
            await redis.set(cache_key, cached_val, ex=86400 * 3)
            logger.info("Successfully wrote Redis L2 cache for %s", record.request_id)
        except Exception as e:
            logger.warning("Failed writing Redis L2 payload cache for %s: %s", record.request_id, e)

    async def write_vlogs() -> None:
        if not record.prompt and not record.response:
            return
        try:
            try:
                p_obj = json.loads(record.prompt) if record.prompt else {}
            except Exception:
                p_obj = {"content": record.prompt}

            try:
                r_obj = json.loads(record.response) if record.response else {}
            except Exception:
                r_obj = {"reply": record.response}

            meta = {
                "date": created_time.strftime("%Y-%m-%d"),
                "timestamp": created_time.isoformat(),
                "model": record.model_used,
                "key_alias": record.api_key_alias,
                "status_code": record.status_code,
                "latency_ms": record.latency_ms,
                "prompt_tokens": record.prompt_tokens,
                "completion_tokens": record.completion_tokens,
                "reasoning_tokens": record.reasoning_tokens,
                "total_tokens": record.total_tokens,
                "spend": record.cost_usd,
            }
            await backend.write_payload(
                request_id=record.request_id,
                prompt=p_obj,
                response=r_obj,
                metadata=meta,
            )
            logger.info("Successfully archived payload to VictoriaLogs for %s", record.request_id)
        except Exception as e:
            logger.warning("Failed archiving payload to VictoriaLogs for %s: %s", record.request_id, e)

    # 并发三路异步落地，零串行拖延
    asyncio.create_task(asyncio.gather(write_mysql(), write_redis(), write_vlogs()))

    return {"status": "accepted", "request_id": record.request_id}
