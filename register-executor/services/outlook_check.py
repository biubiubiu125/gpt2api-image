from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any

from curl_cffi import requests

from services.proxy_service import proxy_settings
from services.register import mail_provider


TOKEN_URL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
GRAPH_MESSAGES_URL = "https://graph.microsoft.com/v1.0/me/messages"
GRAPH_SCOPE = "offline_access https://graph.microsoft.com/Mail.Read"


def _mask_email(email: str) -> str:
    local, sep, domain = str(email or "").partition("@")
    if not sep:
        return "***"
    if len(local) <= 2:
        return f"{local[:1]}***@{domain}"
    return f"{local[:2]}***{local[-1:]}@{domain}"


def _outlook_credentials(register_config: dict[str, Any]) -> list[dict[str, str]]:
    mail = register_config.get("mail") if isinstance(register_config.get("mail"), dict) else {}
    providers = mail.get("providers") if isinstance(mail.get("providers"), list) else []
    credentials: list[dict[str, str]] = []
    for provider in providers:
        if not isinstance(provider, dict):
            continue
        if provider.get("type") != "outlook_token" or not provider.get("enable"):
            continue
        credentials.extend(mail_provider.parse_outlook_credentials(str(provider.get("mailboxes") or "")))
    return credentials


def _check_one(credential: dict[str, str], proxy: str) -> dict[str, Any]:
    email = credential["email"]
    item: dict[str, Any] = {"email": _mask_email(email), "ok": False, "messages": 0, "error": ""}
    session = requests.Session(**proxy_settings.build_session_kwargs(proxy=proxy, impersonate="chrome"))
    try:
        token_resp = session.post(
            TOKEN_URL,
            data={
                "client_id": credential["client_id"],
                "grant_type": "refresh_token",
                "refresh_token": credential["refresh_token"],
                "scope": GRAPH_SCOPE,
            },
            timeout=30,
        )
        token_data = token_resp.json() if token_resp.text else {}
        access_token = str(token_data.get("access_token") or "").strip()
        if token_resp.status_code != 200 or not access_token:
            detail = str(token_data.get("error_description") or token_data.get("error") or token_resp.text or "")[:300]
            raise RuntimeError(f"token refresh failed: HTTP {token_resp.status_code}, {detail}")
        msg_resp = session.get(
            GRAPH_MESSAGES_URL,
            headers={"Authorization": f"Bearer {access_token}", "Accept": "application/json"},
            params={"$top": 1, "$orderby": "receivedDateTime desc", "$select": "subject,receivedDateTime"},
            timeout=30,
        )
        msg_data = msg_resp.json() if msg_resp.text else {}
        if msg_resp.status_code != 200:
            detail = str((msg_data.get("error") or {}).get("message") if isinstance(msg_data.get("error"), dict) else msg_resp.text)[:300]
            raise RuntimeError(f"graph messages failed: HTTP {msg_resp.status_code}, {detail}")
        values = msg_data.get("value") if isinstance(msg_data, dict) else []
        item["ok"] = True
        item["messages"] = len(values) if isinstance(values, list) else 0
    except Exception as exc:
        item["error"] = str(exc)
    finally:
        session.close()
    return item


def check_outlook_pool(register_config: dict[str, Any], limit: int = 5) -> dict[str, Any]:
    credentials = _outlook_credentials(register_config)
    limit = max(1, min(int(limit or 5), 50))
    selected = credentials[:limit]
    checked: list[dict[str, Any]] = []
    proxy = str(register_config.get("proxy") or "").strip()
    mail_cfg = register_config.get("mail") if isinstance(register_config.get("mail"), dict) else {}
    if not bool(mail_cfg.get("api_use_register_proxy", True)):
        proxy = ""
    if selected:
        workers = min(10, len(selected))
        with ThreadPoolExecutor(max_workers=workers) as executor:
            futures = [executor.submit(_check_one, credential, proxy) for credential in selected]
            for future in as_completed(futures):
                checked.append(future.result())
    return {
        "total": len(credentials),
        "checked": len(checked),
        "ok": sum(1 for item in checked if item.get("ok")),
        "failed": sum(1 for item in checked if not item.get("ok")),
        "items": checked,
    }
