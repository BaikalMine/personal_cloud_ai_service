package gateway

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeArchiveSHA256(t *testing.T) {
	const checksum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherChecksum := strings.Repeat("a", 64)

	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "plain", value: checksum, want: checksum, ok: true},
		{name: "uppercase", value: strings.ToUpper(checksum), want: checksum, ok: true},
		{name: "prefixed", value: "sha256:" + checksum, want: checksum, ok: true},
		{name: "checksum file", value: checksum + "  SRBMiner-Multi.zip", want: checksum, ok: true},
		{name: "openssl", value: "SHA256(SRBMiner-Multi.zip)= " + strings.ToUpper(checksum), want: checksum, ok: true},
		{name: "certutil", value: "SHA256 hash of SRBMiner-Multi.zip:\r\n" + checksum + "\r\nCertUtil: -hashfile command completed successfully.", want: checksum, ok: true},
		{name: "grouped", value: "01234567 89abcdef 01234567 89abcdef 01234567 89abcdef 01234567 89abcdef", want: checksum, ok: true},
		{name: "empty", value: "", ok: false},
		{name: "short", value: checksum[:32], ok: false},
		{name: "non hexadecimal", value: strings.Repeat("g", 64), ok: false},
		{name: "embedded in longer hex", value: "f" + checksum, ok: false},
		{name: "ambiguous", value: checksum + "\n" + otherChecksum, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeArchiveSHA256(tt.value)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("normalizeArchiveSHA256(%q) = %q, %v; want %q, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMinerUpdateChecksumFieldAllowsStandardFormats(t *testing.T) {
	template, err := os.ReadFile("templates/admin_mining.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(template)
	for _, obsolete := range []string{`pattern="[A-Fa-f0-9]{64}"`, `minlength="64"`, `maxlength="64"`} {
		if strings.Contains(html, obsolete) {
			t.Fatalf("checksum input still contains restrictive attribute %s", obsolete)
		}
	}
	if !strings.Contains(html, `name="archive_sha256"`) || !strings.Contains(html, `maxlength="512"`) {
		t.Fatal("checksum input is missing its bounded flexible format")
	}
}
