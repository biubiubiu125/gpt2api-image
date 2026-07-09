from __future__ import annotations

import sys
import os
import tempfile
import types
import unittest
from pathlib import Path


fake_curl = types.ModuleType("curl_cffi")
fake_requests = types.SimpleNamespace(
    Session=lambda *args, **kwargs: types.SimpleNamespace(
        headers={},
        request=lambda *args, **kwargs: None,
        close=lambda: None,
        proxies={},
    ),
    get=lambda *args, **kwargs: None,
    post=lambda *args, **kwargs: None,
)
fake_curl.requests = fake_requests
sys.modules.setdefault("curl_cffi", fake_curl)
sys.modules.setdefault("curl_cffi.requests", fake_requests)

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from fastapi import HTTPException  # noqa: E402

import app as register_executor_app  # noqa: E402
from services.account_service import AccountService  # noqa: E402
from services.register import mail_provider, openai_register  # noqa: E402


class YYDSDomainBlacklistTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.original_blacklist_file = mail_provider.YYDS_DOMAIN_BLACKLIST_FILE
        self.original_success_file = mail_provider.YYDS_DOMAIN_SUCCESS_FILE
        mail_provider.YYDS_DOMAIN_BLACKLIST_FILE = Path(self.temp_dir.name) / "yyds_domain_blacklist.json"
        mail_provider.YYDS_DOMAIN_SUCCESS_FILE = Path(self.temp_dir.name) / "yyds_domain_success.json"
        mail_provider.replace_yyds_domain_blacklist([])

    def tearDown(self) -> None:
        mail_provider.replace_yyds_domain_blacklist([])
        mail_provider.YYDS_DOMAIN_BLACKLIST_FILE = self.original_blacklist_file
        mail_provider.YYDS_DOMAIN_SUCCESS_FILE = self.original_success_file
        self.temp_dir.cleanup()

    def test_register_http_400_blacklists_yyds_source_domain(self) -> None:
        added = mail_provider.mark_yyds_mailbox_error(
            {"provider": "yyds_mail", "address": "user@fallback.example", "source_domain": "@Bad.Example."},
            RuntimeError('user_register_http_400 detail={"error":"unsupported_email"}'),
        )

        self.assertTrue(added)
        self.assertEqual(mail_provider.yyds_domain_blacklist_items(), ["bad.example"])
        self.assertFalse(
            mail_provider.mark_yyds_mailbox_error(
                {"provider": "yyds_mail", "address": "user@other.example", "source_domain": "other.example"},
                RuntimeError("user_register_http_500"),
            )
        )

    def test_mail_code_timeout_does_not_blacklist_yyds_source_domain(self) -> None:
        added = mail_provider.mark_yyds_mailbox_error(
            {"provider": "yyds_mail", "address": "user@slow.example", "source_domain": "slow.example"},
            RuntimeError("等待邮箱验证码超时"),
        )

        self.assertFalse(added)
        self.assertTrue(mail_provider.is_yyds_mail_code_timeout_error(RuntimeError("mail_code_timeout")))
        self.assertFalse(mail_provider.is_yyds_mail_code_timeout_error(RuntimeError("oauth code exchange timeout")))
        self.assertEqual(mail_provider.yyds_domain_blacklist_items(), [])

    def test_generic_openai_http_400_does_not_blacklist_yyds_source_domain(self) -> None:
        for error in [
            RuntimeError('user_register_http_400 detail={"message":"Failed to create account. Please try again."}'),
            RuntimeError('create_account_http_400 detail={"message":"Failed to create account. Please try again."}'),
        ]:
            with self.subTest(error=str(error)):
                added = mail_provider.mark_yyds_mailbox_error(
                    {"provider": "yyds_mail", "address": "user@generic.example", "source_domain": "generic.example"},
                    error,
                )
                self.assertFalse(added)
                self.assertEqual(mail_provider.yyds_domain_blacklist_items(), [])

    def test_runtime_resource_writes_return_409_while_register_active(self) -> None:
        original_service = register_executor_app.register_service
        original_auth_key = os.environ.get("GPT2API_IMAGE_AUTH_KEY")
        original_internal_key = os.environ.get("GPT2API_IMAGE_REGISTER_INTERNAL_KEY")

        class ActiveService:
            def reset_outlook_pool(self, scope="all"):
                raise register_executor_app.RegisterTaskActiveError("register task is running")

            def add_yyds_domain_blacklist(self, domains):
                raise register_executor_app.RegisterTaskActiveError("register task is running")

            def remove_yyds_domain_blacklist(self, domains):
                raise register_executor_app.RegisterTaskActiveError("register task is running")

            def replace_yyds_domain_blacklist(self, domains):
                raise register_executor_app.RegisterTaskActiveError("register task is running")

            def reset_yyds_domain_blacklist(self):
                raise register_executor_app.RegisterTaskActiveError("register task is running")

        try:
            os.environ["GPT2API_IMAGE_AUTH_KEY"] = "test-key"
            os.environ["GPT2API_IMAGE_REGISTER_INTERNAL_KEY"] = "internal-test-key"
            register_executor_app.register_service = ActiveService()
            body = register_executor_app.YYDSDomainBlacklistRequest(domains=["blocked.example"])
            calls = [
                lambda: register_executor_app.reset_outlook_pool(register_executor_app.OutlookPoolResetRequest(scope="failed"), x_register_internal_key="internal-test-key", authorization=""),
                lambda: register_executor_app.add_yyds_domain_blacklist(body, x_register_internal_key="internal-test-key", authorization=""),
                lambda: register_executor_app.remove_yyds_domain_blacklist(body, x_register_internal_key="internal-test-key", authorization=""),
                lambda: register_executor_app.replace_yyds_domain_blacklist(body, x_register_internal_key="internal-test-key", authorization=""),
                lambda: register_executor_app.reset_yyds_domain_blacklist(x_register_internal_key="internal-test-key", authorization=""),
            ]
            for call in calls:
                with self.subTest(call=call):
                    with self.assertRaises(HTTPException) as raised:
                        call()
                    self.assertEqual(raised.exception.status_code, 409)
            self.assertEqual(mail_provider.yyds_domain_blacklist_items(), [])
        finally:
            register_executor_app.register_service = original_service
            if original_auth_key is None:
                os.environ.pop("GPT2API_IMAGE_AUTH_KEY", None)
            else:
                os.environ["GPT2API_IMAGE_AUTH_KEY"] = original_auth_key
            if original_internal_key is None:
                os.environ.pop("GPT2API_IMAGE_REGISTER_INTERNAL_KEY", None)
            else:
                os.environ["GPT2API_IMAGE_REGISTER_INTERNAL_KEY"] = original_internal_key

    def test_internal_auth_does_not_fallback_to_auth_key(self) -> None:
        original_auth_key = os.environ.get("GPT2API_IMAGE_AUTH_KEY")
        original_internal_key = os.environ.get("GPT2API_IMAGE_REGISTER_INTERNAL_KEY")
        try:
            os.environ["GPT2API_IMAGE_AUTH_KEY"] = "test-key"
            os.environ.pop("GPT2API_IMAGE_REGISTER_INTERNAL_KEY", None)

            with self.assertRaises(HTTPException) as raised:
                register_executor_app._require_internal("", "Bearer test-key")
            self.assertEqual(raised.exception.status_code, 401)
            self.assertNotIn("Authorization", AccountService()._headers())

            os.environ["GPT2API_IMAGE_REGISTER_INTERNAL_KEY"] = "internal-test-key"
            with self.assertRaises(HTTPException) as raised:
                register_executor_app._require_internal("", "Bearer test-key")
            self.assertEqual(raised.exception.status_code, 401)

            register_executor_app._require_internal("", "Bearer internal-test-key")
            headers = AccountService()._headers()
            self.assertEqual(headers.get("X-Register-Internal-Key"), "internal-test-key")
            self.assertNotIn("Authorization", headers)
        finally:
            if original_auth_key is None:
                os.environ.pop("GPT2API_IMAGE_AUTH_KEY", None)
            else:
                os.environ["GPT2API_IMAGE_AUTH_KEY"] = original_auth_key
            if original_internal_key is None:
                os.environ.pop("GPT2API_IMAGE_REGISTER_INTERNAL_KEY", None)
            else:
                os.environ["GPT2API_IMAGE_REGISTER_INTERNAL_KEY"] = original_internal_key

    def test_yyds_release_retries_with_api_key_after_mailbox_token_failure(self) -> None:
        provider = object.__new__(mail_provider.YydsMailProvider)
        provider.conf = {}
        calls: list[tuple[str, str, str]] = []

        def fake_delete(path, *, token="", params=None, payload=None):
            calls.append(("DELETE", path, token))
            if token:
                return False, "HTTP 401"
            return True, ""

        provider._delete_mailbox_candidate = fake_delete

        released, reason = provider.release_mailbox({"account_id": "acc-1", "token": "mail-token"})

        self.assertTrue(released, reason)
        self.assertEqual(
            calls,
            [
                ("DELETE", "/accounts/acc-1", "mail-token"),
                ("DELETE", "/accounts/acc-1", ""),
            ],
        )

    def test_register_retries_after_yyds_domain_400_and_logs_release_failure(self) -> None:
        original_create = mail_provider.create_mailbox
        original_release = mail_provider.release_mailbox
        original_wait = openai_register.wait_for_code
        original_step = openai_register.step
        original_heartbeat = openai_register.heartbeat
        original_mail_config = openai_register._mail_config
        released_domains: list[str] = []
        steps: list[tuple[str, str]] = []
        mailboxes = [
            {
                "provider": "yyds_mail",
                "provider_ref": "test",
                "address": "bad@bad.example",
                "domain": "bad.example",
                "source_domain": "bad.example",
                "token": "t1",
                "account_id": "a1",
            },
            {
                "provider": "yyds_mail",
                "provider_ref": "test",
                "address": "ok@good.example",
                "domain": "good.example",
                "source_domain": "good.example",
                "token": "t2",
                "account_id": "a2",
            },
        ]

        def fake_create_mailbox(mail_config, username=None):
            return dict(mailboxes[len(released_domains)])

        def fake_release_mailbox(mailbox, mail_config=None):
            released_domains.append(mailbox.get("source_domain") or mailbox.get("domain"))
            return False, "delete failed"

        try:
            mail_provider.create_mailbox = fake_create_mailbox
            mail_provider.release_mailbox = fake_release_mailbox
            openai_register.wait_for_code = lambda *args, **kwargs: "123456"
            openai_register.step = lambda index, text, color="": steps.append((text, color))
            openai_register.heartbeat = lambda *args, **kwargs: None
            openai_register._mail_config = lambda *args, **kwargs: {"providers": []}

            class FakeRegistrar:
                proxy = None
                deadline = None
                stop_event = None

                def _check_task_control(self):
                    return None

                def _platform_authorize(self, email, index):
                    return None

                def _register_user(self, email, password, index):
                    if email.endswith("@bad.example"):
                        raise RuntimeError('user_register_http_400 detail={"error":"unsupported_email"}')
                    return None

                def _send_otp(self, index):
                    return None

                def _validate_otp(self, code, index):
                    return None

                def _create_account(self, name, birthdate, index):
                    return None

                def _exchange_registered_tokens(self, index):
                    return {"access_token": "access", "refresh_token": "refresh"}

                def _login_and_exchange_tokens(self, email, password, mailbox, index):
                    raise AssertionError("fallback login should not run")

            result = openai_register.PlatformRegistrar.register(FakeRegistrar(), 1)

            self.assertEqual(result["email"], "ok@good.example")
            self.assertEqual(released_domains, ["bad.example"])
            self.assertIn("bad.example", mail_provider.yyds_domain_blacklist_items())
            self.assertTrue(any("delete failed" in text for text, _ in steps), steps)
        finally:
            mail_provider.create_mailbox = original_create
            mail_provider.release_mailbox = original_release
            openai_register.wait_for_code = original_wait
            openai_register.step = original_step
            openai_register.heartbeat = original_heartbeat
            openai_register._mail_config = original_mail_config

    def test_yyds_wait_for_code_scans_recent_messages(self) -> None:
        provider = object.__new__(mail_provider.YydsMailProvider)
        provider.conf = {"wait_timeout": 1, "wait_interval": 0.01}

        def fake_recent_messages(mailbox, limit=10):
            return [
                {
                    "provider": "yyds_mail",
                    "mailbox": mailbox["address"],
                    "message_id": "notice",
                    "subject": "Welcome",
                    "text_content": "hello",
                    "html_content": "",
                },
                {
                    "provider": "yyds_mail",
                    "mailbox": mailbox["address"],
                    "message_id": "code",
                    "subject": "OpenAI Verification code",
                    "text_content": "Verification code: 123456",
                    "html_content": "",
                },
            ]

        provider.fetch_recent_messages = fake_recent_messages

        self.assertEqual(provider.wait_for_code({"address": "user@example.com"}), "123456")

    def test_yyds_wait_for_code_rechecks_message_until_body_has_code(self) -> None:
        provider = object.__new__(mail_provider.YydsMailProvider)
        provider.conf = {"wait_timeout": 1, "wait_interval": 0.01}
        calls = 0

        def fake_recent_messages(mailbox, limit=10):
            nonlocal calls
            calls += 1
            if calls == 1:
                return [
                    {
                        "provider": "yyds_mail",
                        "mailbox": mailbox["address"],
                        "message_id": "same-message",
                        "subject": "OpenAI Verification code",
                        "text_content": "",
                        "html_content": "",
                    }
                ]
            return [
                {
                    "provider": "yyds_mail",
                    "mailbox": mailbox["address"],
                    "message_id": "same-message",
                    "subject": "OpenAI Verification code",
                    "text_content": "Verification code: 123456",
                    "html_content": "",
                }
            ]

        provider.fetch_recent_messages = fake_recent_messages

        self.assertEqual(provider.wait_for_code({"address": "user@example.com"}), "123456")
        self.assertGreaterEqual(calls, 2)

    def test_yyds_success_domain_is_preferred_for_unconfigured_provider(self) -> None:
        self.assertTrue(mail_provider.record_yyds_domain_success("Good.Example", provider_ref="test"))

        provider = mail_provider.YydsMailProvider(
            {"api_key": "AC-test", "provider_ref": "test"},
            {
                "user_agent": "test-agent",
                "request_timeout": 1,
                "wait_timeout": 1,
                "wait_interval": 0.01,
                "proxy": "",
                "deadline": None,
            },
        )
        calls: list[dict] = []

        def fake_request(method, path, token="", params=None, payload=None, expected=(200, 201, 204)):
            calls.append(dict(payload or {}))
            return {"address": "user@good.example", "token": "mail-token"}

        try:
            provider._request = fake_request
            mailbox = provider.create_mailbox("user")
        finally:
            provider.close()

        self.assertEqual(calls[0].get("domain"), "good.example")
        self.assertEqual(mailbox["source_domain"], "good.example")

    def test_register_retries_after_yyds_mail_code_timeout_without_blacklist(self) -> None:
        original_create = mail_provider.create_mailbox
        original_release = mail_provider.release_mailbox
        original_wait = openai_register.wait_for_code
        original_step = openai_register.step
        original_heartbeat = openai_register.heartbeat
        original_mail_config = openai_register._mail_config
        released_domains: list[str] = []
        steps: list[tuple[str, str]] = []
        wait_calls = 0
        mailboxes = [
            {
                "provider": "yyds_mail",
                "provider_ref": "test",
                "address": "slow@slow.example",
                "domain": "slow.example",
                "source_domain": "slow.example",
                "token": "t1",
                "account_id": "a1",
            },
            {
                "provider": "yyds_mail",
                "provider_ref": "test",
                "address": "ok@good.example",
                "domain": "good.example",
                "source_domain": "good.example",
                "token": "t2",
                "account_id": "a2",
            },
        ]

        def fake_create_mailbox(mail_config, username=None):
            return dict(mailboxes[len(released_domains)])

        def fake_release_mailbox(mailbox, mail_config=None):
            released_domains.append(mailbox.get("source_domain") or mailbox.get("domain"))
            return True, ""

        def fake_wait_for_code(*args, **kwargs):
            nonlocal wait_calls
            wait_calls += 1
            return None if wait_calls == 1 else "123456"

        try:
            mail_provider.create_mailbox = fake_create_mailbox
            mail_provider.release_mailbox = fake_release_mailbox
            openai_register.wait_for_code = fake_wait_for_code
            openai_register.step = lambda index, text, color="": steps.append((text, color))
            openai_register.heartbeat = lambda *args, **kwargs: None
            openai_register._mail_config = lambda *args, **kwargs: {"providers": []}

            class FakeRegistrar:
                proxy = None
                deadline = None
                stop_event = None

                def _check_task_control(self):
                    return None

                def _platform_authorize(self, email, index):
                    return None

                def _register_user(self, email, password, index):
                    return None

                def _send_otp(self, index):
                    return None

                def _validate_otp(self, code, index):
                    return None

                def _create_account(self, name, birthdate, index):
                    return None

                def _exchange_registered_tokens(self, index):
                    return {"access_token": "access", "refresh_token": "refresh"}

                def _login_and_exchange_tokens(self, email, password, mailbox, index):
                    raise AssertionError("fallback login should not run")

            result = openai_register.PlatformRegistrar.register(FakeRegistrar(), 1)

            self.assertEqual(result["email"], "ok@good.example")
            self.assertEqual(released_domains, ["slow.example"])
            self.assertEqual(mail_provider.yyds_domain_blacklist_items(), [])
            self.assertTrue(any("收码超时" in text and "黑名单" not in text for text, _ in steps), steps)
        finally:
            mail_provider.create_mailbox = original_create
            mail_provider.release_mailbox = original_release
            openai_register.wait_for_code = original_wait
            openai_register.step = original_step
            openai_register.heartbeat = original_heartbeat
            openai_register._mail_config = original_mail_config

    def test_register_does_not_retry_yyds_mail_code_timeout_after_code_consumed(self) -> None:
        original_create = mail_provider.create_mailbox
        original_release = mail_provider.release_mailbox
        original_wait = openai_register.wait_for_code
        original_step = openai_register.step
        original_heartbeat = openai_register.heartbeat
        original_mail_config = openai_register._mail_config
        released_domains: list[str] = []
        create_calls = 0
        mailbox = {
            "provider": "yyds_mail",
            "provider_ref": "test",
            "address": "used@used.example",
            "domain": "used.example",
            "source_domain": "used.example",
            "token": "t1",
            "account_id": "a1",
        }

        def fake_create_mailbox(mail_config, username=None):
            nonlocal create_calls
            create_calls += 1
            return dict(mailbox)

        def fake_release_mailbox(mailbox, mail_config=None):
            released_domains.append(mailbox.get("source_domain") or mailbox.get("domain"))
            return True, ""

        try:
            mail_provider.create_mailbox = fake_create_mailbox
            mail_provider.release_mailbox = fake_release_mailbox
            openai_register.wait_for_code = lambda *args, **kwargs: "123456"
            openai_register.step = lambda *args, **kwargs: None
            openai_register.heartbeat = lambda *args, **kwargs: None
            openai_register._mail_config = lambda *args, **kwargs: {"providers": []}

            class FakeRegistrar:
                proxy = None
                deadline = None
                stop_event = None

                def _check_task_control(self):
                    return None

                def _platform_authorize(self, email, index):
                    return None

                def _register_user(self, email, password, index):
                    return None

                def _send_otp(self, index):
                    return None

                def _validate_otp(self, code, index):
                    return None

                def _create_account(self, name, birthdate, index):
                    return None

                def _exchange_registered_tokens(self, index):
                    raise RuntimeError("token_exchange_failed")

                def _login_and_exchange_tokens(self, email, password, mailbox, index):
                    raise RuntimeError("mail_code_timeout")

            with self.assertRaisesRegex(RuntimeError, "mail_code_timeout"):
                openai_register.PlatformRegistrar.register(FakeRegistrar(), 1)

            self.assertEqual(create_calls, 1)
            self.assertEqual(released_domains, ["used.example"])
            self.assertEqual(mail_provider.yyds_domain_blacklist_items(), [])
        finally:
            mail_provider.create_mailbox = original_create
            mail_provider.release_mailbox = original_release
            openai_register.wait_for_code = original_wait
            openai_register.step = original_step
            openai_register.heartbeat = original_heartbeat
            openai_register._mail_config = original_mail_config

    def test_register_generic_create_account_400_after_code_consumed_does_not_blacklist(self) -> None:
        original_create = mail_provider.create_mailbox
        original_release = mail_provider.release_mailbox
        original_wait = openai_register.wait_for_code
        original_step = openai_register.step
        original_heartbeat = openai_register.heartbeat
        original_mail_config = openai_register._mail_config
        released_domains: list[str] = []
        create_calls = 0
        mailbox = {
            "provider": "yyds_mail",
            "provider_ref": "test",
            "address": "ok@generic.example",
            "domain": "generic.example",
            "source_domain": "generic.example",
            "token": "t1",
            "account_id": "a1",
        }

        def fake_create_mailbox(mail_config, username=None):
            nonlocal create_calls
            create_calls += 1
            return dict(mailbox)

        def fake_release_mailbox(mailbox, mail_config=None):
            released_domains.append(mailbox.get("source_domain") or mailbox.get("domain"))
            return True, ""

        try:
            mail_provider.create_mailbox = fake_create_mailbox
            mail_provider.release_mailbox = fake_release_mailbox
            openai_register.wait_for_code = lambda *args, **kwargs: "123456"
            openai_register.step = lambda *args, **kwargs: None
            openai_register.heartbeat = lambda *args, **kwargs: None
            openai_register._mail_config = lambda *args, **kwargs: {"providers": []}

            class FakeRegistrar:
                proxy = None
                deadline = None
                stop_event = None

                def _check_task_control(self):
                    return None

                def _platform_authorize(self, email, index):
                    return None

                def _register_user(self, email, password, index):
                    return None

                def _send_otp(self, index):
                    return None

                def _validate_otp(self, code, index):
                    return None

                def _create_account(self, name, birthdate, index):
                    raise RuntimeError('create_account_http_400 detail={"message":"Failed to create account. Please try again."}')

                def _exchange_registered_tokens(self, index):
                    raise AssertionError("token exchange should not run")

                def _login_and_exchange_tokens(self, email, password, mailbox, index):
                    raise AssertionError("fallback login should not run")

            with self.assertRaisesRegex(RuntimeError, "create_account_http_400"):
                openai_register.PlatformRegistrar.register(FakeRegistrar(), 1)

            self.assertEqual(create_calls, 1)
            self.assertEqual(released_domains, ["generic.example"])
            self.assertEqual(mail_provider.yyds_domain_blacklist_items(), [])
        finally:
            mail_provider.create_mailbox = original_create
            mail_provider.release_mailbox = original_release
            openai_register.wait_for_code = original_wait
            openai_register.step = original_step
            openai_register.heartbeat = original_heartbeat
            openai_register._mail_config = original_mail_config


if __name__ == "__main__":
    unittest.main()
