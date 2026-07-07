from __future__ import annotations

import json
import os
import secrets
import threading
from typing import Any

import uvicorn
from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, ConfigDict, Field

from services.config import config
from services.outlook_check import check_outlook_pool
from services.register import mail_provider
from services.register_service import RegisterTaskActiveError, register_service


app = FastAPI(title="gpt2api-image register executor")
_stop_event = threading.Event()


class RegisterConfigRequest(BaseModel):
    model_config = ConfigDict(extra="allow")


class OutlookPoolResetRequest(BaseModel):
    scope: str = "all"


class OutlookPoolTestRequest(BaseModel):
    limit: int = 5


class YYDSDomainBlacklistRequest(BaseModel):
    domain: str | None = None
    domains: list[str] = Field(default_factory=list)


def _require_internal(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)) -> None:
    expected = config.register_internal_key
    if not expected:
        raise HTTPException(status_code=401, detail="register executor internal key is required")
    bearer = ""
    if authorization and authorization.lower().startswith("bearer "):
        bearer = authorization[7:].strip()
    header_key = x_register_internal_key or ""
    if secrets.compare_digest(header_key, expected) or secrets.compare_digest(bearer, expected):
        return
    raise HTTPException(status_code=401, detail="register executor unauthorized")


def _raise_task_active(exc: RegisterTaskActiveError) -> None:
    raise HTTPException(status_code=409, detail=str(exc)) from exc


@app.on_event("startup")
def _startup() -> None:
    register_service.start_auto_refill_watcher(_stop_event)


@app.on_event("shutdown")
def _shutdown() -> None:
    _stop_event.set()
    register_service.stop()


@app.get("/health")
def health() -> dict[str, Any]:
    return {"ok": True, "running": register_service.is_running()}


@app.get("/api/register")
def get_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.get()}


@app.post("/api/register")
def update_register(body: RegisterConfigRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    try:
        return {"register": register_service.update(body.model_dump(exclude_none=True))}
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.post("/api/register/start")
def start_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.start()}


@app.post("/api/register/repair-abnormal")
def repair_abnormal_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.repair_abnormal()}


@app.post("/api/register/stop")
def stop_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.stop()}


@app.post("/api/register/reset")
def reset_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    try:
        return {"register": register_service.reset()}
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.post("/api/register/outlook-pool/reset")
def reset_outlook_pool(body: OutlookPoolResetRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    try:
        return {"register": register_service.reset_outlook_pool(body.scope or "all")}
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.post("/api/register/outlook-pool/test")
def test_outlook_pool(body: OutlookPoolTestRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"result": check_outlook_pool(register_service.get(redact=False), body.limit)}


@app.get("/api/register/yyds-domain-blacklist")
def get_yyds_domain_blacklist(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"items": mail_provider.yyds_domain_blacklist_items()}


@app.post("/api/register/yyds-domain-blacklist")
def add_yyds_domain_blacklist(body: YYDSDomainBlacklistRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    domains = list(body.domains or [])
    if body.domain:
        domains.append(body.domain)
    try:
        return register_service.add_yyds_domain_blacklist(domains)
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.post("/api/register/yyds-domain-blacklist/remove")
def remove_yyds_domain_blacklist(body: YYDSDomainBlacklistRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    domains = list(body.domains or [])
    if body.domain:
        domains.append(body.domain)
    try:
        return register_service.remove_yyds_domain_blacklist(domains)
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.post("/api/register/yyds-domain-blacklist/replace")
def replace_yyds_domain_blacklist(body: YYDSDomainBlacklistRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    domains = list(body.domains or [])
    if body.domain:
        domains.append(body.domain)
    try:
        return register_service.replace_yyds_domain_blacklist(domains)
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.post("/api/register/yyds-domain-blacklist/reset")
def reset_yyds_domain_blacklist(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    try:
        return register_service.reset_yyds_domain_blacklist()
    except RegisterTaskActiveError as exc:
        _raise_task_active(exc)


@app.get("/api/register/events")
async def register_events(request: Request, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)

    async def stream():
        last = ""
        while True:
            if await request.is_disconnected():
                break
            payload = json.dumps(register_service.get(), ensure_ascii=False)
            if payload != last:
                yield f"data: {payload}\n\n"
                last = payload
            import asyncio

            await asyncio.sleep(0.5)

    return StreamingResponse(stream(), media_type="text/event-stream")


if __name__ == "__main__":
    host = os.getenv("REGISTER_EXECUTOR_ADDR", "0.0.0.0")
    port = int(os.getenv("REGISTER_EXECUTOR_PORT", "8091"))
    uvicorn.run("app:app", host=host, port=port, log_level="info")
