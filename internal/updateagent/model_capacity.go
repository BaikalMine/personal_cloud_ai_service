package updateagent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ai-access-gateway/internal/updates"
)

func modelCapacityRoot(working, root string) (string, error) {
	if working == "" || !filepath.IsAbs(root) || len(root) > 2048 {
		return "", errors.New("invalid model root")
	}
	base, err := filepath.EvalSymlinks(filepath.Join(working, "models"))
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", errors.New("model root outside ComfyUI")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return path, nil
}
func (s *Server) handleModelCapacity(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		methodNotAllowed(w, "POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in struct {
		Root string `json:"root"`
	}
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	root, err := modelCapacityRoot(s.comfyTarget.WorkingDirectory, in.Root)
	if err != nil {
		http.Error(w, "model root unavailable", 409)
		return
	}
	free, err := modelDiskFree(root)
	if err != nil {
		http.Error(w, "disk capacity unavailable", 503)
		return
	}
	writeJSON(w, 200, updates.ModelCapacity{Protocol: 1, FreeBytes: free})
}
