from __future__ import annotations

from dataclasses import dataclass, field
import re
from urllib.parse import quote, urlparse


def _colon_proxy_to_url(url: str) -> str:
    text = str(url or "").strip()
    if not text:
        return ""
    if "://" in text:
        return text
    parts = text.split(":")
    if len(parts) == 2:
        host, port = parts
        return f"http://{host}:{port}"
    if len(parts) >= 4:
        host, port, user = parts[:3]
        password = ":".join(parts[3:])
        return f"http://{quote(user)}:{quote(password)}@{host}:{port}"
    return text


def normalize_proxy_url(url: str) -> str:
    candidate = str(url or "").strip()
    if candidate and "://" not in candidate:
        candidate = _colon_proxy_to_url(candidate)
    if candidate.lower().startswith("socks://"):
        return "socks5://" + candidate[len("socks://") :]
    return candidate


def validate_proxy_url(url: str) -> str:
    candidate = normalize_proxy_url(url)
    if not candidate:
        return ""
    if any(char.isspace() for char in candidate):
        raise ValueError("proxy URL contains whitespace")
    parsed = urlparse(candidate)
    if not parsed.scheme:
        raise ValueError("proxy URL missing scheme")
    if not parsed.netloc:
        raise ValueError("proxy URL missing host")
    if not parsed.hostname:
        raise ValueError("proxy URL missing host")
    host_part = parsed.netloc.rsplit("@", 1)[-1]
    if host_part.endswith(":"):
        raise ValueError("proxy URL invalid port")
    try:
        port = parsed.port
    except ValueError as exc:
        raise ValueError("proxy URL invalid port") from exc
    if port is not None and port < 1:
        raise ValueError("proxy URL invalid port")
    if parsed.scheme.lower() not in {"http", "https", "socks4", "socks4a", "socks5", "socks5h"}:
        raise ValueError(f"unsupported proxy scheme {parsed.scheme!r}")
    if parsed.path not in {"", "/"}:
        raise ValueError("proxy URL must not include a path")
    if parsed.query or parsed.fragment:
        raise ValueError("proxy URL must not include query or fragment")
    return candidate


@dataclass(frozen=True)
class ClearanceBundle:
    target_host: str
    proxy_url: str = ""
    cookies: dict[str, str] = field(default_factory=dict, repr=False)
    user_agent: str = ""


@dataclass(frozen=True)
class ProxyRuntimeProfile:
    proxy_url: str = ""
    clearance: dict[str, object] = field(default_factory=dict, repr=False)

    @property
    def clearance_enabled(self) -> bool:
        return False


class ProxySettingsStore:
    def build_session_kwargs(self, **kwargs):
        proxy = normalize_proxy_url(str(kwargs.get("proxy") or ""))
        result = {}
        if proxy:
            result["proxy"] = proxy
        impersonate = str(kwargs.get("impersonate") or "").strip()
        if impersonate:
            result["impersonate"] = impersonate
        if "verify" in kwargs:
            result["verify"] = bool(kwargs.get("verify"))
        return result

    def build_headers(self, headers=None, **_kwargs):
        return dict(headers or {})

    def get_profile(self, **kwargs) -> ProxyRuntimeProfile:
        return ProxyRuntimeProfile(proxy_url=normalize_proxy_url(str(kwargs.get("proxy") or "")))

    def refresh_clearance(self, **_kwargs):
        return None


proxy_settings = ProxySettingsStore()
