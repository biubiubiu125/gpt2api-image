from __future__ import annotations

import base64
import json
import random
import re
import threading
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime
from typing import TYPE_CHECKING, Any

from utils.sentinel_vm import SessionObserverRuntime, build_browser_environment, solve_turnstile_dx

if TYPE_CHECKING:
    from curl_cffi.requests import Session


DEFAULT_SENTINEL_USER_AGENT = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
    "AppleWebKit/537.36 (KHTML, like Gecko) "
    "Chrome/145.0.0.0 Safari/537.36"
)
DEFAULT_SENTINEL_SEC_CH_UA = '"Chromium";v="145", "Google Chrome";v="145", "Not/A)Brand";v="99"'
DEFAULT_SENTINEL_SEC_CH_UA_FULL_VERSION_LIST = '"Chromium";v="145.0.0.0", "Google Chrome";v="145.0.0.0", "Not/A)Brand";v="99.0.0.0"'
DEFAULT_SENTINEL_WRAPPER_URL = "https://chatgpt.com/backend-api/sentinel/sdk.js"
DEFAULT_SENTINEL_SDK_VERSION = "20260219f9f6"
DEFAULT_SENTINEL_SDK_URL = f"https://chatgpt.com/sentinel/{DEFAULT_SENTINEL_SDK_VERSION}/sdk.js"
DEFAULT_SENTINEL_FLOW = "chat-requirements"
DEFAULT_SENTINEL_PAGE_URL = "https://chatgpt.com/"
SENTINEL_REQ_URL = "https://sentinel.openai.com/backend-api/sentinel/req"
SDK_CACHE_SECONDS = 3600
DEVICE_COOKIE_DOMAINS = [
    ".auth.openai.com",
    "auth.openai.com",
    ".openai.com",
    "openai.com",
    ".chatgpt.com",
    "chatgpt.com",
    "sentinel.openai.com",
]

_SDK_URL_RE = re.compile(r"script\.src\s*=\s*'([^']+/sentinel/([^/]+)/sdk\.js)'")
_sdk_cache_lock = threading.Lock()
_sdk_cache = {
    "expires_at": 0.0,
    "version": DEFAULT_SENTINEL_SDK_VERSION,
    "url": DEFAULT_SENTINEL_SDK_URL,
}
_protocol_runtime_cache_lock = threading.Lock()
_protocol_runtime_cache = {
    "key": "",
    "ok": False,
}
_CHROME_VERSION_RE = re.compile(r"Chrome/(?P<version>\d+(?:\.\d+){0,3})")
_BRAND_ENTRY_RE = re.compile(r'"([^"]+)";v="([^"]+)"')


def _b64_json(data: Any) -> str:
    return base64.b64encode(
        json.dumps(data, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    ).decode("ascii")


def _normalized_chrome_versions(user_agent: str) -> tuple[str, str]:
    match = _CHROME_VERSION_RE.search(str(user_agent or ""))
    version = str((match.group("version") if match else "") or "145.0.0.0").strip()
    parts = [part for part in version.split(".") if part]
    while len(parts) < 4:
        parts.append("0")
    full = ".".join(parts[:4]) or "145.0.0.0"
    major = parts[0] if parts else "145"
    return major, full


def _parse_brand_entries(value: object) -> list[dict[str, str]]:
    return [
        {"brand": str(brand or "").strip(), "version": str(version or "").strip()}
        for brand, version in _BRAND_ENTRY_RE.findall(str(value or ""))
        if str(brand or "").strip() and str(version or "").strip()
    ]


def build_user_agent_client_hints(
    user_agent: str,
    *,
    sec_ch_ua: str = "",
    sec_ch_ua_full_version_list: str = "",
) -> dict[str, Any]:
    resolved_user_agent = str(user_agent or "").strip() or DEFAULT_SENTINEL_USER_AGENT
    major, full = _normalized_chrome_versions(resolved_user_agent)
    resolved_sec_ch_ua = str(sec_ch_ua or "").strip() or DEFAULT_SENTINEL_SEC_CH_UA.replace("145", major, 2)
    resolved_full_version_list = str(sec_ch_ua_full_version_list or "").strip() or DEFAULT_SENTINEL_SEC_CH_UA_FULL_VERSION_LIST.replace("145.0.0.0", full, 2)
    brands = _parse_brand_entries(resolved_sec_ch_ua)
    full_version_list = _parse_brand_entries(resolved_full_version_list)
    metadata = {
        "brands": brands,
        "fullVersionList": full_version_list or [{"brand": item["brand"], "version": full} for item in brands],
        "fullVersion": full,
        "platform": "Windows",
        "platformVersion": "10.0.0",
        "architecture": "x86",
        "bitness": "64",
        "model": "",
        "mobile": False,
        "wow64": False,
    }
    return {
        "user_agent": resolved_user_agent,
        "sec_ch_ua": resolved_sec_ch_ua,
        "sec_ch_ua_full_version_list": resolved_full_version_list,
        "user_agent_metadata": metadata,
    }


def _fnv1a_32(text: str) -> str:
    value = 2166136261
    for ch in text:
        value ^= ord(ch)
        value = (value * 16777619) & 0xFFFFFFFF
    value ^= value >> 16
    value = (value * 2246822507) & 0xFFFFFFFF
    value ^= value >> 13
    value = (value * 3266489909) & 0xFFFFFFFF
    value ^= value >> 16
    return format(value & 0xFFFFFFFF, "08x")


def _js_date_string() -> str:
    now = datetime.now().astimezone()
    offset = now.strftime("%z") or "+0000"
    zone = now.tzname() or "Coordinated Universal Time"
    return f"{now.strftime('%a %b %d %Y %H:%M:%S')} GMT{offset} ({zone})"


def _sdk_headers(user_agent: str, sec_ch_ua: str, sec_ch_ua_full_version_list: str = "") -> dict[str, str]:
    return {
        "User-Agent": user_agent,
        "user-agent": user_agent,
        "sec-ch-ua": sec_ch_ua,
        "sec-ch-ua-full-version-list": sec_ch_ua_full_version_list,
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": '"Windows"',
    }


def _payload_json(payload: dict[str, Any], device_id: str, flow: str) -> str:
    data = dict(payload)
    data["id"] = str(device_id or "")
    data["flow"] = str(flow or DEFAULT_SENTINEL_FLOW)
    return json.dumps(data, separators=(",", ":"), ensure_ascii=False)


def _set_cookie_best_effort(session: "Session", name: str, value: str, domains: list[str]) -> None:
    for domain in domains:
        try:
            session.cookies.set(name, value, domain=domain)
        except Exception:
            continue


def _prime_device_cookies(session: "Session", device_id: str) -> None:
    _set_cookie_best_effort(session, "oai-did", str(device_id or ""), DEVICE_COOKIE_DOMAINS)


def _json_object(value: object) -> dict[str, Any]:
    if isinstance(value, dict):
        return dict(value)
    text = str(value or "").strip()
    if not text:
        return {}
    try:
        parsed = json.loads(text)
    except Exception:
        return {}
    return dict(parsed) if isinstance(parsed, dict) else {}


def ensure_protocol_sentinel_runtime(
    *,
    proxy: str = "",
    user_agent: str = "",
    sec_ch_ua: str = "",
    sec_ch_ua_full_version_list: str = "",
    check_control=None,
) -> None:
    hints = build_user_agent_client_hints(
        user_agent or DEFAULT_SENTINEL_USER_AGENT,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
    )
    cache_key = "protocol|" + str(proxy or "").strip() + "|" + hints["user_agent"] + "|" + hints["sec_ch_ua"]
    with _protocol_runtime_cache_lock:
        if _protocol_runtime_cache.get("ok") and _protocol_runtime_cache.get("key") == cache_key:
            return
    if callable(check_control):
        check_control()
    with _protocol_runtime_cache_lock:
        _protocol_runtime_cache.update({"key": cache_key, "ok": True})


def _resolve_sdk_info(
    session: "Session",
    *,
    user_agent: str,
    sec_ch_ua: str,
    sec_ch_ua_full_version_list: str = "",
) -> tuple[str, str]:
    now = time.time()
    with _sdk_cache_lock:
        if now < float(_sdk_cache.get("expires_at") or 0):
            return str(_sdk_cache["version"]), str(_sdk_cache["url"])
    version = DEFAULT_SENTINEL_SDK_VERSION
    url = DEFAULT_SENTINEL_SDK_URL
    try:
        resp = session.get(
            DEFAULT_SENTINEL_WRAPPER_URL,
            headers=_sdk_headers(user_agent, sec_ch_ua, sec_ch_ua_full_version_list),
            timeout=20,
        )
        text = str(getattr(resp, "text", "") or "")
        match = _SDK_URL_RE.search(text)
        if match:
            url = match.group(1).strip()
            version = match.group(2).strip() or version
    except Exception:
        pass
    with _sdk_cache_lock:
        _sdk_cache.update(
            {
                "expires_at": now + SDK_CACHE_SECONDS,
                "version": version,
                "url": url,
            }
        )
    return version, url


class SentinelTokenGenerator:
    MAX_ATTEMPTS = 500_000
    ERROR_PREFIX = "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"

    def __init__(
        self,
        device_id: str,
        user_agent: str,
        *,
        sdk_url: str = DEFAULT_SENTINEL_SDK_URL,
        script_sources: list[str] | None = None,
        data_build: str = "",
    ) -> None:
        self.device_id = str(device_id or "")
        self.user_agent = user_agent
        self.sdk_url = str(sdk_url or DEFAULT_SENTINEL_SDK_URL)
        self.script_sources = [src for src in (script_sources or [self.sdk_url]) if str(src or "").strip()] or [self.sdk_url]
        self.data_build = str(data_build or "")
        self.sid = str(uuid.uuid4())
        self.environment = build_browser_environment(
            user_agent=self.user_agent,
            script_sources=list(self.script_sources),
            data_build=self.data_build,
            page_url=DEFAULT_SENTINEL_PAGE_URL,
        )

    def _random_script_source(self) -> str:
        return random.choice(self.script_sources or [self.sdk_url])

    def _navigator_signature(self) -> str:
        navigator = self.environment.window.get("navigator") or {}
        keys = list(navigator.keys()) if isinstance(navigator, dict) else []
        key = random.choice(keys or ["userAgent"])
        value = navigator.get(key, "") if isinstance(navigator, dict) else ""
        return f"{key}\u2212{value}"

    def _document_key(self) -> str:
        keys = list((self.environment.document or {}).keys())
        return random.choice(keys or ["location"])

    def _window_key(self) -> str:
        keys = list((self.environment.window or {}).keys())
        return random.choice(keys or ["window"])

    def get_config(self) -> list[Any]:
        window = self.environment.window
        performance = window.get("performance") or {}
        navigator = window.get("navigator") or {}
        screen = window.get("screen") or {}
        perf_now = float(performance.get("now", lambda: 0.0)())
        memory = performance.get("memory") or {}
        search_keys = ""
        try:
            raw_search = str((window.get("location") or {}).get("search") or "")
            if raw_search.startswith("?"):
                search_keys = ",".join(key for key, _value in [part.partition("=") for part in raw_search[1:].split("&") if part])
        except Exception:
            search_keys = ""
        return [
            int(screen.get("width") or 0) + int(screen.get("height") or 0),
            _js_date_string(),
            int(memory.get("jsHeapSizeLimit") or 4294705152),
            random.random(),
            self.user_agent,
            self._random_script_source(),
            self.data_build,
            str(navigator.get("language") or "en-US"),
            ",".join(str(item) for item in (navigator.get("languages") or ["en-US", "en"])),
            random.random(),
            self._navigator_signature(),
            self._document_key(),
            self._window_key(),
            perf_now,
            self.sid,
            search_keys,
            int(navigator.get("hardwareConcurrency") or 8),
            float(performance.get("timeOrigin") or (time.time() * 1000 - perf_now)),
            int("ai" in window),
            int("requestIdleCallback" in window),
            int("cache" in performance),
            int("memory" in performance),
            int("solana" in window),
            int("documentPictureInPicture" in window),
            int("InstallTrigger" in window),
        ]

    def generate_requirements_token(self) -> str:
        data = self.get_config()
        data[3] = 1
        data[9] = round(self.environment.performance_now())
        return "gAAAAAC" + _b64_json(data)

    def build_generate_fail_message(self, seed: str | None = None) -> str:
        return self.ERROR_PREFIX + _b64_json(str(seed or "e"))

    def generate_token(self, seed: str, difficulty: str) -> str:
        started = self.environment.performance_now()
        data = self.get_config()
        challenge = str(difficulty or "0")
        for attempt in range(self.MAX_ATTEMPTS):
            data[3] = attempt
            data[9] = round(self.environment.performance_now() - started)
            payload = _b64_json(data)
            if _fnv1a_32(str(seed or "") + payload)[: len(challenge)] <= challenge:
                return "gAAAAAB" + payload + "~S"
        return "gAAAAAB" + self.build_generate_fail_message(seed)

    def get_enforcement_token(self, sentinel_req: dict[str, Any]) -> str:
        pow_data = sentinel_req.get("proofofwork") if isinstance(sentinel_req, dict) else None
        if not isinstance(pow_data, dict) or not pow_data.get("required"):
            return self.generate_requirements_token()
        seed = str(pow_data.get("seed") or "").strip()
        difficulty = str(pow_data.get("difficulty") or "0").strip()
        if not seed or not difficulty:
            return self.generate_requirements_token()
        return self.generate_token(seed, difficulty)


@dataclass
class SentinelArtifacts:
    sentinel_header: str
    so_header: str = ""
    oai_sc_cookie: str = ""
    sdk_version: str = DEFAULT_SENTINEL_SDK_VERSION
    sdk_url: str = DEFAULT_SENTINEL_SDK_URL
    challenge_token: str = ""
    requirements_token: str = ""
    proof_token: str = ""
    turnstile_token: str = ""
    session_observer_token: str = ""
    proof_required: bool = False
    turnstile_required: bool = False
    so_required: bool = False
    req_payload: dict[str, Any] = field(default_factory=dict)
    executor: str = "protocol"


def _prepare_sentinel_artifacts_protocol(
    session: "Session",
    device_id: str,
    flow: str,
    *,
    include_so: bool = False,
    observer_wait_ms: int = 5000,
    user_agent: str = "",
    sec_ch_ua: str = "",
    sec_ch_ua_full_version_list: str = "",
    strict_turnstile: bool = False,
    strict_so: bool = False,
    check_control=None,
) -> SentinelArtifacts:
    ua = user_agent or DEFAULT_SENTINEL_USER_AGENT
    hints = build_user_agent_client_hints(
        ua,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
    )
    ua = hints["user_agent"]
    ch_ua = hints["sec_ch_ua"]
    ch_ua_full_version_list = hints["sec_ch_ua_full_version_list"]
    _prime_device_cookies(session, device_id)
    if callable(check_control):
        check_control()
    sdk_version, sdk_url = _resolve_sdk_info(
        session,
        user_agent=ua,
        sec_ch_ua=ch_ua,
        sec_ch_ua_full_version_list=ch_ua_full_version_list,
    )
    generator = SentinelTokenGenerator(
        device_id,
        ua,
        sdk_url=sdk_url,
        script_sources=[sdk_url, DEFAULT_SENTINEL_WRAPPER_URL],
    )
    requirements_token = generator.generate_requirements_token()
    body = _payload_json({"p": requirements_token}, device_id, flow)
    resp = session.post(
        SENTINEL_REQ_URL,
        data=body,
        headers={
            "Content-Type": "text/plain;charset=UTF-8",
            "Referer": "https://sentinel.openai.com/backend-api/sentinel/frame.html",
            "Origin": "https://sentinel.openai.com",
            **_sdk_headers(ua, ch_ua, ch_ua_full_version_list),
        },
        timeout=20,
    )
    text = str(getattr(resp, "text", "") or "")
    if resp.status_code != 200:
        raise RuntimeError(f"sentinel_req_failed_{resp.status_code}")
    try:
        data = resp.json() if text.strip() else {}
    except Exception as error:
        raise RuntimeError(f"sentinel_req_invalid_json: {error}") from error
    challenge_token = str(data.get("token") or "").strip()
    if not challenge_token:
        raise RuntimeError(f"sentinel_req_failed_{resp.status_code}")

    proof_token = generator.get_enforcement_token(data if isinstance(data, dict) else {})
    env = build_browser_environment(
        user_agent=ua,
        script_sources=[sdk_url, DEFAULT_SENTINEL_WRAPPER_URL],
        page_url=DEFAULT_SENTINEL_PAGE_URL,
    )
    turnstile_data = data.get("turnstile") if isinstance(data, dict) else {}
    turnstile_required = bool((turnstile_data if isinstance(turnstile_data, dict) else {}).get("required"))
    turnstile_token = ""
    if isinstance(turnstile_data, dict) and str(turnstile_data.get("dx") or "").strip():
        turnstile_token = str(solve_turnstile_dx(str(turnstile_data["dx"]), requirements_token, env) or "").strip()
    if strict_turnstile and turnstile_required and not turnstile_token:
        raise RuntimeError("sentinel_turnstile_token_failed")
    sentinel_header = _payload_json(
        {
            "p": proof_token,
            "t": turnstile_token or None,
            "c": challenge_token,
        },
        device_id,
        flow,
    )

    so_token = ""
    so_header = ""
    so_data = data.get("so") if isinstance(data, dict) else {}
    so_required = bool((so_data if isinstance(so_data, dict) else {}).get("required"))
    if include_so and so_required and isinstance(so_data, dict) and str(so_data.get("snapshot_dx") or "").strip():
        if callable(check_control):
            check_control()
        observer = SessionObserverRuntime(env, requirements_token)
        collector_dx = str(so_data.get("collector_dx") or "").strip()
        if collector_dx:
            observer.run_collector(collector_dx)
        observer.simulate_default_activity(observer_wait_ms)
        if observer_wait_ms > 0:
            started = time.monotonic()
            delay = max(0.0, observer_wait_ms) / 1000.0
            while time.monotonic() - started < delay:
                if callable(check_control):
                    check_control()
                time.sleep(min(0.1, max(0.0, delay - (time.monotonic() - started))))
        so_token = str(observer.run_snapshot(str(so_data.get("snapshot_dx") or "")) or "").strip()
        if so_token:
            so_header = _payload_json({"so": so_token, "c": challenge_token}, device_id, flow)
    if include_so and strict_so and so_required and not so_token:
        raise RuntimeError("sentinel_so_token_failed")

    oai_sc_value = "0" + challenge_token
    _set_cookie_best_effort(
        session,
        "oai-sc",
        oai_sc_value,
        [".auth.openai.com", "auth.openai.com", ".openai.com", "openai.com", ".chatgpt.com", "chatgpt.com"],
    )
    return SentinelArtifacts(
        sentinel_header=sentinel_header,
        so_header=so_header,
        oai_sc_cookie=oai_sc_value,
        sdk_version=sdk_version,
        sdk_url=sdk_url,
        challenge_token=challenge_token,
        requirements_token=requirements_token,
        proof_token=proof_token,
        turnstile_token=turnstile_token,
        session_observer_token=so_token,
        proof_required=bool(((data.get("proofofwork") or {}) if isinstance(data, dict) else {}).get("required")),
        turnstile_required=turnstile_required,
        so_required=so_required,
        req_payload=data if isinstance(data, dict) else {},
        executor="protocol",
    )


def prepare_sentinel_artifacts(
    session: "Session",
    device_id: str,
    flow: str,
    *,
    include_so: bool = False,
    observer_wait_ms: int = 5000,
    user_agent: str = "",
    sec_ch_ua: str = "",
    sec_ch_ua_full_version_list: str = "",
    strict_turnstile: bool = False,
    strict_so: bool = False,
    proxy: str = "",
    check_control=None,
) -> SentinelArtifacts:
    return _prepare_sentinel_artifacts_protocol(
        session,
        device_id,
        flow,
        include_so=include_so,
        observer_wait_ms=observer_wait_ms,
        user_agent=user_agent,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
        strict_turnstile=strict_turnstile,
        strict_so=strict_so,
        check_control=check_control,
    )


def build_sentinel_token(
    session: "Session",
    device_id: str,
    flow: str,
    *,
    user_agent: str = "",
    sec_ch_ua: str = "",
    sec_ch_ua_full_version_list: str = "",
    proxy: str = "",
    check_control=None,
) -> tuple[str, str]:
    artifacts = prepare_sentinel_artifacts(
        session,
        device_id,
        flow,
        include_so=False,
        observer_wait_ms=0,
        user_agent=user_agent,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
        strict_turnstile=True,
        strict_so=False,
        proxy=proxy,
        check_control=check_control,
    )
    return artifacts.sentinel_header, artifacts.oai_sc_cookie
