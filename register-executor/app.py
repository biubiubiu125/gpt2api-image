from __future__ import annotations

import json
import os
import threading
from typing import Any

import uvicorn
from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, ConfigDict

from services.config import config
from services.outlook_check import check_outlook_pool
from services.register_service import register_service


app = FastAPI(title="gpt2api-image register executor")
_stop_event = threading.Event()


class RegisterConfigRequest(BaseModel):
    model_config = ConfigDict(extra="allow")


class OutlookPoolResetRequest(BaseModel):
    scope: str = "all"


class OutlookPoolTestRequest(BaseModel):
    limit: int = 5


def _require_internal(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)) -> None:
    expected = config.register_internal_key
    if not expected:
        return
    bearer = ""
    if authorization and authorization.lower().startswith("bearer "):
        bearer = authorization[7:].strip()
    if x_register_internal_key == expected or bearer == expected:
        return
    raise HTTPException(status_code=401, detail="register executor unauthorized")


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
    return {"register": register_service.update(body.model_dump(exclude_none=True))}


@app.post("/api/register/start")
def start_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.start()}


@app.post("/api/register/stop")
def stop_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.stop()}


@app.post("/api/register/reset")
def reset_register(x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.reset()}


@app.post("/api/register/outlook-pool/reset")
def reset_outlook_pool(body: OutlookPoolResetRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"register": register_service.reset_outlook_pool(body.scope or "all")}


@app.post("/api/register/outlook-pool/test")
def test_outlook_pool(body: OutlookPoolTestRequest, x_register_internal_key: str | None = Header(default=None), authorization: str | None = Header(default=None)):
    _require_internal(x_register_internal_key, authorization)
    return {"result": check_outlook_pool(register_service.get(), body.limit)}


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
