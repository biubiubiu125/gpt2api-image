package app

import (
	"context"
	"fmt"
	"io"
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
		if v, ok := body["image_account_fallback_attempts"]; ok {
			next.ImageAccountFallbackAttempts = intAny(v, next.ImageAccountFallbackAttempts)
			if next.ImageAccountFallbackAttempts < 1 {
				next.ImageAccountFallbackAttempts = 1
			}
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
		if v, ok := body["auto_delete_quota_zero_accounts"]; ok {
			next.AutoDeleteQuotaZeroAccounts = boolAny(v, true)
		}
		if v, ok := body["auto_delete_upload_quota_zero_accounts"]; ok {
			next.AutoDeleteUploadQuotaZeroAccounts = boolAny(v, true)
		}
		if v, ok := body["delete_403_consecutive"]; ok {
			next.Delete403Consecutive = intAny(v, next.Delete403Consecutive)
			if next.Delete403Consecutive < 1 {
				next.Delete403Consecutive = 1
			}
		}
		if v, ok := body["delete_timeout_consecutive"]; ok {
			next.DeleteTimeoutConsecutive = intAny(v, next.DeleteTimeoutConsecutive)
			if next.DeleteTimeoutConsecutive < 1 {
				next.DeleteTimeoutConsecutive = 1
			}
		}
		if v, ok := body["auto_refresh_accounts_enabled"]; ok {
			next.AutoRefreshAccountsEnabled = boolAny(v, true)
		}
		if v, ok := body["auto_refresh_accounts_interval_minutes"]; ok {
			next.AutoRefreshAccountsIntervalMinutes = intAny(v, next.AutoRefreshAccountsIntervalMinutes)
			if next.AutoRefreshAccountsIntervalMinutes < 1 {
				next.AutoRefreshAccountsIntervalMinutes = 1
			}
		}
		if v, ok := body["auto_refresh_accounts_batch_size"]; ok {
			next.AutoRefreshAccountsBatchSize = intAny(v, next.AutoRefreshAccountsBatchSize)
			if next.AutoRefreshAccountsBatchSize < 0 {
				next.AutoRefreshAccountsBatchSize = 0
			}
		}
		if v, ok := body["auto_refresh_delete_failed_accounts"]; ok {
			next.AutoRefreshDeleteFailedAccounts = boolAny(v, true)
		}
		if v, ok := body["auto_refresh_trigger_refill"]; ok {
			next.AutoRefreshTriggerRefill = boolAny(v, true)
		}
		if v, ok := body["auto_cleanup_accounts_enabled"]; ok {
			next.AutoCleanupAccountsEnabled = boolAny(v, true)
		}
		if v, ok := body["auto_cleanup_accounts_interval_seconds"]; ok {
			next.AutoCleanupAccountsIntervalSeconds = intAny(v, next.AutoCleanupAccountsIntervalSeconds)
			if next.AutoCleanupAccountsIntervalSeconds < 10 {
				next.AutoCleanupAccountsIntervalSeconds = 10
			}
		}
		if v, ok := body["auto_refill_use_effective_available"]; ok {
			next.AutoRefillUseEffectiveAvailable = boolAny(v, true)
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
		s.applyRuntimeConfig(next)
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
		s.applyRuntimeConfig(next)
		writeJSON(w, 200, map[string]any{"proxy": map[string]any{"url": s.cfg.Proxy, "enabled": s.cfg.Proxy != ""}})
		return
	}
	writeErr(w, 405, "method not allowed")
}
func (s *Server) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body map[string]any
	if !readBody(w, r, &body) {
		return
	}
	candidate := strings.TrimSpace(strAny(body["url"], strAny(body["proxy"], s.cfg.Proxy)))
	result := s.testProxyConnectivity(r.Context(), candidate)
	writeJSON(w, 200, map[string]any{"result": result})
}

func (s *Server) testProxyConnectivity(ctx context.Context, candidate string) map[string]any {
	result := map[string]any{
		"ok":         false,
		"proxy":      maskProxyURL(candidate),
		"latency_ms": 0,
		"error":      nil,
	}
	proxy, err := normalizeProxyURL(candidate)
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	if proxy == "" {
		result["error"] = "proxy is required"
		return result
	}
	proxyURL, _ := url.Parse(proxy)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	result["latency_ms"] = latency
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	defer resp.Body.Close()
	result["status"] = resp.StatusCode
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	trace := parseCloudflareTrace(string(body))
	if resp.StatusCode != http.StatusOK {
		result["error"] = fmt.Sprintf("proxy check http %d", resp.StatusCode)
		return result
	}
	if strings.TrimSpace(trace["ip"]) == "" {
		result["error"] = "proxy check did not return exit ip"
		return result
	}
	result["ok"] = true
	result["ip"] = trace["ip"]
	result["country"] = trace["loc"]
	result["colo"] = trace["colo"]
	result["http"] = trace["http"]
	result["tls"] = trace["tls"]
	result["target"] = "https://www.cloudflare.com/cdn-cgi/trace"
	return result
}

func parseCloudflareTrace(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out
}

func maskProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || u.User == nil {
		return value
	}
	username := u.User.Username()
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(username, "***")
	}
	return u.String()
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
