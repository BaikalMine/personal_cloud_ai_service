package security

import "testing"

func TestRandomTokenAndHash(t *testing.T) {
	first, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 40 {
		t.Fatal("random tokens are not sufficiently distinct")
	}
	if got := HashToken(first); len(got) != 64 || got != HashToken(first) {
		t.Fatalf("unexpected token hash %q", got)
	}
}

func TestCSRFSigner(t *testing.T) {
	signer := NewCSRFSigner("01234567890123456789012345678901")
	token := signer.Token("session-token")
	if !signer.Verify("session-token", token) {
		t.Fatal("valid csrf token did not verify")
	}
	if signer.Verify("different-session", token) || signer.Verify("session-token", token+"x") {
		t.Fatal("invalid csrf token verified")
	}
}
