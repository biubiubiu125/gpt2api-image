package app

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"config": s.configMap(false)})
	case http.MethodPost:
		var body map[string]any
		if !readBody(w, r, &body) {
			return
		}
		for k, v := range body {
			if isLegacyConfigKey(k) {
				continue
			}
			s.cfg.Extra[k] = v
		}
		delete(s.cfg.Extra, "backup")
		delete(s.cfg.Extra, "backup_state")
		delete(s.cfg.Extra, "cleanup_protect_gallery")
		if v, ok := body["proxy"]; ok {
			s.cfg.Proxy = strAny(v, "")
		}
		if v, ok := body["base_url"]; ok {
			s.cfg.BaseURL = strings.TrimRight(strAny(v, ""), "/")
		}
		if v, ok := body["global_system_prompt"]; ok {
			s.cfg.GlobalSystemPrompt = strAny(v, "")
		}
		if v, ok := body["refresh_account_interval_minute"]; ok {
			s.cfg.RefreshAccountIntervalMinute = intAny(v, s.cfg.RefreshAccountIntervalMinute)
		}
		if v, ok := body["image_retention_days"]; ok {
			s.cfg.ImageRetentionDays = intAny(v, s.cfg.ImageRetentionDays)
		}
		if v, ok := body["image_poll_timeout_secs"]; ok {
			s.cfg.ImagePollTimeoutSecs = intAny(v, s.cfg.ImagePollTimeoutSecs)
		}
		if v, ok := body["image_poll_interval_secs"]; ok {
			s.cfg.ImagePollIntervalSecs = intAny(v, s.cfg.ImagePollIntervalSecs)
		}
		if v, ok := body["image_poll_initial_wait_secs"]; ok {
			s.cfg.ImagePollInitialWaitSecs = intAny(v, s.cfg.ImagePollInitialWaitSecs)
		}
		if v, ok := body["image_task_timeout_secs"]; ok {
			s.cfg.ImageTaskTimeoutSecs = intAny(v, s.cfg.ImageTaskTimeoutSecs)
			if s.cfg.ImageTaskTimeoutSecs < minImageTaskTimeoutSecs {
				s.cfg.ImageTaskTimeoutSecs = minImageTaskTimeoutSecs
			}
		}
		if v, ok := body["image_task_claim_ttl_secs"]; ok {
			s.cfg.ImageTaskClaimTTLSecs = intAny(v, s.cfg.ImageTaskClaimTTLSecs)
			if s.cfg.ImageTaskClaimTTLSecs < minImageTaskClaimTTLSecs {
				s.cfg.ImageTaskClaimTTLSecs = minImageTaskClaimTTLSecs
			}
		}
		if v, ok := body["image_worker_poll_interval_secs"]; ok {
			s.cfg.ImageWorkerPollIntervalSecs = intAny(v, s.cfg.ImageWorkerPollIntervalSecs)
		}
		if v, ok := body["image_account_concurrency"]; ok {
			s.cfg.ImageAccountConcurrency = intAny(v, s.cfg.ImageAccountConcurrency)
		}
		if v, ok := body["auto_remove_invalid_accounts"]; ok {
			s.cfg.AutoRemoveInvalidAccounts = boolAny(v, false)
		}
		if v, ok := body["auto_remove_rate_limited_accounts"]; ok {
			s.cfg.AutoRemoveRateLimitedAccounts = boolAny(v, false)
		}
		if v, ok := body["cleanup_protect_user_images"]; ok {
			s.cfg.CleanupProtectUserImages = boolAny(v, true)
		}
		if v, ok := body["log_levels"]; ok {
			s.cfg.LogLevels = stringSliceAny(v)
		}
		if v, ok := body["sensitive_words"]; ok {
			s.cfg.SensitiveWords = stringSliceAny(v)
		}
		if v, ok := body["ai_review"].(map[string]any); ok {
			s.cfg.AIReview = v
		}
		_ = s.saveConfig()
		writeJSON(w, 200, map[string]any{"config": s.configMap(false)})
	default:
		writeErr(w, 405, "method not allowed")
	}
}
func (s *Server) handleStorageInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"backend": map[string]any{"type": "json", "description": "本地 JSON 文件存储", "accounts_file_path": s.store.path("accounts.json"), "auth_keys_file_path": s.store.path("auth_keys.json"), "image_owners_file_path": s.store.path("image_owners.json")}, "health": map[string]any{"status": "healthy", "backend": "json"}})
}
func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	transport := "curl-impersonate"
	bin := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_CURL_IMPERSONATE_BIN"))
	if bin == "" {
		if p, err := s.ensureCurlImpersonateBinary(); err == nil {
			bin = p
		}
	}
	binOK := false
	if bin != "" {
		if st, err := os.Stat(bin); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			binOK = true
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "version": s.version(), "storage": "json", "transport": transport, "curl_impersonate_bin": bin, "curl_impersonate_executable": binOK, "accounts": len(s.store.LoadAccounts()), "tasks": len(s.store.LoadTasks())})
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"proxy": map[string]any{"url": s.cfg.Proxy, "enabled": s.cfg.Proxy != ""}})
		return
	}
	if r.Method == http.MethodPost {
		var b map[string]any
		if !readBody(w, r, &b) {
			return
		}
		s.cfg.Proxy = strAny(b["url"], strAny(b["proxy"], s.cfg.Proxy))
		s.cfg.Extra["proxy"] = s.cfg.Proxy
		_ = s.saveConfig()
		writeJSON(w, 200, map[string]any{"proxy": map[string]any{"url": s.cfg.Proxy, "enabled": s.cfg.Proxy != ""}})
		return
	}
	writeErr(w, 405, "method not allowed")
}
func (s *Server) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	target := "https://chatgpt.com/backend-api/me"
	client := &http.Client{Timeout: 15 * time.Second}
	if strings.TrimSpace(s.cfg.Proxy) != "" {
		proxyURL, err := url.Parse(s.cfg.Proxy)
		if err != nil {
			writeJSON(w, 200, map[string]any{"result": map[string]any{"ok": false, "message": err.Error()}})
			return
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"ok": false, "message": err.Error()}})
		return
	}
	defer resp.Body.Close()
	ok := resp.StatusCode > 0 && resp.StatusCode < 500
	writeJSON(w, 200, map[string]any{"result": map[string]any{"ok": ok, "status": resp.StatusCode, "message": resp.Status}})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	startDate := strings.TrimSpace(r.URL.Query().Get("start_date"))
	endDate := strings.TrimSpace(r.URL.Query().Get("end_date"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	items := s.logSvc.listFiltered(typ, startDate, endDate, status, endpoint, model, query, 200)
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) handleLogsDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var b struct {
		IDs []string `json:"ids"`
	}
	if !readBody(w, r, &b) {
		return
	}
	removed := s.logSvc.delete(b.IDs)
	writeJSON(w, 200, map[string]any{"removed": removed})
}
