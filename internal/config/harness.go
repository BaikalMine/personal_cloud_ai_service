package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Only ModelKey is exposed to the agent; native ownership and browser bootstrap
// use separate credentials which are never sent to model tools or browsers.
type HarnessConfig struct {
	Upstream            *url.URL
	BootstrapURL        *url.URL
	BootstrapKey        string
	ModelUpstream       *url.URL
	ModelKey            string
	ClientKey           string
	AdmissionKey        string
	IngressHost         string
	IngressPeers        []*net.IPNet
	PauseMining         bool
	MiningUserID        int64
	UltraKey            string
	UltraIdleCacheBytes int64
	UltraHistoryURL     *url.URL
	UltraHistoryKey     string
	AgentAPIKey         string
	TenantURL           *url.URL
	TenantKey           string
}

func (h HarnessConfig) Enabled() bool { return h.Upstream != nil }

func loadHarnessConfig() (HarnessConfig, error) {
	var h HarnessConfig
	if strings.TrimSpace(env("HARNESS_UPSTREAM", "")) == "" {
		return h, nil
	}
	var err error
	idleCache, err := strconv.ParseInt(env("HARNESS_ULTRA_IDLE_CACHE_MIB", "256"), 10, 64)
	if err != nil || idleCache < 0 || idleCache > 512 {
		return h, fmt.Errorf("HARNESS_ULTRA_IDLE_CACHE_MIB must be between 0 and 512")
	}
	h.UltraIdleCacheBytes = idleCache << 20
	h.PauseMining, err = strconv.ParseBool(env("HARNESS_PAUSE_MINING", "false"))
	if err != nil {
		return h, fmt.Errorf("HARNESS_PAUSE_MINING requires a boolean")
	}
	if value := strings.TrimSpace(env("HARNESS_MINING_USER_ID", "")); value != "" {
		h.MiningUserID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || h.MiningUserID <= 0 {
			return h, fmt.Errorf("HARNESS_MINING_USER_ID requires a positive account ID")
		}
	}
	if h.PauseMining && h.MiningUserID == 0 {
		return h, fmt.Errorf("Harness automatic mining requires an explicit operator account")
	}
	for key, target := range map[string]**url.URL{
		"HARNESS_UPSTREAM": &h.Upstream, "HARNESS_BOOTSTRAP_URL": &h.BootstrapURL,
		"HARNESS_MODEL_UPSTREAM": &h.ModelUpstream,
	} {
		u, err := url.Parse(strings.TrimSpace(env(key, "")))
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return HarnessConfig{}, fmt.Errorf("%s requires an HTTP origin without credentials or query", key)
		}
		*target = u
	}
	h.IngressHost = strings.TrimSpace(env("HARNESS_INGRESS_HOST", ""))
	peers := strings.TrimSpace(env("HARNESS_INGRESS_PEERS", ""))
	if (h.IngressHost == "") != (peers == "") {
		return HarnessConfig{}, fmt.Errorf("HARNESS_INGRESS_HOST and HARNESS_INGRESS_PEERS must be set together")
	}
	if h.IngressHost != "" {
		u, err := url.Parse("http://" + h.IngressHost)
		if err != nil || u.Host != h.IngressHost || u.Hostname() == "" || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return HarnessConfig{}, fmt.Errorf("HARNESS_INGRESS_HOST requires a host with optional port")
		}
		h.IngressPeers, err = parseNetworks(peers)
		if err != nil || len(h.IngressPeers) == 0 {
			return HarnessConfig{}, fmt.Errorf("HARNESS_INGRESS_PEERS requires explicit proxy IP addresses or networks")
		}
	}
	seen := map[string]bool{}
	historyURL := strings.TrimSpace(env("HARNESS_ULTRA_HISTORY_URL", ""))
	h.UltraHistoryKey = strings.TrimSpace(env("HARNESS_ULTRA_HISTORY_KEY", ""))
	if (historyURL == "") != (h.UltraHistoryKey == "") {
		return HarnessConfig{}, fmt.Errorf("HARNESS_ULTRA_HISTORY_URL and HARNESS_ULTRA_HISTORY_KEY must be set together")
	}
	if historyURL != "" {
		u, err := url.Parse(historyURL)
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return HarnessConfig{}, fmt.Errorf("HARNESS_ULTRA_HISTORY_URL requires an HTTP origin")
		}
		if len(h.UltraHistoryKey) < 32 || len(h.UltraHistoryKey) > 256 || strings.IndexFunc(h.UltraHistoryKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return HarnessConfig{}, fmt.Errorf("HARNESS_ULTRA_HISTORY_KEY requires a 32-256 character ASCII key")
		}
		h.UltraHistoryURL = u
		seen[h.UltraHistoryKey] = true
	}
	h.UltraKey = strings.TrimSpace(env("HARNESS_ULTRA_BROKER_KEY", ""))
	if h.UltraKey != "" {
		if len(h.UltraKey) < 32 || len(h.UltraKey) > 256 || strings.IndexFunc(h.UltraKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || seen[h.UltraKey] {
			return HarnessConfig{}, fmt.Errorf("HARNESS_ULTRA_BROKER_KEY requires a 32-256 character ASCII key")
		}
		seen[h.UltraKey] = true
	}
	for key, target := range map[string]*string{
		"HARNESS_BOOTSTRAP_KEY": &h.BootstrapKey, "HARNESS_MODEL_KEY": &h.ModelKey,
		"HARNESS_CLIENT_KEY": &h.ClientKey, "HARNESS_ADMISSION_KEY": &h.AdmissionKey,
	} {
		value := strings.TrimSpace(env(key, ""))
		if len(value) < 32 || len(value) > 256 || strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || seen[value] {
			return HarnessConfig{}, fmt.Errorf("%s requires a distinct 32-256 character ASCII key", key)
		}
		seen[value] = true
		*target = value
	}
	h.AgentAPIKey = strings.TrimSpace(env("HARNESS_AGENT_API_KEY", ""))
	tenantURL := strings.TrimSpace(env("HARNESS_TENANT_URL", ""))
	h.TenantKey = strings.TrimSpace(env("HARNESS_TENANT_KEY", ""))
	if (tenantURL == "") != (h.TenantKey == "") {
		return HarnessConfig{}, fmt.Errorf("Harness tenant URL and key must be configured together")
	}
	if tenantURL != "" {
		u, err := url.Parse(tenantURL)
		if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.RawPath != "" || u.Fragment != "" || u.ForceQuery {
			return HarnessConfig{}, fmt.Errorf("HARNESS_TENANT_URL requires an internal HTTP origin")
		}
		if len(h.TenantKey) < 32 || len(h.TenantKey) > 256 || seen[h.TenantKey] || h.TenantKey == h.AgentAPIKey || strings.IndexFunc(h.TenantKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return HarnessConfig{}, fmt.Errorf("HARNESS_TENANT_KEY requires a distinct ASCII key")
		}
		h.TenantURL = u
	}
	if h.AgentAPIKey != "" && (len(h.AgentAPIKey) < 32 || len(h.AgentAPIKey) > 256 || seen[h.AgentAPIKey] ||
		strings.IndexFunc(h.AgentAPIKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0) {
		return HarnessConfig{}, fmt.Errorf("HARNESS_AGENT_API_KEY requires a distinct 32-256 character ASCII key")
	}
	return h, nil
}
