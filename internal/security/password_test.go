package security

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordLifecycle(t *testing.T) {
	password := "correct-horse-battery-staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("valid password did not verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("invalid password verified")
	}
	if PasswordHashNeedsUpgrade(hash) {
		t.Fatal("new password hash unexpectedly needs an upgrade")
	}
}

func TestLongPasswordLifecycle(t *testing.T) {
	password := strings.Repeat("д", 100)
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("long UTF-8 password did not verify")
	}
}

func TestLegacyBcryptPasswordIsAcceptedAndMarkedForUpgrade(t *testing.T) {
	password := "legacy-password"
	legacy, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(string(legacy), password) {
		t.Fatal("legacy bcrypt password was rejected")
	}
	if !PasswordHashNeedsUpgrade(string(legacy)) {
		t.Fatal("legacy bcrypt hash was not marked for upgrade")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("short password error = %v", err)
	}
	if err := ValidatePassword(strings.Repeat("x", MaxPasswordLength+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("long password error = %v", err)
	}
	if err := ValidatePassword("парол"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("five-rune UTF-8 password error = %v", err)
	}
}

func TestVerifyPasswordUsesDummyHashForUnknownUser(t *testing.T) {
	if VerifyPassword("", "not-the-dummy-password") {
		t.Fatal("empty hash must never authenticate")
	}
}
