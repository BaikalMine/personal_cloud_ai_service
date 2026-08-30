package miningagent

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
)

var defaultMinerArchivePrefixes = []string{
	"https://github.com/doktor83/SRBMiner-Multi/releases/download/",
}

var githubArchiveRedirectHosts = []string{
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

type archiveSourcePolicy struct {
	prefixes           []*url.URL
	assetRedirectHosts map[string]struct{}
}

func newArchiveSourcePolicy(rawPrefixes []string) (archiveSourcePolicy, error) {
	if len(rawPrefixes) == 0 {
		rawPrefixes = defaultMinerArchivePrefixes
	}
	policy := archiveSourcePolicy{assetRedirectHosts: make(map[string]struct{})}
	for _, raw := range rawPrefixes {
		parsed, err := validateArchiveURL(raw)
		if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" {
			return archiveSourcePolicy{}, errors.New("доверенный источник обновлений должен быть HTTPS-префиксом без query и fragment")
		}
		if !strings.HasSuffix(parsed.Path, "/") {
			parsed.Path += "/"
		}
		policy.prefixes = append(policy.prefixes, parsed)
	}
	for _, hostname := range githubArchiveRedirectHosts {
		policy.assetRedirectHosts[hostname] = struct{}{}
	}
	return policy, nil
}

func (p archiveSourcePolicy) validateInitial(raw string) (*url.URL, error) {
	parsed, err := validateArchiveURL(raw)
	if err != nil {
		return nil, err
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("исходная ссылка на обновление не должна содержать query или fragment")
	}
	if p.matchesTrustedPrefix(parsed) {
		return parsed, nil
	}
	return nil, errors.New("ссылка не относится к доверенному источнику обновлений майнера")
}

func (p archiveSourcePolicy) matchesTrustedPrefix(parsed *url.URL) bool {
	for _, prefix := range p.prefixes {
		if strings.EqualFold(parsed.Hostname(), prefix.Hostname()) && strings.HasPrefix(parsed.Path, prefix.Path) {
			return true
		}
	}
	return false
}

func (p archiveSourcePolicy) validateRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil || len(via) == 0 || via[0] == nil || via[0].URL == nil {
		return errors.New("перенаправление не связано с проверенной исходной ссылкой")
	}
	if _, err := p.validateInitial(via[0].URL.String()); err != nil {
		return errors.New("исходная ссылка перенаправления больше не соответствует доверенному источнику")
	}
	parsed, err := validateArchiveURL(request.URL.String())
	if err != nil {
		return err
	}
	if p.matchesTrustedPrefix(parsed) {
		return nil
	}
	if _, allowed := p.assetRedirectHosts[strings.ToLower(parsed.Hostname())]; allowed {
		return nil
	}
	return errors.New("перенаправление ведёт за пределы доверенных источников обновлений")
}

var reservedArchivePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func validateArchiveURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" {
		return nil, errors.New("ссылка на обновление должна быть HTTPS-ссылкой без логина, пароля и нестандартного порта")
	}
	if err := validateArchivePath(parsed); err != nil {
		return nil, err
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" || hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") {
		return nil, errors.New("ссылки на локальные и приватные адреса запрещены")
	}
	if ip, err := netip.ParseAddr(hostname); err == nil && forbiddenArchiveIP(ip) {
		return nil, errors.New("ссылки на локальные и приватные адреса запрещены")
	}
	return parsed, nil
}

func validateArchivePath(parsed *url.URL) error {
	if parsed == nil || parsed.Path == "" || strings.ContainsAny(parsed.Path, "\\\x00") {
		return errors.New("ссылка на обновление содержит некорректный путь")
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return errors.New("закодированные разделители пути в ссылке на обновление запрещены")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return errors.New("переходы между каталогами в ссылке на обновление запрещены")
		}
	}
	cleaned := path.Clean(parsed.Path)
	if cleaned != parsed.Path && cleaned+"/" != parsed.Path {
		return errors.New("ссылка на обновление должна содержать канонический путь")
	}
	return nil
}

func forbiddenArchiveIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return true
	}
	for _, prefix := range reservedArchivePrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
