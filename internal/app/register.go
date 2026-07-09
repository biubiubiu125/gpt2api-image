package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const registerSecretPlaceholder = "********"

var registerProviderRuntimeFields = []string{
	"auto_domain_blacklist",
	"auto_domain_blacklist_entries",
	"mailboxes_count",
	"mailboxes_preview",
	"mailboxes_stats",
}

type RegisterConfig struct {
	Enabled                 bool                   `json:"enabled"`
	Lifecycle               string                 `json:"lifecycle,omitempty"`
	IsRunning               bool                   `json:"is_running,omitempty"`
	IsStopping              bool                   `json:"is_stopping,omitempty"`
	Mail                    RegisterMailConfig     `json:"mail"`
	Proxy                   string                 `json:"proxy"`
	TaskTimeoutSeconds      int                    `json:"task_timeout_seconds"`
	TaskStallTimeoutSeconds int                    `json:"task_stall_timeout_seconds"`
	Total                   int                    `json:"total"`
	Threads                 int                    `json:"threads"`
	Mode                    string                 `json:"mode"`
	TargetQuota             int                    `json:"target_quota"`
	TargetAvailable         int                    `json:"target_available"`
	CheckInterval           int                    `json:"check_interval"`
	FixedPassword           string                 `json:"fixed_password"`
	AutoRefill              map[string]any         `json:"auto_refill,omitempty"`
	Stats                   RegisterStats          `json:"stats"`
	Logs                    []RegisterLog          `json:"logs,omitempty"`
	Executor                map[string]any         `json:"executor,omitempty"`
	Extra                   map[string]interface{} `json:"-"`
}

type RegisterMailConfig struct {
	RequestTimeout      int              `json:"request_timeout"`
	WaitTimeout         int              `json:"wait_timeout"`
	WaitInterval        int              `json:"wait_interval"`
	APIUseRegisterProxy bool             `json:"api_use_register_proxy"`
	Providers           []map[string]any `json:"providers"`
}

func (c *RegisterMailConfig) UnmarshalJSON(data []byte) error {
	type alias RegisterMailConfig
	next := alias{
		APIUseRegisterProxy: false,
	}
	if err := json.Unmarshal(data, &next); err != nil {
		return err
	}
	*c = RegisterMailConfig(next)
	return nil
}

type RegisterStats struct {
	JobID            string           `json:"job_id,omitempty"`
	JobKind          string           `json:"job_kind,omitempty"`
	Success          int              `json:"success"`
	UsableSuccess    int              `json:"usable_success"`
	Fail             int              `json:"fail"`
	Done             int              `json:"done"`
	Saved            int              `json:"saved"`
	RefreshFailed    int              `json:"refresh_failed"`
	Running          int              `json:"running"`
	Threads          int              `json:"threads"`
	ElapsedSeconds   float64          `json:"elapsed_seconds"`
	AvgSeconds       float64          `json:"avg_seconds"`
	SuccessRate      float64          `json:"success_rate"`
	CurrentQuota     int              `json:"current_quota"`
	CurrentAvailable int              `json:"current_available"`
	StartedAt        string           `json:"started_at,omitempty"`
	UpdatedAt        string           `json:"updated_at,omitempty"`
	FinishedAt       string           `json:"finished_at,omitempty"`
	Trigger          string           `json:"trigger,omitempty"`
	Workers          []map[string]any `json:"workers,omitempty"`
	FailureReasons   map[string]int   `json:"failure_reasons,omitempty"`
	Lifecycle        string           `json:"lifecycle,omitempty"`
	IsRunning        bool             `json:"is_running,omitempty"`
	IsStopping       bool             `json:"is_stopping,omitempty"`
}

type RegisterLog struct {
	Time  string `json:"time"`
	Text  string `json:"text"`
	Level string `json:"level"`
}

func defaultRegisterConfig() RegisterConfig {
	return RegisterConfig{
		Enabled:   false,
		Lifecycle: "idle",
		Mail: RegisterMailConfig{
			RequestTimeout:      30,
			WaitTimeout:         30,
			WaitInterval:        2,
			APIUseRegisterProxy: false,
			Providers:           []map[string]any{},
		},
		Proxy:                   "",
		TaskTimeoutSeconds:      300,
		TaskStallTimeoutSeconds: 60,
		Total:                   10,
		Threads:                 3,
		Mode:                    "total",
		TargetQuota:             100,
		TargetAvailable:         10,
		CheckInterval:           5,
		FixedPassword:           "",
		AutoRefill: map[string]any{
			"enabled":        false,
			"min_available":  30,
			"batch_total":    100,
			"check_interval": 300,
		},
		Stats: RegisterStats{
			Success:          0,
			UsableSuccess:    0,
			Fail:             0,
			Done:             0,
			Saved:            0,
			RefreshFailed:    0,
			Running:          0,
			Threads:          3,
			ElapsedSeconds:   0,
			AvgSeconds:       0,
			SuccessRate:      0,
			CurrentQuota:     0,
			CurrentAvailable: 0,
			FailureReasons:   map[string]int{},
			Lifecycle:        "idle",
		},
		Logs: []RegisterLog{},
		Executor: map[string]any{
			"status":  "not_configured",
			"message": "注册执行器未配置；配置 GPT2API_IMAGE_REGISTER_EXECUTOR_URL 后可接入独立执行器。",
		},
	}
}

func (s *Store) LoadRegisterConfig() RegisterConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeRegisterConfig(readJSONFile(s.path("register.json"), defaultRegisterConfig()))
}

func (s *Store) SaveRegisterConfig(cfg RegisterConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONFile(s.path("register.json"), normalizeRegisterConfig(cfg))
}

func (s *Store) UpdateRegisterConfig(fn func(RegisterConfig) RegisterConfig) (RegisterConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := normalizeRegisterConfig(readJSONFile(s.path("register.json"), defaultRegisterConfig()))
	cfg = normalizeRegisterConfig(fn(cfg))
	err := writeJSONFile(s.path("register.json"), cfg)
	return cfg, err
}

func normalizeRegisterConfig(cfg RegisterConfig) RegisterConfig {
	def := defaultRegisterConfig()
	if cfg.Mail.RequestTimeout <= 0 && cfg.Mail.WaitTimeout <= 0 && cfg.Mail.WaitInterval <= 0 && cfg.Mail.Providers == nil {
		cfg.Mail = def.Mail
	}
	if cfg.Mail.RequestTimeout <= 0 {
		cfg.Mail.RequestTimeout = def.Mail.RequestTimeout
	}
	if cfg.Mail.WaitTimeout <= 0 {
		cfg.Mail.WaitTimeout = def.Mail.WaitTimeout
	}
	if cfg.Mail.WaitInterval <= 0 {
		cfg.Mail.WaitInterval = def.Mail.WaitInterval
	}
	if cfg.Mail.Providers == nil {
		cfg.Mail.Providers = []map[string]any{}
	}
	cfg.Mail.APIUseRegisterProxy = false
	for i := range cfg.Mail.Providers {
		stripRegisterProviderRuntimeFields(cfg.Mail.Providers[i])
		if strings.TrimSpace(strAny(cfg.Mail.Providers[i]["provider_id"], "")) == "" {
			cfg.Mail.Providers[i]["provider_id"] = randID(16)
		}
	}
	cfg.Proxy = strings.TrimSpace(cfg.Proxy)
	cfg.Total = maxInt(1, cfg.Total)
	cfg.Threads = maxInt(1, cfg.Threads)
	if cfg.Mode != "total" && cfg.Mode != "quota" && cfg.Mode != "available" {
		cfg.Mode = "total"
	}
	cfg.TargetQuota = maxInt(1, cfg.TargetQuota)
	cfg.TargetAvailable = maxInt(1, cfg.TargetAvailable)
	cfg.CheckInterval = maxInt(1, cfg.CheckInterval)
	cfg.TaskTimeoutSeconds = maxInt(30, cfg.TaskTimeoutSeconds)
	cfg.TaskStallTimeoutSeconds = maxInt(0, cfg.TaskStallTimeoutSeconds)
	cfg.FixedPassword = strings.TrimSpace(cfg.FixedPassword)
	if cfg.AutoRefill == nil {
		cfg.AutoRefill = def.AutoRefill
	}
	cfg.Stats.Threads = cfg.Threads
	if cfg.Logs == nil {
		cfg.Logs = []RegisterLog{}
	}
	if len(cfg.Logs) > 300 {
		cfg.Logs = cfg.Logs[len(cfg.Logs)-300:]
	}
	if cfg.Executor == nil {
		cfg.Executor = def.Executor
	}
	return cfg
}

func stripRegisterProviderRuntimeFields(provider map[string]any) {
	for _, key := range registerProviderRuntimeFields {
		delete(provider, key)
	}
}

func (s *Server) registerSnapshot() RegisterConfig {
	cfg := s.store.LoadRegisterConfig()
	cfg.Stats.CurrentQuota, cfg.Stats.CurrentAvailable = s.registerPoolMetrics()
	cfg.Stats.Threads = cfg.Threads
	stopping := !cfg.Enabled && cfg.Stats.Running > 0
	running := cfg.Enabled
	lifecycle := "idle"
	if stopping {
		lifecycle = "stopping"
	} else if running && cfg.Stats.JobKind == "repair_abnormal" {
		lifecycle = "repairing"
	} else if running {
		lifecycle = "running"
	}
	cfg.Lifecycle = lifecycle
	cfg.IsRunning = running
	cfg.IsStopping = stopping
	cfg.Stats.Lifecycle = lifecycle
	cfg.Stats.IsRunning = running
	cfg.Stats.IsStopping = stopping
	cfg = redactRegisterSecrets(cfg)
	return cfg
}

func (s *Server) registerPoolMetrics() (int, int) {
	quota := 0
	available := 0
	for _, account := range s.store.LoadAccounts() {
		if !isAccountStatus(account.Status, accountStatusNormal) || account.ImageQuotaUnknown || account.Quota <= 0 || account.PendingDelete {
			continue
		}
		available++
		quota += account.Quota
	}
	return quota, available
}

func redactRegisterSecrets(cfg RegisterConfig) RegisterConfig {
	if strings.TrimSpace(cfg.FixedPassword) != "" {
		cfg.FixedPassword = registerSecretPlaceholder
	}
	for i := range cfg.Mail.Providers {
		provider := cfg.Mail.Providers[i]
		redactRegisterProviderSecrets(provider)
		if strings.EqualFold(strAny(provider["type"], ""), "outlook_token") {
			text := strAny(provider["mailboxes"], "")
			provider["mailboxes"] = ""
			provider["mailboxes_count"] = countNonEmptyLines(text)
			provider["mailboxes_preview"] = maskedMailboxPreview(text, 20)
		}
		if strings.EqualFold(strAny(provider["type"], ""), "yyds_mail") {
			if _, ok := provider["auto_domain_blacklist"]; !ok {
				provider["auto_domain_blacklist"] = []string{}
			}
		}
	}
	return cfg
}

func redactRegisterProviderSecrets(provider map[string]any) {
	for key, value := range provider {
		if isRegisterSecretKey(key) && strings.TrimSpace(strAny(value, "")) != "" {
			provider[key] = registerSecretPlaceholder
		}
	}
}

func isRegisterSecretKey(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	switch k {
	case "api_key", "admin_password", "password", "token", "access_token", "refresh_token", "id_token",
		"ddg_token", "cf_api_key", "client_secret", "private_key", "secret", "authorization":
		return true
	}
	return strings.HasSuffix(k, "_token") ||
		strings.HasSuffix(k, "_password") ||
		strings.HasSuffix(k, "_api_key") ||
		strings.Contains(k, "secret")
}

func countNonEmptyLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func maskedMailboxPreview(text string, limit int) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		if len(out) >= limit {
			break
		}
		email := strings.TrimSpace(strings.Split(line, "----")[0])
		if email == "" {
			continue
		}
		local, domain, found := strings.Cut(email, "@")
		if !found {
			out = append(out, "***")
			continue
		}
		mask := "***"
		if len(local) > 0 {
			mask = local[:1] + "***"
			if len(local) > 2 {
				mask += local[len(local)-1:]
			}
		}
		out = append(out, mask+"@"+domain)
	}
	return out
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.registerExecutorConfigured() {
		s.proxyRegisterExecutorJSON(w, r, "/api/register")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"register": s.registerSnapshot()})
	case http.MethodPost:
		var body map[string]any
		if !readBody(w, r, &body) {
			return
		}
		if v, ok := body["proxy"]; ok {
			proxy, err := normalizeProxyURL(strAny(v, ""))
			if err != nil {
				writeErr(w, 400, "invalid register proxy: "+err.Error())
				return
			}
			body["proxy"] = proxy
		}
		_, err := s.store.UpdateRegisterConfig(func(cfg RegisterConfig) RegisterConfig {
			applyRegisterUpdates(&cfg, body)
			s.appendRegisterLogLocked(&cfg, "注册配置已保存", "green")
			return cfg
		})
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"register": s.registerSnapshot()})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func applyRegisterUpdates(cfg *RegisterConfig, body map[string]any) {
	if v, ok := body["mail"]; ok {
		if raw, err := json.Marshal(v); err == nil {
			oldMail := cfg.Mail
			var nextMail RegisterMailConfig
			if json.Unmarshal(raw, &nextMail) == nil {
				preserveRegisterProviderSecrets(oldMail, &nextMail)
				mergeRegisterOutlookMailboxes(oldMail, &nextMail)
				cfg.Mail = nextMail
			}
		}
	}
	if v, ok := body["proxy"]; ok {
		cfg.Proxy = strAny(v, "")
	}
	if v, ok := body["total"]; ok {
		cfg.Total = intAny(v, cfg.Total)
	}
	if v, ok := body["threads"]; ok {
		cfg.Threads = intAny(v, cfg.Threads)
	}
	if v, ok := body["mode"]; ok {
		cfg.Mode = strings.TrimSpace(strAny(v, cfg.Mode))
	}
	if v, ok := body["target_quota"]; ok {
		cfg.TargetQuota = intAny(v, cfg.TargetQuota)
	}
	if v, ok := body["target_available"]; ok {
		cfg.TargetAvailable = intAny(v, cfg.TargetAvailable)
	}
	if v, ok := body["check_interval"]; ok {
		cfg.CheckInterval = intAny(v, cfg.CheckInterval)
	}
	if v, ok := body["fixed_password"]; ok {
		next := strAny(v, "")
		if strings.TrimSpace(next) != registerSecretPlaceholder {
			cfg.FixedPassword = next
		}
	}
	if v, ok := body["auto_refill"]; ok {
		if raw, err := json.Marshal(v); err == nil {
			var autoRefill map[string]any
			if json.Unmarshal(raw, &autoRefill) == nil {
				cfg.AutoRefill = autoRefill
			}
		}
	}
	if v, ok := body["task_timeout_seconds"]; ok {
		cfg.TaskTimeoutSeconds = intAny(v, cfg.TaskTimeoutSeconds)
	}
	if v, ok := body["task_stall_timeout_seconds"]; ok {
		cfg.TaskStallTimeoutSeconds = intAny(v, cfg.TaskStallTimeoutSeconds)
	}
}

func preserveRegisterProviderSecrets(oldMail RegisterMailConfig, nextMail *RegisterMailConfig) {
	oldByID := map[string]map[string]any{}
	for _, provider := range oldMail.Providers {
		if id := strings.TrimSpace(strAny(provider["provider_id"], strAny(provider["id"], ""))); id != "" {
			oldByID[id] = provider
		}
	}
	for index, provider := range nextMail.Providers {
		id := strings.TrimSpace(strAny(provider["provider_id"], strAny(provider["id"], "")))
		old := oldByID[id]
		if old == nil && index < len(oldMail.Providers) {
			old = oldMail.Providers[index]
		}
		if old == nil {
			continue
		}
		for key, value := range provider {
			if !isRegisterSecretKey(key) || strings.TrimSpace(strAny(value, "")) != registerSecretPlaceholder {
				continue
			}
			if oldValue, ok := old[key]; ok {
				provider[key] = oldValue
			} else {
				provider[key] = ""
			}
		}
	}
}

func mergeRegisterOutlookMailboxes(oldMail RegisterMailConfig, nextMail *RegisterMailConfig) {
	oldByID := map[string]map[string]any{}
	oldOutlooks := []map[string]any{}
	for _, provider := range oldMail.Providers {
		if strAny(provider["type"], "") != "outlook_token" {
			continue
		}
		oldOutlooks = append(oldOutlooks, provider)
		if id := strings.TrimSpace(strAny(provider["provider_id"], strAny(provider["id"], ""))); id != "" {
			oldByID[id] = provider
		}
	}
	outlookIndex := 0
	for index, provider := range nextMail.Providers {
		if strAny(provider["type"], "") != "outlook_token" {
			continue
		}
		id := strings.TrimSpace(strAny(provider["provider_id"], strAny(provider["id"], "")))
		old := oldByID[id]
		if old == nil && id == "" && outlookIndex < len(oldOutlooks) {
			old = oldOutlooks[outlookIndex]
			outlookIndex++
		}
		if old == nil && index < len(oldMail.Providers) && strAny(oldMail.Providers[index]["type"], "") == "outlook_token" {
			old = oldMail.Providers[index]
		}
		oldText := ""
		if old != nil {
			oldText = strAny(old["mailboxes"], "")
		}
		newText := strAny(provider["mailboxes"], "")
		if strings.TrimSpace(oldText) != "" || strings.TrimSpace(newText) != "" {
			provider["mailboxes"] = mergeOutlookMailboxText(oldText, newText)
		}
		delete(provider, "mailboxes_count")
		delete(provider, "mailboxes_preview")
		delete(provider, "mailboxes_stats")
	}
}

func mergeOutlookMailboxText(oldText string, newText string) string {
	lines := []string{}
	positions := map[string]int{}
	addLine := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		email := line
		if before, _, ok := strings.Cut(line, "----"); ok {
			email = before
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			return
		}
		if pos, ok := positions[email]; ok {
			lines[pos] = line
			return
		}
		positions[email] = len(lines)
		lines = append(lines, line)
	}
	for _, line := range strings.Split(oldText, "\n") {
		addLine(line)
	}
	for _, line := range strings.Split(newText, "\n") {
		addLine(line)
	}
	return strings.Join(lines, "\n")
}

func (s *Server) handleRegisterStart(w http.ResponseWriter, r *http.Request) {
	s.handleRegisterStartKind(w, r, "register")
}

func (s *Server) handleRegisterRepairAbnormal(w http.ResponseWriter, r *http.Request) {
	s.handleRegisterStartKind(w, r, "repair_abnormal")
}

func (s *Server) handleRegisterStartKind(w http.ResponseWriter, r *http.Request, kind string) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if s.registerExecutorConfigured() {
		if kind == "repair_abnormal" {
			s.proxyRegisterExecutorJSON(w, r, "/api/register/repair-abnormal")
			return
		}
		s.proxyRegisterExecutorJSON(w, r, "/api/register/start")
		return
	}
	_, err := s.store.UpdateRegisterConfig(func(cfg RegisterConfig) RegisterConfig {
		now := nowISO()
		jobID := randID(16)
		cfg.Enabled = false
		cfg.Stats = RegisterStats{
			JobID:            jobID,
			JobKind:          kind,
			Success:          0,
			Fail:             1,
			Done:             1,
			Running:          0,
			Threads:          cfg.Threads,
			ElapsedSeconds:   0,
			AvgSeconds:       0,
			SuccessRate:      0,
			CurrentQuota:     0,
			CurrentAvailable: 0,
			StartedAt:        now,
			UpdatedAt:        now,
			FinishedAt:       now,
			Trigger:          "manual",
			Workers: []map[string]any{
				{
					"index":          1,
					"status":         "failed",
					"failure_reason": "register_executor_pending_migration",
					"last_error":     "Go 版自动注册执行器尚未迁移完成；请先手动导入账号或接入独立注册执行器。",
					"updated_at":     now,
				},
			},
		}
		cfg.Executor = map[string]any{
			"status":  "pending_migration",
			"message": "Go 版自动注册执行器尚未迁移完成；API 生图与异步队列可正常运行。",
		}
		s.appendRegisterLogLocked(&cfg, "已收到启动注册机请求，但 Go 版自动注册执行器尚未迁移完成；本次任务不会创建账号。", "red")
		return cfg
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.registerSnapshot()})
}

func (s *Server) handleRegisterStop(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.registerExecutorConfigured() {
		s.proxyRegisterExecutorJSON(w, r, "/api/register/stop")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	_, err := s.store.UpdateRegisterConfig(func(cfg RegisterConfig) RegisterConfig {
		cfg.Enabled = false
		cfg.Stats.Running = 0
		cfg.Stats.UpdatedAt = nowISO()
		s.appendRegisterLogLocked(&cfg, "已停止注册任务", "yellow")
		return cfg
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.registerSnapshot()})
}

func (s *Server) handleRegisterReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.registerExecutorConfigured() {
		s.proxyRegisterExecutorJSON(w, r, "/api/register/reset")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	quota, available := s.registerPoolMetrics()
	_, err := s.store.UpdateRegisterConfig(func(cfg RegisterConfig) RegisterConfig {
		cfg.Enabled = false
		cfg.Stats = RegisterStats{
			Success:          0,
			Fail:             0,
			Done:             0,
			Running:          0,
			Threads:          cfg.Threads,
			ElapsedSeconds:   0,
			AvgSeconds:       0,
			SuccessRate:      0,
			CurrentQuota:     quota,
			CurrentAvailable: available,
			UpdatedAt:        nowISO(),
		}
		cfg.Logs = []RegisterLog{}
		return cfg
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.registerSnapshot()})
}

func (s *Server) handleRegisterOutlookPoolReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.registerExecutorConfigured() {
		s.proxyRegisterExecutorJSON(w, r, "/api/register/outlook-pool/reset")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	_, err := s.store.UpdateRegisterConfig(func(cfg RegisterConfig) RegisterConfig {
		s.appendRegisterLogLocked(&cfg, "Outlook 邮箱池运行状态已重置；Go 版暂不维护独立邮箱池占用表。", "yellow")
		return cfg
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"register": s.registerSnapshot()})
}

func (s *Server) handleRegisterOutlookPoolTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.registerExecutorConfigured() {
		s.proxyRegisterExecutorJSON(w, r, "/api/register/outlook-pool/test")
		return
	}
	writeErr(w, 400, "register executor is not configured")
}

func (s *Server) handleRegisterYYDSDomainBlacklist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if !s.registerExecutorConfigured() {
		if r.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{"items": []string{}, "executor": "not_configured"})
			return
		}
		writeErr(w, 400, "register executor is not configured")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	s.proxyRegisterExecutorJSON(w, r, "/api/register/yyds-domain-blacklist")
}

func (s *Server) handleRegisterYYDSDomainBlacklistRemove(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if !s.registerExecutorConfigured() {
		writeErr(w, 400, "register executor is not configured")
		return
	}
	s.proxyRegisterExecutorJSON(w, r, "/api/register/yyds-domain-blacklist/remove")
}

func (s *Server) handleRegisterYYDSDomainBlacklistReplace(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if !s.registerExecutorConfigured() {
		writeErr(w, 400, "register executor is not configured")
		return
	}
	s.proxyRegisterExecutorJSON(w, r, "/api/register/yyds-domain-blacklist/replace")
}

func (s *Server) handleRegisterYYDSDomainBlacklistReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if !s.registerExecutorConfigured() {
		writeErr(w, 400, "register executor is not configured")
		return
	}
	s.proxyRegisterExecutorJSON(w, r, "/api/register/yyds-domain-blacklist/reset")
}

func (s *Server) handleRegisterEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.registerExecutorConfigured() {
		s.proxyRegisterExecutorEvents(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		payload, _ := json.Marshal(s.registerSnapshot())
		cur := string(payload)
		if cur != last {
			fmt.Fprintf(w, "data: %s\n\n", cur)
			flushSSE(w)
			last = cur
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) appendRegisterLogLocked(cfg *RegisterConfig, text, level string) {
	cfg.Logs = append(cfg.Logs, RegisterLog{Time: nowISO(), Text: text, Level: firstNonEmpty(level, "info")})
	if len(cfg.Logs) > 300 {
		cfg.Logs = cfg.Logs[len(cfg.Logs)-300:]
	}
}
