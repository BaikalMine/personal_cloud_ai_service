package loratraining

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

var artifactOwnerPattern = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
var artifactFilenamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,127}\.safetensors$`)

// ArtifactRelativeName mirrors the installed training layout, not authorization.
// Callers must first resolve an owned training record and its actual artifact.
func ArtifactRelativeName(owner, family, artifactName string) (string, error) {
	if !artifactFilenamePattern.MatchString(artifactName) {
		return "", errors.New("invalid trained adapter filename")
	}
	var directory string
	switch family {
	case "krea2":
		directory = "Krea2"
	case "flux2-klein":
		directory = "Flux2-Klein"
	default:
		return "", errors.New("unsupported training family")
	}
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(owner))))
	label := strings.Trim(artifactOwnerPattern.ReplaceAllString(owner, "_"), "_-")
	if label == "" {
		label = "user"
	}
	if len(label) > 40 {
		label = label[:40]
	}
	return "Trained/" + directory + "/" + label + "-" + hex.EncodeToString(hash[:4]) + "/" + artifactName, nil
}
