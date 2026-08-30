package miningagent

import (
	"net/http"
	"net/netip"
	"net/url"
	"testing"
)

func TestValidateArchiveURLRejectsLocalTargets(t *testing.T) {
	for _, value := range []string{
		"http://example.com/miner.zip",
		"https://localhost/miner.zip",
		"https://host.local/miner.zip",
		"https://127.0.0.1/miner.zip",
		"https://10.0.0.1/miner.zip",
		"https://100.64.0.1/miner.zip",
		"https://192.0.2.1/miner.zip",
		"https://[::1]/miner.zip",
		"https://github.com:4444/example/miner.zip",
	} {
		if _, err := validateArchiveURL(value); err == nil {
			t.Fatalf("unsafe URL %q was accepted", value)
		}
	}
	if _, err := validateArchiveURL("https://github.com/example/miner.zip"); err != nil {
		t.Fatalf("public HTTPS URL was rejected: %v", err)
	}
}

func TestArchiveSourcePolicyRestrictsInitialPublisher(t *testing.T) {
	policy, err := newArchiveSourcePolicy([]string{"https://github.com/doktor83/SRBMiner-Multi/releases/download/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.validateInitial("https://github.com/doktor83/SRBMiner-Multi/releases/download/2.7.8/miner.zip"); err != nil {
		t.Fatalf("official release rejected: %v", err)
	}
	for _, candidate := range []string{
		"https://github.com/attacker/SRBMiner-Multi/releases/download/2.7.8/miner.zip",
		"https://example.com/miner.zip",
		"https://github.com/doktor83/SRBMiner-Multi/releases/download/../../../../attacker/repo/releases/download/tag/miner.zip",
		"https://github.com/doktor83/SRBMiner-Multi/releases/download/%2e%2e/%2e%2e/attacker/miner.zip",
		"https://github.com/doktor83/SRBMiner-Multi/releases/download%2fattacker/miner.zip",
	} {
		if _, err := policy.validateInitial(candidate); err == nil {
			t.Fatalf("untrusted publisher %q accepted", candidate)
		}
	}
	initial, _ := url.Parse("https://github.com/doktor83/SRBMiner-Multi/releases/download/2.7.8/miner.zip")
	asset, _ := url.Parse("https://release-assets.githubusercontent.com/github-production-release-asset/file.zip?signature=test")
	if err := policy.validateRedirect(&http.Request{URL: asset}, []*http.Request{{URL: initial}}); err != nil {
		t.Fatalf("GitHub asset redirect rejected: %v", err)
	}
	foreignInitial, _ := url.Parse("https://github.com/attacker/repo/releases/download/tag/miner.zip")
	if err := policy.validateRedirect(&http.Request{URL: asset}, []*http.Request{{URL: foreignInitial}}); err == nil {
		t.Fatal("asset redirect from an untrusted repository was accepted")
	}
}

func TestForbiddenArchiveIP(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "169.254.1.2", "192.168.1.1", "100.64.1.2", "198.18.0.1", "2001:db8::1", "::1"} {
		if !forbiddenArchiveIP(netip.MustParseAddr(value)) {
			t.Fatalf("reserved IP %s was accepted", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if forbiddenArchiveIP(netip.MustParseAddr(value)) {
			t.Fatalf("public IP %s was rejected", value)
		}
	}
}
