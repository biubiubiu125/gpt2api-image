from __future__ import annotations

import base64
import hashlib
import json
import random
import re
import secrets
import string
import threading
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlencode, urljoin, urlparse

from curl_cffi import requests

from services.account_service import account_service
from services.proxy_service import proxy_settings
from services.register import mail_provider
from services.register.proxy_pool import RegisterProxySelection, classify_register_failure, register_proxy_pool
from utils.log import logger


class RegisteredAccountValidationError(RuntimeError):
    pass


class RegisteredTokenAcquiredRefreshError(RegisteredAccountValidationError):
    pass


_TOKEN_LIKE_RE = re.compile(r"\b(eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|[A-Za-z0-9_-]{48,})\b")
_SENSITIVE_TOKEN_KEYS = {"token", "access_token", "refresh_token", "id_token"}
_CHROME_VERSION_RE = re.compile(r"Chrome/(?P<version>\d+(?:\.\d+){0,3})")
_CLIENT_HINT_VERSION_RE = re.compile(r'("(?P<brand>[^"]+)";v=")(?P<version>[^"]+)(")')


def _int_or_default(value: object, default: int) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _mask_token(value: object) -> str:
    text = str(value or "")
    if not text:
        return ""
    if len(text) <= 14:
        return "***"
    return f"{text[:8]}...{text[-6:]}"


def _redact_refresh_errors(value: object) -> object:
    if isinstance(value, dict):
        redacted: dict = {}
        for key, item in value.items():
            normalized = str(key).lower()
            if normalized in _SENSITIVE_TOKEN_KEYS or normalized.endswith("_token"):
                redacted[key] = _mask_token(item)
            else:
                redacted[key] = _redact_refresh_errors(item)
        return redacted
    if isinstance(value, list):
        return [_redact_refresh_errors(item) for item in value]
    if isinstance(value, tuple):
        return tuple(_redact_refresh_errors(item) for item in value)
    if isinstance(value, str):
        return _TOKEN_LIKE_RE.sub(lambda match: _mask_token(match.group(0)), value)
    return value


def _normalized_chrome_version(user_agent_value: object) -> tuple[str, str]:
    match = _CHROME_VERSION_RE.search(str(user_agent_value or ""))
    version = str((match.group("version") if match else "") or "145.0.0.0").strip()
    parts = [part for part in version.split(".") if part]
    while len(parts) < 4:
        parts.append("0")
    full = ".".join(parts[:4]) or "145.0.0.0"
    major = parts[0] if parts else "145"
    return major, full


def _replace_client_hint_versions(template: str, major: str, full: str) -> str:
    text = str(template or "").strip()
    if not text:
        return ""

    def _replace(match: re.Match[str]) -> str:
        brand = str(match.group("brand") or "").strip()
        version = str(match.group("version") or "").strip()
        if brand not in {"Google Chrome", "Chromium"}:
            return match.group(0)
        resolved = full if "." in version else major
        return f'{match.group(1)}{resolved}{match.group(4)}'

    return _CLIENT_HINT_VERSION_RE.sub(_replace, text)


def _resolve_user_agent_client_hints(user_agent_value: object) -> dict[str, str]:
    resolved_user_agent = str(user_agent_value or "").strip() or user_agent
    major, full = _normalized_chrome_version(resolved_user_agent)
    return {
        "user_agent": resolved_user_agent,
        "sec_ch_ua": _replace_client_hint_versions(sec_ch_ua, major, full) or sec_ch_ua,
        "sec_ch_ua_full_version_list": _replace_client_hint_versions(sec_ch_ua_full_version_list, major, full) or sec_ch_ua_full_version_list,
    }


def _saved_image_account_usable(item: dict | None) -> bool:
    if not isinstance(item, dict):
        return False
    return (
        str(item.get("status") or "正常") == "正常"
        and not bool(item.get("pending_delete"))
        and not bool(item.get("image_quota_unknown"))
        and _int_or_default(item.get("quota"), 0) > 0
    )


def _delete_saved_account_or_raise(access_token: str) -> None:
    token = str(access_token or "").strip()
    if not token:
        return
    try:
        account_service.delete_accounts([token])
    except Exception as exc:
        raise RegisteredAccountValidationError(f"registered_account_delete_failed: {exc}") from exc
    try:
        remaining = account_service.get_account(token)
    except Exception as exc:
        raise RegisteredAccountValidationError(f"registered_account_delete_check_failed: {exc}") from exc
    if remaining is not None:
        raise RegisteredAccountValidationError("registered_account_delete_not_persisted")


def _refresh_failed_validation_error(access_token: str, refresh_error: object) -> tuple[RegisteredAccountValidationError, bool]:
    try:
        _delete_saved_account_or_raise(access_token)
    except Exception as cleanup_exc:
        err = RegisteredTokenAcquiredRefreshError(
            f"registered_account_refresh_failed: {refresh_error}; registered_account_delete_failed: {cleanup_exc}"
        )
        err.refresh_failed = True
        err.token_acquired = True
        err.failure_reason = "account_delete_failed"
        return err, False
    err = RegisteredTokenAcquiredRefreshError(f"registered_account_refresh_failed: {refresh_error}")
    err.refresh_failed = True
    err.token_acquired = True
    err.failure_reason = "token_acquired_refresh_failed"
    return err, True


def _require_register_tokens(tokens: dict | None) -> dict:
    if not isinstance(tokens, dict):
        raise RuntimeError("token_exchange_failed: token response is empty")
    missing = [
        key
        for key in ("access_token", "refresh_token", "id_token")
        if not str(tokens.get(key) or "").strip()
    ]
    if missing:
        raise RuntimeError(f"token_exchange_failed: missing {', '.join(missing)}")
    return tokens


base_dir = Path(__file__).resolve().parent
config = {
    "mail": {
        "request_timeout": 30,
        "wait_timeout": 30,
        "wait_interval": 2,
        "api_use_register_proxy": False,
        "providers": [],
    },
    "proxy": "",
    "task_timeout_seconds": 300,
    "task_stall_timeout_seconds": 60,
    "fixed_password": "",
    "total": 10,
    "threads": 3,
}
register_config_file = base_dir.parents[1] / "data" / "register.json"
try:
    saved_config = json.loads(register_config_file.read_text(encoding="utf-8"))
    config.update({key: saved_config[key] for key in config if key in saved_config})
except Exception:
    pass

auth_base = "https://auth.openai.com"
platform_base = "https://platform.openai.com"
platform_oauth_client_id = "app_2SKx67EdpoN0G6j64rFvigXD"
platform_oauth_redirect_uri = f"{platform_base}/auth/callback"
platform_oauth_audience = "https://api.openai.com/v1"
platform_auth0_client = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
user_agent = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
    "AppleWebKit/537.36 (KHTML, like Gecko) "
    "Chrome/145.0.0.0 Safari/537.36"
)
sec_ch_ua = '"Google Chrome";v="145", "Not?A_Brand";v="8", "Chromium";v="145"'
sec_ch_ua_full_version_list = '"Chromium";v="145.0.0.0", "Not:A-Brand";v="99.0.0.0", "Google Chrome";v="145.0.0.0"'
default_timeout = 30
print_lock = threading.Lock()
stats_lock = threading.Lock()
stats = {"done": 0, "success": 0, "usable_success": 0, "fail": 0, "saved": 0, "refresh_failed": 0, "token_acquired_refresh_failed": 0, "start_time": 0.0}
register_log_sink = None
worker_state_lock = threading.Lock()
worker_states: dict[str, dict[str, Any]] = {}
WORKER_STATE_ACTIVE_STATUSES = {"running", "waiting_proxy"}
WORKER_STATE_HISTORY_LIMIT = 100
active_run_id = ""
active_run_lock = threading.Lock()
worker_run_context = threading.local()


class RegisterStopped(RuntimeError):
    pass


class RegisterTaskTimeout(RuntimeError):
    pass


class RegisterRunInvalidated(RegisterStopped):
    pass


def set_active_run_id(run_id: str) -> None:
    global active_run_id
    with active_run_lock:
        active_run_id = str(run_id or "")


def clear_active_run_id(run_id: str = "") -> None:
    global active_run_id
    with active_run_lock:
        if not run_id or active_run_id == str(run_id):
            active_run_id = ""


def _check_run_active(run_id: str = "") -> None:
    if not run_id:
        return
    with active_run_lock:
        current = active_run_id
    if current != str(run_id):
        raise RegisterRunInvalidated("register_run_invalidated")


def _current_worker_run_id() -> str:
    return str(getattr(worker_run_context, "run_id", "") or "")


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _set_worker_state(index: int, **updates: Any) -> None:
    key = str(index)
    terminal = bool(updates.pop("_terminal", False))
    force = bool(updates.pop("_force", False))
    update_run_id = str(updates.pop("run_id", "") or _current_worker_run_id())
    with worker_state_lock:
        current = dict(worker_states.get(key) or {})
        current_run_id = str(current.get("run_id") or "")
        if update_run_id:
            with active_run_lock:
                active = active_run_id
            if active and update_run_id != active and not force:
                return
            if current_run_id and current_run_id != update_run_id and not force:
                return
        if current.get("terminal") and not force:
            return
        if (
            current.get("failure_reason") == "register_task_stalled"
            and updates.get("failure_reason") != "register_task_stalled"
            and not (updates.get("status") == "failed" and updates.get("failure_reason"))
        ):
            return
        current["index"] = index
        if update_run_id:
            current["run_id"] = update_run_id
        current.update(updates)
        if terminal:
            current["terminal"] = True
        current["updated_at"] = _utc_now()
        worker_states[key] = current
        _prune_worker_states_locked()


def _worker_state_sort_key(key: str) -> tuple[int, str]:
    return (int(key), key) if key.isdigit() else (-1, key)


def _prune_worker_states_locked() -> None:
    completed = [
        key
        for key, value in worker_states.items()
        if str(value.get("status") or "") not in WORKER_STATE_ACTIVE_STATUSES
    ]
    overflow = len(completed) - WORKER_STATE_HISTORY_LIMIT
    if overflow <= 0:
        return
    for key in sorted(completed, key=_worker_state_sort_key)[:overflow]:
        worker_states.pop(key, None)


def get_worker_states() -> list[dict[str, Any]]:
    with worker_state_lock:
        _prune_worker_states_locked()
        return [dict(value) for _, value in sorted(worker_states.items(), key=lambda item: _worker_state_sort_key(item[0]))]


def clear_worker_states() -> None:
    with worker_state_lock:
        worker_states.clear()


def mark_worker_states_failed(indexes: list[int], reason: str, error: str) -> None:
    for index in indexes:
        _set_worker_state(index, status="failed", failure_reason=reason, last_error=error, _terminal=True)


def mark_worker_states_stopped(indexes: list[int], error: str) -> None:
    for index in indexes:
        _set_worker_state(index, status="stopped", last_error=error, _terminal=True)


def mark_worker_states_failed_for_run(indexes: list[int], run_id: str, reason: str, error: str, terminal: bool = True) -> None:
    for index in indexes:
        _set_worker_state(index, status="failed", failure_reason=reason, last_error=error, run_id=run_id, _terminal=terminal)


def mark_worker_states_stopped_for_run(indexes: list[int], run_id: str, error: str, terminal: bool = True) -> None:
    for index in indexes:
        _set_worker_state(index, status="stopped", last_error=error, run_id=run_id, _terminal=terminal)


def mark_worker_states_stopping_for_run(indexes: list[int], run_id: str, error: str) -> None:
    for index in indexes:
        _set_worker_state(index, status="stopping", last_error=error, run_id=run_id)

common_headers = {
    "accept": "application/json",
    "accept-encoding": "gzip, deflate, br",
    "accept-language": "en-US,en;q=0.9",
    "cache-control": "no-cache",
    "connection": "keep-alive",
    "content-type": "application/json",
    "dnt": "1",
    "origin": auth_base,
    "priority": "u=1, i",
    "sec-gpc": "1",
    "sec-ch-ua": sec_ch_ua,
    "sec-ch-ua-arch": '"x86_64"',
    "sec-ch-ua-bitness": '"64"',
    "sec-ch-ua-full-version-list": sec_ch_ua_full_version_list,
    "sec-ch-ua-mobile": "?0",
    "sec-ch-ua-model": '""',
    "sec-ch-ua-platform": '"Windows"',
    "sec-ch-ua-platform-version": '"10.0.0"',
    "sec-fetch-dest": "empty",
    "sec-fetch-mode": "cors",
    "sec-fetch-site": "same-origin",
    "user-agent": user_agent,
}

navigate_headers = {
    "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
    "accept-encoding": "gzip, deflate, br",
    "accept-language": "en-US,en;q=0.9",
    "cache-control": "max-age=0",
    "connection": "keep-alive",
    "dnt": "1",
    "sec-gpc": "1",
    "sec-ch-ua": sec_ch_ua,
    "sec-ch-ua-arch": '"x86_64"',
    "sec-ch-ua-bitness": '"64"',
    "sec-ch-ua-full-version-list": sec_ch_ua_full_version_list,
    "sec-ch-ua-mobile": "?0",
    "sec-ch-ua-model": '""',
    "sec-ch-ua-platform": '"Windows"',
    "sec-ch-ua-platform-version": '"10.0.0"',
    "sec-fetch-dest": "document",
    "sec-fetch-mode": "navigate",
    "sec-fetch-site": "same-origin",
    "sec-fetch-user": "?1",
    "upgrade-insecure-requests": "1",
    "user-agent": user_agent,
}


def log(text: str, color: str = "") -> None:
    colors = {"red": "\033[31m", "green": "\033[32m", "yellow": "\033[33m"}
    if register_log_sink:
        try:
            register_log_sink(text, color)
        except Exception:
            pass
    with print_lock:
        prefix = colors.get(color, "")
        suffix = "\033[0m" if prefix else ""
        print(f"{prefix}{datetime.now().strftime('%H:%M:%S')} {text}{suffix}")


def step(index: int, text: str, color: str = "") -> None:
    _set_worker_state(index, step=text, level=color or "info")
    log(f"[任务{index}] {text}", color)


def heartbeat(index: int) -> None:
    _set_worker_state(index)


def heartbeat_with_proxy(index: int, selection: RegisterProxySelection | None) -> None:
    heartbeat(index)


def _make_trace_headers() -> dict[str, str]:
    trace_id = str(random.getrandbits(64))
    parent_id = str(random.getrandbits(64))
    return {
        "traceparent": f"00-{uuid.uuid4().hex}-{format(int(parent_id), '016x')}-01",
        "tracestate": "dd=s:1;o:rum",
        "x-datadog-origin": "rum",
        "x-datadog-parent-id": parent_id,
        "x-datadog-sampling-priority": "1",
        "x-datadog-trace-id": trace_id,
    }


from utils.pkce import generate_pkce as _generate_pkce  # noqa: F401


def _random_password(length: int = 16) -> str:
    chars = string.ascii_letters + string.digits + "!@#$%"
    value = list(
        secrets.choice(string.ascii_uppercase)
        + secrets.choice(string.ascii_lowercase)
        + secrets.choice(string.digits)
        + secrets.choice("!@#$%")
        + "".join(secrets.choice(chars) for _ in range(max(0, length - 4)))
    )
    random.shuffle(value)
    return "".join(value)


def _random_name() -> tuple[str, str]:
    return random.choice(["James", "Robert", "John", "Michael", "David", "Mary", "Emma", "Olivia"]), random.choice(
        ["Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller"]
    )


def _random_birthdate() -> str:
    return f"{random.randint(1996, 2006):04d}-{random.randint(1, 12):02d}-{random.randint(1, 28):02d}"


def _response_json(resp) -> dict:
    try:
        data = resp.json()
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def _decode_jwt_payload(token: str) -> dict:
    try:
        payload = str(token or "").split(".")[1]
        payload += "=" * ((4 - len(payload) % 4) % 4)
        data = json.loads(base64.urlsafe_b64decode(payload.encode("ascii")))
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def _response_debug_detail(resp, limit: int = 800) -> str:
    if resp is None:
        return ""
    data = _response_json(resp)
    parts = [
        f"url={str(getattr(resp, 'url', '') or '')[:300]}",
        f"content_type={str(getattr(resp, 'headers', {}).get('content-type') or '')}",
    ]
    for key in ("cf-ray", "x-request-id", "openai-processing-ms"):
        value = str(getattr(resp, "headers", {}).get(key) or "").strip()
        if value:
            parts.append(f"{key}={value}")
    if data:
        parts.append(f"json={json.dumps(data, ensure_ascii=False)[:limit]}")
    else:
        parts.append(f"body={str(getattr(resp, 'text', '') or '')[:limit]}")
    return ", ".join(parts)


def _is_cloudflare_challenge(resp) -> bool:
    if resp is None:
        return False
    try:
        status_code = int(getattr(resp, "status_code", 0) or 0)
    except (TypeError, ValueError):
        status_code = 0
    if status_code not in (403, 503):
        return False
    text = str(getattr(resp, "text", "") or "").lower()
    return (
        "<title>just a moment" in text
        or "just a moment" in text
        or "<title>attention required! | cloudflare" in text
        or "attention required! | cloudflare" in text
        or "cloudflare" in str(getattr(resp, "headers", {}).get("server") or "").lower()
        or "cf-chl-" in text
        or "__cf_chl_" in text
        or "cf-browser-verification" in text
    )


def _mail_config(register_proxy: str = "", deadline: float | None = None, check_control=None) -> dict:
    mail = dict(config.get("mail") if isinstance(config.get("mail"), dict) else {})
    mail.pop("proxy", None)
    # Keep registration proxy isolated to OpenAI signup; mailbox provider APIs must stay direct.
    mail["api_use_register_proxy"] = False
    if deadline is not None:
        mail["_deadline"] = deadline
    if callable(check_control):
        mail["_check_control"] = check_control
    return mail


def _authorize_landed_page(resp) -> str:
    """Return a rough authorize landing page label for logs only."""

    if resp is None:
        return ""
    final_url = str(getattr(resp, "url", "") or "").lower()
    data = _response_json(resp)
    page_type = ""
    page = data.get("page") if isinstance(data, dict) else None
    if isinstance(page, dict):
        page_type = str(page.get("type") or "").lower()
    if "create-account" in final_url or "signup" in final_url or "create_account" in page_type:
        return "signup"
    if "/log-in" in final_url or "/login" in final_url or page_type in {"login", "password_verification"}:
        return "login"
    return ""


def create_mailbox(
    username: str | None = None,
    register_proxy: str = "",
    deadline: float | None = None,
    check_control=None,
) -> dict:
    return mail_provider.create_mailbox(_mail_config(register_proxy, deadline, check_control), username)


def wait_for_code(
    mailbox: dict,
    register_proxy: str = "",
    check_control=None,
    deadline: float | None = None,
    heartbeat_fn=None,
) -> str | None:
    def wrapped_check_control() -> None:
        if heartbeat_fn is not None:
            heartbeat_fn()
        if check_control is not None:
            check_control()

    return mail_provider.wait_for_code(_mail_config(register_proxy, deadline, wrapped_check_control), mailbox, wrapped_check_control)


def _normalize_domain_name(value: object) -> str:
    return str(value or "").strip().lower().lstrip("@").rstrip(".")


def _mailbox_source_meta(mailbox: dict | None) -> dict[str, str]:
    if not isinstance(mailbox, dict):
        return {}
    address = str(mailbox.get("address") or "").strip()
    email_domain = _normalize_domain_name(address.partition("@")[2] if "@" in address else "")
    source_domain = _normalize_domain_name(mailbox.get("source_domain") or mailbox.get("domain") or email_domain)
    return {
        "provider": str(mailbox.get("provider") or "").strip(),
        "provider_id": str(mailbox.get("provider_id") or "").strip(),
        "provider_ref": str(mailbox.get("provider_ref") or "").strip(),
        "email_domain": email_domain,
        "source_domain": source_domain,
    }


def _release_mailbox(mailbox: dict, mail_config: dict, index: int) -> None:
    released, reason = mail_provider.release_mailbox(mailbox, mail_config)
    if released:
        return
    provider = str(mailbox.get("provider") or "")
    address = str(mailbox.get("address") or "").strip() or "-"
    if provider == "yyds_mail":
        step(index, f"yyds 邮箱释放失败: {address}, {reason or 'unknown error'}", "yellow")


from utils.sentinel import build_sentinel_token as _build_sentinel_token_tuple, ensure_protocol_sentinel_runtime, prepare_sentinel_artifacts  # noqa: F401


def _set_oai_device_cookies(session: requests.Session, device_id: str) -> None:
    domains = [
        ".auth.openai.com",
        "auth.openai.com",
        ".openai.com",
        "openai.com",
        ".chatgpt.com",
        "chatgpt.com",
        "sentinel.openai.com",
    ]
    for domain in domains:
        try:
            session.cookies.set("oai-did", device_id, domain=domain)
        except Exception:
            continue


def build_sentinel_token(session: requests.Session, device_id: str, flow: str, *, proxy: str = "") -> str:
    """Return sentinel token header value."""
    _set_oai_device_cookies(session, device_id)
    hints = _resolve_user_agent_client_hints(user_agent)
    sentinel_val, _oai_sc_val = _build_sentinel_token_tuple(
        session,
        device_id,
        flow,
        user_agent=hints["user_agent"],
        sec_ch_ua=hints["sec_ch_ua"],
        sec_ch_ua_full_version_list=hints["sec_ch_ua_full_version_list"],
        proxy=proxy,
    )
    return sentinel_val


def create_session(proxy: str = "") -> Any:
    kwargs = proxy_settings.build_session_kwargs(
        proxy=proxy,
        upstream=True,
        select_pool=False,
        impersonate="chrome",
    )
    return requests.Session(**kwargs)


def _check_task_control(stop_event: threading.Event | None = None, deadline: float | None = None) -> None:
    if stop_event is not None and stop_event.is_set():
        raise RegisterStopped("register_task_stopped")
    if deadline is not None and time.monotonic() >= deadline:
        raise RegisterTaskTimeout("register_task_timeout")


def _remaining_timeout(deadline: float | None, fallback: float = default_timeout) -> float:
    timeout = max(0.5, float(fallback or default_timeout))
    if deadline is None:
        return timeout
    try:
        remaining = float(deadline) - time.monotonic()
    except (TypeError, ValueError):
        return timeout
    if remaining <= 0:
        return 0.5
    return max(0.5, min(timeout, remaining))


def _request_timeout(deadline: float | None, fallback: float = default_timeout) -> float:
    return _remaining_timeout(deadline, fallback)


def _set_case_insensitive_header(headers: dict[str, str], name: str, value: str) -> None:
    target_key = next((key for key in headers if key.lower() == name.lower()), name)
    headers[target_key] = value


def _build_request_headers(
    headers: dict[str, str],
    target_url: str,
    proxy: str = "",
    user_agent_override: str = "",
) -> dict[str, str]:
    merged = proxy_settings.build_headers(
        headers=headers,
        target_url=target_url,
        proxy=proxy,
        upstream=True,
        select_pool=False,
    )
    normalized = {str(key): str(value) for key, value in merged.items()}
    if user_agent_override:
        hints = _resolve_user_agent_client_hints(user_agent_override)
        _set_case_insensitive_header(normalized, "user-agent", hints["user_agent"])
        _set_case_insensitive_header(normalized, "sec-ch-ua", hints["sec_ch_ua"])
        _set_case_insensitive_header(normalized, "sec-ch-ua-full-version-list", hints["sec_ch_ua_full_version_list"])
    return normalized


def _resolve_user_agent_override(value: object) -> str:
    if callable(value):
        try:
            value = value()
        except Exception:
            value = ""
    return str(value or "").strip()


def _request_with_retry_headers(
    session: requests.Session,
    method: str,
    url: str,
    *,
    headers: dict[str, str],
    proxy: str = "",
    user_agent_override: object = "",
    stop_event: threading.Event | None = None,
    deadline: float | None = None,
    retry_attempts: int = 3,
    **kwargs,
) -> tuple[object | None, str]:
    return request_with_local_retry(
        session,
        method,
        url,
        headers=_build_request_headers(
            dict(headers),
            url,
            proxy,
            _resolve_user_agent_override(user_agent_override),
        ),
        retry_attempts=retry_attempts,
        stop_event=stop_event,
        deadline=deadline,
        **kwargs,
    )


def _cloudflare_block_message(resp, prefix: str = "遇到 Cloudflare 拦截", reason: str = "") -> str:
    status = getattr(resp, "status_code", "unknown")
    debug = _response_debug_detail(resp)
    reason = reason or "当前代理 IP 或环境被 Cloudflare 拦截"
    return f"{prefix}: {reason}: status={status}, {debug}"


def request_with_local_retry(
    session: requests.Session,
    method: str,
    url: str,
    retry_attempts: int = 3,
    stop_event: threading.Event | None = None,
    deadline: float | None = None,
    **kwargs,
):
    last_error = ""
    request_timeout_override = kwargs.pop("timeout", None)
    for _ in range(max(1, retry_attempts)):
        _check_task_control(stop_event, deadline)
        try:
            resp = session.request(
                method.upper(),
                url,
                timeout=request_timeout_override if request_timeout_override is not None else _request_timeout(deadline),
                **kwargs,
            )
            return resp, ""
        except Exception as error:
            last_error = str(error)
            if stop_event is not None and stop_event.wait(1):
                raise RegisterStopped("register_task_stopped")
            _check_task_control(stop_event, deadline)
    return None, last_error


def extract_oauth_callback_params_from_url(url: str) -> dict[str, str] | None:
    if not url:
        return None
    try:
        params = parse_qs(urlparse(url).query)
    except Exception:
        return None
    code = str((params.get("code") or [""])[0]).strip()
    if not code:
        return None
    return {"code": code, "state": str((params.get("state") or [""])[0]).strip(), "scope": str((params.get("scope") or [""])[0]).strip()}


def _find_matching_oauth_callback(urls: list[str], expected_state: str = "") -> tuple[dict[str, str] | None, bool]:
    normalized_expected_state = str(expected_state or "").strip()
    mismatch_found = False
    for url in urls:
        params = extract_oauth_callback_params_from_url(str(url or "").strip())
        if not params:
            continue
        callback_state = str(params.get("state") or "").strip()
        if normalized_expected_state and callback_state != normalized_expected_state:
            mismatch_found = True
            continue
        return params, mismatch_found
    return None, mismatch_found


def _select_oauth_callback_params(
    urls: list[str],
    expected_state: str = "",
    *,
    error_prefix: str = "oauth_callback",
) -> dict[str, str] | None:
    params, mismatch_found = _find_matching_oauth_callback(urls, expected_state)
    if params:
        return params
    if mismatch_found and str(expected_state or "").strip():
        raise RuntimeError(f"{error_prefix}_state_mismatch")
    return None


def _select_oauth_continue_url(
    urls: list[str],
    expected_state: str = "",
    *,
    error_prefix: str = "oauth_callback",
) -> str:
    normalized_expected_state = str(expected_state or "").strip()
    fallback_url = ""
    mismatch_found = False
    for raw_url in urls:
        url = str(raw_url or "").strip()
        if not url:
            continue
        params = extract_oauth_callback_params_from_url(url)
        if not params:
            if not fallback_url:
                fallback_url = url
            continue
        callback_state = str(params.get("state") or "").strip()
        if normalized_expected_state and callback_state != normalized_expected_state:
            mismatch_found = True
            continue
        return url
    if mismatch_found and normalized_expected_state and not fallback_url:
        raise RuntimeError(f"{error_prefix}_state_mismatch")
    return fallback_url


def _absolute_auth_url(url: str) -> str:
    url = str(url or "").strip()
    if not url:
        return ""
    return urljoin(f"{auth_base}/", url)


def extract_oauth_callback_params_from_consent_session(
    session: requests.Session,
    consent_url: str,
    device_id: str,
    stop_event: threading.Event | None = None,
    deadline: float | None = None,
    *,
    proxy: str = "",
    user_agent_override: object = "",
    expected_state: str = "",
) -> dict[str, str] | None:
    current_url = _absolute_auth_url(consent_url)
    if not current_url:
        return None
    headers = dict(navigate_headers)
    headers["referer"] = auth_base
    headers["oai-device-id"] = device_id
    state_mismatch_found = False
    for _ in range(10):
        _check_task_control(stop_event, deadline)
        resp, _error = _request_with_retry_headers(
            session,
            "get",
            current_url,
            headers=headers,
            proxy=proxy,
            user_agent_override=user_agent_override,
            allow_redirects=False,
            stop_event=stop_event,
            deadline=deadline,
            retry_attempts=1,
        )
        if resp is None:
            break
        callback_params, mismatch_found = _find_matching_oauth_callback(
            [
                str(getattr(resp, "url", "") or ""),
                str(getattr(resp, "headers", {}).get("location") or getattr(resp, "headers", {}).get("Location") or ""),
            ],
            expected_state,
        )
        state_mismatch_found = state_mismatch_found or mismatch_found
        if callback_params:
            return callback_params
        location = str(getattr(resp, "headers", {}).get("location") or getattr(resp, "headers", {}).get("Location") or "").strip()
        if getattr(resp, "status_code", 0) not in (301, 302, 303, 307, 308) or not location:
            break
        current_url = _absolute_auth_url(location)

    raw_session = (
        session.cookies.get("oai-client-auth-session", domain=".auth.openai.com")
        or session.cookies.get("oai-client-auth-session", domain="auth.openai.com")
        or session.cookies.get("oai-client-auth-session")
    )
    if not raw_session:
        if state_mismatch_found and str(expected_state or "").strip():
            raise RuntimeError("consent_callback_state_mismatch")
        return None
    try:
        first_part = str(raw_session).split(".")[0]
        first_part += "=" * ((4 - len(first_part) % 4) % 4)
        session_payload = json.loads(base64.urlsafe_b64decode(first_part.encode("ascii")))
        workspaces = session_payload.get("workspaces") if isinstance(session_payload, dict) else []
        workspace_id = str(((workspaces or [{}])[0] or {}).get("id") or "").strip()
    except Exception:
        workspace_id = ""
    if not workspace_id:
        return None

    headers = dict(common_headers)
    headers["referer"] = current_url
    headers["oai-device-id"] = device_id
    headers.update(_make_trace_headers())
    resp, _error = _request_with_retry_headers(
        session,
        "post",
        f"{auth_base}/api/accounts/workspace/select",
        json={"workspace_id": workspace_id},
        headers=headers,
        proxy=proxy,
        user_agent_override=user_agent_override,
        allow_redirects=False,
        stop_event=stop_event,
        deadline=deadline,
        retry_attempts=1,
    )
    if resp is None:
        if state_mismatch_found and str(expected_state or "").strip():
            raise RuntimeError("consent_callback_state_mismatch")
        return None
    callback_params, mismatch_found = _find_matching_oauth_callback(
        [str(getattr(resp, "headers", {}).get("location") or getattr(resp, "headers", {}).get("Location") or "")],
        expected_state,
    )
    state_mismatch_found = state_mismatch_found or mismatch_found
    if callback_params:
        return callback_params

    data = _response_json(resp)
    orgs = ((data.get("data") or {}).get("orgs") or []) if isinstance(data, dict) else []
    org = (orgs or [{}])[0] or {}
    org_id = str(org.get("id") or "").strip()
    projects = org.get("projects") or []
    project_id = str(((projects or [{}])[0] or {}).get("id") or "").strip()
    if not org_id:
        return None
    org_headers = dict(common_headers)
    org_headers["referer"] = str(data.get("continue_url") or current_url)
    org_headers["oai-device-id"] = device_id
    org_headers.update(_make_trace_headers())
    body = {"org_id": org_id}
    if project_id:
        body["project_id"] = project_id
    resp, _error = _request_with_retry_headers(
        session,
        "post",
        f"{auth_base}/api/accounts/organization/select",
        json=body,
        headers=org_headers,
        proxy=proxy,
        user_agent_override=user_agent_override,
        allow_redirects=False,
        stop_event=stop_event,
        deadline=deadline,
        retry_attempts=1,
    )
    if resp is None:
        if state_mismatch_found and str(expected_state or "").strip():
            raise RuntimeError("consent_callback_state_mismatch")
        return None
    callback_params, mismatch_found = _find_matching_oauth_callback(
        [str(getattr(resp, "headers", {}).get("location") or getattr(resp, "headers", {}).get("Location") or "")],
        expected_state,
    )
    state_mismatch_found = state_mismatch_found or mismatch_found
    if callback_params:
        return callback_params
    if state_mismatch_found and str(expected_state or "").strip():
        raise RuntimeError("consent_callback_state_mismatch")
    return None


def request_platform_oauth_token(
    session: requests.Session,
    code: str,
    code_verifier: str,
    stop_event: threading.Event | None = None,
    deadline: float | None = None,
    *,
    proxy: str = "",
    user_agent_override: object = "",
) -> dict | None:
    headers = {
        "accept": "*/*",
        "accept-language": "zh-CN,zh;q=0.9",
        "auth0-client": platform_auth0_client,
        "cache-control": "no-cache",
        "content-type": "application/json",
        "origin": platform_base,
        "pragma": "no-cache",
        "priority": "u=1, i",
        "referer": f"{platform_base}/",
        "sec-ch-ua": sec_ch_ua,
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": '"Windows"',
        "sec-fetch-dest": "empty",
        "sec-fetch-mode": "cors",
        "sec-fetch-site": "same-site",
        "user-agent": user_agent,
    }
    _check_task_control(stop_event, deadline)
    resp, _error = _request_with_retry_headers(
        session,
        "post",
        f"{auth_base}/api/accounts/oauth/token",
        headers=headers,
        json={
            "client_id": platform_oauth_client_id,
            "code_verifier": code_verifier,
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": platform_oauth_redirect_uri,
        },
        proxy=proxy,
        user_agent_override=user_agent_override,
        stop_event=stop_event,
        deadline=deadline,
        timeout=_request_timeout(deadline, 60),
    )
    if resp is None:
        return None
    if resp.status_code != 200:
        logger.warning({
            "event": "register_oauth_token_rejected",
            "status": resp.status_code,
            "detail": _response_json(resp) or str(getattr(resp, "text", "") or "")[:300],
        })
        return None
    return _response_json(resp)


def exchange_platform_tokens(
    session: requests.Session,
    device_id: str,
    code_verifier: str,
    consent_url: str,
    stop_event: threading.Event | None = None,
    deadline: float | None = None,
    *,
    proxy: str = "",
    user_agent_override: object = "",
    expected_state: str = "",
) -> dict | None:
    callback_params = extract_oauth_callback_params_from_consent_session(
        session,
        consent_url,
        device_id,
        stop_event,
        deadline,
        proxy=proxy,
        user_agent_override=user_agent_override,
        expected_state=expected_state,
    )
    if not callback_params:
        try:
            fallback_headers = dict(navigate_headers)
            fallback_headers["referer"] = auth_base
            fallback_headers["oai-device-id"] = device_id
            resp, _error = _request_with_retry_headers(
                session,
                "get",
                _absolute_auth_url(consent_url),
                headers=fallback_headers,
                proxy=proxy,
                user_agent_override=user_agent_override,
                allow_redirects=True,
                stop_event=stop_event,
                deadline=deadline,
                retry_attempts=1,
            )
            if resp is None:
                callback_params = None
                raise RuntimeError("consent_fetch_failed")
            history_locations = [
                str(getattr(history_resp, "headers", {}).get("location") or getattr(history_resp, "headers", {}).get("Location") or "").strip()
                for history_resp in (getattr(resp, "history", []) or [])
            ]
            callback_params = _select_oauth_callback_params(
                [str(getattr(resp, "url", "") or ""), *history_locations],
                expected_state,
                error_prefix="consent_callback",
            )
        except RuntimeError as error:
            if "state_mismatch" in str(error):
                raise
            callback_params = None
        except Exception:
            callback_params = None
    code = str((callback_params or {}).get("code") or "").strip()
    if not code:
        return None
    tokens = request_platform_oauth_token(
        session,
        code,
        code_verifier,
        stop_event,
        deadline,
        proxy=proxy,
        user_agent_override=user_agent_override,
    )
    if not tokens:
        return None
    payload = _decode_jwt_payload(str(tokens.get("id_token") or "")) or _decode_jwt_payload(str(tokens.get("access_token") or ""))
    email = str(payload.get("email") or "").strip()
    if email and not tokens.get("email"):
        tokens["email"] = email
    return tokens


class PlatformRegistrar:
    def __init__(
        self,
        proxy: str = "",
        stop_event: threading.Event | None = None,
        deadline: float | None = None,
        run_id: str = "",
    ) -> None:
        self.proxy = str(proxy or "").strip()
        self.session = create_session(self.proxy)
        self.stop_event = stop_event
        self.deadline = deadline
        self.run_id = str(run_id or "")
        self.device_id = str(uuid.uuid4())
        self.code_verifier = ""
        self.platform_auth_state = ""
        self.platform_auth_code = ""
        self.platform_continue_url = ""
        self.last_mailbox: dict[str, Any] = {}
        self.otp_verified = False

    def close(self) -> None:
        self.session.close()

    def _request(self, session: requests.Session, method: str, url: str, **kwargs) -> tuple[object | None, str]:
        kwargs.pop("stop_event", None)
        kwargs.pop("deadline", None)
        return request_with_local_retry(
            session,
            method,
            url,
            stop_event=self.stop_event,
            deadline=self.deadline,
            **kwargs,
        )

    def _check_task_control(self) -> None:
        _check_task_control(self.stop_event, self.deadline)
        _check_run_active(self.run_id)

    def _navigate_headers(self, referer: str = "") -> dict[str, str]:
        headers = dict(navigate_headers)
        if referer:
            headers["referer"] = referer
        return headers

    def _json_headers(self, referer: str, device_id: str = "") -> dict[str, str]:
        headers = dict(common_headers)
        headers["referer"] = referer
        headers["oai-device-id"] = str(device_id or self.device_id)
        headers.update(_make_trace_headers())
        return headers

    def _sentinel_user_agent(self, session: requests.Session | None = None) -> str:
        active_session = session or self.session
        session_headers = getattr(active_session, "headers", {}) or {}
        return str(session_headers.get("User-Agent") or session_headers.get("user-agent") or user_agent).strip() or user_agent

    def _sentinel_client_hints(self, session: requests.Session | None = None) -> dict[str, str]:
        return _resolve_user_agent_client_hints(self._sentinel_user_agent(session))

    def _ensure_sentinel_runtime(self, index: int) -> None:
        self._check_task_control()
        hints = self._sentinel_client_hints(self.session)
        ensure_protocol_sentinel_runtime(
            proxy=self.proxy,
            user_agent=hints["user_agent"],
            sec_ch_ua=hints["sec_ch_ua"],
            sec_ch_ua_full_version_list=hints["sec_ch_ua_full_version_list"],
            check_control=self._check_task_control,
        )

    def _prepare_sentinel_headers(
        self,
        session: requests.Session,
        device_id: str,
        flow: str,
        index: int,
        *,
        referer: str,
        target_url: str,
        include_so: bool = False,
        observer_wait_ms: int = 0,
        strict_turnstile: bool = True,
        strict_so: bool = False,
        log_event: str = "register_sentinel",
    ) -> tuple[dict[str, str], object]:
        _set_oai_device_cookies(session, device_id)
        sentinel_hints = self._sentinel_client_hints(session)
        artifacts = prepare_sentinel_artifacts(
            session,
            device_id,
            flow,
            include_so=include_so,
            observer_wait_ms=observer_wait_ms,
            user_agent=sentinel_hints["user_agent"],
            sec_ch_ua=sentinel_hints["sec_ch_ua"],
            sec_ch_ua_full_version_list=sentinel_hints["sec_ch_ua_full_version_list"],
            strict_turnstile=strict_turnstile,
            strict_so=strict_so,
            proxy=self.proxy,
            check_control=self._check_task_control,
        )
        logger.info(
            {
                "event": log_event,
                "executor": getattr(artifacts, "executor", ""),
                "sdk_version": getattr(artifacts, "sdk_version", ""),
                "sentinel_token_len": len(getattr(artifacts, "sentinel_header", "") or ""),
                "so_token_len": len(getattr(artifacts, "so_header", "") or ""),
                "so_token_generated": bool(getattr(artifacts, "so_header", "")),
                "proof_required": getattr(artifacts, "proof_required", False),
                "turnstile_required": getattr(artifacts, "turnstile_required", False),
                "so_required": getattr(artifacts, "so_required", False),
            }
        )
        so_header = str(getattr(artifacts, "so_header", "") or "")
        if include_so and strict_so and getattr(artifacts, "so_required", False) and not so_header:
            raise RuntimeError("sentinel_so_token_failed")
        headers = self._json_headers(referer, device_id)
        headers["openai-sentinel-token"] = str(getattr(artifacts, "sentinel_header", "") or "")
        if include_so and so_header:
            headers["openai-sentinel-so-token"] = so_header
        return _build_request_headers(headers, target_url, self.proxy, user_agent_override=sentinel_hints["user_agent"]), artifacts

    def _platform_authorize(self, email: str, index: int) -> None:
        self._check_task_control()
        step(index, "开始 platform authorize")
        _set_oai_device_cookies(self.session, self.device_id)
        self.platform_auth_code = ""
        self.platform_continue_url = ""
        self.platform_auth_state = ""
        self.code_verifier, code_challenge = _generate_pkce()
        auth_state = secrets.token_urlsafe(32)
        self.platform_auth_state = auth_state
        params = {
            "issuer": auth_base,
            "client_id": platform_oauth_client_id,
            "audience": platform_oauth_audience,
            "redirect_uri": platform_oauth_redirect_uri,
            "device_id": self.device_id,
            "screen_hint": "signup",
            "max_age": "0",
            "login_hint": email,
            "scope": "openid profile email offline_access",
            "response_type": "code",
            "response_mode": "query",
            "state": auth_state,
            "nonce": secrets.token_urlsafe(32),
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
            "auth0Client": platform_auth0_client,
        }
        target_url = f"{auth_base}/api/accounts/authorize?{urlencode(params)}"
        headers = _build_request_headers(
            self._navigate_headers(f"{platform_base}/"),
            target_url,
            self.proxy,
            user_agent_override=self._sentinel_user_agent(self.session),
        )
        resp, error = self._request(self.session, "get", target_url, headers=headers, allow_redirects=True, stop_event=self.stop_event, deadline=self.deadline)
        if _is_cloudflare_challenge(resp):
            raise RuntimeError(_cloudflare_block_message(resp))
        if resp is None or resp.status_code != 200:
            err = _response_json(resp).get("error", {}) if resp is not None else {}
            detail = f": {err.get('code', '')} - {err.get('message', '')}".strip(" -") if err else ""
            debug = _response_debug_detail(resp)
            status = getattr(resp, "status_code", "unknown")
            raise RuntimeError(error or f"platform_authorize_http_{status}{detail}, {debug}")
        landed = _authorize_landed_page(resp)
        step(index, f"platform authorize 完成[{landed or '?'}] url={str(getattr(resp, 'url', '') or '')[:160]}")

    def _submit_signup_email(self, email: str, index: int) -> None:
        self._check_task_control()
        step(index, "提交注册邮箱")
        url = f"{auth_base}/api/accounts/authorize/continue"
        response = None
        error = ""
        for attempt in range(3):
            headers, _artifacts = self._prepare_sentinel_headers(
                self.session,
                self.device_id,
                "authorize_continue",
                index,
                referer=f"{auth_base}/create-account?usernameKind=email",
                target_url=url,
                log_event="register_authorize_continue_sentinel",
            )
            response, error = self._request(
                self.session,
                "post",
                url,
                json={"username": {"kind": "email", "value": email}},
                headers=headers,
                allow_redirects=False,
                stop_event=self.stop_event,
                deadline=self.deadline,
            )
            if _is_cloudflare_challenge(response):
                raise RuntimeError(_cloudflare_block_message(response))
            if response is not None and getattr(response, "status_code", 0) == 409 and attempt < 2:
                step(index, f"注册邮箱 409/会话失效，重新 authorize 后重试 ({attempt + 2}/3)", "yellow")
                self._platform_authorize(email, index)
                continue
            break
        if response is None or getattr(response, "status_code", 0) != 200:
            data = _response_json(response) if response is not None else {}
            detail = json.dumps(data, ensure_ascii=False) if data else ""
            raise RuntimeError(error or f"signup_email_submit_http_{getattr(response, 'status_code', 'unknown')}" + (f": {detail}" if detail else ""))
        step(index, "注册邮箱提交成功")

    def _register_user(self, email: str, password: str, index: int) -> None:
        self._check_task_control()
        step(index, "提交注册账号")
        url = f"{auth_base}/api/accounts/user/register"
        headers, _artifacts = self._prepare_sentinel_headers(
            self.session,
            self.device_id,
            "username_password_create",
            index,
            referer=f"{auth_base}/create-account/password",
            target_url=url,
            log_event="register_username_password_create_sentinel",
        )
        resp, error = self._request(self.session, "post", url, json={"username": email, "password": password}, headers=headers, stop_event=self.stop_event, deadline=self.deadline)
        if _is_cloudflare_challenge(resp):
            raise RuntimeError(_cloudflare_block_message(resp))
        if resp is None or resp.status_code != 200:
            data = _response_json(resp) if resp is not None else {}
            if data.get("message") == "Failed to create account. Please try again.":
                step(index, "OpenAI 返回创建失败，通常是账号/代理/风控问题", "yellow")
            detail = f", detail={json.dumps(data, ensure_ascii=False)}" if data else ""
            raise RuntimeError(error or f"user_register_http_{getattr(resp, 'status_code', 'unknown')}{detail}")
        step(index, "注册账号成功")

    def _send_otp(self, index: int) -> None:
        self._check_task_control()
        step(index, "发送邮箱验证码")
        url = f"{auth_base}/api/accounts/email-otp/send"
        headers = _build_request_headers(
            self._navigate_headers(f"{auth_base}/create-account/password"),
            url,
            self.proxy,
            user_agent_override=self._sentinel_user_agent(self.session),
        )
        resp, error = self._request(self.session, "get", url, headers=headers, allow_redirects=True, stop_event=self.stop_event, deadline=self.deadline)
        if _is_cloudflare_challenge(resp):
            raise RuntimeError(_cloudflare_block_message(resp))
        if resp is None or resp.status_code not in (200, 302):
            raise RuntimeError(error or f"send_otp_http_{getattr(resp, 'status_code', 'unknown')}")
        step(index, "邮箱验证码已发送")

    def _validate_otp_response(
        self,
        session: requests.Session,
        device_id: str,
        code: str,
        index: int,
        *,
        log_event: str,
    ) -> tuple[object | None, str]:
        url = f"{auth_base}/api/accounts/email-otp/validate"
        headers, _artifacts = self._prepare_sentinel_headers(
            session,
            device_id,
            "authorize_continue",
            index,
            referer=f"{auth_base}/email-verification",
            target_url=url,
            log_event=log_event,
        )
        resp, error = self._request(
            session,
            "post",
            url,
            json={"code": code},
            headers=headers,
            stop_event=self.stop_event,
            deadline=self.deadline,
        )
        if _is_cloudflare_challenge(resp):
            raise RuntimeError(_cloudflare_block_message(resp))
        return resp, error

    def _validate_otp(self, code: str, index: int) -> None:
        self._check_task_control()
        step(index, f"验证邮箱验证码 {code}")
        resp, error = self._validate_otp_response(
            self.session,
            self.device_id,
            code,
            index,
            log_event="register_validate_otp_sentinel",
        )
        if resp is None or resp.status_code != 200:
            body = ""
            try:
                body = (resp.text or "")[:500] if resp is not None else ""
            except Exception:
                pass
            raise RuntimeError(error or f"validate_otp_http_{getattr(resp, 'status_code', 'unknown')}_body={body}")
        self.otp_verified = True
        step(index, "邮箱验证码验证成功")

    def _create_account(self, name: str, birthdate: str, index: int) -> None:
        self._check_task_control()
        step(index, "创建账号资料")
        url = f"{auth_base}/api/accounts/create_account"

        def _create_headers() -> dict[str, str]:
            headers, _artifacts = self._prepare_sentinel_headers(
                self.session,
                self.device_id,
                "oauth_create_account",
                index,
                referer=f"{auth_base}/about-you",
                target_url=url,
                include_so=True,
                observer_wait_ms=5000,
                strict_turnstile=True,
                strict_so=True,
                log_event="register_create_account_sentinel",
            )
            return headers

        headers = _create_headers()
        resp, error = self._request(self.session, "post", url, json={"name": name, "birthdate": birthdate}, headers=headers, stop_event=self.stop_event, deadline=self.deadline)
        if _is_cloudflare_challenge(resp):
            raise RuntimeError(_cloudflare_block_message(resp))
        if resp is None or resp.status_code not in (200, 302):
            data = _response_json(resp) if resp is not None else {}
            if data.get("message") == "Failed to create account. Please try again.":
                step(index, "OpenAI 返回创建资料失败，通常是账号/代理/风控问题", "yellow")
            detail = f", detail={json.dumps(data, ensure_ascii=False)}" if data else ""
            raise RuntimeError(error or f"create_account_http_{getattr(resp, 'status_code', 'unknown')}{detail}")
        data = _response_json(resp)
        continue_url = str(data.get("continue_url") or "").strip()
        location = str(getattr(resp, "headers", {}).get("location") or getattr(resp, "headers", {}).get("Location") or "").strip()
        response_url = str(getattr(resp, "url", "") or "").strip()
        history_urls = [
            str(getattr(history_resp, "headers", {}).get("location") or getattr(history_resp, "headers", {}).get("Location") or "").strip()
            for history_resp in (getattr(resp, "history", []) or [])
        ]
        response_continue_url = response_url if (
            extract_oauth_callback_params_from_url(response_url)
            or "/sign-in-with-chatgpt/" in response_url
            or "/auth/callback" in response_url
        ) else ""
        callback_params = _select_oauth_callback_params(
            [continue_url, location, response_url, *history_urls],
            self.platform_auth_state,
            error_prefix="create_account_callback",
        )
        self.platform_continue_url = _select_oauth_continue_url(
            [continue_url, location, response_continue_url, *history_urls],
            self.platform_auth_state,
            error_prefix="create_account_callback",
        )
        self.platform_auth_code = str((callback_params or {}).get("code") or "").strip()
        if not self.platform_auth_code and not self.platform_continue_url:
            raise RuntimeError("create_account_missing_continue_url")
        step(index, "账号资料创建成功")

    def _exchange_registered_tokens(self, index: int) -> dict:
        self._check_task_control()
        step(index, "交换 token")
        user_agent_override = self._sentinel_user_agent(self.session)
        if self.platform_auth_code:
            tokens = request_platform_oauth_token(
                self.session,
                self.platform_auth_code,
                self.code_verifier,
                self.stop_event,
                self.deadline,
                proxy=self.proxy,
                user_agent_override=user_agent_override,
            )
        elif self.platform_continue_url:
            tokens = exchange_platform_tokens(
                self.session,
                self.device_id,
                self.code_verifier,
                self.platform_continue_url,
                self.stop_event,
                self.deadline,
                proxy=self.proxy,
                user_agent_override=user_agent_override,
                expected_state=self.platform_auth_state,
            )
        else:
            raise RuntimeError("token_exchange_failed: missing callback state")
        if not tokens:
            raise RuntimeError("token 交换失败")
        step(index, "token 交换成功")
        return tokens

    def _login_and_exchange_tokens(self, email: str, password: str, mailbox: dict, index: int) -> dict:
        self._check_task_control()
        step(index, "登录账号并重新交换 token")
        login_session = create_session(self.proxy)
        login_device_id = str(uuid.uuid4())
        _set_oai_device_cookies(login_session, login_device_id)
        code_verifier, code_challenge = _generate_pkce()
        login_auth_state = secrets.token_urlsafe(32)
        params = {
            "issuer": auth_base,
            "client_id": platform_oauth_client_id,
            "audience": platform_oauth_audience,
            "redirect_uri": platform_oauth_redirect_uri,
            "device_id": login_device_id,
            "screen_hint": "login_or_signup",
            "max_age": "0",
            "login_hint": email,
            "scope": "openid profile email offline_access",
            "response_type": "code",
            "response_mode": "query",
            "state": login_auth_state,
            "nonce": secrets.token_urlsafe(32),
            "code_challenge": code_challenge,
            "code_challenge_method": "S256",
            "auth0Client": platform_auth0_client,
        }

        def _login_nav_headers(referer: str = "") -> dict[str, str]:
            headers = dict(navigate_headers)
            if referer:
                headers["referer"] = referer
            return headers

        def _login_json_headers(referer: str) -> dict[str, str]:
            headers = dict(common_headers)
            headers["referer"] = referer
            headers["oai-device-id"] = login_device_id
            headers.update(_make_trace_headers())
            return headers

        def _clear_login_auth_cookies() -> None:
            for cookie in list(login_session.cookies):
                if "auth.openai.com" in str(cookie.domain):
                    login_session.cookies.clear(domain=cookie.domain, path=cookie.path, name=cookie.name)
            _set_oai_device_cookies(login_session, login_device_id)

        def _do_login_authorize(label: str) -> tuple[object | None, str]:
            target_url = f"{auth_base}/api/accounts/authorize?{urlencode(params)}"
            headers = _build_request_headers(
                _login_nav_headers(f"{platform_base}/"),
                target_url,
                self.proxy,
                user_agent_override=self._sentinel_user_agent(login_session),
            )
            resp, error = self._request(login_session, "get", target_url, headers=headers, allow_redirects=True, stop_event=self.stop_event, deadline=self.deadline)
            if resp is None:
                raise RuntimeError(error or f"platform_login_authorize_{label}_failed")
            if _is_cloudflare_challenge(resp):
                raise RuntimeError(_cloudflare_block_message(resp))
            if resp is None or getattr(resp, "status_code", 0) not in (200, 302):
                raise RuntimeError(error or f"platform_login_authorize_{label}_http_{getattr(resp, 'status_code', 'unknown')}")
            step(index, "登录 authorize 完成" if label == "initial" else f"登录 authorize 完成[{label}]")
            return resp, error

        def _do_authorize_continue() -> tuple[object | None, str]:
            url = f"{auth_base}/api/accounts/authorize/continue"
            headers, _artifacts = self._prepare_sentinel_headers(
                login_session,
                login_device_id,
                "authorize_continue",
                index,
                referer=f"{auth_base}/log-in?usernameKind=email",
                target_url=url,
                log_event="login_authorize_continue_sentinel",
            )
            resp, error = self._request(login_session, "post", url, json={"username": {"kind": "email", "value": email}}, headers=headers, allow_redirects=False, stop_event=self.stop_event, deadline=self.deadline)
            if _is_cloudflare_challenge(resp):
                raise RuntimeError(_cloudflare_block_message(resp))
            return resp, error

        def _submit_email_with_reauth() -> None:
            nonlocal resp, error
            step(index, "提交登录邮箱")
            for attempt in range(3):
                if attempt:
                    step(index, f"登录邮箱 409/会话失效，清理 cookie 后重试 authorize ({attempt + 1}/3)", "yellow")
                    _clear_login_auth_cookies()
                    resp, error = _do_login_authorize(f"email-{attempt + 1}")
                resp, error = _do_authorize_continue()
                if resp is not None and getattr(resp, "status_code", 0) == 409:
                    continue
                break
            if resp is None or getattr(resp, "status_code", 0) != 200:
                data = _response_json(resp) if resp is not None else {}
                detail = json.dumps(data, ensure_ascii=False) if data else ""
                raise RuntimeError(error or f"email_submit_http_{getattr(resp, 'status_code', 'unknown')}" + (f": {detail}" if detail else ""))
            step(index, "登录邮箱提交成功")

        def _verify_password_once() -> tuple[object | None, str]:
            url = f"{auth_base}/api/accounts/password/verify"
            headers, _artifacts = self._prepare_sentinel_headers(
                login_session,
                login_device_id,
                "password_verify",
                index,
                referer=f"{auth_base}/log-in/password",
                target_url=url,
                log_event="login_password_verify_sentinel",
            )
            resp, error = self._request(login_session, "post", url, json={"password": password}, headers=headers, allow_redirects=False, stop_event=self.stop_event, deadline=self.deadline)
            if _is_cloudflare_challenge(resp):
                raise RuntimeError(_cloudflare_block_message(resp))
            return resp, error

        def _verify_password_with_reauth() -> None:
            nonlocal resp, error
            step(index, "验证登录密码")
            for attempt in range(3):
                if attempt:
                    step(index, f"登录密码 HTTP 409，重新 authorize 后重试 ({attempt + 1}/3)", "yellow")
                    _clear_login_auth_cookies()
                    resp, error = _do_login_authorize(f"password-{attempt + 1}")
                    _submit_email_with_reauth()
                resp, error = _verify_password_once()
                if resp is not None and getattr(resp, "status_code", 0) == 409:
                    continue
                break
            if resp is None or getattr(resp, "status_code", 0) != 200:
                body = ""
                try:
                    body = (resp.text or "")[:500] if resp is not None else ""
                except Exception:
                    pass
                raise RuntimeError(error or f"password_verify_http_{getattr(resp, 'status_code', '')}_body={body}")
            step(index, "登录密码验证成功")

        try:
            resp = None
            error = ""
            resp, error = _do_login_authorize("initial")
            _submit_email_with_reauth()
            _verify_password_with_reauth()

            payload = _response_json(resp)
            continue_url = str(payload.get("continue_url") or "").strip()
            page_type = str(((payload.get("page") or {}).get("type")) or "")

            if page_type == "email_otp_verification" or "email-verification" in continue_url or "email-otp" in continue_url:
                step(index, "登录触发邮箱验证码")
                self._check_task_control()
                code = wait_for_code(mailbox, self.proxy, self._check_task_control, self.deadline, lambda: heartbeat(index))
                if not code:
                    raise RuntimeError("等待登录验证码超时")
                step(index, f"收到登录验证码 {code}")
                resp, reason = self._validate_otp_response(
                    login_session,
                    login_device_id,
                    code,
                    index,
                    log_event="login_validate_otp_sentinel",
                )
                if resp is None or resp.status_code != 200:
                    data = _response_json(resp) if resp is not None else {}
                    message = str((data.get("error") or {}).get("message") or data.get("message") or "").strip()
                    raise RuntimeError(reason or f"登录验证码验证失败{': ' + message if message else ''}")
                otp_payload = _response_json(resp)
                continue_url = str(otp_payload.get("continue_url") or continue_url).strip()
                step(index, "登录验证码验证成功")

            if not continue_url:
                continue_url = f"{auth_base}/sign-in-with-chatgpt/codex/consent"
            callback_params = _select_oauth_callback_params(
                [continue_url],
                login_auth_state,
                error_prefix="login_callback",
            )
            code = str((callback_params or {}).get("code") or "").strip()
            login_user_agent_override = self._sentinel_user_agent(login_session)
            if code:
                tokens = request_platform_oauth_token(
                    login_session,
                    code,
                    code_verifier,
                    self.stop_event,
                    self.deadline,
                    proxy=self.proxy,
                    user_agent_override=login_user_agent_override,
                )
            else:
                tokens = exchange_platform_tokens(
                    login_session,
                    login_device_id,
                    code_verifier,
                    continue_url,
                    self.stop_event,
                    self.deadline,
                    proxy=self.proxy,
                    user_agent_override=login_user_agent_override,
                    expected_state=login_auth_state,
                )
            if not tokens:
                raise RuntimeError("token 交换失败")
            step(index, "登录 token 交换成功")
            return tokens
        finally:
            login_session.close()

    def register(self, index: int) -> dict:
        mail_config = _mail_config(self.proxy, self.deadline, self._check_task_control)
        mailbox_attempts = 8
        last_error: Exception | None = None
        for mailbox_attempt in range(mailbox_attempts):
            self._check_task_control()
            self.otp_verified = False
            step(index, "创建邮箱")
            runtime_check = getattr(self, "_ensure_sentinel_runtime", None)
            if callable(runtime_check):
                runtime_check(index)
            mailbox = mail_provider.create_mailbox(mail_config)
            self.last_mailbox = dict(mailbox or {})
            email = str(mailbox.get("address") or "").strip()
            if not email:
                _release_mailbox(mailbox, mail_config, index)
                raise RuntimeError("邮箱服务未返回 address")
            label = str(mailbox.get("label") or "")
            step(index, f"创建邮箱[{label}]: {email}")
            code_consumed = False
            try:
                fixed_password = str(config.get("fixed_password") or "").strip()
                password = fixed_password or _random_password()
                first_name, last_name = _random_name()
                self._platform_authorize(email, index)
                self._register_user(email, password, index)
                self._send_otp(index)
                step(index, "等待邮箱验证码")
                self._check_task_control()
                code = wait_for_code(mailbox, self.proxy, self._check_task_control, self.deadline, lambda: heartbeat(index))
                if not code:
                    raise RuntimeError("等待邮箱验证码超时")
                code_consumed = True
                step(index, f"收到邮箱验证码 {code}")
                self._validate_otp(code, index)
                self._create_account(f"{first_name} {last_name}", _random_birthdate(), index)
                try:
                    tokens = self._exchange_registered_tokens(index)
                except Exception as error:
                    step(index, f"注册后 token 交换失败，尝试登录补取：{error}", "yellow")
                    tokens = self._login_and_exchange_tokens(email, password, mailbox, index)
            except (RegisterStopped, RegisterRunInvalidated) as error:
                if code_consumed:
                    mail_provider.mark_mailbox_result(mailbox, success=False, error=error)
                    _release_mailbox(mailbox, mail_config, index)
                else:
                    _release_mailbox(mailbox, mail_config, index)
                raise
            except Exception as error:
                last_error = error
                yyds_hard_reject = mail_provider.mark_yyds_mailbox_error(mailbox, error)
                if code_consumed:
                    mail_provider.mark_mailbox_result(mailbox, success=False, error=error)
                    _release_mailbox(mailbox, mail_config, index)
                else:
                    _release_mailbox(mailbox, mail_config, index)
                if yyds_hard_reject and mailbox_attempt < mailbox_attempts - 1:
                    source_domain = _normalize_domain_name(mailbox.get("source_domain") or mailbox.get("domain") or email)
                    detail = f"：{source_domain}" if source_domain else ""
                    step(index, f"YYDS 邮箱域名被 OpenAI 拒绝，已自动加入黑名单{detail}，切换邮箱重试 ({mailbox_attempt + 2}/{mailbox_attempts})", "yellow")
                    cooldown = min(10.0, 3.0 + mailbox_attempt * 1.5)
                    deadline = time.monotonic() + cooldown
                    while time.monotonic() < deadline:
                        self._check_task_control()
                        wait_seconds = min(0.5, max(0.0, deadline - time.monotonic()))
                        if wait_seconds <= 0:
                            break
                        if self.stop_event is not None:
                            if self.stop_event.wait(wait_seconds):
                                raise RegisterStopped("register_task_stopped")
                        else:
                            time.sleep(wait_seconds)
                    try:
                        self.session.close()
                    except Exception:
                        pass
                    self.session = create_session(self.proxy)
                    self.device_id = str(uuid.uuid4())
                    self.code_verifier = ""
                    self.platform_auth_state = ""
                    self.platform_auth_code = ""
                    self.platform_continue_url = ""
                    continue
                raise
            mail_provider.mark_mailbox_result(mailbox, success=True)
            return {
                "email": email,
                "password": password,
                "access_token": str(tokens.get("access_token") or "").strip(),
                "refresh_token": str(tokens.get("refresh_token") or "").strip(),
                "id_token": str(tokens.get("id_token") or "").strip(),
                "client_id": platform_oauth_client_id,
                "source_type": "web",
                "created_at": datetime.now(timezone.utc).isoformat(),
                "_mailbox": dict(mailbox or {}),
            }
        if last_error:
            raise last_error
        raise RuntimeError("register_failed_without_exception")

def _task_timeout_seconds(value: object = None) -> int:
    try:
        return max(30, int(value if value is not None else config.get("task_timeout_seconds") or 300))
    except Exception:
        return 300


def _wait_for_register_proxy(
    index: int,
    stop_event: threading.Event | None,
    deadline: float,
    proxy_selection: RegisterProxySelection | None = None,
) -> RegisterProxySelection:
    selection = proxy_selection or register_proxy_pool.next_proxy()
    next_log_at = 0.0
    while (
        selection.last_error
        and not selection.proxy
        and selection.source != "direct"
        and selection.wait_retriable
    ):
        _set_worker_state(
            index,
            status="waiting_proxy",
            proxy=selection.proxy,
            proxy_source=selection.source_label,
            proxy_count=selection.count,
            bind_account_proxy=selection.bind_to_account,
            last_error=selection.last_error,
        )
        heartbeat_with_proxy(index, selection)
        now = time.monotonic()
        if now >= next_log_at:
            step(index, f"注册代理暂不可用：{selection.last_error}，等待代理来源恢复", "yellow")
            next_log_at = now + 10
        try:
            _check_task_control(stop_event, deadline)
        except RegisterTaskTimeout as exc:
            raise RuntimeError(f"register_proxy_unavailable: {selection.last_error or '没有可用注册代理'}") from exc
        if stop_event is not None and stop_event.wait(1):
            raise RegisterStopped("register_task_stopped")
        if stop_event is None:
            time.sleep(1)
        try:
            _check_task_control(stop_event, deadline)
        except RegisterTaskTimeout as exc:
            raise RuntimeError(f"register_proxy_unavailable: {selection.last_error or '没有可用注册代理'}") from exc
        selection = register_proxy_pool.next_proxy()
    return selection


def worker(
    index: int,
    stop_event: threading.Event | None = None,
    proxy_selection: RegisterProxySelection | None = None,
    task_timeout_seconds: int | None = None,
    run_id: str = "",
) -> dict:
    previous_run_id = _current_worker_run_id()
    worker_run_context.run_id = str(run_id or "")
    start = time.time()
    deadline = time.monotonic() + _task_timeout_seconds(task_timeout_seconds)
    selection: RegisterProxySelection | None = None
    registrar: PlatformRegistrar | None = None
    proxy_reported = False
    saved_access_token = ""
    source_meta: dict[str, str] = {}
    otp_verified = False
    try:
        _check_run_active(run_id)
        selection = _wait_for_register_proxy(index, stop_event, deadline, proxy_selection)
        _check_run_active(run_id)
        _set_worker_state(
            index,
            status="running",
            started_at=_utc_now(),
            proxy=selection.proxy,
            proxy_source=selection.source_label,
            proxy_count=selection.count,
            bind_account_proxy=selection.bind_to_account,
            last_error="",
            failure_reason="",
        )
        step(index, f"注册代理来源={selection.source_label}，可用数量={selection.count}")
        if selection.last_error and not selection.proxy and selection.source != "direct":
            raise RuntimeError(f"register_proxy_unavailable: {selection.last_error}")
        _check_task_control(stop_event, deadline)
        _check_run_active(run_id)
        registrar = PlatformRegistrar(selection.proxy, stop_event=stop_event, deadline=deadline, run_id=run_id)
        result = registrar.register(index)
        source_meta = _mailbox_source_meta((result if isinstance(result, dict) else {}).get("_mailbox") or getattr(registrar, "last_mailbox", None))
        otp_verified = bool(getattr(registrar, "otp_verified", False))
        result = _require_register_tokens(result)
        result.pop("_mailbox", None)
        _check_task_control(stop_event, deadline)
        _check_run_active(run_id)
        if selection.bind_to_account and selection.proxy:
            result["proxy"] = selection.proxy
        result["image_quota_unknown"] = True
        cost = time.time() - start
        access_token = str(result["access_token"])
        _check_task_control(stop_event, deadline)
        _check_run_active(run_id)
        account_service.add_account_items([result])
        saved_access_token = access_token
        try:
            _check_task_control(stop_event, deadline)
            _check_run_active(run_id)
        except RegisterStopped:
            _delete_saved_account_or_raise(access_token)
            saved_access_token = ""
            raise
        try:
            refresh_result = account_service.refresh_accounts([access_token], defer_invalid_removal=False)
            _check_run_active(run_id)
        except RegisterStopped:
            _delete_saved_account_or_raise(access_token)
            saved_access_token = ""
            raise
        except Exception as exc:
            refresh_error = str(exc)
            redacted_refresh_error = _redact_refresh_errors(refresh_error)
            step(index, f"账号保存成功，但刷新账号信息异常：{redacted_refresh_error}", "yellow")
            err, cleaned = _refresh_failed_validation_error(access_token, redacted_refresh_error)
            if cleaned:
                saved_access_token = ""
                step(index, "刷新异常，已删除刚保存的账号", "yellow")
            else:
                step(index, "刷新异常，但刚保存的账号删除失败，请在账号列表检查", "red")
            raise err from exc
        if refresh_result.get("errors"):
            redacted_refresh_errors = _redact_refresh_errors(refresh_result["errors"])
            step(index, f"账号保存成功，但刷新账号信息失败：{redacted_refresh_errors}", "yellow")
            err, cleaned = _refresh_failed_validation_error(access_token, redacted_refresh_errors)
            if cleaned:
                saved_access_token = ""
                step(index, "刷新失败，已删除刚保存的账号", "yellow")
            else:
                step(index, "刷新失败，但刚保存的账号删除失败，请在账号列表检查", "red")
            raise err
        if not _saved_image_account_usable(account_service.get_account(access_token)):
            step(index, "账号保存后未通过额度校验，已被移除或标记清退，不计入成功", "yellow")
            _delete_saved_account_or_raise(access_token)
            saved_access_token = ""
            register_proxy_pool.report(selection, ok=True)
            proxy_reported = True
            raise RegisteredAccountValidationError("registered_account_unusable_after_refresh")
        _check_run_active(run_id)
        register_proxy_pool.report(selection, ok=True)
        proxy_reported = True
        with stats_lock:
            stats["done"] += 1
            stats["success"] += 1
            stats["usable_success"] = int(stats.get("usable_success") or 0) + 1
            stats["saved"] = int(stats.get("saved") or 0) + 1
            avg = (time.time() - stats["start_time"]) / stats["success"] if stats.get("success") else 0
        _set_worker_state(index, status="success", elapsed_seconds=round(cost, 1), email=result.get("email"), last_error="", failure_reason="")
        log(f'{result["email"]} 注册成功，耗时 {cost:.1f}s，平均 {avg:.1f}s', "green")
        return {"ok": True, "index": index, "result": result, "saved": True, "usable": True, "otp_verified": otp_verified, **source_meta}
    except RegisterStopped as e:
        cost = time.time() - start
        if saved_access_token:
            try:
                _delete_saved_account_or_raise(saved_access_token)
            except Exception as cleanup_exc:
                reason = classify_register_failure(cleanup_exc)
                if not proxy_reported:
                    register_proxy_pool.report(selection, ok=False, reason=reason, error=cleanup_exc)
                with stats_lock:
                    stats["done"] += 1
                    stats["fail"] += 1
                _set_worker_state(index, status="failed", elapsed_seconds=round(cost, 1), last_error=str(cleanup_exc), failure_reason=reason)
                log(f"任务{index} 停止后清理账号失败，耗时 {cost:.1f}s，原因={reason}，错误：{cleanup_exc}", "red")
                return {"ok": False, "index": index, "error": str(cleanup_exc), "reason": reason, "otp_verified": otp_verified, **source_meta}
        if not proxy_reported:
            register_proxy_pool.report(selection, ok=False, reason="stopped", error=e)
        with stats_lock:
            stats["done"] += 1
        _set_worker_state(index, status="stopped", elapsed_seconds=round(cost, 1), last_error=str(e))
        log(f"任务{index} 已停止，耗时 {cost:.1f}s", "yellow")
        return {"ok": False, "index": index, "stopped": True, "error": str(e), "otp_verified": otp_verified, **source_meta}
    except Exception as e:
        cost = time.time() - start
        reason = str(getattr(e, "failure_reason", "") or "") or classify_register_failure(e)
        if not proxy_reported:
            register_proxy_pool.report(selection, ok=False, reason=reason, error=e)
        with stats_lock:
            stats["done"] += 1
            stats["fail"] += 1
        _set_worker_state(index, status="failed", elapsed_seconds=round(cost, 1), last_error=str(e), failure_reason=reason)
        log(f"任务{index} 注册失败，耗时 {cost:.1f}s，原因={reason}，错误：{e}", "red")
        if registrar is not None and not source_meta:
            source_meta = _mailbox_source_meta(getattr(registrar, "last_mailbox", None))
            otp_verified = bool(getattr(registrar, "otp_verified", False))
        return {"ok": False, "index": index, "error": str(e), "reason": reason, "refresh_failed": bool(getattr(e, "refresh_failed", False)), "token_acquired": bool(getattr(e, "token_acquired", False)), "otp_verified": otp_verified, **source_meta}
    finally:
        worker_run_context.run_id = previous_run_id
        if registrar is not None:
            registrar.close()
