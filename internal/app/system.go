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
		next := cloneConfig(s.cfg)
		for k, v := range body {
			if isLegacyConfigKey(k) {
				continue
			}
			next.Extra[k] = v
		}
		delete(next.Extra, "backup")
		delete(next.Extra, "backup_state")
		delete(next.Extra, "cleanup_protect_gallery")
		if v, ok := body["proxy"]; ok {
			proxy, err := normalizeProxyURL(strAny(v, ""))
			if err != nil {
				writeErr(w, 400, "invalid proxy: "+err.Error())
				return
			}
			next.Proxy = proxy
		}
		if v, ok := body["base_url"]; ok {
			next.BaseURL = strings.TrimRight(strAny(v, ""), "/")
		}
		if v, ok := body["global_system_prompt"]; ok {
			next.GlobalSystemPrompt = strAny(v, "")
		}
		if v, ok := body["refresh_account_interval_minute"]; ok {
			next.RefreshAccountIntervalMinute = intAny(v, next.RefreshAccountIntervalMinute)
		}
		if v, ok := body["image_retention_days"]; ok {
			next.ImageRetentionDays = intAny(v, next.ImageRetentionDays)
		}
		if v, ok := body["image_max_storage_mb"]; ok {
			next.ImageMaxStorageMB = intAny(v, next.ImageMaxStorageMB)
			if next.ImageMaxStorageMB < 0 {
				next.ImageMaxStorageMB = 0
			}
		}
		if v, ok := body["image_poll_timeout_secs"]; ok {
			next.ImagePollTimeoutSecs = intAny(v, next.ImagePollTimeoutSecs)
		}
		if v, ok := body["image_poll_interval_secs"]; ok {
			next.ImagePollIntervalSecs = intAny(v, next.ImagePollIntervalSecs)
		}
		if v, ok := body["image_poll_initial_wait_secs"]; ok {
			next.ImagePollInitialWaitSecs = intAny(v, next.ImagePollInitialWaitSecs)
		}
		if v, ok := body["image_task_timeout_secs"]; ok {
			next.ImageTaskTimeoutSecs = intAny(v, next.ImageTaskTimeoutSecs)
			if next.ImageTaskTimeoutSecs < minImageTaskTimeoutSecs {
				next.ImageTaskTimeoutSecs = minImageTaskTimeoutSecs
			}
		}
		if v, ok := body["image_task_claim_ttl_secs"]; ok {
			next.ImageTaskClaimTTLSecs = intAny(v, next.ImageTaskClaimTTLSecs)
			if next.ImageTaskClaimTTLSecs < minImageTaskClaimTTLSecs {
				next.ImageTaskClaimTTLSecs = minImageTaskClaimTTLSecs
			}
		}
		if v, ok := body["image_worker_poll_interval_secs"]; ok {
			next.ImageWorkerPollIntervalSecs = intAny(v, next.ImageWorkerPollIntervalSecs)
		}
		if v, ok := body["image_account_concurrency"]; ok {
			next.ImageAccountConcurrency = intAny(v, next.ImageAccountConcurrency)
		}
		if v, ok := body["auto_remove_invalid_accounts"]; ok {
			next.AutoRemoveInvalidAccounts = boolAny(v, false)
		}
		if v, ok := body["auto_remove_rate_limited_accounts"]; ok {
			next.AutoRemoveRateLimitedAccounts = boolAny(v, false)
		}
		if v, ok := body["cleanup_protect_user_images"]; ok {
			next.CleanupProtectUserImages = boolAny(v, true)
		}
		if v, ok := body["log_levels"]; ok {
			next.LogLevels = stringSliceAny(v)
		}
		if v, ok := body["log_request_text"]; ok {
			next.LogRequestText = boolAny(v, false)
		}
		if v, ok := body["cors_allowed_origins"]; ok {
			next.CORSAllowedOrigins = stringSliceAny(v)
		}
		if v, ok := body["upstream_transport"]; ok {
			next.UpstreamTransport = normalizeUpstreamTransport(strAny(v, ""))
		}
		if v, ok := body["image_route_strategy"]; ok {
			next.ImageRouteStrategy = normalizeImageRouteStrategy(strAny(v, ""))
		}
		if v, ok := body["sensitive_words"]; ok {
			next.SensitiveWords = stringSliceAny(v)
		}
		delete(next.Extra, "ai_review")
		if err := s.saveConfigValue(next); err != nil {
			writeErr(w, 500, "failed to save config: "+err.Error())
			return
		}
		s.cfg = next
		writeJSON(w, 200, map[string]any{"config": s.configMap(false)})
	default:
		writeErr(w, 405, "method not allowed")
	}
}
func (s *Server) handleStorageInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.taskStore != nil {
		writeJSON(w, 200, map[string]any{
			"backend": map[string]any{
				"type":                   "postgresql",
				"description":            "PostgreSQL 图片任务队列，本地 JSON 仍保存账号、密钥和图片归属",
				"accounts_file_path":     s.store.path("accounts.json"),
				"auth_keys_file_path":    s.store.path("auth_keys.json"),
				"image_owners_file_path": s.store.path("image_owners.json"),
			},
			"health": map[string]any{"status": "healthy", "backend": "postgresql"},
		})
		return
	}
	writeJSON(w, 200, map[string]any{"backend": map[string]any{"type": "json", "description": "本地 JSON 文件存储", "accounts_file_path": s.store.path("accounts.json"), "auth_keys_file_path": s.store.path("auth_keys.json"), "image_owners_file_path": s.store.path("image_owners.json")}, "health": map[string]any{"status": "healthy", "backend": "json"}})
}
func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	transport := normalizeUpstreamTransport(s.cfg.UpstreamTransport)
	bin := ""
	binOK := false
	if transport == "curl-impersonate" {
		bin = strings.TrimSpace(os.Getenv("GPT2API_IMAGE_CURL_IMPERSONATE_BIN"))
		if bin == "" {
			if p, err := s.ensureCurlImpersonateBinary(); err == nil {
				bin = p
			}
		}
		if bin != "" {
			if st, err := os.Stat(bin); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
				binOK = true
			}
		}
	}
	storage := "json"
	taskCount := len(s.store.LoadTasks())
	body := map[string]any{"ok": true, "version": s.version(), "storage": storage, "transport": transport, "image_route_strategy": normalizeImageRouteStrategy(s.cfg.ImageRouteStrategy), "curl_impersonate_bin": bin, "curl_impersonate_executable": binOK, "accounts": len(s.store.LoadAccounts()), "tasks": taskCount}
	if s.taskStore != nil {
		storage = "postgresql"
		body["storage"] = storage
		if count, err := s.taskStore.CountTasks(r.Context()); err == nil {
			body["tasks"] = count
		} else {
			body["tasks"] = nil
			body["tasks_error"] = err.Error()
		}
	}
	writeJSON(w, 200, body)
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
		next := cloneConfig(s.cfg)
		proxy, err := normalizeProxyURL(strAny(b["url"], strAny(b["proxy"], next.Proxy)))
		if err != nil {
			writeErr(w, 400, "invalid proxy: "+err.Error())
			return
		}
		next.Proxy = proxy
		next.Extra["proxy"] = next.Proxy
		if err := s.saveConfigValue(next); err != nil {
			writeErr(w, 500, "failed to save config: "+err.Error())
			return
		}
		s.cfg = next
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
		proxy, err := normalizeProxyURL(s.cfg.Proxy)
		if err != nil {
			writeJSON(w, 200, map[string]any{"result": map[string]any{"ok": false, "message": err.Error()}})
			return
		}
		proxyURL, _ := url.Parse(proxy)
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
