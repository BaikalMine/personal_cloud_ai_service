package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-access-gateway/internal/updates"
)

func (s *Server) handleModelVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		methodNotAllowed(w, "POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in updates.ModelVerifyRequest
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	if !s.modelVerifyMu.TryLock() {
		http.Error(w, "verification busy", 409)
		return
	}
	defer s.modelVerifyMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 24*time.Minute)
	defer cancel()
	result, err := verifyModelFile(ctx, s.comfyTarget.WorkingDirectory, in)
	if err != nil {
		http.Error(w, "model verification unavailable", 409)
		return
	}
	writeJSON(w, 200, result)
}

func verifyModelFile(ctx context.Context, working string, in updates.ModelVerifyRequest) (updates.ModelVerification, error) {
	out := updates.ModelVerification{Protocol: 1}
	hash, err := hex.DecodeString(in.SHA256)
	if err != nil || len(hash) != sha256.Size || len(in.RelativePath) > 2048 || strings.ContainsAny(in.RelativePath, "\\:\x00") || !strings.HasSuffix(strings.ToLower(in.RelativePath), ".safetensors") {
		return out, errors.New("invalid verification identity")
	}
	for _, part := range strings.Split(in.RelativePath, "/") {
		if part == "" || part == "." || part == ".." {
			return out, errors.New("invalid relative path")
		}
	}
	root, err := modelCapacityRoot(working, in.Root)
	if err != nil {
		return out, err
	}
	before, err := os.Stat(root)
	if err != nil {
		return out, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return out, err
	}
	defer dir.Close()
	opened, err := dir.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return out, errors.New("model root changed")
	}
	name := filepath.FromSlash(in.RelativePath)
	// Both supported download backends keep a sidecar while writing the file.
	partial := func() (bool, error) {
		for _, suffix := range []string{".part", ".aria2"} {
			if _, err := dir.Lstat(name + suffix); err == nil {
				return true, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
		return false, nil
	}
	if active, err := partial(); err != nil {
		return out, err
	} else if active {
		out.State = "incomplete"
		return out, nil
	}
	initial, err := dir.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		out.State = "missing"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !initial.Mode().IsRegular() || initial.Size() <= 0 || initial.Size() > 200<<30 {
		return out, errors.New("invalid model file")
	}
	out.Size = initial.Size()
	matched, err := hasExpectedSHA256(ctx, dir, name, in.SHA256, initial)
	if err != nil {
		return out, err
	}
	current, err := dir.Lstat(name)
	if err != nil {
		return out, err
	}
	if !sameComfyCleanupEntry(initial, current) {
		out.State = "changed"
		return out, nil
	}
	if active, err := partial(); err != nil {
		return out, err
	} else if active {
		out.State = "incomplete"
		return out, nil
	}
	out.State = "mismatch"
	if matched {
		out.State = "verified"
	}
	return out, nil
}
