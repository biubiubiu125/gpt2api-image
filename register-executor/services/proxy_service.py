from __future__ import annotations

from dataclasses import dataclass, field
import re
from urllib.parse import quote


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
