package loratraining

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestArtifactRelativeNamePreservesInstalledLayout(t *testing.T) {
	for _, owner := range []string{"alice", "Alice Smith", "__--", "", strings.Repeat("x", 80), "fixture.user"} {
		name, err := ArtifactRelativeName(owner, "krea2", "portrait.safetensors")
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(owner))))
		if !strings.HasPrefix(name, "Trained/Krea2/") || !strings.HasSuffix(name, "-"+hex.EncodeToString(hash[:4])+"/portrait.safetensors") || strings.Contains(name, "\\") {
			t.Fatal("installed layout changed", name)
		}
	}
	first, _ := ArtifactRelativeName("alice", "krea2", "portrait.safetensors")
	second, _ := ArtifactRelativeName("bob", "krea2", "portrait.safetensors")
	flux, _ := ArtifactRelativeName("alice", "flux2-klein", "portrait.safetensors")
	if first == second || first == flux || !strings.HasPrefix(flux, "Trained/Flux2-Klein/") {
		t.Fatal("artifact namespaces were conflated")
	}
}

func TestArtifactRelativeNameRejectsUnrelatedPaths(t *testing.T) {
	for _, name := range []string{"", "../portrait.safetensors", "alice/portrait.safetensors", `C:\portrait.safetensors`, "portrait.safetensors/extra", "portrait.exe", ".safetensors", "file\x00.safetensors"} {
		if _, err := ArtifactRelativeName("alice", "krea2", name); err == nil {
			t.Fatal("invalid artifact accepted", name)
		}
	}
	if _, err := ArtifactRelativeName("alice", "video", "portrait.safetensors"); err == nil {
		t.Fatal("unrelated family accepted")
	}
}
