package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
)

const (
	defaultGenerationRetention  = 24 * time.Hour
	defaultPinnedMediaRetention = 30 * 24 * time.Hour
	defaultAIContentRetention   = 7 * 24 * time.Hour
	defaultComfyInputRetention  = 72 * time.Hour
	defaultHostMetricRetention  = 7 * 24 * time.Hour
	defaultAuditLogRetention    = 90 * 24 * time.Hour
	defaultProxyRetention       = 90 * 24 * time.Hour
	defaultWebSocketRetention   = 30 * 24 * time.Hour
	defaultRequestRetention     = 7 * 24 * time.Hour
	defaultDailyUsageRetention  = 90 * 24 * time.Hour
	defaultInviteRetention      = 90 * 24 * time.Hour
	defaultCleanupBatchSize     = 1000
	defaultCleanupMaxBatches    = 20
	defaultDependencyCheck      = 10 * time.Second
	defaultDependencyStale      = 45 * time.Second
	defaultDependencyOffline    = 3 * time.Minute
	defaultComfyObjectInfoTTL   = 30 * time.Second
	defaultComfyObjectInfoMax   = 24 * time.Hour
	defaultMediaInflightMB      = 256
)

// RetentionPolicy is the single source of truth for data lifetime. Generation
// history and its media intentionally share one configured duration so the
// gallery cannot retain an unusable history entry after its file is gone.
type RetentionPolicy struct {
	GenerationHistory  time.Duration
	GenerationMedia    time.Duration
	PinnedMedia        time.Duration
	AIContent          time.Duration
	ComfyInputs        time.Duration
	HostMetrics        time.Duration
	AuditLog           time.Duration
	ProxyRequests      time.Duration
	WebSocketSessions  time.Duration
	GenerationRequests time.Duration
	DailyUsage         time.Duration
	InviteHistory      time.Duration
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		GenerationHistory:  defaultGenerationRetention,
		GenerationMedia:    defaultGenerationRetention,
		PinnedMedia:        defaultPinnedMediaRetention,
		AIContent:          defaultAIContentRetention,
		ComfyInputs:        defaultComfyInputRetention,
		HostMetrics:        defaultHostMetricRetention,
		AuditLog:           defaultAuditLogRetention,
		ProxyRequests:      defaultProxyRetention,
		WebSocketSessions:  defaultWebSocketRetention,
		GenerationRequests: defaultRequestRetention,
		DailyUsage:         defaultDailyUsageRetention,
		InviteHistory:      defaultInviteRetention,
	}
}

// WithDefaults keeps hand-built Config values used by tests and helper
// commands safe. Generation media is authoritative when both generation
// values are present because the history must follow the file lifetime.
func (p RetentionPolicy) WithDefaults() RetentionPolicy {
	defaults := DefaultRetentionPolicy()
	generation := p.GenerationMedia
	if generation <= 0 {
		generation = p.GenerationHistory
	}
	if generation <= 0 {
		generation = defaults.GenerationMedia
	}
	p.GenerationHistory = generation
	p.GenerationMedia = generation
	if p.PinnedMedia <= 0 {
		p.PinnedMedia = defaults.PinnedMedia
	}
	if p.PinnedMedia < generation {
		p.PinnedMedia = generation
	}
	if p.AIContent <= 0 {
		p.AIContent = defaults.AIContent
	}
	if p.AIContent < generation {
		p.AIContent = generation
	}
	if p.ComfyInputs <= 0 {
		p.ComfyInputs = defaults.ComfyInputs
	}
	if p.HostMetrics <= 0 {
		p.HostMetrics = defaults.HostMetrics
	}
	if p.AuditLog <= 0 {
		p.AuditLog = defaults.AuditLog
	}
	if p.ProxyRequests <= 0 {
		p.ProxyRequests = defaults.ProxyRequests
	}
	if p.WebSocketSessions <= 0 {
		p.WebSocketSessions = defaults.WebSocketSessions
	}
	if p.GenerationRequests <= 0 {
		p.GenerationRequests = defaults.GenerationRequests
	}
	if p.GenerationRequests < generation {
		p.GenerationRequests = generation
	}
	if p.DailyUsage <= 0 {
		p.DailyUsage = defaults.DailyUsage
	}
	if p.InviteHistory <= 0 {
		p.InviteHistory = defaults.InviteHistory
	}
	return p
}

type Config struct {
	DatabaseURL               string
	AdminUsername             string
	AdminPassword             string
	SessionSecret             string
	PublicBaseURL             string
	PublicURL                 *url.URL
	AdminBaseURL              string
	AdminURL                  *url.URL
	CookieSecure              bool
	PublicAddr                string
	AdminAddr                 string
	ComfyUIUpstream           *url.URL
	OpenWebUIUpstream         *url.URL
	OllamaUpstream            *url.URL
	ContentModeratorUpstream  *url.URL
	PromptAssistantModel      string
	ComfyUIUpstreamAuthHeader string
	OpenWebUIUpstreamAuth     string
	MiningAgentURL            *url.URL
	MiningAgentToken          string
	SystemMonitorAgentURL     *url.URL
	SystemMonitorAgentToken   string
	UpdateAgentURL            *url.URL
	UpdateAgentToken          string
	VirusTotalAPIKey          string
	FeatureSuggestionsEnabled bool
	TrustedProxies            []*net.IPNet
	AdminAllowedNetworks      []*net.IPNet
	SessionTTL                time.Duration
	SessionIdleTimeout        time.Duration
	AccountLockThreshold      int
	AccountLockDuration       time.Duration
	DependencyCheckInterval   time.Duration
	DependencyStaleAfter      time.Duration
	DependencyOfflineAfter    time.Duration
	ComfyObjectInfoCacheTTL   time.Duration
	ComfyObjectInfoMaxStale   time.Duration
	MediaInFlightLimitBytes   int64
	MediaSpoolDir             string
	PprofEnabled              bool
	Retention                 RetentionPolicy
	DatabaseCleanupBatchSize  int
	DatabaseCleanupMaxBatches int
}

func Load() (Config, error) {
	publicBaseURL, err := parseBaseURL(env("PUBLIC_BASE_URL", "http://127.0.0.1:8090"))
	if err != nil {
		return Config{}, fmt.Errorf("PUBLIC_BASE_URL: %w", err)
	}
	adminBaseURL, err := parseBaseURL(env("ADMIN_BASE_URL", "http://127.0.0.1:8091"))
	if err != nil {
		return Config{}, fmt.Errorf("ADMIN_BASE_URL: %w", err)
	}
	comfy, err := parseUpstream(env("COMFYUI_UPSTREAM", "http://host.docker.internal:8088"))
	if err != nil {
		return Config{}, fmt.Errorf("COMFYUI_UPSTREAM: %w", err)
	}
	openWebUI, err := parseUpstream(env("OPENWEBUI_UPSTREAM", "http://host.docker.internal:8089"))
	if err != nil {
		return Config{}, fmt.Errorf("OPENWEBUI_UPSTREAM: %w", err)
	}
	ollama, err := parseUpstream(env("OLLAMA_UPSTREAM", "http://host.docker.internal:11434"))
	if err != nil {
		return Config{}, fmt.Errorf("OLLAMA_UPSTREAM: %w", err)
	}
	contentModerator, err := parseUpstream(env("CONTENT_MODERATOR_UPSTREAM", "http://content-moderator:8080"))
	if err != nil {
		return Config{}, fmt.Errorf("CONTENT_MODERATOR_UPSTREAM: %w", err)
	}
	promptAssistantModel := strings.TrimSpace(env("PROMPT_ASSISTANT_MODEL", "huihui_ai/gemma-4-abliterated:e4b"))
	if promptAssistantModel == "" || len(promptAssistantModel) > 256 {
		return Config{}, fmt.Errorf("PROMPT_ASSISTANT_MODEL must contain between 1 and 256 characters")
	}
	miningAgentURL, err := parseBaseURL(env("MINING_AGENT_URL", "http://host.docker.internal:8092"))
	if err != nil {
		return Config{}, fmt.Errorf("MINING_AGENT_URL: %w", err)
	}
	systemMonitorAgentURL, err := parseBaseURL(env("SYSTEM_MONITOR_AGENT_URL", "http://host.docker.internal:8094"))
	if err != nil {
		return Config{}, fmt.Errorf("SYSTEM_MONITOR_AGENT_URL: %w", err)
	}
	updateAgentURL, err := parseBaseURL(env("UPDATE_AGENT_URL", "http://host.docker.internal:8093"))
	if err != nil {
		return Config{}, fmt.Errorf("UPDATE_AGENT_URL: %w", err)
	}
	trustedProxies, err := parseNetworks(env("TRUSTED_PROXIES", "127.0.0.1,::1"))
	if err != nil {
		return Config{}, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	adminNetworks, err := parseNetworks(env("ADMIN_ALLOWED_CIDRS", "127.0.0.1,::1,172.16.0.0/12,192.168.65.0/24"))
	if err != nil {
		return Config{}, fmt.Errorf("ADMIN_ALLOWED_CIDRS: %w", err)
	}
	sessionTTL, err := durationEnv("SESSION_TTL", 7*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if sessionTTL < time.Hour || sessionTTL > 30*24*time.Hour {
		return Config{}, fmt.Errorf("SESSION_TTL must be between 1h and 30d")
	}
	defaultIdleTimeout := 24 * time.Hour
	if sessionTTL < defaultIdleTimeout {
		defaultIdleTimeout = sessionTTL
	}
	sessionIdleTimeout, err := durationEnv("SESSION_IDLE_TIMEOUT", defaultIdleTimeout)
	if err != nil {
		return Config{}, err
	}
	if sessionIdleTimeout < 15*time.Minute || sessionIdleTimeout > sessionTTL {
		return Config{}, fmt.Errorf("SESSION_IDLE_TIMEOUT must be between 15m and SESSION_TTL")
	}
	accountLockThreshold, err := integerEnv("ACCOUNT_LOCK_THRESHOLD", 10)
	if err != nil {
		return Config{}, err
	}
	if accountLockThreshold < 3 || accountLockThreshold > 100 {
		return Config{}, fmt.Errorf("ACCOUNT_LOCK_THRESHOLD must be between 3 and 100")
	}
	accountLockDuration, err := durationEnv("ACCOUNT_LOCK_DURATION", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	if accountLockDuration < time.Minute || accountLockDuration > 24*time.Hour {
		return Config{}, fmt.Errorf("ACCOUNT_LOCK_DURATION must be between 1m and 24h")
	}
	dependencyCheckInterval, err := durationEnv("DEPENDENCY_CHECK_INTERVAL", defaultDependencyCheck)
	if err != nil {
		return Config{}, err
	}
	if dependencyCheckInterval < 5*time.Second || dependencyCheckInterval > 5*time.Minute {
		return Config{}, fmt.Errorf("DEPENDENCY_CHECK_INTERVAL must be between 5s and 5m")
	}
	dependencyStaleAfter, err := durationEnv("DEPENDENCY_STALE_AFTER", defaultDependencyStale)
	if err != nil {
		return Config{}, err
	}
	if dependencyStaleAfter < 2*dependencyCheckInterval || dependencyStaleAfter > 30*time.Minute {
		return Config{}, fmt.Errorf("DEPENDENCY_STALE_AFTER must be at least two check intervals and no more than 30m")
	}
	dependencyOfflineAfter, err := durationEnv("DEPENDENCY_OFFLINE_AFTER", defaultDependencyOffline)
	if err != nil {
		return Config{}, err
	}
	if dependencyOfflineAfter <= dependencyStaleAfter || dependencyOfflineAfter > 2*time.Hour {
		return Config{}, fmt.Errorf("DEPENDENCY_OFFLINE_AFTER must be greater than DEPENDENCY_STALE_AFTER and no more than 2h")
	}
	comfyObjectInfoCacheTTL, err := durationEnv("COMFY_OBJECT_INFO_CACHE_TTL", defaultComfyObjectInfoTTL)
	if err != nil {
		return Config{}, err
	}
	if comfyObjectInfoCacheTTL < 5*time.Second || comfyObjectInfoCacheTTL > 10*time.Minute {
		return Config{}, fmt.Errorf("COMFY_OBJECT_INFO_CACHE_TTL must be between 5s and 10m")
	}
	comfyObjectInfoMaxStale, err := durationEnv("COMFY_OBJECT_INFO_MAX_STALE", defaultComfyObjectInfoMax)
	if err != nil {
		return Config{}, err
	}
	if comfyObjectInfoMaxStale < comfyObjectInfoCacheTTL || comfyObjectInfoMaxStale > 7*24*time.Hour {
		return Config{}, fmt.Errorf("COMFY_OBJECT_INFO_MAX_STALE must be at least COMFY_OBJECT_INFO_CACHE_TTL and no more than 7d")
	}
	mediaInflightMB, err := integerEnv("MEDIA_INFLIGHT_LIMIT_MB", defaultMediaInflightMB)
	if err != nil {
		return Config{}, err
	}
	if mediaInflightMB < 64 || mediaInflightMB > 2048 {
		return Config{}, fmt.Errorf("MEDIA_INFLIGHT_LIMIT_MB must be between 64 and 2048")
	}
	mediaSpoolDir := filepath.Clean(strings.TrimSpace(env("MEDIA_SPOOL_DIR", filepath.Join(os.TempDir(), "ai-access-gateway-media"))))
	if !filepath.IsAbs(mediaSpoolDir) {
		return Config{}, fmt.Errorf("MEDIA_SPOOL_DIR must be an absolute path")
	}
	pprofEnabled, err := booleanEnv("PPROF_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	retention, err := loadRetentionPolicy()
	if err != nil {
		return Config{}, err
	}
	databaseCleanupBatchSize, err := integerEnv("DATABASE_CLEANUP_BATCH_SIZE", defaultCleanupBatchSize)
	if err != nil {
		return Config{}, err
	}
	if databaseCleanupBatchSize < 100 || databaseCleanupBatchSize > 10000 {
		return Config{}, fmt.Errorf("DATABASE_CLEANUP_BATCH_SIZE must be between 100 and 10000")
	}
	databaseCleanupMaxBatches, err := integerEnv("DATABASE_CLEANUP_MAX_BATCHES", defaultCleanupMaxBatches)
	if err != nil {
		return Config{}, err
	}
	if databaseCleanupMaxBatches < 1 || databaseCleanupMaxBatches > 100 {
		return Config{}, fmt.Errorf("DATABASE_CLEANUP_MAX_BATCHES must be between 1 and 100")
	}

	databaseURL := requiredEnv("DATABASE_URL")
	adminUsername := strings.TrimSpace(env("ADMIN_USERNAME", "admin"))
	adminPassword := requiredEnv("ADMIN_PASSWORD")
	sessionSecret := requiredEnv("SESSION_SECRET")
	miningAgentToken := requiredEnv("MINING_AGENT_TOKEN")
	systemMonitorAgentToken := strings.TrimSpace(env("SYSTEM_MONITOR_AGENT_TOKEN", miningAgentToken))
	updateAgentToken := requiredEnv("UPDATE_AGENT_TOKEN")
	virusTotalAPIKey := strings.TrimSpace(env("VIRUSTOTAL_API_KEY", ""))
	featureSuggestionsEnabled := strings.EqualFold(env("FEATURE_SUGGESTIONS_ENABLED", "false"), "true")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if adminUsername == "" {
		return Config{}, fmt.Errorf("ADMIN_USERNAME is required")
	}
	if err := security.ValidatePassword(adminPassword); err != nil {
		return Config{}, fmt.Errorf("ADMIN_PASSWORD: %w", err)
	}
	if len(sessionSecret) < 32 {
		return Config{}, fmt.Errorf("SESSION_SECRET must be at least 32 characters")
	}
	if miningAgentToken != "" && len(miningAgentToken) < 32 {
		return Config{}, fmt.Errorf("MINING_AGENT_TOKEN must be at least 32 characters when configured")
	}
	if systemMonitorAgentToken != "" && len(systemMonitorAgentToken) < 32 {
		return Config{}, fmt.Errorf("SYSTEM_MONITOR_AGENT_TOKEN must be at least 32 characters when configured")
	}
	if updateAgentToken != "" && len(updateAgentToken) < 32 {
		return Config{}, fmt.Errorf("UPDATE_AGENT_TOKEN must be at least 32 characters when configured")
	}

	cookieSecure := strings.EqualFold(env("COOKIE_SECURE", "false"), "true")
	if cookieSecure && publicBaseURL.Scheme != "https" {
		return Config{}, fmt.Errorf("COOKIE_SECURE=true requires an https PUBLIC_BASE_URL")
	}
	comfyAuth, err := upstreamBasicAuth("COMFYUI")
	if err != nil {
		return Config{}, err
	}
	openWebUIAuth, err := upstreamBasicAuth("OPENWEBUI")
	if err != nil {
		return Config{}, err
	}

	return Config{
		DatabaseURL:               databaseURL,
		AdminUsername:             adminUsername,
		AdminPassword:             adminPassword,
		SessionSecret:             sessionSecret,
		PublicBaseURL:             strings.TrimRight(publicBaseURL.String(), "/"),
		PublicURL:                 publicBaseURL,
		AdminBaseURL:              strings.TrimRight(adminBaseURL.String(), "/"),
		AdminURL:                  adminBaseURL,
		CookieSecure:              cookieSecure,
		PublicAddr:                env("PUBLIC_ADDR", ":8090"),
		AdminAddr:                 env("ADMIN_ADDR", ":8091"),
		ComfyUIUpstream:           comfy,
		OpenWebUIUpstream:         openWebUI,
		OllamaUpstream:            ollama,
		ContentModeratorUpstream:  contentModerator,
		PromptAssistantModel:      promptAssistantModel,
		ComfyUIUpstreamAuthHeader: comfyAuth,
		OpenWebUIUpstreamAuth:     openWebUIAuth,
		MiningAgentURL:            miningAgentURL,
		MiningAgentToken:          miningAgentToken,
		SystemMonitorAgentURL:     systemMonitorAgentURL,
		SystemMonitorAgentToken:   systemMonitorAgentToken,
		UpdateAgentURL:            updateAgentURL,
		UpdateAgentToken:          updateAgentToken,
		VirusTotalAPIKey:          virusTotalAPIKey,
		FeatureSuggestionsEnabled: featureSuggestionsEnabled,
		TrustedProxies:            trustedProxies,
		AdminAllowedNetworks:      adminNetworks,
		SessionTTL:                sessionTTL,
		SessionIdleTimeout:        sessionIdleTimeout,
		AccountLockThreshold:      accountLockThreshold,
		AccountLockDuration:       accountLockDuration,
		DependencyCheckInterval:   dependencyCheckInterval,
		DependencyStaleAfter:      dependencyStaleAfter,
		DependencyOfflineAfter:    dependencyOfflineAfter,
		ComfyObjectInfoCacheTTL:   comfyObjectInfoCacheTTL,
		ComfyObjectInfoMaxStale:   comfyObjectInfoMaxStale,
		MediaInFlightLimitBytes:   int64(mediaInflightMB) << 20,
		MediaSpoolDir:             mediaSpoolDir,
		PprofEnabled:              pprofEnabled,
		Retention:                 retention,
		DatabaseCleanupBatchSize:  databaseCleanupBatchSize,
		DatabaseCleanupMaxBatches: databaseCleanupMaxBatches,
	}, nil
}

func loadRetentionPolicy() (RetentionPolicy, error) {
	generation, err := durationEnvBetween("GENERATION_RETENTION", defaultGenerationRetention, time.Hour, 30*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	pinnedMedia, err := durationEnvBetween("PINNED_GENERATION_RETENTION", defaultPinnedMediaRetention, time.Hour, 365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	if pinnedMedia < generation {
		return RetentionPolicy{}, fmt.Errorf("PINNED_GENERATION_RETENTION must be greater than or equal to GENERATION_RETENTION")
	}
	aiContent, err := durationEnvBetween("AI_CONTENT_RETENTION", defaultAIContentRetention, time.Hour, 365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	if aiContent < generation {
		return RetentionPolicy{}, fmt.Errorf("AI_CONTENT_RETENTION must be greater than or equal to GENERATION_RETENTION")
	}
	comfyInputs, err := durationEnvBetween("COMFY_INPUT_RETENTION", defaultComfyInputRetention, time.Hour, 30*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	hostMetrics, err := durationEnvBetween("HOST_METRIC_RETENTION", defaultHostMetricRetention, time.Hour, 90*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	auditLog, err := durationEnvBetween("AUDIT_LOG_RETENTION", defaultAuditLogRetention, 24*time.Hour, 365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	proxyRequests, err := durationEnvBetween("PROXY_REQUEST_RETENTION", defaultProxyRetention, 30*24*time.Hour, 365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	websocketSessions, err := durationEnvBetween("WEBSOCKET_SESSION_RETENTION", defaultWebSocketRetention, 24*time.Hour, 365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	generationRequests, err := durationEnvBetween("GENERATION_REQUEST_RETENTION", defaultRequestRetention, time.Hour, 30*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	if generationRequests < generation {
		return RetentionPolicy{}, fmt.Errorf("GENERATION_REQUEST_RETENTION must be greater than or equal to GENERATION_RETENTION")
	}
	dailyUsage, err := durationEnvBetween("DAILY_USAGE_RETENTION", defaultDailyUsageRetention, 7*24*time.Hour, 365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	inviteHistory, err := durationEnvBetween("INVITE_HISTORY_RETENTION", defaultInviteRetention, 7*24*time.Hour, 2*365*24*time.Hour)
	if err != nil {
		return RetentionPolicy{}, err
	}
	return RetentionPolicy{
		GenerationHistory:  generation,
		GenerationMedia:    generation,
		PinnedMedia:        pinnedMedia,
		AIContent:          aiContent,
		ComfyInputs:        comfyInputs,
		HostMetrics:        hostMetrics,
		AuditLog:           auditLog,
		ProxyRequests:      proxyRequests,
		WebSocketSessions:  websocketSessions,
		GenerationRequests: generationRequests,
		DailyUsage:         dailyUsage,
		InviteHistory:      inviteHistory,
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func requiredEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid URL %q", raw)
	}
	if parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("must be an origin URL without credentials, path, query or fragment")
	}
	parsed.Path = ""
	return parsed, nil
}

func parseUpstream(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid URL %q", raw)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("credentials must use the dedicated upstream environment variables")
	}
	parsed.Fragment = ""
	return parsed, nil
}

func upstreamBasicAuth(prefix string) (string, error) {
	username := env(prefix+"_UPSTREAM_USERNAME", "")
	password := env(prefix+"_UPSTREAM_PASSWORD", "")
	if username == "" && password == "" {
		return "", nil
	}
	if username == "" || password == "" {
		return "", fmt.Errorf("%s upstream credentials must include both username and password", prefix)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return "Basic " + encoded, nil
}

func parseNetworks(raw string) ([]*net.IPNet, error) {
	var networks []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			bits := 128
			if ipv4 := ip.To4(); ipv4 != nil {
				ip = ipv4
				bits = 32
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("invalid network %q", item)
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, fmt.Errorf("at least one network is required")
	}
	return networks, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", key, raw)
	}
	return duration, nil
}

func durationEnvBetween(key string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value, err := durationEnv(key, fallback)
	if err != nil {
		return 0, err
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", key, minimum, maximum)
	}
	return value, nil
}

func integerEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q", key, raw)
	}
	return value, nil
}

func booleanEnv(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", key, raw)
	}
	return value, nil
}
