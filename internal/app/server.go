package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	minImageTaskTimeoutSecs  = 60
	minImageTaskClaimTTLSecs = 15
)

type Server struct {
	root             string
	dataDir          string
	imagesDir        string
	webDist          string
	cfg              Config
	store            *Store
	mux              *http.ServeMux
	accMu            sync.Mutex
	authMu           sync.Mutex
	taskMu           sync.Mutex
	logMu            sync.Mutex
	imageCleanupMu   sync.Mutex
	lastImageCleanup time.Time
	taskCleanupMu    sync.Mutex
	lastTaskCleanup  time.Time
	callStarts       map[string]time.Time
	taskCancels      map[string]context.CancelFunc
	accountPool      *accountPool
	logSvc           *logService
	taskStore        *PGTaskStore
	imageGenerator   imageGeneratorFunc
}

func NewServer(root string) (*Server, error) {
	return newServer(root, true)
}

func newServer(root string, startWatcher bool) (*Server, error) {
	root, _ = filepath.Abs(root)
	cfg, err := loadConfig(filepath.Join(root, "config.json"))
	if err != nil {
		return nil, err
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_AUTH_KEY")); env != "" {
		cfg.AuthKey = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_DATABASE_URL")); env != "" {
		cfg.DatabaseURL = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_BASE_URL")); env != "" {
		cfg.BaseURL = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_REGISTER_EXECUTOR_URL")); env != "" {
		cfg.RegisterExecutorURL = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_REGISTER_INTERNAL_KEY")); env != "" {
		cfg.RegisterInternalKey = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_UPSTREAM_TRANSPORT")); env != "" {
		cfg.UpstreamTransport = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_ROUTE_STRATEGY")); env != "" {
		cfg.ImageRouteStrategy = env
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_CORS_ALLOWED_ORIGINS")); env != "" {
		cfg.CORSAllowedOrigins = splitConfigList(env)
	}
	if env := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_LOG_REQUEST_TEXT")); env != "" {
		cfg.LogRequestText = parseBoolEnv(env)
	}
	if strings.TrimSpace(cfg.AuthKey) == "" {
		return nil, errors.New("auth-key 未设置")
	}
	if isUnsafeDefaultAuthKey(cfg.AuthKey) {
		return nil, errors.New("auth-key 不能使用默认占位值，请设置 GPT2API_IMAGE_AUTH_KEY 或修改 config.json")
	}
	if cfg.RefreshAccountIntervalMinute <= 0 {
		cfg.RefreshAccountIntervalMinute = 60
	}
	if cfg.ImageRetentionDays <= 0 {
		cfg.ImageRetentionDays = 15
	}
	if cfg.ImagePollTimeoutSecs <= 0 {
		cfg.ImagePollTimeoutSecs = 120
	}
	if cfg.ImagePollIntervalSecs <= 0 {
		cfg.ImagePollIntervalSecs = 4
	}
	if cfg.ImagePollInitialWaitSecs < 0 {
		cfg.ImagePollInitialWaitSecs = 0
	}
	if cfg.ImageTaskTimeoutSecs <= 0 {
		cfg.ImageTaskTimeoutSecs = 300
	}
	if cfg.ImageTaskTimeoutSecs < minImageTaskTimeoutSecs {
		cfg.ImageTaskTimeoutSecs = minImageTaskTimeoutSecs
	}
	if cfg.ImageTaskClaimTTLSecs <= 0 {
		cfg.ImageTaskClaimTTLSecs = 300
	}
	if cfg.ImageTaskClaimTTLSecs < minImageTaskClaimTTLSecs {
		cfg.ImageTaskClaimTTLSecs = minImageTaskClaimTTLSecs
	}
	if cfg.ImageWorkerPollIntervalSecs <= 0 {
		cfg.ImageWorkerPollIntervalSecs = 1
	}
	if cfg.ImageAccountConcurrency <= 0 {
		cfg.ImageAccountConcurrency = 3
	}
	cfg.UpstreamTransport = normalizeUpstreamTransport(cfg.UpstreamTransport)
	cfg.ImageRouteStrategy = normalizeImageRouteStrategy(cfg.ImageRouteStrategy)
	s := &Server{root: root, dataDir: filepath.Join(root, "data"), imagesDir: filepath.Join(root, "data", "images"), webDist: filepath.Join(root, "web_dist"), cfg: cfg, callStarts: map[string]time.Time{}, taskCancels: map[string]context.CancelFunc{}, accountPool: newAccountPool(&cfg)}
	if err := os.MkdirAll(s.imagesDir, 0755); err != nil {
		return nil, err
	}
	s.logSvc = newLogService(s.dataDir)
	s.store = NewStore(s.dataDir)
	if strings.TrimSpace(cfg.DatabaseURL) != "" {
		taskStore, err := NewPGTaskStore(cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		s.taskStore = taskStore
	}
	s.recoverUnfinishedTasks()
	s.cleanupOldTasks()
	s.mux = http.NewServeMux()
	s.routes()
	if startWatcher {
		s.startLimitedAccountWatcher()
	}
	return s, nil
}

func isUnsafeDefaultAuthKey(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.NewReplacer(" ", "", "_", "", "-", "", ".", "", "<", "", ">", "").Replace(v)
	switch v {
	case "changeme", "changeit", "change", "default", "password", "yourkey", "yourauthkey", "yourapikey",
		"replacewithalongrandomadminkey", "replacewithrandomadminkey", "replacewithyouradminkey",
		"longrandomadminkey", "exampleadminkey", "testadminkey", "opensslrandhex32":
		return true
	default:
		return false
	}
}

func loadConfig(path string) (Config, error) {
	var raw map[string]any
	if err := ensureNotDir(path); err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		raw = map[string]any{}
	} else {
		_ = json.Unmarshal(b, &raw)
	}
	var cfg Config
	if b, _ := json.Marshal(raw); len(b) > 0 {
		_ = json.Unmarshal(b, &cfg)
	}
	cfg.Extra = raw
	return cfg, nil
}

func (s *Server) saveConfig() error {
	m := s.configMap(true)
	return writeJSONFile(filepath.Join(s.root, "config.json"), m)
}

func (s *Server) configMap(includeAuth bool) map[string]any {
	m := map[string]any{}
	for k, v := range s.cfg.Extra {
		if isLegacyConfigKey(k) || k == "ai_review" {
			continue
		}
		m[k] = v
	}
	if includeAuth {
		m["auth-key"] = s.cfg.AuthKey
	} else {
		delete(m, "auth-key")
		delete(m, "register_internal_key")
	}
	m["refresh_account_interval_minute"] = s.cfg.RefreshAccountIntervalMinute
	m["image_retention_days"] = s.cfg.ImageRetentionDays
	m["image_poll_timeout_secs"] = s.cfg.ImagePollTimeoutSecs
	m["image_poll_interval_secs"] = s.cfg.ImagePollIntervalSecs
	m["image_poll_initial_wait_secs"] = s.cfg.ImagePollInitialWaitSecs
	m["image_task_timeout_secs"] = s.cfg.ImageTaskTimeoutSecs
	m["image_task_claim_ttl_secs"] = s.cfg.ImageTaskClaimTTLSecs
	m["image_worker_poll_interval_secs"] = s.cfg.ImageWorkerPollIntervalSecs
	m["auto_remove_rate_limited_accounts"] = s.cfg.AutoRemoveRateLimitedAccounts
	m["auto_remove_invalid_accounts"] = s.cfg.AutoRemoveInvalidAccounts
	m["log_levels"] = s.cfg.LogLevels
	m["log_request_text"] = s.cfg.LogRequestText
	m["cors_allowed_origins"] = s.cfg.CORSAllowedOrigins
	m["proxy"] = s.cfg.Proxy
	m["upstream_transport"] = s.cfg.UpstreamTransport
	m["image_route_strategy"] = s.cfg.ImageRouteStrategy
	m["base_url"] = s.cfg.BaseURL
	m["sensitive_words"] = s.cfg.SensitiveWords
	m["global_system_prompt"] = s.cfg.GlobalSystemPrompt
	m["image_account_concurrency"] = s.cfg.ImageAccountConcurrency
	m["cleanup_protect_user_images"] = s.cfg.CleanupProtectUserImages
	m["register_executor_url"] = s.cfg.RegisterExecutorURL
	if includeAuth {
		m["register_internal_key"] = s.cfg.RegisterInternalKey
	} else {
		delete(m, "register_internal_key")
	}
	return m
}

func splitConfigList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func parseBoolEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	allowed := s.cfg.CORSAllowedOrigins
	if len(allowed) == 0 {
		return
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return
	}
	allowOrigin := ""
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == "*" {
			allowOrigin = "*"
			break
		}
		if item == origin {
			allowOrigin = origin
			break
		}
	}
	if allowOrigin == "" {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
	if allowOrigin != "*" {
		w.Header().Add("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,x-api-key,anthropic-version")
}

func isLegacyConfigKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "backup", "backup_state", "cleanup_protect_gallery":
		return true
	default:
		return false
	}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Trace-Id"), newTraceID())
		ctx := withTraceID(r.Context(), traceID)
		r = r.WithContext(ctx)
		tw := &traceResponseWriter{ResponseWriter: w}
		start := time.Now()
		traceLogf(ctx, "┌─ client request %s %s remote=%s ua=%q", r.Method, r.URL.RequestURI(), r.RemoteAddr, truncateText(r.UserAgent(), 160))
		defer func() {
			status := tw.status
			if status == 0 {
				status = http.StatusOK
			}
			traceLogf(ctx, "└─ client response status=%d bytes=%d duration=%s", status, tw.bytes, traceHTTPDuration(start))
		}()
		s.applyCORS(tw, r)
		if r.Method == http.MethodOptions {
			tw.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			tw.Header().Set("Cache-Control", "no-store")
		}
		s.mux.ServeHTTP(tw, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("/auth/login", s.handleLogin)
	s.mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"version": s.version()})
	})
	s.mux.HandleFunc("/api/auth/me", s.handleAuthMe)
	s.mux.HandleFunc("/api/auth/users", s.handleAuthUsers)
	s.mux.HandleFunc("/api/auth/users/", s.handleAuthUserID)
	s.mux.HandleFunc("/api/accounts", s.handleAccounts)
	s.mux.HandleFunc("/api/accounts/refresh", s.handleAccountsRefresh)
	s.mux.HandleFunc("/api/accounts/update", s.handleAccountsUpdate)
	s.mux.HandleFunc("/api/settings", s.handleSettings)
	s.mux.HandleFunc("/api/storage/info", s.handleStorageInfo)
	s.mux.HandleFunc("/api/system/status", s.handleSystemStatus)
	s.mux.HandleFunc("/api/transport/status", s.handleSystemStatus)
	s.mux.HandleFunc("/api/proxy", s.handleProxy)
	s.mux.HandleFunc("/api/proxy/test", s.handleProxyTest)
	s.mux.HandleFunc("/api/logs", s.handleLogs)
	s.mux.HandleFunc("/api/logs/delete", s.handleLogsDelete)
	s.mux.HandleFunc("/api/images", s.handleImages)
	s.mux.HandleFunc("/api/me/images", s.handleMyImages)
	s.mux.HandleFunc("/api/images/owners", s.handleImageOwners)
	s.mux.HandleFunc("/api/images/delete", s.handleImageDelete)
	s.mux.HandleFunc("/api/images/download/", s.handleImageDownloadSingle)
	s.mux.HandleFunc("/api/images/download", s.handleImageDownload)
	s.mux.HandleFunc("/api/images/tags", s.handleImageTags)
	s.mux.HandleFunc("/api/images/tags/", s.handleImageTagDelete)
	s.mux.HandleFunc("/image-thumbnails/", s.handleThumbnail)
	s.mux.HandleFunc("/api/image-tasks", s.handleImageTasks)
	s.mux.HandleFunc("/api/image-tasks/generations", s.handleImageTaskGeneration)
	s.mux.HandleFunc("/api/image-tasks/edits", s.handleImageTaskEdit)
	s.mux.HandleFunc("/api/image-tasks/cancel", s.handleImageTaskCancel)
	s.mux.HandleFunc("/api/register", s.handleRegister)
	s.mux.HandleFunc("/api/register/start", s.handleRegisterStart)
	s.mux.HandleFunc("/api/register/stop", s.handleRegisterStop)
	s.mux.HandleFunc("/api/register/reset", s.handleRegisterReset)
	s.mux.HandleFunc("/api/register/repair-abnormal", s.handleRegisterRepairAbnormal)
	s.mux.HandleFunc("/api/register/outlook-pool/reset", s.handleRegisterOutlookPoolReset)
	s.mux.HandleFunc("/api/register/outlook-pool/test", s.handleRegisterOutlookPoolTest)
	s.mux.HandleFunc("/api/register/events", s.handleRegisterEvents)
	s.mux.HandleFunc("/internal/register/accounts", s.handleInternalRegisterAccounts)
	s.mux.HandleFunc("/internal/register/accounts/refresh", s.handleInternalRegisterAccountsRefresh)
	s.mux.HandleFunc("/internal/register/accounts/delete", s.handleInternalRegisterAccountsDelete)
	s.mux.HandleFunc("/v1/models", s.handleV1Models)
	s.mux.HandleFunc("/v1/images/generations", s.handleV1ImagesGenerations)
	s.mux.HandleFunc("/v1/images/edits", s.handleV1ImagesEdits)
	s.mux.HandleFunc("/v1/chat/completions", s.handleV1ChatCompletionsImageOnly)
	s.mux.HandleFunc("/v1/responses", s.handleV1ResponsesImageOnly)
	s.mux.HandleFunc("/v1/messages", s.handleV1MessagesDisabled)
	s.mux.HandleFunc("/images/", s.handleStoredImage)
	s.mux.HandleFunc("/", s.handleWeb)
}

func (s *Server) version() string {
	b, err := os.ReadFile(filepath.Join(s.root, "VERSION"))
	if err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	return "go-0.1.0"
}

func (s *Server) baseURL(r *http.Request) string {
	if strings.TrimSpace(s.cfg.BaseURL) != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = strings.Split(xf, ",")[0]
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}
