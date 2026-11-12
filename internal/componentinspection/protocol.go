package componentinspection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"
	"unicode"
)

const MaxReferences = 64
const MaxFileBytes int64 = 64 << 30
const Source = "configured_generation_directories"

type Reference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func ValidKind(kind string) bool {
	switch kind {
	case "checkpoint", "diffusion_model", "text_encoder", "vae", "lora", "upscale_model", "interpolation_model", "lut", "model":
		return true
	}
	return false
}

func NormalizeName(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || len(name) > 1024 || strings.Contains(name, ":") || strings.HasPrefix(name, "/") || path.Clean(name) != name ||
		name == ".." || strings.HasPrefix(name, "../") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return "", false
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".safetensors", ".gguf", ".pth", ".pt", ".ckpt", ".bin", ".cube", ".3dl":
		return name, true
	}
	return "", false
}

func (ref Reference) Key() string { return ref.Kind + "\x00" + ref.Name }
func (ref Reference) Valid() bool {
	name, ok := NormalizeName(ref.Name)
	return ValidKind(ref.Kind) && ok && name == ref.Name
}

type Request struct {
	Version    int         `json:"version"`
	RequestID  string      `json:"request_id"`
	References []Reference `json:"references"`
}

func (request Request) Valid() bool {
	if request.Version != 1 || len(request.RequestID) < 16 || len(request.RequestID) > 96 || len(request.References) == 0 || len(request.References) > MaxReferences {
		return false
	}
	for _, ch := range request.RequestID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	seen := map[string]bool{}
	for _, ref := range request.References {
		if !ref.Valid() || seen[ref.Key()] {
			return false
		}
		seen[ref.Key()] = true
	}
	return true
}

func (request Request) Digest() string {
	value, _ := json.Marshal(request)
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type Observation struct {
	Reference
	State     string    `json:"state"`
	Bytes     int64     `json:"bytes,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	HashedAt  time.Time `json:"hashed_at,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Cached    bool      `json:"cached,omitempty"`
}

type Response struct {
	Version       int           `json:"version"`
	RequestID     string        `json:"request_id"`
	RequestSHA256 string        `json:"request_sha256"`
	Source        string        `json:"source"`
	State         string        `json:"state"`
	Items         []Observation `json:"items"`
}

func (response Response) ValidFor(request Request) bool {
	if !request.Valid() || response.Version != 1 || response.RequestID != request.RequestID || response.RequestSHA256 != request.Digest() ||
		response.Source != Source || len(response.Items) != len(request.References) {
		return false
	}
	pending := false
	for index, item := range response.Items {
		if item.Reference != request.References[index] || item.CheckedAt.IsZero() || item.CheckedAt.After(time.Now().Add(time.Minute)) {
			return false
		}
		if item.State == "verified" {
			digest, err := hex.DecodeString(item.SHA256)
			if err != nil || len(digest) != 32 || strings.ToLower(item.SHA256) != item.SHA256 || item.Bytes < 0 || item.Bytes > MaxFileBytes ||
				item.HashedAt.IsZero() || item.HashedAt.After(item.CheckedAt) {
				return false
			}
		} else {
			if item.SHA256 != "" || item.Bytes != 0 || !item.HashedAt.IsZero() || item.Cached {
				return false
			}
			switch item.State {
			case "pending":
				pending = true
			case "missing", "unconfigured", "ambiguous", "unavailable", "unstable", "too_large":
			default:
				return false
			}
		}
	}
	return response.State == "pending" && pending || response.State == "complete" && !pending
}
