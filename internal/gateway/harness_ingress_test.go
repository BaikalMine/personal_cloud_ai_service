package gateway

import (
	"net"
	"net/http/httptest"
	"testing"

	"ai-access-gateway/internal/config"
)

func TestHarnessTrustedIngress(t *testing.T) {
	_, network, _ := net.ParseCIDR("10.0.0.7/32")
	app := &App{cfg: Config{PublicURL: harnessURL(t, "https://public.example"), Harness: config.HarnessConfig{
		IngressHost: "localhost:8080", IngressPeers: []*net.IPNet{network},
	}}}
	for _, tc := range []struct {
		name, host, peer, origin, forwarded string
		normalized                          bool
		rejection                           string
	}{
		{"trusted navigation", "localhost:8080", "10.0.0.7:40000", "", "", true, ""},
		{"trusted same-origin upload", "localhost:8080", "10.0.0.7:40000", "https://public.example", "", true, ""},
		{"trusted foreign origin", "localhost:8080", "10.0.0.7:40000", "https://evil.example", "", true, "origin_rejected"},
		{"wrong peer", "localhost:8080", "10.0.0.8:40000", "", "", false, "host_mismatch"},
		{"forwarding spoof", "localhost:8080", "10.0.0.8:40000", "", "10.0.0.7", false, "host_mismatch"},
		{"wrong host", "evil.example", "10.0.0.7:40000", "", "", false, "host_mismatch"},
		{"malformed peer", "localhost:8080", "invalid", "", "", false, "host_mismatch"},
		{"canonical host", "public.example", "10.0.0.8:40000", "https://public.example", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+tc.host+"/harness/upload", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			r.Header.Set("X-Forwarded-Host", "public.example")
			result := app.harnessIngressRequest(r)
			if (result != r) != tc.normalized || r.Host != tc.host {
				t.Fatal("incorrect normalization or original request mutated")
			}
			if got := app.harnessSourceRejection(result); got != tc.rejection {
				t.Fatalf("rejection=%q want %q", got, tc.rejection)
			}
		})
	}
}

func TestHarnessIngressDiagnosticsExcludeArbitraryHosts(t *testing.T) {
	for _, host := range []string{"secret.example:443", "token@example:443", "localhost:invalid", "private-token", "localhost:65536"} {
		if harnessIngressHostLabel(host) != "other" {
			t.Fatal("arbitrary host leaked into diagnostics")
		}
	}
}

func TestHarnessTrustedIngressMultipleDockerGateways(t *testing.T) {
	peers := make([]*net.IPNet, 0, 2)
	for _, cidr := range []string{"172.19.0.1/32", "172.20.0.1/32"} {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, network)
	}
	app := &App{cfg: Config{PublicURL: harnessURL(t, "https://public.example"), Harness: config.HarnessConfig{
		IngressHost: "192.168.1.86", IngressPeers: peers,
	}}}
	for _, peer := range []string{"172.19.0.1", "172.20.0.1", "172.19.0.2", "172.20.0.2"} {
		r := httptest.NewRequest("POST", "http://192.168.1.86/harness/upload", nil)
		r.RemoteAddr = peer + ":40000"
		r.Header.Set("Origin", "https://public.example")
		r.Header.Set("X-Forwarded-For", "172.19.0.1")
		want := "host_mismatch"
		if peer == "172.19.0.1" || peer == "172.20.0.1" {
			want = ""
		}
		if got := app.harnessSourceRejection(app.harnessIngressRequest(r)); got != want {
			t.Fatalf("peer %s rejection=%q want %q", peer, got, want)
		}
	}
}
