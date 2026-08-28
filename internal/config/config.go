package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
)

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
	PromptAssistantModel      string
	ComfyUIUpstreamAuthHeader string
	OpenWebUIUpstreamAuth     string
	MiningAgentURL            *url.URL
	MiningAgentToken          string
	SystemMonitorAgentURL     *url.URL
	SystemMonitorAgentToken   string
	UpdateAgentURL            *url.URL
	UpdateAgentToken          string
	TrustedProxies            []*net.IPNet
	AdminAllowedNetworks      []*net.IPNet
	SessionTTL                time.Duration
	SessionIdleTimeout        time.Duration
	AccountLockThreshold      int
	AccountLockDuration       time.Duration
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

	databaseURL := requiredEnv("DATABASE_URL")
	adminUsername := strings.TrimSpace(env("ADMIN_USERNAME", "admin"))
	adminPassword := requiredEnv("ADMIN_PASSWORD")
	sessionSecret := requiredEnv("SESSION_SECRET")
	miningAgentToken := requiredEnv("MINING_AGENT_TOKEN")
	systemMonitorAgentToken := strings.TrimSpace(env("SYSTEM_MONITOR_AGENT_TOKEN", miningAgentToken))
	updateAgentToken := requiredEnv("UPDATE_AGENT_TOKEN")
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
		PromptAssistantModel:      promptAssistantModel,
		ComfyUIUpstreamAuthHeader: comfyAuth,
		OpenWebUIUpstreamAuth:     openWebUIAuth,
		MiningAgentURL:            miningAgentURL,
		MiningAgentToken:          miningAgentToken,
		SystemMonitorAgentURL:     systemMonitorAgentURL,
		SystemMonitorAgentToken:   systemMonitorAgentToken,
		UpdateAgentURL:            updateAgentURL,
		UpdateAgentToken:          updateAgentToken,
		TrustedProxies:            trustedProxies,
		AdminAllowedNetworks:      adminNetworks,
		SessionTTL:                sessionTTL,
		SessionIdleTimeout:        sessionIdleTimeout,
		AccountLockThreshold:      accountLockThreshold,
		AccountLockDuration:       accountLockDuration,
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
