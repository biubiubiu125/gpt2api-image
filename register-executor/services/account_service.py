from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any

from services.config import config


PLATFORM_CLIENT_ID = "app_2SKx67EdpoN0G6j64rFvigXD"


class AccountService:
    def _headers(self) -> dict[str, str]:
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if config.register_internal_key:
            headers["X-Register-Internal-Key"] = config.register_internal_key
        elif config.auth_key:
            headers["Authorization"] = f"Bearer {config.auth_key}"
        return headers

    def _request(self, method: str, path: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
        if not config.go_api_base_url:
            raise RuntimeError("GPT2API_IMAGE_API_BASE_URL is required")
        body = None
        if payload is not None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(
            f"{config.go_api_base_url}{path}",
            data=body,
            headers=self._headers(),
            method=method.upper(),
        )
        try:
            with urllib.request.urlopen(req, timeout=90) as resp:
                text = resp.read().decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"go_api_http_{exc.code}: {detail[:500]}") from exc
        if not text:
            return {}
        data = json.loads(text)
        return data if isinstance(data, dict) else {}

    @staticmethod
    def _normalize_payload(item: dict[str, Any]) -> dict[str, Any]:
        payload = dict(item or {})
        payload.setdefault("source_type", "web")
        payload.setdefault("client_id", PLATFORM_CLIENT_ID)
        return payload

    def list_accounts(self) -> list[dict[str, Any]]:
        data = self._request("GET", "/internal/register/accounts")
        items = data.get("items")
        return items if isinstance(items, list) else []

    def get_account(self, access_token: str) -> dict[str, Any] | None:
        target = str(access_token or "").strip()
        if not target:
            return None
        return next((item for item in self.list_accounts() if str(item.get("access_token") or "") == target), None)

    def add_account_items(self, items: list[dict[str, Any]]) -> dict[str, Any]:
        payloads = [self._normalize_payload(item) for item in items if isinstance(item, dict)]
        return self._request("POST", "/internal/register/accounts", {"account_records": payloads})

    def refresh_accounts(self, access_tokens: list[str], defer_invalid_removal: bool = False) -> dict[str, Any]:
        return self._request(
            "POST",
            "/internal/register/accounts/refresh",
            {"access_tokens": access_tokens, "defer_invalid_removal": bool(defer_invalid_removal)},
        )

    def delete_accounts(self, tokens: list[str]) -> dict[str, Any]:
        return self._request("POST", "/internal/register/accounts/delete", {"tokens": tokens})


account_service = AccountService()
