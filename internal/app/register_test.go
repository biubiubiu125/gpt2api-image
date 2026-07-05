package app

import "testing"

func TestRedactRegisterSecretsMasksProviderSecrets(t *testing.T) {
	cfg := defaultRegisterConfig()
	cfg.FixedPassword = "fixed-password"
	cfg.Mail.Providers = []map[string]any{
		{
			"type":    "tempmail_lol",
			"api_key": "provider-key",
			"domain":  []any{"example.com"},
		},
		{
			"type":           "cloudmail",
			"admin_email":    "admin@example.com",
			"admin_password": "admin-password",
		},
		{
			"type":      "outlook_token",
			"mailboxes": "user@example.com----mail-password----client-id----refresh-token",
		},
	}

	got := redactRegisterSecrets(cfg)
	if got.FixedPassword != registerSecretPlaceholder {
		t.Fatalf("fixed_password = %q, want placeholder", got.FixedPassword)
	}
	if got.Mail.Providers[0]["api_key"] != registerSecretPlaceholder {
		t.Fatalf("api_key was not redacted: %#v", got.Mail.Providers[0]["api_key"])
	}
	if got.Mail.Providers[1]["admin_password"] != registerSecretPlaceholder {
		t.Fatalf("admin_password was not redacted: %#v", got.Mail.Providers[1]["admin_password"])
	}
	if got.Mail.Providers[1]["admin_email"] != "admin@example.com" {
		t.Fatalf("admin_email should not be redacted: %#v", got.Mail.Providers[1]["admin_email"])
	}
	if got.Mail.Providers[2]["mailboxes"] != "" {
		t.Fatalf("outlook mailboxes should be hidden: %#v", got.Mail.Providers[2]["mailboxes"])
	}
	if got.Mail.Providers[2]["mailboxes_count"] != 1 {
		t.Fatalf("mailboxes_count = %#v, want 1", got.Mail.Providers[2]["mailboxes_count"])
	}
}

func TestApplyRegisterUpdatesPreservesRedactedSecrets(t *testing.T) {
	cfg := defaultRegisterConfig()
	cfg.FixedPassword = "old-fixed"
	cfg.Mail.Providers = []map[string]any{
		{
			"type":        "tempmail_lol",
			"provider_id": "provider-1",
			"api_key":     "old-key",
		},
		{
			"type":        "outlook_token",
			"provider_id": "provider-2",
			"mailboxes":   "user@example.com----mail-password----client-id----refresh-token",
		},
	}

	applyRegisterUpdates(&cfg, map[string]any{
		"fixed_password": registerSecretPlaceholder,
		"mail": map[string]any{
			"providers": []map[string]any{
				{
					"type":        "tempmail_lol",
					"provider_id": "provider-1",
					"api_key":     registerSecretPlaceholder,
				},
				{
					"type":        "outlook_token",
					"provider_id": "provider-2",
					"mailboxes":   "",
				},
			},
		},
	})

	if cfg.FixedPassword != "old-fixed" {
		t.Fatalf("fixed_password = %q, want old-fixed", cfg.FixedPassword)
	}
	if cfg.Mail.Providers[0]["api_key"] != "old-key" {
		t.Fatalf("api_key = %#v, want old-key", cfg.Mail.Providers[0]["api_key"])
	}
	if cfg.Mail.Providers[1]["mailboxes"] != "user@example.com----mail-password----client-id----refresh-token" {
		t.Fatalf("outlook mailboxes were not preserved: %#v", cfg.Mail.Providers[1]["mailboxes"])
	}

	applyRegisterUpdates(&cfg, map[string]any{
		"fixed_password": "",
		"mail": map[string]any{
			"providers": []map[string]any{
				{
					"type":        "tempmail_lol",
					"provider_id": "provider-1",
					"api_key":     "",
				},
			},
		},
	})

	if cfg.FixedPassword != "" {
		t.Fatalf("fixed_password = %q, want cleared", cfg.FixedPassword)
	}
	if cfg.Mail.Providers[0]["api_key"] != "" {
		t.Fatalf("api_key = %#v, want cleared", cfg.Mail.Providers[0]["api_key"])
	}
}
