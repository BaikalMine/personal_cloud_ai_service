//go:build windows

package modelruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipGeneration(t *testing.T) {
	r, _ := newFixture(t, "normal")
	first, err := r.Advance(context.Background())
	if err != nil || first.Generation != 1 || len(first.ID) != 64 || len(first.Token) != 64 {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := r.Advance(context.Background())
	if err != nil || second.ID != first.ID || second.Generation != 2 || second.Token == first.Token {
		t.Fatalf("advance: %+v %v", second, err)
	}
	if _, err = New(r.config); err == nil {
		t.Fatal("second owner accepted")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(r.config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	third, err := next.Advance(context.Background())
	if err != nil || third.ID != first.ID || third.Generation != 3 || third.Token == second.Token {
		t.Fatalf("restart: %+v %v", third, err)
	}
}

func TestOwnershipCorruptState(t *testing.T) {
	r, _ := newFixture(t, "normal")
	if _, err := r.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.config.StateDirectory, "model-owner.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(context.Background()); err == nil {
		t.Fatal("corrupt state accepted")
	}
}
