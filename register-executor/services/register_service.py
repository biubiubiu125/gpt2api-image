from __future__ import annotations

import json
import threading
import time
import uuid
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from datetime import datetime, timezone
from pathlib import Path

from services.account_service import account_service
from services.config import DATA_DIR
from services.log_service import LOG_TYPE_ACCOUNT, log_service
from services.proxy_service import validate_proxy_url
from services.register import mail_provider, openai_register
from services.register.proxy_pool import register_proxy_pool


REGISTER_FILE = DATA_DIR / "register.json"
REGISTER_STALL_FAILURE_REASON = "register_task_stalled"
SECRET_PLACEHOLDER = "********"
REGISTER_RUNTIME_FIELDS = {"enabled", "stats", "logs", "executor", "lifecycle", "is_running", "is_stopping"}
REGISTER_PROVIDER_RUNTIME_FIELDS = {
    "auto_domain_blacklist",
    "auto_domain_blacklist_entries",
    "mailboxes_count",
    "mailboxes_preview",
    "mailboxes_stats",
}
SOURCE_AUTO_BLACKLIST_REASONS = {"unsupported_email"}


class RegisterTaskActiveError(RuntimeError):
    pass


def _is_secret_key(key: object) -> bool:
    normalized = str(key or "").strip().lower().replace("-", "_")
    if normalized in {
        "api_key",
        "admin_password",
        "password",
        "token",
        "access_token",
        "refresh_token",
        "id_token",
        "ddg_token",
        "cf_api_key",
        "client_secret",
        "private_key",
        "secret",
        "authorization",
    }:
        return True
    return (
        normalized.endswith("_token")
        or normalized.endswith("_password")
        or normalized.endswith("_api_key")
        or "secret" in normalized
    )


def _serialize_outlook_pool(credentials: list[dict]) -> str:
    return "\n".join(
        f'{c["email"]}----{c.get("password", "")}----{c["client_id"]}----{c["refresh_token"]}' for c in credentials
    )


def _merge_outlook_pool(old_text: str, new_text: str) -> str:
    """合并已存邮箱池与新导入文本，按邮箱去重，新导入的同名邮箱覆盖旧凭据。"""
    merged: dict[str, dict] = {}
    for credential in mail_provider.parse_outlook_credentials(old_text or ""):
        merged[credential["email"].strip().lower()] = credential
    for credential in mail_provider.parse_outlook_credentials(new_text or ""):
        merged[credential["email"].strip().lower()] = credential
    return _serialize_outlook_pool(list(merged.values()))


def _provider_id(value: object) -> str:
    text = str(value or "").strip()
    return text if text else uuid.uuid4().hex


def _normalize_mail_providers(cfg: dict) -> None:
    mail = cfg.get("mail")
    if not isinstance(mail, dict):
        return
    providers = mail.get("providers")
    if not isinstance(providers, list):
        mail["providers"] = []
        return
    for provider in providers:
        if isinstance(provider, dict):
            provider["provider_id"] = _provider_id(provider.get("provider_id") or provider.get("id"))
            for runtime_key in REGISTER_PROVIDER_RUNTIME_FIELDS:
                provider.pop(runtime_key, None)


def _enabled_provider(provider: dict) -> bool:
    return provider.get("enable") is not False


def _non_empty_list(value: object) -> list[str]:
    if isinstance(value, list):
        return [str(item).strip() for item in value if str(item).strip()]
    text = str(value or "").strip()
    return [text] if text else []


def _split_domain_values(value: object) -> list[str]:
    if isinstance(value, list):
        values: list[str] = []
        for item in value:
            values.extend(_split_domain_values(item))
        return values
    text = str(value or "").strip()
    if not text:
        return []
    return [item.strip() for item in text.replace(",", "\n").splitlines() if item.strip()]


def _validate_mail_providers(cfg: dict) -> None:
    mail = cfg.get("mail")
    if not isinstance(mail, dict):
        return
    providers = mail.get("providers")
    if not isinstance(providers, list):
        return
    for provider in providers:
        if not isinstance(provider, dict) or not _enabled_provider(provider):
            continue
        if provider.get("type") == "cloudmail_gen" and not _non_empty_list(provider.get("domain")):
            raise ValueError("CloudMailGen 需要至少配置一个 domain")


def _migrate_yyds_provider_domain_blacklist(cfg: dict) -> tuple[bool, int]:
    mail = cfg.get("mail")
    if not isinstance(mail, dict):
        return False, 0
    providers = mail.get("providers")
    if not isinstance(providers, list):
        return False, 0
    changed = False
    migrated = 0
    for provider in providers:
        if not isinstance(provider, dict) or provider.get("type") != "yyds_mail":
            continue
        if "domain_blacklist" not in provider:
            continue
        changed = True
        for domain in _split_domain_values(provider.get("domain_blacklist")):
            if mail_provider.add_yyds_domain_blacklist(domain):
                migrated += 1
        provider.pop("domain_blacklist", None)
    return changed, migrated


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _default_auto_refill_config() -> dict:
    return {
        "enabled": False,
        "min_available": 30,
        "batch_total": 100,
        "check_interval": 300,
    }


def _int_or_default(value: object, default: int) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _failure_reason_key(value: object, default: str = "unknown_error") -> str:
    reason = str(value or "").strip()
    return reason if reason else default


def _bump_failure_reason(counts: dict[str, int], reason: object, amount: int = 1) -> None:
    key = _failure_reason_key(reason)
    if amount <= 0:
        return
    counts[key] = counts.get(key, 0) + amount


def _source_stats_key(value: object, default: str = "unknown") -> str:
    key = str(value or "").strip().lower()
    return key if key else default


def _provider_domain_stats_key(provider_key: object, domain_key: object) -> str:
    provider = str(provider_key or "").strip()
    domain = _source_stats_key(domain_key, "")
    if provider and domain:
        return f"{provider}::{domain}"
    return provider or domain


def _normalize_source_stats(raw: object) -> dict[str, dict]:
    data = raw if isinstance(raw, dict) else {}
    normalized: dict[str, dict] = {}
    for key, item in data.items():
        if not isinstance(item, dict):
            continue
        failure_reasons = {}
        raw_reasons = item.get("failure_reasons") if isinstance(item.get("failure_reasons"), dict) else {}
        for reason, count in raw_reasons.items():
            normalized_reason = _failure_reason_key(reason)
            numeric_count = int(count or 0)
            if normalized_reason and numeric_count > 0:
                failure_reasons[normalized_reason] = numeric_count
        source_key = _source_stats_key(key)
        normalized_item = {
            "attempts": max(0, _int_or_default(item.get("attempts"), 0)),
            "success": max(0, _int_or_default(item.get("success"), 0)),
            "usable_success": max(0, _int_or_default(item.get("usable_success"), 0)),
            "fail": max(0, _int_or_default(item.get("fail"), 0)),
            "saved": max(0, _int_or_default(item.get("saved"), 0)),
            "otp_verified": max(0, _int_or_default(item.get("otp_verified"), 0)),
            "post_otp_fail": max(0, _int_or_default(item.get("post_otp_fail"), 0)),
            "unsupported_email": max(0, _int_or_default(item.get("unsupported_email"), 0)),
            "registration_disallowed": max(0, _int_or_default(item.get("registration_disallowed"), 0)),
            "success_rate": round(float(item.get("success_rate") or 0), 1),
            "failure_reasons": failure_reasons,
            "updated_at": str(item.get("updated_at") or ""),
        }
        provider = str(item.get("provider") or "").strip()
        provider_ref = str(item.get("provider_ref") or "").strip()
        domain = _source_stats_key(item.get("domain"), "")
        if provider:
            normalized_item["provider"] = provider
        if provider_ref:
            normalized_item["provider_ref"] = provider_ref
        if domain:
            normalized_item["domain"] = domain
        normalized[source_key] = normalized_item
    return normalized


def _update_source_stats(
    bucket: dict[str, dict],
    key: object,
    result: dict,
    *,
    updated_at: str,
    provider: object = "",
    provider_ref: object = "",
    domain: object = "",
) -> None:
    source_key = _source_stats_key(key)
    item = bucket.setdefault(
        source_key,
        {
            "attempts": 0,
            "success": 0,
            "usable_success": 0,
            "fail": 0,
            "saved": 0,
            "otp_verified": 0,
            "post_otp_fail": 0,
            "unsupported_email": 0,
            "registration_disallowed": 0,
            "success_rate": 0.0,
            "failure_reasons": {},
            "updated_at": "",
            "provider": "",
            "provider_ref": "",
            "domain": "",
        },
    )
    provider_value = str(provider or "").strip()
    provider_ref_value = str(provider_ref or "").strip()
    domain_value = _source_stats_key(domain, "")
    if provider_value:
        item["provider"] = provider_value
    if provider_ref_value:
        item["provider_ref"] = provider_ref_value
    if domain_value:
        item["domain"] = domain_value
    item["attempts"] += 1
    if result.get("otp_verified"):
        item["otp_verified"] += 1
    if result.get("ok"):
        item["success"] += 1
        if result.get("usable"):
            item["usable_success"] += 1
        if result.get("saved"):
            item["saved"] += 1
    else:
        item["fail"] += 1
        reason = _failure_reason_key(result.get("reason"))
        _bump_failure_reason(item["failure_reasons"], reason)
        if reason == "unsupported_email":
            item["unsupported_email"] += 1
        elif reason == "registration_disallowed":
            item["registration_disallowed"] += 1
        if result.get("otp_verified"):
            item["post_otp_fail"] += 1
    item["success_rate"] = round(item["usable_success"] * 100 / max(1, item["attempts"]), 1)
    item["updated_at"] = updated_at


def _register_settings() -> dict:
    try:
        settings = account_service.get_settings()
        return settings if isinstance(settings, dict) else {}
    except Exception:
        return {}


def _settings_bool(settings: dict | None, key: str, default: bool) -> bool:
    if not isinstance(settings, dict) or key not in settings:
        return default
    return bool(settings.get(key))


def _delete_403_threshold(settings: dict | None = None) -> int:
    data = settings if isinstance(settings, dict) else _register_settings()
    return max(1, _int_or_default(data.get("delete_403_consecutive"), 2))


def _delete_timeout_threshold(settings: dict | None = None) -> int:
    data = settings if isinstance(settings, dict) else _register_settings()
    return max(1, _int_or_default(data.get("delete_timeout_consecutive"), 2))


def _auto_refresh_delete_failed_accounts(settings: dict | None = None) -> bool:
    data = settings if isinstance(settings, dict) else _register_settings()
    return _settings_bool(data, "auto_refresh_delete_failed_accounts", True)


def _image_account_usable(item: dict, settings: dict | None = None) -> bool:
    data = settings if isinstance(settings, dict) else _register_settings()
    upload_quota_known = "upload_quota" in item and not bool(item.get("upload_quota_unknown"))
    return (
        str(item.get("status") or "\u6b63\u5e38") == "\u6b63\u5e38"
        and not bool(item.get("pending_delete"))
        and _int_or_default(item.get("consecutive_403"), 0) < _delete_403_threshold(data)
        and _int_or_default(item.get("consecutive_timeout"), 0) < _delete_timeout_threshold(data)
        and (not _auto_refresh_delete_failed_accounts(data) or not str(item.get("last_refresh_error") or "").strip())
        and not bool(item.get("image_quota_unknown"))
        and not (upload_quota_known and _int_or_default(item.get("upload_quota"), 0) <= 0)
        and _int_or_default(item.get("quota"), 0) > 0
    )


def _delete_account_or_raise(token: str) -> None:
    token = str(token or "").strip()
    if not token:
        return
    account_service.delete_accounts([token])
    if account_service.get_account(token) is not None:
        raise RuntimeError("account_delete_not_persisted")


def _default_config() -> dict:
    return {
        **openai_register.config,
        "mode": "total",
        "target_quota": 100,
        "target_available": 10,
        "check_interval": 5,
        "fixed_password": "",
        "auto_refill": _default_auto_refill_config(),
        "enabled": False,
        "stats": {
            "success": 0,
            "usable_success": 0,
            "fail": 0,
            "done": 0,
            "saved": 0,
            "refresh_failed": 0,
            "token_acquired_refresh_failed": 0,
            "running": 0,
            "threads": openai_register.config["threads"],
            "elapsed_seconds": 0,
            "avg_seconds": 0,
            "success_rate": 0,
            "current_quota": 0,
            "current_available": 0,
            "failure_reasons": {},
            "provider_stats": {},
            "domain_stats": {},
            "provider_domain_stats": {},
        },
    }


def _normalize(raw: dict) -> dict:
    cfg = _default_config()
    cfg.update({k: v for k, v in raw.items() if k not in {"stats", "logs", "lifecycle", "is_running", "is_stopping"}})
    cfg["total"] = max(1, int(cfg.get("total") or 1))
    cfg["threads"] = max(1, int(cfg.get("threads") or 1))
    cfg["mode"] = str(cfg.get("mode") or "total").strip() if str(cfg.get("mode") or "total").strip() in {"total", "quota", "available"} else "total"
    cfg["target_quota"] = max(1, int(cfg.get("target_quota") or 1))
    cfg["target_available"] = max(1, int(cfg.get("target_available") or 1))
    cfg["check_interval"] = max(1, int(cfg.get("check_interval") or 5))
    cfg["fixed_password"] = str(cfg.get("fixed_password") or "").strip()
    cfg["proxy"] = str(cfg.get("proxy") or "").strip()
    for key in list(cfg):
        if str(key).startswith("proxy_"):
            cfg.pop(key, None)
    cfg.pop("proxy_url", None)
    cfg.pop("proxy_list_text", None)
    cfg.pop("proxy_bind_url", None)
    cfg.pop("proxy_bind_text", None)
    cfg.pop("proxy_cloudflare_cooldown_minutes", None)
    cfg.pop("proxy_network_cooldown_minutes", None)
    cfg.pop("proxy_failure_threshold", None)
    cfg.pop("proxy_blacklist_seconds", None)
    cfg.pop("proxy_success_clear_failures", None)
    cfg.pop("proxy_lease_seconds", None)
    cfg["task_timeout_seconds"] = max(30, int(cfg.get("task_timeout_seconds") or 300))
    cfg["task_stall_timeout_seconds"] = max(0, _int_or_default(cfg.get("task_stall_timeout_seconds"), 60))
    auto_refill_raw = cfg.get("auto_refill") if isinstance(cfg.get("auto_refill"), dict) else {}
    auto_refill = {**_default_auto_refill_config(), **auto_refill_raw}
    auto_refill["enabled"] = bool(auto_refill.get("enabled"))
    auto_refill["min_available"] = max(1, int(auto_refill.get("min_available") or 1))
    auto_refill["batch_total"] = max(1, int(auto_refill.get("batch_total") or 1))
    auto_refill["check_interval"] = max(10, int(auto_refill.get("check_interval") or 300))
    cfg["auto_refill"] = auto_refill
    if isinstance(cfg.get("mail"), dict):
        cfg["mail"]["api_use_register_proxy"] = False
        cfg["mail"].pop("proxy", None)
        cfg["mail"].pop("yyds_delete_after_success", None)
    _normalize_mail_providers(cfg)
    cfg["enabled"] = bool(cfg.get("enabled"))
    stats = {**_default_config()["stats"], **(raw.get("stats") if isinstance(raw.get("stats"), dict) else {}),
             "threads": cfg["threads"]}
    for runtime_key in ("lifecycle", "is_running", "is_stopping"):
        stats.pop(runtime_key, None)
    cfg["stats"] = stats
    return cfg


def _validate_register_proxy(cfg: dict) -> None:
    cfg["proxy"] = validate_proxy_url(str(cfg.get("proxy") or ""))


class RegisterService:
    def __init__(self, store_file: Path):
        self._store_file = store_file
        self._lock = threading.RLock()
        self._runner: threading.Thread | None = None
        self._stop_event: threading.Event | None = None
        self._run_id = ""
        self._active_futures: set = set()
        self._logs: list[dict] = []
        openai_register.register_log_sink = self._append_log
        self._config = self._load()
        self._sanitize_loaded_config()
        self._sync_proxy_pool_state_locked(force=True)
        if self._config["enabled"]:
            self._auto_start_loaded_config()

    def _load(self) -> dict:
        try:
            return _normalize(json.loads(self._store_file.read_text(encoding="utf-8")))
        except Exception:
            return _normalize({})

    def _save(self) -> None:
        self._store_file.parent.mkdir(parents=True, exist_ok=True)
        self._store_file.write_text(json.dumps(self._config, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    def _save_loaded_config_migration(self) -> None:
        try:
            self._save()
        except Exception as exc:
            self._append_log(f"注册配置迁移保存失败：{exc}", "red")

    def _sanitize_loaded_config(self) -> None:
        changed = False
        original_proxy = str(self._config.get("proxy") or "")
        try:
            _validate_register_proxy(self._config)
        except ValueError as exc:
            self._config["proxy"] = ""
            self._config["enabled"] = False
            changed = True
            self._append_log(f"注册配置中的代理无效，已清空并停止自动启动：{exc}", "yellow")
        else:
            changed = self._config.get("proxy") != original_proxy
        migrated_blacklist, migrated_count = _migrate_yyds_provider_domain_blacklist(self._config)
        if migrated_blacklist:
            changed = True
            if migrated_count > 0:
                self._append_log(f"已将 {migrated_count} 个旧版 YYDS provider 手工禁用域名迁移到统一黑名单", "yellow")
        if changed:
            self._save_loaded_config_migration()

    def _auto_start_loaded_config(self) -> None:
        try:
            self.start()
        except Exception as exc:
            self._config["enabled"] = False
            self._append_log(f"注册任务自动启动失败，已停止自动启动：{exc}", "red")
            self._save_loaded_config_migration()

    def _runtime_state_locked(self) -> dict:
        running = bool(self._runner and self._runner.is_alive())
        stopping = bool(running and self._stop_event and self._stop_event.is_set())
        stats = self._config.get("stats") if isinstance(self._config.get("stats"), dict) else {}
        if stopping:
            lifecycle = "stopping"
        elif running and stats.get("job_kind") == "repair_abnormal":
            lifecycle = "repairing"
        elif running:
            lifecycle = "running"
        else:
            lifecycle = "idle"
        return {"lifecycle": lifecycle, "is_running": running, "is_stopping": stopping}

    @staticmethod
    def _attach_runtime_state(snapshot: dict, runtime: dict) -> None:
        stats = snapshot.get("stats")
        if not isinstance(stats, dict):
            stats = {}
            snapshot["stats"] = stats
        for key, value in runtime.items():
            snapshot[key] = value
            stats[key] = value

    def _require_idle_locked(self, action: str) -> None:
        if self._runner and self._runner.is_alive():
            raise RegisterTaskActiveError(f"register task is running; stop and wait for cleanup before {action}")

    def ensure_idle(self, action: str = "changing register runtime resources") -> None:
        with self._lock:
            self._require_idle_locked(action)

    def get(self, redact: bool = True) -> dict:
        with self._lock:
            self._refresh_proxy_pool_state_locked(force=False)
            snapshot = json.loads(json.dumps({**self._config, "logs": self._logs[-300:]}, ensure_ascii=False))
            self._attach_runtime_state(snapshot, self._runtime_state_locked())
        if redact:
            self._redact_secrets(snapshot)
        return snapshot

    @staticmethod
    def _mask_email(email: str) -> str:
        local, sep, domain = str(email or "").partition("@")
        if not sep:
            return "***"
        masked = (local[:2] + "***" + local[-1:]) if len(local) > 2 else (local[:1] + "***")
        return f"{masked}@{domain}"

    def _redact_secrets(self, snapshot: dict) -> None:
        """把注册配置里的密钥从对外输出中抹掉，仅保留占位和必要统计。

        mailboxes 改为只写导入框（输出为空），避免把密码与 refresh_token 通过 GET/SSE 反复广播。
        """
        if str(snapshot.get("fixed_password") or "").strip():
            snapshot["fixed_password"] = SECRET_PLACEHOLDER
        mail = snapshot.get("mail")
        if not isinstance(mail, dict):
            return
        providers = mail.get("providers")
        if not isinstance(providers, list):
            return
        for provider in providers:
            if not isinstance(provider, dict):
                continue
            for key, value in list(provider.items()):
                if _is_secret_key(key) and str(value or "").strip():
                    provider[key] = SECRET_PLACEHOLDER
            if provider.get("type") == "outlook_token":
                credentials = mail_provider.parse_outlook_credentials(str(provider.get("mailboxes") or ""))
                provider["mailboxes"] = ""
                provider["mailboxes_count"] = len(credentials)
                provider["mailboxes_preview"] = [self._mask_email(c["email"]) for c in credentials]
                provider["mailboxes_stats"] = mail_provider.outlook_token_pool_stats(credentials)
            if provider.get("type") == "yyds_mail":
                provider["auto_domain_blacklist"] = mail_provider.yyds_domain_blacklist_items("auto")
                provider["auto_domain_blacklist_entries"] = [
                    entry
                    for entry in mail_provider.yyds_domain_blacklist_entries()
                    if bool(entry.get("auto")) and mail_provider.yyds_blacklist_matches_source(entry.get("source"), "openai_hard_reject")
                ]

    def _drop_mail_proxy(self) -> None:
        if isinstance(self._config.get("mail"), dict):
            self._config["mail"].pop("proxy", None)

    def _merge_outlook_pools(self, updates: dict) -> None:
        """对 outlook_token provider：把前端新导入的 mailboxes 与已存池按邮箱合并去重。

        前端 mailboxes 是只写导入框，留空表示不改动；填入的新行追加/覆盖已存凭据。
        优先按 provider_id 对齐；旧配置没有 provider_id 时才按数组下标兼容。
        """
        mail = updates.get("mail")
        if not isinstance(mail, dict) or not isinstance(mail.get("providers"), list):
            return
        old_mail = self._config.get("mail") if isinstance(self._config.get("mail"), dict) else {}
        old_providers = old_mail.get("providers") if isinstance(old_mail.get("providers"), list) else []
        old_by_id = {
            str(item.get("provider_id") or "").strip(): item
            for item in old_providers
            if isinstance(item, dict) and item.get("type") == "outlook_token" and str(item.get("provider_id") or "").strip()
        }
        old_outlook_providers = [item for item in old_providers if isinstance(item, dict) and item.get("type") == "outlook_token"]
        outlook_without_id_index = 0
        for index, provider in enumerate(mail["providers"]):
            if not isinstance(provider, dict) or provider.get("type") != "outlook_token":
                continue
            incoming_provider_id = str(provider.get("provider_id") or provider.get("id") or "").strip()
            provider["provider_id"] = _provider_id(incoming_provider_id)
            old = (old_by_id.get(incoming_provider_id) or {}) if incoming_provider_id else {}
            if not old and not incoming_provider_id and outlook_without_id_index < len(old_outlook_providers):
                old = old_outlook_providers[outlook_without_id_index]
                outlook_without_id_index += 1
            if not old and not incoming_provider_id and index < len(old_providers) and isinstance(old_providers[index], dict):
                old = old_providers[index]
            old_text = str(old.get("mailboxes") or "") if old.get("type") == "outlook_token" else ""
            new_text = str(provider.get("mailboxes") or "")
            provider["mailboxes"] = _merge_outlook_pool(old_text, new_text) if (old_text or new_text) else ""
            for key in ("mailboxes_count", "mailboxes_preview", "mailboxes_stats"):
                provider.pop(key, None)

    def _preserve_redacted_secrets(self, updates: dict) -> None:
        if str(updates.get("fixed_password") or "").strip() == SECRET_PLACEHOLDER:
            updates["fixed_password"] = self._config.get("fixed_password", "")
        mail = updates.get("mail")
        if not isinstance(mail, dict) or not isinstance(mail.get("providers"), list):
            return
        old_mail = self._config.get("mail") if isinstance(self._config.get("mail"), dict) else {}
        old_providers = old_mail.get("providers") if isinstance(old_mail.get("providers"), list) else []
        old_by_id = {
            str(item.get("provider_id") or item.get("id") or "").strip(): item
            for item in old_providers
            if isinstance(item, dict) and str(item.get("provider_id") or item.get("id") or "").strip()
        }
        for index, provider in enumerate(mail["providers"]):
            if not isinstance(provider, dict):
                continue
            provider_id = str(provider.get("provider_id") or provider.get("id") or "").strip()
            old = old_by_id.get(provider_id) or {}
            if not old and index < len(old_providers) and isinstance(old_providers[index], dict):
                old = old_providers[index]
            for key, value in list(provider.items()):
                if not _is_secret_key(key) or str(value or "").strip() != SECRET_PLACEHOLDER:
                    continue
                provider[key] = old.get(key, "")

    def _prune_unused_outlook_pools(self) -> int:
        mail = self._config.get("mail")
        if not isinstance(mail, dict):
            return 0
        providers = mail.get("providers")
        if not isinstance(providers, list):
            return 0
        total_removed = 0
        for provider in providers:
            if not isinstance(provider, dict) or provider.get("type") != "outlook_token":
                continue
            credentials = mail_provider.parse_outlook_credentials(str(provider.get("mailboxes") or ""))
            kept, removed = mail_provider.prune_outlook_unused_credentials(credentials)
            if removed:
                provider["mailboxes"] = _serialize_outlook_pool(kept)
                total_removed += removed
            for key in ("mailboxes_count", "mailboxes_preview", "mailboxes_stats"):
                provider.pop(key, None)
        return total_removed

    def update(self, updates: dict) -> dict:
        with self._lock:
            self._require_idle_locked("updating config")
            updates = {k: v for k, v in dict(updates or {}).items() if k not in REGISTER_RUNTIME_FIELDS}
            self._preserve_redacted_secrets(updates)
            self._merge_outlook_pools(updates)
            next_config = _normalize({**self._config, **updates})
            _validate_register_proxy(next_config)
            _validate_mail_providers(next_config)
            migrated_blacklist, migrated_count = _migrate_yyds_provider_domain_blacklist(next_config)
            self._config = next_config
            self._drop_mail_proxy()
            self._sync_openai_register_config(self._config)
            self._sync_proxy_pool_state_locked(force=True)
            self._save()
            if migrated_blacklist and migrated_count > 0:
                self._append_log(f"已将 {migrated_count} 个旧版 YYDS provider 手工禁用域名迁移到统一黑名单", "yellow")
            return self.get()

    def _sync_proxy_pool_state_locked(self, force: bool = False) -> None:
        register_proxy_pool.configure(self._config)
        self._refresh_proxy_pool_state_locked(force=force)

    def _refresh_proxy_pool_state_locked(self, force: bool = False) -> None:
        register_proxy_pool.prepare(force=force)
        self._config.setdefault("stats", {})["proxy_pool"] = register_proxy_pool.state()

    @staticmethod
    def _sync_openai_register_config(cfg: dict) -> None:
        openai_register.config.update({key: cfg[key] for key in openai_register.config if key in cfg})

    def start(self) -> dict:
        return self._start(trigger="manual")

    def repair_abnormal(self) -> dict:
        with self._lock:
            if self._runner and self._runner.is_alive():
                if not (self._stop_event and self._stop_event.is_set()):
                    self._config["enabled"] = True
                self._save()
                return self.get()
        try:
            metrics = self._pool_metrics()
        except Exception as exc:
            self._append_log(f"异常账号修复启动失败：{exc}", "red")
            raise
        with self._lock:
            if self._runner and self._runner.is_alive():
                if not (self._stop_event and self._stop_event.is_set()):
                    self._config["enabled"] = True
                self._save()
                return self.get()
            self._config["enabled"] = True
            self._logs = []
            self._stop_event = threading.Event()
            self._active_futures = set()
            openai_register.clear_worker_states()
            job_id = uuid.uuid4().hex
            self._run_id = job_id
            self._config["stats"] = {
                "job_id": job_id,
                "job_kind": "repair_abnormal",
                "success": 0,
                "usable_success": 0,
                "fail": 0,
                "done": 0,
                "saved": 0,
                "refresh_failed": 0,
                "token_acquired_refresh_failed": 0,
                "running": 1,
                "threads": 1,
                "failure_reasons": {},
                "provider_stats": {},
                "domain_stats": {},
                "provider_domain_stats": {},
                **metrics,
                "started_at": _now(),
                "updated_at": _now(),
                "trigger": "manual",
                "workers": [],
            }
            self._save()
            self._runner = threading.Thread(
                target=self._run_repair_abnormal,
                args=(self._stop_event, job_id),
                daemon=True,
                name="register-repair-abnormal",
            )
            self._runner.start()
            self._append_log("异常账号修复任务启动", "yellow")
            return self.get()

    def _start(
        self,
        trigger: str = "manual",
        run_overrides: dict | None = None,
        trigger_log: str | None = None,
    ) -> dict:
        with self._lock:
            if self._runner and self._runner.is_alive():
                if not (self._stop_event and self._stop_event.is_set()):
                    self._config["enabled"] = True
                self._save()
                if trigger == "auto_refill":
                    current_available = int(self._config.get("stats", {}).get("current_available") or 0)
                    auto_refill = self._config.get("auto_refill") if isinstance(self._config.get("auto_refill"), dict) else {}
                    self._log_auto_refill_decision(
                        started=False,
                        reason="register_task_running",
                        current_available=current_available,
                        min_available=max(1, int(auto_refill.get("min_available") or 1)),
                        batch_total=max(1, int((run_overrides or {}).get("total") or auto_refill.get("batch_total") or 1)),
                        message=trigger_log or "",
                    )
                return self.get()
            run_config = _normalize({**self._config, **(run_overrides or {})})
        try:
            _validate_register_proxy(run_config)
            _validate_mail_providers(run_config)
            metrics = self._pool_metrics()
        except Exception as exc:
            self._append_log(f"注册任务启动失败：{exc}", "red")
            raise
        with self._lock:
            if self._runner and self._runner.is_alive():
                if not (self._stop_event and self._stop_event.is_set()):
                    self._config["enabled"] = True
                self._save()
                if trigger == "auto_refill":
                    current_available = int(self._config.get("stats", {}).get("current_available") or 0)
                    auto_refill = self._config.get("auto_refill") if isinstance(self._config.get("auto_refill"), dict) else {}
                    self._log_auto_refill_decision(
                        started=False,
                        reason="register_task_running",
                        current_available=current_available,
                        min_available=max(1, int(auto_refill.get("min_available") or 1)),
                        batch_total=max(1, int((run_overrides or {}).get("total") or auto_refill.get("batch_total") or 1)),
                        message=trigger_log or "",
                    )
                return self.get()
            self._config["enabled"] = True
            self._drop_mail_proxy()
            self._logs = []
            register_proxy_pool.configure(run_config)
            register_proxy_pool.prepare(force=True)
            self._stop_event = threading.Event()
            self._active_futures = set()
            openai_register.clear_worker_states()
            job_id = uuid.uuid4().hex
            self._run_id = job_id
            self._config["stats"] = {
                "job_id": job_id,
                "success": 0,
                "usable_success": 0,
                "fail": 0,
                "done": 0,
                "saved": 0,
                "refresh_failed": 0,
                "token_acquired_refresh_failed": 0,
                "running": 0,
                "threads": run_config["threads"],
                "failure_reasons": {},
                "provider_stats": {},
                "domain_stats": {},
                "provider_domain_stats": {},
                **metrics,
                "started_at": _now(),
                "updated_at": _now(),
                "trigger": trigger,
                "run_mode": run_config["mode"],
                "run_total": run_config["total"],
                "proxy_pool": register_proxy_pool.state(),
                "workers": [],
            }
            self._sync_openai_register_config(run_config)
            with openai_register.stats_lock:
                openai_register.stats.update({"done": 0, "success": 0, "usable_success": 0, "fail": 0, "saved": 0, "refresh_failed": 0, "token_acquired_refresh_failed": 0, "start_time": time.time()})
            self._save()
            openai_register.set_active_run_id(job_id)
            self._runner = threading.Thread(target=self._run, args=(run_config, trigger, self._stop_event, job_id), daemon=True, name="openai-register")
            self._runner.start()
            if trigger_log:
                self._append_log(trigger_log, "yellow")
            if trigger == "auto_refill":
                self._log_auto_refill_decision(
                    started=True,
                    reason="below_min_available",
                    current_available=int(metrics.get("current_available") or 0),
                    min_available=max(1, int(run_config.get("auto_refill", {}).get("min_available") or 1)),
                    batch_total=max(1, int(run_config.get("total") or 1)),
                    message=trigger_log or "",
                )
            self._append_log(f"注册任务启动，模式={run_config['mode']}，线程数={run_config['threads']}，触发={trigger}", "yellow")
            return self.get()

    def start_auto_refill(self, batch_total: int, trigger_log: str | None = None) -> dict:
        if not trigger_log:
            trigger_log = f"自动补号触发：本轮注册={max(1, int(batch_total or 1))}"
        return self._start(
            trigger="auto_refill",
            run_overrides={"mode": "total", "total": batch_total},
            trigger_log=trigger_log,
        )

    def stop(self) -> dict:
        with self._lock:
            self._config["enabled"] = False
            job_id = str(self._run_id or self._config.get("stats", {}).get("job_id") or "")
            if self._stop_event is not None:
                self._stop_event.set()
            if job_id:
                active_indexes = self._worker_indexes(self._active_worker_states())
                openai_register.mark_worker_states_stopping_for_run(active_indexes, job_id, "register task stopping by user")
            self._config["stats"]["updated_at"] = _now()
            self._save()
            self._append_log(
                "已请求停止注册任务；未开始任务会由运行线程统一取消，运行中的底层请求会在超时后退出",
                "yellow",
            )
            return self.get()

    def reset(self) -> dict:
        with self._lock:
            self._require_idle_locked("resetting stats")
            self._logs = []
            self._config["stats"] = {"success": 0, "usable_success": 0, "fail": 0, "done": 0, "saved": 0, "refresh_failed": 0, "token_acquired_refresh_failed": 0, "running": 0, "threads": self._config["threads"], "elapsed_seconds": 0, "avg_seconds": 0, "success_rate": 0, "failure_reasons": {}, "provider_stats": {}, "domain_stats": {}, "provider_domain_stats": {}, **self._pool_metrics(), "updated_at": _now()}
            with openai_register.stats_lock:
                openai_register.stats.update({"done": 0, "success": 0, "usable_success": 0, "fail": 0, "saved": 0, "refresh_failed": 0, "token_acquired_refresh_failed": 0, "start_time": 0.0})
            self._save()
            return self.get()

    def is_running(self) -> bool:
        with self._lock:
            return bool(self._runner and self._runner.is_alive())

    def reset_outlook_pool(self, scope: str = "all") -> dict:
        scope = str(scope or "all").strip().lower()
        with self._lock:
            self._require_idle_locked("resetting Outlook mailbox pool")
            if scope == "unused":
                removed = self._prune_unused_outlook_pools()
                self._sync_openai_register_config(self._config)
                self._save()
                self._append_log(f"已清空 Outlook 邮箱池未使用邮箱，移除 {removed} 个", "yellow")
                return self.get()
            scope = "failed" if str(scope) == "failed" else "all"
            cleared = mail_provider.reset_outlook_token_pool_state(scope)
            self._append_log(
                f"已重置 Outlook 邮箱池状态（范围={'仅失败/占用' if scope == 'failed' else '全部'}），清除 {cleared} 条记录",
                "yellow",
            )
        return self.get()

    def add_yyds_domain_blacklist(self, domains: list[object]) -> dict:
        with self._lock:
            self._require_idle_locked("changing YYDS domain blacklist")
            added = sum(1 for domain in domains if mail_provider.add_yyds_domain_blacklist(domain))
            return {**mail_provider.yyds_domain_blacklist_state(), "added": added}

    def remove_yyds_domain_blacklist(self, domains: list[object]) -> dict:
        with self._lock:
            self._require_idle_locked("changing YYDS domain blacklist")
            removed = sum(1 for domain in domains if mail_provider.remove_yyds_domain_blacklist(domain))
            return {**mail_provider.yyds_domain_blacklist_state(), "removed": removed}

    def replace_yyds_domain_blacklist(self, domains: list[object]) -> dict:
        with self._lock:
            self._require_idle_locked("changing YYDS domain blacklist")
            items = mail_provider.replace_yyds_domain_blacklist(domains)
            return {**mail_provider.yyds_domain_blacklist_state(), "replaced": len(items)}

    def reset_yyds_domain_blacklist(self) -> dict:
        with self._lock:
            self._require_idle_locked("changing YYDS domain blacklist")
            cleared = mail_provider.reset_yyds_domain_blacklist()
            return {**mail_provider.yyds_domain_blacklist_state(), "cleared": cleared}

    def _append_log(self, text: str, color: str = "") -> None:
        with self._lock:
            self._logs.append({"time": _now(), "text": str(text), "level": str(color or "info")})
            self._logs = self._logs[-300:]

    def _record_source_stats(
        self,
        result: dict,
        provider_stats: dict[str, dict],
        domain_stats: dict[str, dict],
        provider_domain_stats: dict[str, dict],
    ) -> None:
        if not isinstance(result, dict) or result.get("stopped"):
            return
        updated_at = _now()
        provider_name = str(result.get("provider") or "").strip()
        provider_key = result.get("provider_ref") or result.get("provider")
        domain_key = result.get("email_domain") or result.get("source_domain")
        if provider_key:
            _update_source_stats(
                provider_stats,
                provider_key,
                result,
                updated_at=updated_at,
                provider=provider_name,
                provider_ref=provider_key,
            )
        if domain_key:
            _update_source_stats(
                domain_stats,
                domain_key,
                result,
                updated_at=updated_at,
                provider=provider_name,
                provider_ref=provider_key,
                domain=domain_key,
            )
            if provider_key:
                _update_source_stats(
                    provider_domain_stats,
                    _provider_domain_stats_key(provider_key, domain_key),
                    result,
                    updated_at=updated_at,
                    provider=provider_name,
                    provider_ref=provider_key,
                    domain=domain_key,
                )
            self._maybe_auto_blacklist_domain(result, domain_stats)

    def _maybe_auto_blacklist_domain(self, result: dict, domain_stats: dict[str, dict]) -> None:
        if not isinstance(result, dict) or result.get("ok") or result.get("stopped"):
            return
        if str(result.get("provider") or "") != "yyds_mail":
            return
        reason = _failure_reason_key(result.get("reason"))
        if reason not in SOURCE_AUTO_BLACKLIST_REASONS:
            return
        domain = _source_stats_key(result.get("email_domain") or result.get("source_domain"), "")
        if not domain:
            return
        if mail_provider.record_yyds_domain_blacklist(
            domain,
            source="openai_hard_reject",
            reason=reason,
            provider_ref=str(result.get("provider_ref") or ""),
        ):
            self._append_log(
                f"YYDS 域名被 OpenAI 明确拒绝，已自动停用：{domain}（{reason}）",
                "yellow",
            )

    @staticmethod
    def _add_account_log(summary: str, detail: dict) -> None:
        try:
            log_service.add(LOG_TYPE_ACCOUNT, summary, detail)
        except Exception:
            pass

    def _log_auto_refill_decision(
        self,
        *,
        started: bool,
        reason: str,
        current_available: int,
        min_available: int,
        batch_total: int,
        message: str = "",
    ) -> None:
        detail = {
            "trigger": "auto_refill",
            "started": started,
            "reason": reason,
            "current_available": current_available,
            "min_available": min_available,
            "batch_total": batch_total,
        }
        if message:
            detail["message"] = message
        self._add_account_log("自动补号启动" if started else "自动补号跳过", detail)

    def _pool_metrics(self) -> dict:
        items = account_service.list_accounts()
        if not items:
            return {"current_quota": 0, "current_available": 0}
        settings = _register_settings()
        normal = [item for item in items if _image_account_usable(item, settings)]
        return {
            "current_quota": sum(int(item.get("quota") or 0) for item in normal),
            "current_available": len(normal),
        }

    def _target_reached(self, cfg: dict, submitted: int) -> bool:
        mode = str(cfg.get("mode") or "total")
        metrics = self._pool_metrics()
        self._bump(**metrics)
        if mode == "quota":
            reached = metrics["current_quota"] >= int(cfg.get("target_quota") or 1)
            self._append_log(f"检查号池：当前正常账号={metrics['current_available']}，当前剩余额度={metrics['current_quota']}，目标额度={cfg.get('target_quota')}，{'跳过注册' if reached else '继续注册'}", "yellow")
            return reached
        if mode == "available":
            reached = metrics["current_available"] >= int(cfg.get("target_available") or 1)
            self._append_log(f"检查号池：当前正常账号={metrics['current_available']}，目标账号={cfg.get('target_available')}，当前剩余额度={metrics['current_quota']}，{'跳过注册' if reached else '继续注册'}", "yellow")
            return reached
        return submitted >= int(cfg.get("total") or 1)

    def _bump(self, **updates) -> None:
        with self._lock:
            workers = openai_register.get_worker_states()
            updates.setdefault("proxy_pool", register_proxy_pool.state())
            updates.setdefault("workers", workers)
            if "failure_reasons" in updates:
                raw_reasons = updates.get("failure_reasons") if isinstance(updates.get("failure_reasons"), dict) else {}
                updates["failure_reasons"] = {
                    _failure_reason_key(reason): int(count)
                    for reason, count in raw_reasons.items()
                    if _failure_reason_key(reason) and int(count or 0) > 0
                }
            else:
                current_reasons = self._config.get("stats", {}).get("failure_reasons")
                if isinstance(current_reasons, dict):
                    updates["failure_reasons"] = dict(current_reasons)
            if "provider_stats" in updates:
                updates["provider_stats"] = _normalize_source_stats(updates.get("provider_stats"))
            else:
                updates["provider_stats"] = _normalize_source_stats(self._config.get("stats", {}).get("provider_stats"))
            if "domain_stats" in updates:
                updates["domain_stats"] = _normalize_source_stats(updates.get("domain_stats"))
            else:
                updates["domain_stats"] = _normalize_source_stats(self._config.get("stats", {}).get("domain_stats"))
            if "provider_domain_stats" in updates:
                updates["provider_domain_stats"] = _normalize_source_stats(updates.get("provider_domain_stats"))
            else:
                updates["provider_domain_stats"] = _normalize_source_stats(self._config.get("stats", {}).get("provider_domain_stats"))
            self._config["stats"].update(updates)
            stats = self._config["stats"]
            started_at = str(stats.get("started_at") or "")
            if started_at:
                try:
                    elapsed = max(0.0, (datetime.now(timezone.utc) - datetime.fromisoformat(started_at)).total_seconds())
                except Exception:
                    elapsed = 0.0
                done = int(stats.get("done") or 0)
                success = int(stats.get("success") or 0)
                usable_success = int(stats.get("usable_success") or 0)
                fail = int(stats.get("fail") or 0)
                stats["elapsed_seconds"] = round(elapsed, 1)
                stats["avg_seconds"] = round(elapsed / success, 1) if success else 0
                stats["success_rate"] = round(usable_success * 100 / max(1, success + fail), 1)
            self._config["stats"]["updated_at"] = _now()
            self._save()

    def _set_active_futures(self, futures: set) -> None:
        with self._lock:
            self._active_futures = set(futures)

    @staticmethod
    def _parse_iso_timestamp(value: object) -> float:
        raw = str(value or "").strip()
        if not raw:
            return 0.0
        try:
            return datetime.fromisoformat(raw.replace("Z", "+00:00")).timestamp()
        except Exception:
            return 0.0

    def _stalling_worker_states(self, stall_timeout_seconds: int) -> list[dict]:
        if stall_timeout_seconds <= 0:
            return []
        now = time.time()
        stalled: list[dict] = []
        for worker in openai_register.get_worker_states():
            status = str(worker.get("status") or "")
            if status not in openai_register.WORKER_STATE_ACTIVE_STATUSES:
                continue
            updated_at = self._parse_iso_timestamp(worker.get("updated_at"))
            if updated_at and now - updated_at >= stall_timeout_seconds:
                stalled.append(worker)
        return stalled

    @staticmethod
    def _active_worker_states() -> list[dict]:
        return [
            worker
            for worker in openai_register.get_worker_states()
            if str(worker.get("status") or "") in openai_register.WORKER_STATE_ACTIVE_STATUSES
        ]

    @staticmethod
    def _worker_indexes(workers: list[dict]) -> list[int]:
        indexes: list[int] = []
        for worker in workers:
            raw_index = worker.get("index")
            if str(raw_index or "").isdigit():
                indexes.append(int(raw_index))
        return indexes

    @staticmethod
    def _cancel_pending_futures(futures: set) -> tuple[set, int]:
        remaining = set()
        cancelled = 0
        for future in list(futures):
            if future.cancel():
                cancelled += 1
            else:
                remaining.add(future)
        return remaining, cancelled

    def _invalidate_running_workers(
        self,
        *,
        run_id: str,
        stop_event: threading.Event | None,
        futures: set,
        reason: str,
        error: str,
        failed: bool = False,
    ) -> tuple[set, int]:
        openai_register.clear_active_run_id(run_id)
        if stop_event is not None:
            stop_event.set()
        active_workers = self._active_worker_states()
        active_indexes = self._worker_indexes(active_workers)
        if failed:
            openai_register.mark_worker_states_failed_for_run(active_indexes, run_id, reason, error, terminal=False)
        else:
            openai_register.mark_worker_states_stopping_for_run(active_indexes, run_id, error)
        return self._cancel_pending_futures(futures)

    def _run(
        self,
        run_config: dict | None = None,
        trigger: str = "manual",
        stop_event: threading.Event | None = None,
        run_id: str = "",
    ) -> None:
        base_config = dict(run_config or self.get(redact=False))
        threads = int(base_config["threads"])
        submitted, done, success, usable_success, fail = 0, 0, 0, 0, 0
        saved, refresh_failed, token_acquired_refresh_failed = 0, 0, 0
        failure_reasons: dict[str, int] = {}
        provider_stats: dict[str, dict] = {}
        domain_stats: dict[str, dict] = {}
        provider_domain_stats: dict[str, dict] = {}
        stall_logged = False
        shutdown_requested = False
        stopped_failure_reason = ""
        stop_event = stop_event or threading.Event()
        executor = ThreadPoolExecutor(max_workers=threads)
        shutdown_wait = True

        def record_worker_result(result: dict) -> None:
            nonlocal success, usable_success, fail, saved, refresh_failed, token_acquired_refresh_failed
            if result.get("refresh_failed"):
                refresh_failed += 1
            if result.get("token_acquired") and result.get("refresh_failed"):
                token_acquired_refresh_failed += 1
            if result.get("ok"):
                success += 1
                if result.get("usable"):
                    usable_success += 1
                if result.get("saved"):
                    saved += 1
            elif result.get("stopped"):
                if stopped_failure_reason:
                    fail += 1
                    _bump_failure_reason(failure_reasons, stopped_failure_reason)
            else:
                fail += 1
                _bump_failure_reason(failure_reasons, result.get("reason"))
            self._record_source_stats(result, provider_stats, domain_stats, provider_domain_stats)

        try:
            futures = set()
            while True:
                cfg = dict(base_config if trigger == "auto_refill" else self.get(redact=False))
                self._set_active_futures(futures)
                if stop_event and stop_event.is_set():
                    if not shutdown_requested:
                        futures, cancelled_count = self._invalidate_running_workers(
                            run_id=run_id,
                            stop_event=stop_event,
                            futures=futures,
                            reason="register_task_stopped",
                            error="register task stopped by user",
                            failed=False,
                        )
                        done += cancelled_count
                        shutdown_requested = True
                        stopped_failure_reason = ""
                        self._append_log(
                            f"注册任务已停止，已取消未开始任务 {cancelled_count} 个，等待运行中任务完成清理",
                            "yellow",
                        )
                        self._bump(running=len(futures), done=done, success=success, usable_success=usable_success, fail=fail, saved=saved, refresh_failed=refresh_failed, token_acquired_refresh_failed=token_acquired_refresh_failed, failure_reasons=dict(failure_reasons), provider_stats=dict(provider_stats), domain_stats=dict(domain_stats), provider_domain_stats=dict(provider_domain_stats))
                    if not futures:
                        break
                stalled_workers = [] if shutdown_requested else self._stalling_worker_states(int(cfg.get("task_stall_timeout_seconds") or 0))
                if stalled_workers:
                    futures, cancelled_count = self._invalidate_running_workers(
                        run_id=run_id,
                        stop_event=stop_event,
                        futures=futures,
                        reason=REGISTER_STALL_FAILURE_REASON,
                        error="register task stalled",
                        failed=True,
                    )
                    fail += cancelled_count
                    done += cancelled_count
                    _bump_failure_reason(failure_reasons, REGISTER_STALL_FAILURE_REASON, cancelled_count)
                    shutdown_requested = True
                    stopped_failure_reason = REGISTER_STALL_FAILURE_REASON
                    if not stall_logged:
                        worker_refs = ", ".join(
                            f"#{worker.get('index')} {worker.get('status')} {worker.get('step') or worker.get('last_error') or ''}".strip()
                            for worker in stalled_workers[:5]
                        )
                        self._append_log(
                            f"注册任务超过 {int(cfg.get('task_stall_timeout_seconds') or 0)} 秒无进展，已请求强制停止：{worker_refs}；未开始的任务已取消，运行中的底层请求会在超时后退出",
                            "red",
                        )
                        stall_logged = True
                    self._bump(running=len(futures), done=done, success=success, usable_success=usable_success, fail=fail, saved=saved, refresh_failed=refresh_failed, token_acquired_refresh_failed=token_acquired_refresh_failed, failure_reasons=dict(failure_reasons), provider_stats=dict(provider_stats), domain_stats=dict(domain_stats), provider_domain_stats=dict(provider_domain_stats))
                    if not futures:
                        break
                while (
                    self.get()["enabled"]
                    and not (stop_event and stop_event.is_set())
                    and not self._target_reached(cfg, submitted)
                    and len(futures) < threads
                ):
                    submitted += 1
                    futures.add(executor.submit(openai_register.worker, submitted, stop_event, None, cfg.get("task_timeout_seconds"), run_id))
                    self._set_active_futures(futures)
                self._bump(running=len(futures), done=done, success=success, usable_success=usable_success, fail=fail, saved=saved, refresh_failed=refresh_failed, token_acquired_refresh_failed=token_acquired_refresh_failed, failure_reasons=dict(failure_reasons), provider_stats=dict(provider_stats), domain_stats=dict(domain_stats), provider_domain_stats=dict(provider_domain_stats))
                if not futures and (not self.get()["enabled"] or str(cfg.get("mode") or "total") == "total"):
                    break
                if not futures:
                    if stop_event and stop_event.wait(max(1, int(cfg.get("check_interval") or 5))):
                        break
                    if not stop_event:
                        time.sleep(max(1, int(cfg.get("check_interval") or 5)))
                    continue
                finished, futures = wait(futures, timeout=1, return_when=FIRST_COMPLETED)
                if not finished:
                    self._bump(running=len(futures), done=done, success=success, usable_success=usable_success, fail=fail, saved=saved, refresh_failed=refresh_failed, token_acquired_refresh_failed=token_acquired_refresh_failed, failure_reasons=dict(failure_reasons), provider_stats=dict(provider_stats), domain_stats=dict(domain_stats), provider_domain_stats=dict(provider_domain_stats))
                    continue
                for future in finished:
                    done += 1
                    try:
                        result = future.result()
                        record_worker_result(result if isinstance(result, dict) else {"ok": False, "reason": "invalid_worker_result"})
                    except Exception as exc:
                        fail += 1
                        _bump_failure_reason(failure_reasons, exc)
        finally:
            self._set_active_futures(set())
            executor.shutdown(wait=shutdown_wait, cancel_futures=True)
            openai_register.clear_active_run_id(run_id)
        self._bump(running=0, done=done, success=success, usable_success=usable_success, fail=fail, saved=saved, refresh_failed=refresh_failed, token_acquired_refresh_failed=token_acquired_refresh_failed, failure_reasons=dict(failure_reasons), provider_stats=dict(provider_stats), domain_stats=dict(domain_stats), provider_domain_stats=dict(provider_domain_stats), finished_at=_now())
        with self._lock:
            self._config["enabled"] = False
            if self._stop_event is stop_event:
                self._stop_event = None
            if self._run_id == run_id:
                self._run_id = ""
            self._save()
        self._append_log(f"注册任务结束，成功{success}，失败{fail}", "yellow")

    def _run_repair_abnormal(self, stop_event: threading.Event, run_id: str) -> None:
        success, usable_success, fail, done = 0, 0, 0, 0
        failure_reasons: dict[str, int] = {}
        try:
            accounts = account_service.list_accounts()
            candidates = [
                item
                for item in accounts
                if str(item.get("access_token") or "").strip()
                and (
                    str(item.get("status") or "正常") != "正常"
                    or bool(item.get("pending_delete"))
                    or bool(item.get("image_quota_unknown"))
                    or _int_or_default(item.get("quota"), 0) <= 0
                )
            ]
            self._append_log(f"待修复异常/无额度账号 {len(candidates)} 个", "yellow")
            for item in candidates:
                if stop_event.is_set():
                    break
                token = str(item.get("access_token") or "").strip()
                email = str(item.get("email") or token[:8])
                try:
                    result = account_service.refresh_accounts([token], defer_invalid_removal=False)
                    removed = int(result.get("removed_unusable") or 0)
                    errors = result.get("errors") if isinstance(result.get("errors"), list) else []
                    if errors:
                        fail += 1
                        _bump_failure_reason(failure_reasons, "account_refresh_failed")
                        self._append_log(f"{email} 刷新失败，已按状态尝试移除：{errors}", "red")
                    elif removed:
                        fail += 1
                        _bump_failure_reason(failure_reasons, "account_unusable_after_refresh")
                        self._append_log(f"{email} 刷新后仍不可用，已移除", "yellow")
                    else:
                        refreshed_account = account_service.get_account(token)
                        if not _image_account_usable(refreshed_account or {}, _register_settings()):
                            fail += 1
                            if refreshed_account is None:
                                _bump_failure_reason(failure_reasons, "account_unusable_after_refresh")
                                self._append_log(f"{email} 刷新后账号不存在，未恢复", "yellow")
                            else:
                                try:
                                    _delete_account_or_raise(token)
                                    _bump_failure_reason(failure_reasons, "account_unusable_after_refresh")
                                    self._append_log(f"{email} 刷新后仍不可用，已删除", "yellow")
                                except Exception as delete_exc:
                                    _bump_failure_reason(failure_reasons, "account_delete_failed")
                                    self._append_log(f"{email} 刷新后仍不可用，删除失败：{delete_exc}", "red")
                            continue
                        success += 1
                        usable_success += 1
                        self._append_log(f"{email} 刷新恢复正常", "green")
                except Exception as exc:
                    fail += 1
                    _bump_failure_reason(failure_reasons, "repair_account_failed")
                    self._append_log(f"{email} 修复失败：{exc}", "red")
                finally:
                    done += 1
                    self._bump(done=done, success=success, usable_success=usable_success, fail=fail, running=1, failure_reasons=dict(failure_reasons))
        except Exception as exc:
            fail += 1
            done += 1
            _bump_failure_reason(failure_reasons, "repair_abnormal_failed")
            self._append_log(f"异常账号修复任务失败：{exc}", "red")
            self._bump(
                done=done,
                success=success,
                usable_success=usable_success,
                fail=fail,
                running=0,
                failure_reasons=dict(failure_reasons),
                workers=[
                    {
                        "index": 1,
                        "status": "failed",
                        "failure_reason": "repair_abnormal_failed",
                        "last_error": str(exc),
                        "updated_at": _now(),
                    }
                ],
            )
        finally:
            self._bump(running=0, done=done, success=success, usable_success=usable_success, fail=fail, failure_reasons=dict(failure_reasons), finished_at=_now())
            with self._lock:
                self._config["enabled"] = False
                if self._stop_event is stop_event:
                    self._stop_event = None
                if self._run_id == run_id:
                    self._run_id = ""
                self._save()
            self._append_log(f"异常账号修复任务结束，恢复{success}，失败/移除{fail}", "yellow")

    def start_auto_refill_watcher(self, stop_event: threading.Event) -> threading.Thread:
        thread = threading.Thread(
            target=self._auto_refill_loop,
            args=(stop_event,),
            daemon=True,
            name="register-auto-refill",
        )
        thread.start()
        return thread

    def _auto_refill_loop(self, stop_event: threading.Event) -> None:
        while not stop_event.is_set():
            interval = 300
            try:
                cfg = self.get()
                auto_refill = cfg.get("auto_refill") if isinstance(cfg.get("auto_refill"), dict) else {}
                interval = max(10, int(auto_refill.get("check_interval") or 300))
                if auto_refill.get("enabled"):
                    metrics = self._pool_metrics()
                    min_available = max(1, int(auto_refill.get("min_available") or 1))
                    batch_total = max(1, int(auto_refill.get("batch_total") or 1))
                    running = bool(cfg.get("enabled")) or self.is_running()
                    if metrics["current_available"] < min_available and running:
                        self._log_auto_refill_decision(
                            started=False,
                            reason="register_task_running",
                            current_available=metrics["current_available"],
                            min_available=min_available,
                            batch_total=batch_total,
                        )
                    elif metrics["current_available"] < min_available:
                        trigger_log = (
                            f"自动补号触发：当前正常账号={metrics['current_available']}，"
                            f"阈值={min_available}，本轮注册={batch_total}"
                        )
                        self.start_auto_refill(batch_total, trigger_log=trigger_log)
                    else:
                        self._log_auto_refill_decision(
                            started=False,
                            reason="enough_available",
                            current_available=metrics["current_available"],
                            min_available=min_available,
                            batch_total=batch_total,
                        )
            except Exception as exc:
                self._append_log(f"自动补号检查失败：{exc}", "red")
                interval = 60
            if stop_event.wait(interval):
                break


register_service = RegisterService(REGISTER_FILE)
