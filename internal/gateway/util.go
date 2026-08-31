package gateway

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *App) clientIP(r *http.Request) string {
	remote := clientIPFromRemote(r.RemoteAddr)
	if a.isTrustedForwarder(remote) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			ip := net.ParseIP(candidate)
			if ip == nil {
				continue
			}
			if !a.isTrustedForwarder(candidate) {
				return ip.String()
			}
		}
		if len(parts) > 0 {
			if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
				return ip.String()
			}
		}
	}
	if ip := net.ParseIP(remote); ip != nil {
		return ip.String()
	}
	return truncate(remote, 64)
}

func clientIPFromRemote(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func (a *App) isTrustedForwarder(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, network := range a.cfg.TrustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *App) isAdminAllowedIP(clientIP string) bool {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	for _, network := range a.cfg.AdminAllowedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func safeNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || strings.ContainsAny(next, "\\\r\n\x00") {
		return ""
	}
	parsed, err := url.ParseRequestURI(next)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" ||
		!strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") {
		return ""
	}
	return next
}

func validFormOrigin(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" && parsed.Path != "/" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func (a *App) requestProto(r *http.Request) string {
	if a.cfg.PublicURL != nil && strings.EqualFold(r.Host, a.cfg.PublicURL.Host) {
		return a.cfg.PublicURL.Scheme
	}
	remote := clientIPFromRemote(r.RemoteAddr)
	if a.isTrustedForwarder(remote) {
		value := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
		if value == "http" || value == "https" {
			return value
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func sanitizedPath(u *url.URL) string {
	if u.RawQuery == "" {
		return u.Path
	}
	q, _ := url.ParseQuery(u.RawQuery)
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"=redacted")
	}
	return u.Path + "?" + strings.Join(parts, "&")
}

func stripCookie(req *http.Request, name string) {
	raw := req.Header.Get("Cookie")
	if raw == "" {
		return
	}
	var kept []string
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, name+"=") {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		req.Header.Del("Cookie")
		return
	}
	req.Header.Set("Cookie", strings.Join(kept, "; "))
}

func rewriteSetCookiePath(resp *http.Response, prefix string) {
	values := resp.Header.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	resp.Header.Del("Set-Cookie")
	for _, v := range values {
		lower := strings.ToLower(v)
		if !strings.Contains(lower, "path=") {
			v += "; Path=" + prefix
		} else {
			parts := strings.Split(v, ";")
			for i, p := range parts {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "path=") {
					parts[i] = " Path=" + strings.TrimRight(prefix, "/")
				}
			}
			v = strings.Join(parts, ";")
		}
		resp.Header.Add("Set-Cookie", v)
	}
}

func rewriteLocation(resp *http.Response, prefix string, upstream *url.URL) {
	locations := resp.Header.Values("Location")
	if len(locations) == 0 {
		return
	}
	resp.Header.Del("Location")
	for _, raw := range locations {
		location, err := url.Parse(raw)
		if err != nil {
			resp.Header.Add("Location", raw)
			continue
		}
		if location.IsAbs() && location.Host == upstream.Host {
			location.Scheme = ""
			location.Host = ""
		}
		if location.Host == "" && strings.HasPrefix(location.Path, "/") && !strings.HasPrefix(location.Path, prefix) {
			location.Path = singleJoiningSlash(prefix, location.Path)
			location.RawPath = ""
		}
		resp.Header.Add("Location", location.String())
	}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func formatTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Local().Format("02.01.2006 15:04")
	case *time.Time:
		if t != nil {
			return t.Local().Format("02.01.2006 15:04")
		}
	case sql.NullTime:
		if t.Valid {
			return t.Time.Local().Format("02.01.2006 15:04")
		}
	}
	return "-"
}

func formatBytes(v any) string {
	var n int64
	switch x := v.(type) {
	case int64:
		n = x
	case int:
		n = int64(x)
	case *int64:
		if x != nil {
			n = *x
		}
	default:
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func formatNumber(v any) string {
	var n int64
	switch x := v.(type) {
	case int64:
		n = x
	case int:
		n = int64(x)
	default:
		return "0"
	}
	digits := strconv.FormatInt(n, 10)
	start := 0
	if strings.HasPrefix(digits, "-") {
		start = 1
	}
	for i := len(digits) - 3; i > start; i -= 3 {
		digits = digits[:i] + "\u00a0" + digits[i:]
	}
	return digits
}

func formatDuration(v any) string {
	var ms int64
	switch x := v.(type) {
	case int64:
		ms = x
	case int:
		ms = int64(x)
	default:
		return "0 мс"
	}
	if ms < 1000 {
		return fmt.Sprintf("%d мс", ms)
	}
	return fmt.Sprintf("%.2f с", float64(ms)/1000)
}

func accountLifetimeLabel(seconds int64) string {
	if seconds <= 0 {
		return "постоянно"
	}
	days := seconds / int64((24 * time.Hour).Seconds())
	if days > 0 && seconds%int64((24*time.Hour).Seconds()) == 0 {
		return fmt.Sprintf("%d %s", days, russianDayLabel(days))
	}
	hours := seconds / int64(time.Hour.Seconds())
	if hours > 0 && seconds%int64(time.Hour.Seconds()) == 0 {
		return fmt.Sprintf("%d %s", hours, russianHourLabel(hours))
	}
	return fmt.Sprintf("%d мин.", seconds/60)
}

func russianDayLabel(value int64) string {
	lastTwo := value % 100
	if lastTwo >= 11 && lastTwo <= 14 {
		return "дней"
	}
	switch value % 10 {
	case 1:
		return "день"
	case 2, 3, 4:
		return "дня"
	default:
		return "дней"
	}
}

func russianHourLabel(value int64) string {
	lastTwo := value % 100
	if lastTwo >= 11 && lastTwo <= 14 {
		return "часов"
	}
	switch value % 10 {
	case 1:
		return "час"
	case 2, 3, 4:
		return "часа"
	default:
		return "часов"
	}
}

func russianFileLabel(value int64) string {
	lastTwo := value % 100
	if lastTwo >= 11 && lastTwo <= 14 {
		return "файлов"
	}
	switch value % 10 {
	case 1:
		return "файл"
	case 2, 3, 4:
		return "файла"
	default:
		return "файлов"
	}
}

func pct(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

func divFloat(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) * 100 / float64(denominator)
}
