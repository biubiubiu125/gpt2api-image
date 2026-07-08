from __future__ import annotations

import base64
import json
import os
import random
import re
import shutil
import subprocess
import threading
import time
import uuid
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
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
DEFAULT_SENTINEL_RUNTIME_CHECK_FLOW = "oauth_create_account"
DEFAULT_SENTINEL_OBSERVER_WAIT_MS = 5_000
DEFAULT_SENTINEL_REQ_CAPTURE_WAIT_MS = 10_000
DEFAULT_BROWSER_LAUNCH_TIMEOUT_MS = 60_000
DEFAULT_BROWSER_NAVIGATION_TIMEOUT_MS = 60_000
BROWSER_HELPER_BUFFER_MS = 15_000
BROWSER_HELPER_TIMEOUT_MS = (
    DEFAULT_BROWSER_LAUNCH_TIMEOUT_MS
    + DEFAULT_BROWSER_NAVIGATION_TIMEOUT_MS
    + DEFAULT_SENTINEL_OBSERVER_WAIT_MS
    + DEFAULT_SENTINEL_REQ_CAPTURE_WAIT_MS
    + BROWSER_HELPER_BUFFER_MS
)
DEFAULT_BROWSER_CHANNEL = "chrome"
SENTINEL_HELPER_SCRIPT = Path(__file__).with_name("sentinel_browser.js")
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
_browser_runtime_cache_lock = threading.Lock()
_browser_runtime_cache = {
    "key": "",
    "ok": False,
}
_CHROME_VERSION_RE = re.compile(r"Chrome/(?P<version>\d+(?:\.\d+){0,3})")
_BRAND_ENTRY_RE = re.compile(r'"([^"]+)";v="([^"]+)"')


def _legacy_vm_env_enabled() -> bool:
    return any(
        str(os.environ.get(name) or "").strip() == "1"
        for name in (
            "GPT2API_IMAGE_SENTINEL_FORCE_LEGACY_VM",
            "GPT2API_IMAGE_SENTINEL_ALLOW_LEGACY_VM",
        )
    )


def _assert_browser_sdk_required() -> None:
    if _legacy_vm_env_enabled():
        raise RuntimeError("sentinel_legacy_vm_disabled: browser_sdk_required")


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


def _session_cookie_records(session: "Session") -> list[dict[str, Any]]:
    jar = getattr(getattr(session, "cookies", None), "jar", None) or getattr(session, "cookies", None)
    if jar is None:
        return []
    result: list[dict[str, Any]] = []
    try:
        items = list(jar)
    except Exception:
        items = []
    for cookie in items:
        name = str(getattr(cookie, "name", "") or "").strip()
        if not name:
            continue
        record: dict[str, Any] = {
            "name": name,
            "value": str(getattr(cookie, "value", "") or ""),
            "domain": str(getattr(cookie, "domain", "") or "").strip(),
            "path": str(getattr(cookie, "path", "") or "").strip() or "/",
            "secure": bool(getattr(cookie, "secure", False)),
            "expires": getattr(cookie, "expires", None),
            "httpOnly": False,
        }
        rest = getattr(cookie, "_rest", {}) or {}
        for key in ("HttpOnly", "httponly"):
            if key in rest:
                record["httpOnly"] = bool(rest.get(key))
                break
        same_site = ""
        for key in ("SameSite", "samesite"):
            if key in rest and rest.get(key):
                same_site = str(rest.get(key) or "").strip()
                break
        if same_site:
            record["sameSite"] = same_site
        result.append(record)
    return result


def _resolve_node_executable() -> str:
    candidates = [
        os.environ.get("GPT2API_IMAGE_SENTINEL_NODE_PATH"),
        os.environ.get("SENTINEL_NODE_PATH"),
        shutil.which("node"),
        str(Path.home() / ".cache" / "codex-runtimes" / "codex-primary-runtime" / "dependencies" / "node" / "bin" / "node.exe"),
        str(Path.home() / ".cache" / "codex-runtimes" / "codex-primary-runtime" / "dependencies" / "node" / "bin" / "node"),
        str(Path(os.environ.get("ProgramFiles", "")) / "nodejs" / "node.exe"),
        str(Path(os.environ.get("ProgramFiles(x86)", "")) / "nodejs" / "node.exe"),
    ]
    for candidate in candidates:
        if not candidate:
            continue
        path = Path(str(candidate)).expanduser()
        if path.is_file():
            return str(path)
    raise RuntimeError("sentinel_browser_runtime_unavailable: node_not_found")


def _node_module_candidates(node_path: str) -> list[str]:
    values = [
        os.environ.get("GPT2API_IMAGE_SENTINEL_NODE_MODULES"),
        os.environ.get("NODE_PATH"),
    ]
    node_file = Path(node_path)
    base = node_file.parent.parent
    values.append(str(base / "node_modules"))
    values.append(str(base / "node_modules" / ".pnpm" / "node_modules"))
    return [item for item in values if item]


def _browser_subprocess_env(node_path: str) -> dict[str, str]:
    env = dict(os.environ)
    paths: list[str] = []
    for item in _node_module_candidates(node_path):
        for value in str(item).split(os.pathsep):
            value = value.strip()
            if value and value not in paths:
                paths.append(value)
    if paths:
        existing = str(env.get("NODE_PATH") or "").strip()
        if existing:
            for value in existing.split(os.pathsep):
                value = value.strip()
                if value and value not in paths:
                    paths.append(value)
        env["NODE_PATH"] = os.pathsep.join(paths)
    env.setdefault("GPT2API_IMAGE_SENTINEL_BROWSER_CHANNEL", DEFAULT_BROWSER_CHANNEL)
    return env


def _browser_runtime_cache_key(node_path: str, proxy: str = "") -> str:
    values = [
        node_path,
        str(os.environ.get("GPT2API_IMAGE_SENTINEL_NODE_MODULES") or "").strip(),
        str(os.environ.get("NODE_PATH") or "").strip(),
        str(os.environ.get("GPT2API_IMAGE_SENTINEL_BROWSER_CHANNEL", DEFAULT_BROWSER_CHANNEL) or "").strip(),
        str(os.environ.get("GPT2API_IMAGE_SENTINEL_BROWSER_PATH") or "").strip(),
        str(proxy or "").strip(),
    ]
    return "|".join(values)


def _browser_helper_timeout_seconds(payload: dict[str, Any]) -> float:
    launch_timeout_ms = max(0, int(payload.get("launchTimeoutMs") or 0))
    navigation_timeout_ms = max(0, int(payload.get("navigationTimeoutMs") or 0))
    observer_wait_ms = max(0, int(payload.get("observerWaitMs") or 0))
    req_capture_wait_ms = max(0, int(payload.get("reqCaptureWaitMs") or DEFAULT_SENTINEL_REQ_CAPTURE_WAIT_MS))
    budget_ms = launch_timeout_ms + navigation_timeout_ms + observer_wait_ms + req_capture_wait_ms + BROWSER_HELPER_BUFFER_MS
    return max(30.0, max(BROWSER_HELPER_TIMEOUT_MS, budget_ms) / 1000.0)


def _run_browser_helper_subprocess(
    node_path: str,
    payload: dict[str, Any],
    *,
    timeout_seconds: float,
    check_control=None,
) -> subprocess.CompletedProcess[str]:
    process = subprocess.Popen(
        [node_path, str(SENTINEL_HELPER_SCRIPT)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
        env=_browser_subprocess_env(node_path),
    )
    try:
        if process.stdin is not None:
            try:
                process.stdin.write(json.dumps(payload, ensure_ascii=False))
            finally:
                process.stdin.close()
        started = time.monotonic()
        while process.poll() is None:
            if callable(check_control):
                check_control()
            if time.monotonic() - started >= max(1.0, float(timeout_seconds or 0.0)):
                process.kill()
                stdout, stderr = process.communicate()
                raise RuntimeError(
                    _browser_error_message(
                        subprocess.TimeoutExpired(
                            cmd=[node_path, str(SENTINEL_HELPER_SCRIPT)],
                            timeout=max(1.0, float(timeout_seconds or 0.0)),
                            output=stdout,
                            stderr=stderr,
                        )
                    )
                )
            time.sleep(0.1)
        stdout, stderr = process.communicate()
        return subprocess.CompletedProcess(
            args=[node_path, str(SENTINEL_HELPER_SCRIPT)],
            returncode=int(process.returncode or 0),
            stdout=stdout,
            stderr=stderr,
        )
    except BaseException:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)
        raise


def _browser_error_message(error: subprocess.CalledProcessError | subprocess.TimeoutExpired | Exception) -> str:
    if isinstance(error, subprocess.TimeoutExpired):
        return "sentinel_browser_timeout"
    if isinstance(error, subprocess.CalledProcessError):
        stderr = str(getattr(error, "stderr", "") or "").strip()
        payload = _json_object(stderr)
        if payload.get("error"):
            return str(payload.get("error") or "").strip()
        stdout = str(getattr(error, "stdout", "") or "").strip()
        if stdout:
            return stdout[:500]
        return stderr[:500] or "sentinel_browser_subprocess_failed"
    return str(error or "sentinel_browser_failed").strip()


def ensure_browser_sentinel_runtime(
    *,
    proxy: str = "",
    user_agent: str = "",
    sec_ch_ua: str = "",
    sec_ch_ua_full_version_list: str = "",
    check_control=None,
) -> None:
    _assert_browser_sdk_required()
    node_path = _resolve_node_executable()
    if not SENTINEL_HELPER_SCRIPT.is_file():
        raise RuntimeError("sentinel_browser_runtime_unavailable: helper_script_missing")
    hints = build_user_agent_client_hints(
        user_agent or DEFAULT_SENTINEL_USER_AGENT,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
    )
    cache_key = _browser_runtime_cache_key(node_path, proxy=proxy) + "|" + hints["user_agent"] + "|" + hints["sec_ch_ua"]
    with _browser_runtime_cache_lock:
        if _browser_runtime_cache.get("ok") and _browser_runtime_cache.get("key") == cache_key:
            return
    payload = {
        "runtimeCheckOnly": True,
        "flow": DEFAULT_SENTINEL_RUNTIME_CHECK_FLOW,
        "includeSo": True,
        "observerWaitMs": DEFAULT_SENTINEL_OBSERVER_WAIT_MS,
        "reqCaptureWaitMs": DEFAULT_SENTINEL_REQ_CAPTURE_WAIT_MS,
        "proxy": str(proxy or "").strip(),
        "pageUrl": DEFAULT_SENTINEL_PAGE_URL,
        "wrapperUrl": DEFAULT_SENTINEL_WRAPPER_URL,
        "userAgent": hints["user_agent"],
        "secChUa": hints["sec_ch_ua"],
        "secChUaFullVersionList": hints["sec_ch_ua_full_version_list"],
        "userAgentMetadata": hints["user_agent_metadata"],
        "browserChannel": os.environ.get("GPT2API_IMAGE_SENTINEL_BROWSER_CHANNEL", DEFAULT_BROWSER_CHANNEL),
        "browserPath": os.environ.get("GPT2API_IMAGE_SENTINEL_BROWSER_PATH", "").strip(),
        "launchTimeoutMs": DEFAULT_BROWSER_LAUNCH_TIMEOUT_MS,
        "navigationTimeoutMs": DEFAULT_BROWSER_NAVIGATION_TIMEOUT_MS,
    }
    try:
        completed = _run_browser_helper_subprocess(
            node_path,
            payload,
            timeout_seconds=_browser_helper_timeout_seconds(payload),
            check_control=check_control,
        )
    except (OSError, RuntimeError) as error:
        raise RuntimeError(f"sentinel_browser_runtime_unavailable: {_browser_error_message(error)}") from error
    if completed.returncode != 0:
        raise RuntimeError(
            f"sentinel_browser_runtime_unavailable: {_browser_error_message(subprocess.CalledProcessError(
                completed.returncode,
                completed.args,
                output=completed.stdout,
                stderr=completed.stderr,
            ))}"
        )
    data = _json_object(completed.stdout)
    if not data.get("ok") or not data.get("runtimeReady"):
        message = str(data.get("error") or "runtime_check_failed").strip()
        raise RuntimeError(f"sentinel_browser_runtime_unavailable: {message}")
    with _browser_runtime_cache_lock:
        _browser_runtime_cache.update({"key": cache_key, "ok": True})


def _parse_validated_header(name: str, header_value: str, *, require_body_key: str, require_challenge: bool = True) -> dict[str, Any]:
    payload = _json_object(header_value)
    if not payload:
        raise RuntimeError(f"{name}_failed: empty_header")
    if str(payload.get("e") or "").strip():
        raise RuntimeError(f"{name}_failed: {str(payload.get('e') or '').strip()}")
    if require_body_key and not str(payload.get(require_body_key) or "").strip():
        raise RuntimeError(f"{name}_failed: missing_{require_body_key}")
    if require_challenge and not str(payload.get("c") or "").strip():
        raise RuntimeError(f"{name}_failed: missing_c")
    return payload


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
    executor: str = "browser_sdk"


def _run_browser_sentinel(
    session: "Session",
    device_id: str,
    flow: str,
    *,
    include_so: bool,
    observer_wait_ms: int,
    user_agent: str,
    sec_ch_ua: str,
    sec_ch_ua_full_version_list: str,
    proxy: str,
    check_control=None,
) -> dict[str, Any]:
    node_path = _resolve_node_executable()
    if not SENTINEL_HELPER_SCRIPT.is_file():
        raise RuntimeError("sentinel_browser_runtime_unavailable: helper_script_missing")
    _prime_device_cookies(session, device_id)
    hints = build_user_agent_client_hints(
        user_agent,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
    )
    payload = {
        "flow": str(flow or DEFAULT_SENTINEL_FLOW),
        "deviceId": str(device_id or ""),
        "includeSo": bool(include_so),
        "observerWaitMs": max(0, int(observer_wait_ms or 0)),
        "reqCaptureWaitMs": DEFAULT_SENTINEL_REQ_CAPTURE_WAIT_MS,
        "wrapperUrl": DEFAULT_SENTINEL_WRAPPER_URL,
        "pageUrl": DEFAULT_SENTINEL_PAGE_URL,
        "userAgent": hints["user_agent"],
        "secChUa": hints["sec_ch_ua"],
        "secChUaFullVersionList": hints["sec_ch_ua_full_version_list"],
        "userAgentMetadata": hints["user_agent_metadata"],
        "proxy": str(proxy or "").strip(),
        "cookies": _session_cookie_records(session),
        "browserChannel": os.environ.get("GPT2API_IMAGE_SENTINEL_BROWSER_CHANNEL", DEFAULT_BROWSER_CHANNEL),
        "browserPath": os.environ.get("GPT2API_IMAGE_SENTINEL_BROWSER_PATH", "").strip(),
        "launchTimeoutMs": DEFAULT_BROWSER_LAUNCH_TIMEOUT_MS,
        "navigationTimeoutMs": DEFAULT_BROWSER_NAVIGATION_TIMEOUT_MS,
    }
    try:
        completed = _run_browser_helper_subprocess(
            node_path,
            payload,
            timeout_seconds=_browser_helper_timeout_seconds(payload),
            check_control=check_control,
        )
    except (RuntimeError, OSError) as error:
        raise RuntimeError(_browser_error_message(error)) from error
    if completed.returncode != 0:
        raise RuntimeError(_browser_error_message(subprocess.CalledProcessError(
            completed.returncode,
            completed.args,
            output=completed.stdout,
            stderr=completed.stderr,
        )))
    data = _json_object(completed.stdout)
    if not data.get("ok"):
        raise RuntimeError(str(data.get("error") or "sentinel_browser_failed").strip())
    return data


def _prepare_sentinel_artifacts_legacy(
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
    turnstile_token = ""
    if isinstance(turnstile_data, dict) and str(turnstile_data.get("dx") or "").strip():
        turnstile_token = str(solve_turnstile_dx(str(turnstile_data["dx"]), requirements_token, env) or "").strip()
        if strict_turnstile and not turnstile_token:
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
    if include_so and isinstance(so_data, dict) and so_data.get("required") and str(so_data.get("snapshot_dx") or "").strip():
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
        if strict_so and not so_token:
            raise RuntimeError("sentinel_so_token_failed")
        if so_token:
            so_header = _payload_json({"so": so_token, "c": challenge_token}, device_id, flow)

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
        turnstile_required=bool(((data.get("turnstile") or {}) if isinstance(data, dict) else {}).get("required")),
        so_required=bool(((data.get("so") or {}) if isinstance(data, dict) else {}).get("required")),
        req_payload=data if isinstance(data, dict) else {},
        executor="legacy_vm",
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
    _assert_browser_sdk_required()
    hints = build_user_agent_client_hints(
        user_agent or DEFAULT_SENTINEL_USER_AGENT,
        sec_ch_ua=sec_ch_ua,
        sec_ch_ua_full_version_list=sec_ch_ua_full_version_list,
    )
    ua = hints["user_agent"]
    ch_ua = hints["sec_ch_ua"]
    ch_ua_full_version_list = hints["sec_ch_ua_full_version_list"]
    payload = _run_browser_sentinel(
        session,
        device_id,
        flow,
        include_so=include_so,
        observer_wait_ms=observer_wait_ms,
        user_agent=ua,
        sec_ch_ua=ch_ua,
        sec_ch_ua_full_version_list=ch_ua_full_version_list,
        proxy=proxy,
        check_control=check_control,
    )
    req_status = 0
    try:
        req_status = int(payload.get("reqStatus") or 0)
    except (TypeError, ValueError):
        req_status = 0
    req_count = 0
    try:
        req_count = int(payload.get("reqCount") or 0)
    except (TypeError, ValueError):
        req_count = 0
    req_matched_count = 0
    try:
        req_matched_count = int(payload.get("reqMatchedCount") or 0)
    except (TypeError, ValueError):
        req_matched_count = 0
    req_payload = payload.get("reqPayload") if isinstance(payload.get("reqPayload"), dict) else {}
    sentinel_header = str(payload.get("sentinelHeader") or "").strip()
    so_header = str(payload.get("soHeader") or "").strip()
    try:
        sentinel_payload = _parse_validated_header("sentinel_token", sentinel_header, require_body_key="p")
    except RuntimeError as error:
        if req_status == 403:
            raise RuntimeError("sentinel_req_failed_403") from error
        raise
    so_payload = _json_object(so_header)
    sentinel_challenge = str(sentinel_payload.get("c") or "").strip()
    so_challenge = str(so_payload.get("c") or "").strip()
    challenge_token = sentinel_challenge or so_challenge
    if not challenge_token:
        raise RuntimeError("sentinel_token_failed: missing_c")
    if bool(payload.get("challengeMismatch")) or (sentinel_challenge and so_challenge and sentinel_challenge != so_challenge):
        raise RuntimeError("sentinel_challenge_mismatch: header_challenge_mismatch")
    if req_count > 0 and req_matched_count == 0:
        raise RuntimeError("sentinel_challenge_mismatch: req_token_unmatched")
    if req_payload:
        req_challenge = str(req_payload.get("token") or "").strip()
        if not req_challenge:
            raise RuntimeError("sentinel_challenge_mismatch: missing_req_token")
        if req_challenge != challenge_token:
            raise RuntimeError("sentinel_challenge_mismatch: req_token_mismatch")
    if req_matched_count > 0 and req_status >= 400:
        raise RuntimeError(f"sentinel_req_failed_{req_status}")
    if (strict_turnstile or strict_so or include_so) and not req_payload:
        raise RuntimeError("sentinel_req_metadata_missing")
    if req_payload:
        turnstile_required = bool(((req_payload.get("turnstile") or {}) if isinstance(req_payload, dict) else {}).get("required"))
        so_required = bool(((req_payload.get("so") or {}) if isinstance(req_payload, dict) else {}).get("required"))
        proof_required = bool(((req_payload.get("proofofwork") or {}) if isinstance(req_payload, dict) else {}).get("required"))
    else:
        turnstile_required = False
        so_required = False
        proof_required = bool(sentinel_payload.get("p"))
    if include_so and str(so_payload.get("e") or "").strip():
        raise RuntimeError(f"sentinel_so_token_failed: {str(so_payload.get('e') or '').strip()}")
    if strict_so and so_required and not so_header:
        raise RuntimeError("sentinel_so_token_failed")
    if so_header:
        so_payload = _parse_validated_header("sentinel_so_token", so_header, require_body_key="so")
    if strict_turnstile and turnstile_required and not str(sentinel_payload.get("t") or "").strip():
        raise RuntimeError("sentinel_turnstile_token_failed")
    if strict_so and so_required and not str(so_payload.get("so") or "").strip():
        raise RuntimeError("sentinel_so_token_failed")
    sdk_version = str(payload.get("sdkVersion") or "").strip()
    sdk_url = str(payload.get("sdkUrl") or "").strip()
    if not sdk_version or not sdk_url:
        resolved_version, resolved_url = _resolve_sdk_info(
            session,
            user_agent=ua,
            sec_ch_ua=ch_ua,
            sec_ch_ua_full_version_list=ch_ua_full_version_list,
        )
        sdk_version = sdk_version or resolved_version
        sdk_url = sdk_url or resolved_url
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
        requirements_token="",
        proof_token=str(sentinel_payload.get("p") or "").strip(),
        turnstile_token=str(sentinel_payload.get("t") or "").strip(),
        session_observer_token=str(so_payload.get("so") or "").strip(),
        proof_required=proof_required,
        turnstile_required=turnstile_required,
        so_required=so_required,
        req_payload=req_payload if isinstance(req_payload, dict) else {},
        executor=str(payload.get("executor") or "browser_sdk"),
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
