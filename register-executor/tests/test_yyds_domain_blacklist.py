from __future__ import annotations

import sys
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

from services.register import mail_provider, openai_register  # noqa: E402


class YYDSDomainBlacklistTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.original_blacklist_file = mail_provider.YYDS_DOMAIN_BLACKLIST_FILE
        mail_provider.YYDS_DOMAIN_BLACKLIST_FILE = Path(self.temp_dir.name) / "yyds_domain_blacklist.json"
        mail_provider.replace_yyds_domain_blacklist([])

    def tearDown(self) -> None:
        mail_provider.replace_yyds_domain_blacklist([])
        mail_provider.YYDS_DOMAIN_BLACKLIST_FILE = self.original_blacklist_file
        self.temp_dir.cleanup()

    def test_register_http_400_blacklists_yyds_source_domain(self) -> None:
        added = mail_provider.mark_yyds_mailbox_error(
            {"provider": "yyds_mail", "address": "user@fallback.example", "source_domain": "@Bad.Example."},
            RuntimeError("user_register_http_400"),
        )

        self.assertTrue(added)
        self.assertEqual(mail_provider.yyds_domain_blacklist_items(), ["bad.example"])
        self.assertFalse(
            mail_provider.mark_yyds_mailbox_error(
                {"provider": "yyds_mail", "address": "user@other.example", "source_domain": "other.example"},
                RuntimeError("user_register_http_500"),
            )
        )

    def test_yyds_release_retries_with_api_key_after_mailbox_token_failure(self) -> None:
        provider = object.__new__(mail_provider.YydsMailProvider)
        calls: list[tuple[str, str, str]] = []

        def fake_request(method, path, token="", params=None, payload=None, expected=(200, 201, 204)):
            calls.append((method, path, token))
            if token:
                raise RuntimeError("HTTP 401")
            return {}

        provider._request = fake_request

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
            openai_register._mail_config = lambda proxy, deadline: {"providers": []}

            class FakeRegistrar:
                proxy = None
                deadline = None

                def _check_task_control(self):
                    return None

                def _platform_authorize(self, email, index):
                    return None

                def _register_user(self, email, password, index):
                    if email.endswith("@bad.example"):
                        raise RuntimeError("user_register_http_400")
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


if __name__ == "__main__":
    unittest.main()
