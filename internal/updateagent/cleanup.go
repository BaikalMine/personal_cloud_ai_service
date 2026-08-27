package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ai-access-gateway/internal/updates"
)

// DeleteComfyOutputFiles removes an output only after its exact local bytes
// match a result that Gateway has already archived in its database.
func DeleteComfyOutputFiles(ctx context.Context, target ComfyTarget, files []updates.ComfyOutputFile) (updates.ComfyOutputDeleteResult, error) {
	directory := target.OutputDirectory
	if directory == "" {
		directory = filepath.Join(target.WorkingDirectory, "output")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return updates.ComfyOutputDeleteResult{}, err
	}
	info, err := os.Stat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return updates.ComfyOutputDeleteResult{Missing: len(files)}, nil
	}
	if err != nil {
		return updates.ComfyOutputDeleteResult{}, err
	}
	if !info.IsDir() {
		return updates.ComfyOutputDeleteResult{}, errors.New("ComfyUI output path is not a directory")
	}

	result := updates.ComfyOutputDeleteResult{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path, valid := safeComfyOutputPath(directory, file)
		if !valid {
			result.Rejected++
			continue
		}
		entry, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			result.Missing++
			continue
		}
		if err != nil {
			return result, err
		}
		if entry.Mode()&fs.ModeSymlink != 0 || !entry.Mode().IsRegular() || entry.Size() != file.SizeBytes {
			result.Mismatched++
			continue
		}
		matched, err := hasExpectedSHA256(path, file.SHA256)
		if err != nil {
			return result, err
		}
		if !matched {
			result.Mismatched++
			continue
		}
		if err := os.Remove(path); err != nil {
			return result, err
		}
		result.Deleted++
	}
	return result, nil
}

func safeComfyOutputPath(root string, file updates.ComfyOutputFile) (string, bool) {
	if file.StorageType != "output" || file.SizeBytes < 0 || !validSHA256(file.SHA256) {
		return "", false
	}
	filename := strings.TrimSpace(file.Filename)
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, "\\/") || filepath.Base(filename) != filename {
		return "", false
	}
	subfolder := strings.ReplaceAll(strings.TrimSpace(file.Subfolder), "\\", "/")
	if strings.ContainsRune(subfolder, 0) || strings.HasPrefix(subfolder, "/") || strings.Contains(subfolder, ":") {
		return "", false
	}
	cleanSubfolder := filepath.Clean(filepath.FromSlash(subfolder))
	if subfolder == "" || cleanSubfolder == "." {
		cleanSubfolder = ""
	}
	if cleanSubfolder == ".." || strings.HasPrefix(cleanSubfolder, ".."+string(filepath.Separator)) {
		return "", false
	}
	candidate := filepath.Join(root, cleanSubfolder, filename)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return candidate, true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hasExpectedSHA256(path, expected string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected), nil
}
