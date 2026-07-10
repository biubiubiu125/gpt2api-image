from __future__ import annotations

import inspect
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from services import register_service  # noqa: E402
from services.register_service import RegisterService, RegisterTaskActiveError  # noqa: E402
from services.register import openai_register  # noqa: E402


class RegisterStatsTest(unittest.TestCase):
    class _AliveRunner:
        @staticmethod
        def is_alive() -> bool:
            return True

    def test_success_rate_uses_usable_success(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")
            service._config["stats"].update(
                {
                    "started_at": "2026-01-01T00:00:00+00:00",
                    "success": 2,
                    "usable_success": 1,
                    "fail": 1,
                }
            )

            service._bump()

            self.assertEqual(service._config["stats"]["success_rate"], 33.3)

    def test_failure_reasons_are_not_overwritten_by_worker_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")
            service._config["stats"].update(
                {
                    "started_at": "2026-01-01T00:00:00+00:00",
                    "failure_reasons": {"account_refresh_failed": 2},
                }
            )

            service._bump()

            self.assertEqual(service._config["stats"]["failure_reasons"], {"account_refresh_failed": 2})

    def test_get_exposes_stopping_runtime_state_when_runner_alive(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")
            service._runner = self._AliveRunner()
            service._stop_event = threading.Event()
            service._stop_event.set()
            service._config["enabled"] = False
            service._config["stats"]["running"] = 0

            snapshot = service.get()

        self.assertEqual(snapshot["lifecycle"], "stopping")
        self.assertTrue(snapshot["is_running"])
        self.assertTrue(snapshot["is_stopping"])
        self.assertEqual(snapshot["stats"]["lifecycle"], "stopping")
        self.assertTrue(snapshot["stats"]["is_running"])
        self.assertTrue(snapshot["stats"]["is_stopping"])

    def test_update_rejects_while_runner_alive(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")
            service._runner = self._AliveRunner()

            with self.assertRaises(RegisterTaskActiveError):
                service.update({"proxy": "http://127.0.0.1:7890"})

    def test_reset_rejects_while_runner_alive(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")
            service._runner = self._AliveRunner()

            with self.assertRaises(RegisterTaskActiveError):
                service.reset()

    def test_reset_outlook_pool_rejects_while_runner_alive(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")
            service._runner = self._AliveRunner()

            with self.assertRaises(RegisterTaskActiveError):
                service.reset_outlook_pool("failed")

    def test_update_ignores_runtime_fields_when_idle(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            service = RegisterService(Path(temp_dir) / "register.json")

            snapshot = service.update({"enabled": True, "stats": {"success": 9}, "lifecycle": "running"})

        self.assertFalse(snapshot["enabled"])
        self.assertEqual(snapshot["stats"]["success"], 0)
        self.assertEqual(snapshot["lifecycle"], "idle")

    def test_delete_saved_account_requires_persisted_removal(self) -> None:
        deleted: list[list[str]] = []
        original_delete_accounts = openai_register.account_service.delete_accounts
        original_get_account = openai_register.account_service.get_account
        try:
            openai_register.account_service.delete_accounts = lambda tokens: deleted.append(tokens) or {"removed": len(tokens)}
            openai_register.account_service.get_account = lambda token: None

            openai_register._delete_saved_account_or_raise("tok")
        finally:
            openai_register.account_service.delete_accounts = original_delete_accounts
            openai_register.account_service.get_account = original_get_account

        self.assertEqual(deleted, [["tok"]])

    def test_delete_saved_account_requires_account_absent(self) -> None:
        original_delete_accounts = openai_register.account_service.delete_accounts
        original_get_account = openai_register.account_service.get_account
        try:
            openai_register.account_service.delete_accounts = lambda tokens: {"removed": 1}
            openai_register.account_service.get_account = lambda token: {"access_token": token}

            with self.assertRaises(openai_register.RegisteredAccountValidationError):
                openai_register._delete_saved_account_or_raise("tok")
        finally:
            openai_register.account_service.delete_accounts = original_delete_accounts
            openai_register.account_service.get_account = original_get_account

    def test_refresh_failed_validation_keeps_refresh_flag_when_cleanup_fails(self) -> None:
        original_delete_accounts = openai_register.account_service.delete_accounts
        original_get_account = openai_register.account_service.get_account
        try:
            openai_register.account_service.delete_accounts = lambda tokens: {"removed": 0}
            openai_register.account_service.get_account = lambda token: {"access_token": token}

            err, cleaned = openai_register._refresh_failed_validation_error("tok", "refresh failed")
        finally:
            openai_register.account_service.delete_accounts = original_delete_accounts
            openai_register.account_service.get_account = original_get_account

        self.assertFalse(cleaned)
        self.assertTrue(getattr(err, "refresh_failed", False))
        self.assertEqual(getattr(err, "failure_reason", ""), "account_delete_failed")
        self.assertIn("registered_account_refresh_failed", str(err))
        self.assertIn("registered_account_delete_failed", str(err))

    def test_worker_stop_reports_delete_failure_after_account_saved(self) -> None:
        deleted: list[list[str]] = []
        stop_event = threading.Event()
        original_registrar = openai_register.PlatformRegistrar
        original_add_account_items = openai_register.account_service.add_account_items
        original_delete_accounts = openai_register.account_service.delete_accounts
        original_get_account = openai_register.account_service.get_account
        try:
            class FakeRegistrar:
                def __init__(self, *args, **kwargs) -> None:
                    pass

                def register(self, index: int) -> dict:
                    return {"access_token": "tok", "refresh_token": "refresh", "id_token": "id", "email": "user@example.com"}

                def close(self) -> None:
                    pass

            def add_account_items(items: list[dict]) -> dict:
                stop_event.set()
                return {"added": len(items)}

            openai_register.PlatformRegistrar = FakeRegistrar
            openai_register.account_service.add_account_items = add_account_items
            openai_register.account_service.delete_accounts = lambda tokens: deleted.append(tokens) or {"removed": 0}
            openai_register.account_service.get_account = lambda token: {"access_token": token}

            result = openai_register.worker(
                1,
                stop_event=stop_event,
                proxy_selection=openai_register.RegisterProxySelection(source="direct", source_label="直连"),
                task_timeout_seconds=30,
            )
        finally:
            openai_register.PlatformRegistrar = original_registrar
            openai_register.account_service.add_account_items = original_add_account_items
            openai_register.account_service.delete_accounts = original_delete_accounts
            openai_register.account_service.get_account = original_get_account

        self.assertFalse(result.get("ok"))
        self.assertFalse(result.get("stopped", False))
        self.assertEqual(result.get("reason"), "account_delete_failed")
        self.assertEqual(deleted, [["tok"]])

    def test_run_stop_drains_running_worker_cleanup_failure(self) -> None:
        started = threading.Event()
        original_worker = openai_register.worker
        original_list_accounts = register_service.account_service.list_accounts
        try:
            def fake_worker(index: int, stop_event=None, proxy_selection=None, task_timeout_seconds=None, run_id: str = "") -> dict:
                started.set()
                if stop_event is not None:
                    stop_event.wait(2)
                time.sleep(1.2)
                return {"ok": False, "index": index, "error": "delete failed", "reason": "account_delete_failed"}

            openai_register.worker = fake_worker
            register_service.account_service.list_accounts = lambda: []
            with tempfile.TemporaryDirectory() as temp_dir:
                service = RegisterService(Path(temp_dir) / "register.json")
                service._config["enabled"] = True
                service._config["total"] = 1
                service._config["threads"] = 1
                service._config["stats"].update(
                    {
                        "job_id": "run-id",
                        "started_at": "2026-01-01T00:00:00+00:00",
                        "success": 0,
                        "usable_success": 0,
                        "fail": 0,
                        "done": 0,
                        "failure_reasons": {},
                    }
                )
                stop_event = threading.Event()
                runner = threading.Thread(target=service._run, args=(dict(service._config), "manual", stop_event, "run-id"))
                runner.start()
                self.assertTrue(started.wait(2))
                stop_event.set()
                runner.join(5)
                self.assertFalse(runner.is_alive())
                stats = dict(service._config["stats"])
        finally:
            openai_register.worker = original_worker
            register_service.account_service.list_accounts = original_list_accounts

        self.assertEqual(stats["done"], 1)
        self.assertEqual(stats["success"], 0)
        self.assertEqual(stats["usable_success"], 0)
        self.assertEqual(stats["fail"], 1)
        self.assertEqual(stats["failure_reasons"], {"account_delete_failed": 1})

    def test_repair_uses_immediate_invalid_removal(self) -> None:
        refresh_calls: list[tuple[list[str], bool]] = []
        original_list_accounts = register_service.account_service.list_accounts
        original_refresh_accounts = register_service.account_service.refresh_accounts
        try:
            register_service.account_service.list_accounts = lambda: [
                {
                    "access_token": "tok",
                    "email": "user@example.com",
                    "status": "正常",
                    "image_quota_unknown": True,
                    "quota": 0,
                }
            ]
            register_service.account_service.refresh_accounts = (
                lambda tokens, defer_invalid_removal=False: refresh_calls.append((tokens, defer_invalid_removal))
                or {"errors": [{"token": tokens[0], "error": "refresh failed"}], "removed_unusable": 0}
            )

            with tempfile.TemporaryDirectory() as temp_dir:
                service = RegisterService(Path(temp_dir) / "register.json")
                service._run_repair_abnormal(__import__("threading").Event(), "run-id")
        finally:
            register_service.account_service.list_accounts = original_list_accounts
            register_service.account_service.refresh_accounts = original_refresh_accounts

        self.assertEqual(refresh_calls, [(["tok"], False)])

    def test_repair_deletes_read_back_unusable_account(self) -> None:
        deleted: list[list[str]] = []
        state = {
            "account": {
                "access_token": "tok",
                "email": "user@example.com",
                "status": "正常",
                "image_quota_unknown": True,
                "quota": 0,
            }
        }
        original_list_accounts = register_service.account_service.list_accounts
        original_refresh_accounts = register_service.account_service.refresh_accounts
        original_get_account = register_service.account_service.get_account
        original_delete_accounts = register_service.account_service.delete_accounts
        try:
            register_service.account_service.list_accounts = lambda: [state["account"]]
            register_service.account_service.refresh_accounts = (
                lambda tokens, defer_invalid_removal=False: {"errors": [], "removed_unusable": 0}
            )
            register_service.account_service.get_account = lambda token: state["account"]
            register_service.account_service.delete_accounts = (
                lambda tokens: deleted.append(tokens) or state.update(account=None) or {"removed": len(tokens)}
            )

            with tempfile.TemporaryDirectory() as temp_dir:
                service = RegisterService(Path(temp_dir) / "register.json")
                service._run_repair_abnormal(__import__("threading").Event(), "run-id")
                stats = dict(service._config["stats"])
        finally:
            register_service.account_service.list_accounts = original_list_accounts
            register_service.account_service.refresh_accounts = original_refresh_accounts
            register_service.account_service.get_account = original_get_account
            register_service.account_service.delete_accounts = original_delete_accounts

        self.assertEqual(stats["success"], 0)
        self.assertEqual(stats["usable_success"], 0)
        self.assertEqual(stats["fail"], 1)
        self.assertEqual(stats["failure_reasons"], {"account_unusable_after_refresh": 1})
        self.assertEqual(deleted, [["tok"]])

    def test_repair_ignores_global_cleanup_count_for_current_account(self) -> None:
        original_list_accounts = register_service.account_service.list_accounts
        original_refresh_accounts = register_service.account_service.refresh_accounts
        original_get_account = register_service.account_service.get_account
        try:
            register_service.account_service.list_accounts = lambda: [
                {
                    "access_token": "tok",
                    "email": "user@example.com",
                    "status": "正常",
                    "image_quota_unknown": True,
                    "quota": 0,
                }
            ]
            register_service.account_service.refresh_accounts = (
                lambda tokens, defer_invalid_removal=False: {"errors": [], "removed_unusable": 0, "cleanup_removed": 1}
            )
            register_service.account_service.get_account = lambda token: {
                "access_token": token,
                "email": "user@example.com",
                "status": "正常",
                "image_quota_unknown": False,
                "quota": 10,
            }

            with tempfile.TemporaryDirectory() as temp_dir:
                service = RegisterService(Path(temp_dir) / "register.json")
                service._run_repair_abnormal(__import__("threading").Event(), "run-id")
                stats = dict(service._config["stats"])
        finally:
            register_service.account_service.list_accounts = original_list_accounts
            register_service.account_service.refresh_accounts = original_refresh_accounts
            register_service.account_service.get_account = original_get_account

        self.assertEqual(stats["success"], 1)
        self.assertEqual(stats["usable_success"], 1)
        self.assertEqual(stats["fail"], 0)
        self.assertEqual(stats["failure_reasons"], {})

    def test_repair_delete_requires_persisted_removal(self) -> None:
        deleted: list[list[str]] = []
        original_list_accounts = register_service.account_service.list_accounts
        original_refresh_accounts = register_service.account_service.refresh_accounts
        original_get_account = register_service.account_service.get_account
        original_delete_accounts = register_service.account_service.delete_accounts
        try:
            register_service.account_service.list_accounts = lambda: [
                {
                    "access_token": "tok",
                    "email": "user@example.com",
                    "status": "正常",
                    "image_quota_unknown": True,
                    "quota": 0,
                }
            ]
            register_service.account_service.refresh_accounts = (
                lambda tokens, defer_invalid_removal=False: {"errors": [], "removed_unusable": 0}
            )
            register_service.account_service.get_account = lambda token: {
                "access_token": token,
                "email": "user@example.com",
                "status": "正常",
                "image_quota_unknown": True,
                "quota": 0,
            }
            register_service.account_service.delete_accounts = (
                lambda tokens: deleted.append(tokens) or {"removed": 0}
            )

            with tempfile.TemporaryDirectory() as temp_dir:
                service = RegisterService(Path(temp_dir) / "register.json")
                service._run_repair_abnormal(__import__("threading").Event(), "run-id")
                stats = dict(service._config["stats"])
        finally:
            register_service.account_service.list_accounts = original_list_accounts
            register_service.account_service.refresh_accounts = original_refresh_accounts
            register_service.account_service.get_account = original_get_account
            register_service.account_service.delete_accounts = original_delete_accounts

        self.assertEqual(stats["success"], 0)
        self.assertEqual(stats["usable_success"], 0)
        self.assertEqual(stats["fail"], 1)
        self.assertEqual(stats["failure_reasons"], {"account_delete_failed": 1})
        self.assertEqual(deleted, [["tok"]])

    def test_pool_metrics_fetches_register_settings_once(self) -> None:
        settings_calls = 0
        original_list_accounts = register_service.account_service.list_accounts
        original_get_settings = register_service.account_service.get_settings
        try:
            register_service.account_service.list_accounts = lambda: [
                {"access_token": "tok-1", "status": "正常", "quota": 1},
                {"access_token": "tok-2", "status": "正常", "quota": 2},
            ]

            def fake_get_settings() -> dict:
                nonlocal settings_calls
                settings_calls += 1
                return {
                    "delete_403_consecutive": 2,
                    "delete_timeout_consecutive": 2,
                    "auto_refresh_delete_failed_accounts": True,
                }

            register_service.account_service.get_settings = fake_get_settings
            with tempfile.TemporaryDirectory() as temp_dir:
                service = RegisterService(Path(temp_dir) / "register.json")
                metrics = service._pool_metrics()
        finally:
            register_service.account_service.list_accounts = original_list_accounts
            register_service.account_service.get_settings = original_get_settings

        self.assertEqual(metrics, {"current_quota": 3, "current_available": 2})
        self.assertEqual(settings_calls, 1)

    def test_image_account_usable_honors_refresh_failed_switch(self) -> None:
        account = {
            "access_token": "tok",
            "status": "正常",
            "quota": 1,
            "last_refresh_error": "refresh failed",
        }

        self.assertFalse(register_service._image_account_usable(account, {"auto_refresh_delete_failed_accounts": True}))
        self.assertTrue(register_service._image_account_usable(account, {"auto_refresh_delete_failed_accounts": False}))

    def test_image_account_usable_treats_known_zero_upload_quota_as_unusable(self) -> None:
        account = {
            "access_token": "tok",
            "status": "正常",
            "quota": 1,
            "upload_quota": 0,
        }

        self.assertFalse(register_service._image_account_usable(account, {}))


    def test_register_proxy_connect_failure_has_dedicated_reason(self) -> None:
        err = "Failed to perform, curl: (56) CONNECT tunnel failed, response 0"

        self.assertTrue(openai_register.is_register_proxy_connect_error(err))
        self.assertEqual(openai_register.classify_register_failure(err), "register_proxy_connect_failed")

    def test_create_session_uses_browser_fingerprint_proxy_and_verify_false(self) -> None:
        captured: dict = {}
        original_session = openai_register.requests.Session

        class FakeSession:
            def __init__(self, **kwargs) -> None:
                captured.update(kwargs)
                self.headers = {}

        try:
            openai_register.requests.Session = FakeSession
            fp = openai_register._complete_browser_fingerprint({
                "impersonate": "chrome136",
                "major": "136",
                "full_version": "136.0.0.0",
            })
            session = openai_register.create_session("http://proxy.example:8080", fp)
        finally:
            openai_register.requests.Session = original_session

        self.assertEqual(captured["proxy"], "http://proxy.example:8080")
        self.assertEqual(captured["impersonate"], "chrome136")
        self.assertFalse(captured["verify"])
        self.assertEqual(session.headers["user-agent"], fp["user_agent"])

    def test_platform_authorize_rebuilds_session_once_on_proxy_connect_failure(self) -> None:
        calls = {"count": 0, "reset": 0}

        class FakeResponse:
            status_code = 200
            url = "https://auth.openai.com/create-account"

            def json(self) -> dict:
                return {}

        registrar = openai_register.PlatformRegistrar("http://proxy.example:8080")
        registrar._sentinel_user_agent = lambda session=None: registrar.fingerprint["user_agent"]  # type: ignore[method-assign]
        original_request = registrar._request
        original_reset = registrar._reset_session

        def fake_request(session, method, url, **kwargs):
            calls["count"] += 1
            if calls["count"] == 1:
                return None, "Failed to perform, curl: (56) CONNECT tunnel failed, response 0"
            return FakeResponse(), ""

        def fake_reset(*, rotate_fingerprint: bool = False):
            calls["reset"] += 1

        try:
            registrar._request = fake_request  # type: ignore[method-assign]
            registrar._reset_session = fake_reset  # type: ignore[method-assign]
            registrar._platform_authorize("user@example.com", 1)
        finally:
            registrar._request = original_request  # type: ignore[method-assign]
            registrar._reset_session = original_reset  # type: ignore[method-assign]
            registrar.close()

        self.assertEqual(calls, {"count": 2, "reset": 1})


    def test_build_sentinel_token_uses_session_user_agent(self) -> None:
        captured: dict = {}
        original_builder = openai_register._build_sentinel_token_tuple
        fp = openai_register._complete_browser_fingerprint({
            "impersonate": "chrome136",
            "major": "136",
            "full_version": "136.0.0.0",
        })
        session = openai_register.create_session("", fp)

        def fake_builder(session_arg, device_id, flow, **kwargs):
            captured.update(kwargs)
            return "sentinel-token", ""

        try:
            openai_register._build_sentinel_token_tuple = fake_builder
            token = openai_register.build_sentinel_token(session, "device-id", "authorize_continue")
        finally:
            openai_register._build_sentinel_token_tuple = original_builder
            session.close()

        self.assertEqual(token, "sentinel-token")
        self.assertEqual(captured["user_agent"], fp["user_agent"])
        self.assertIn('"136"', captured["sec_ch_ua"])
        self.assertIn('"136.0.0.0"', captured["sec_ch_ua_full_version_list"])

    def test_login_fallback_session_uses_registrar_fingerprint(self) -> None:
        source = inspect.getsource(openai_register.PlatformRegistrar._login_and_exchange_tokens)

        self.assertIn("create_session(self.proxy, self.fingerprint)", source)
        self.assertNotIn("login_session = create_session(self.proxy)\n", source)

    def test_mailbox_retry_rebuilds_session_with_registrar_fingerprint(self) -> None:
        source = inspect.getsource(openai_register.PlatformRegistrar.register)

        self.assertIn("reset_session = getattr(self, \"_reset_session\", None)", source)
        self.assertIn("create_session(self.proxy, getattr(self, \"fingerprint\", None))", source)
        self.assertNotIn("self.session = create_session(self.proxy)\n", source)


if __name__ == "__main__":
    unittest.main()
