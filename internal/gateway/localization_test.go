package gateway

import (
	"strings"
	"testing"
)

func TestRussianLabels(t *testing.T) {
	if got := roleLabel("admin"); got != "администратор" {
		t.Fatalf("roleLabel(admin) = %q", got)
	}
	if got := inviteStatusLabel("revoked"); got != "отозвано" {
		t.Fatalf("inviteStatusLabel(revoked) = %q", got)
	}
	if got := auditActionLabel("user_login_failed"); got != "неудачная попытка входа" {
		t.Fatalf("auditActionLabel(user_login_failed) = %q", got)
	}
	if got := auditTargetLabel("session"); got != "сессия" {
		t.Fatalf("auditTargetLabel(session) = %q", got)
	}
}

func TestAuditMetadataLabel(t *testing.T) {
	got := auditMetadataLabel(`{"username":"alice","reason":"account_locked","comfyui":true}`)
	for _, expected := range []string{"логин: alice", "причина: аккаунт заблокирован", "ComfyUI: да"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("auditMetadataLabel() = %q, want fragment %q", got, expected)
		}
	}
	if got := auditMetadataLabel(`{}`); got != "-" {
		t.Fatalf("empty audit metadata = %q, want -", got)
	}
}

func TestGatewayTemplatesHaveRussianDocumentLanguage(t *testing.T) {
	body, err := embeddedFS.ReadFile("templates/_layout.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `<html lang="ru">`) {
		t.Fatal("gateway document language must be Russian")
	}
}
