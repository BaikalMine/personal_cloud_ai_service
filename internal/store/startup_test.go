package store

import (
	"context"
	"errors"
	"testing"
)

func TestEnsureBootstrapAdminRejectsMissingCredentials(t *testing.T) {
	store := &Store{}
	tests := []struct {
		username string
		hash     string
	}{
		{username: "", hash: "hash"},
		{username: "   ", hash: "hash"},
		{username: "admin", hash: ""},
	}
	for _, test := range tests {
		err := store.EnsureBootstrapAdmin(context.Background(), test.username, test.hash)
		if !errors.Is(err, ErrInvalidBootstrapAdmin) {
			t.Fatalf("EnsureBootstrapAdmin(%q, %q) error = %v, want ErrInvalidBootstrapAdmin", test.username, test.hash, err)
		}
	}
}
