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
	"time"

	"ai-access-gateway/internal/updates"
)

// DeleteComfyOutputFiles removes an output only after its exact local bytes
// match a result that Gateway has already archived in its database.
func DeleteComfyOutputFiles(ctx context.Context, target ComfyTarget, files []updates.ComfyOutputFile) (updates.ComfyOutputDeleteResult, error) {
	for _, file := range files {
		if file.StorageType != "output" {
			return updates.ComfyOutputDeleteResult{Rejected: len(files)}, nil
		}
	}
	return DeleteComfyAssetFiles(ctx, target, files)
}

func DeleteComfyAssetFiles(ctx context.Context, target ComfyTarget, files []updates.ComfyAssetFile) (updates.ComfyAssetDeleteResult, error) {
	result := updates.ComfyAssetDeleteResult{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		directory, valid := comfyAssetRoot(target, file.StorageType)
		if !valid {
			result.Rejected++
			result.Items = append(result.Items, comfyAssetOutcome(file, "rejected"))
			continue
		}
		outcome, err := deleteComfyAssetFile(ctx, directory, file)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, comfyAssetOutcome(file, outcome))
		switch outcome {
		case "deleted":
			result.Deleted++
		case "missing":
			result.Missing++
		case "mismatched":
			result.Mismatched++
		default:
			result.Rejected++
		}
	}
	return result, nil
}

func comfyAssetOutcome(file updates.ComfyAssetFile, status string) updates.ComfyAssetDeleteOutcome {
	return updates.ComfyAssetDeleteOutcome{
		Filename: file.Filename, Subfolder: file.Subfolder,
		StorageType: file.StorageType, SizeBytes: file.SizeBytes, SHA256: file.SHA256, Status: status,
	}
}

func comfyAssetRoot(target ComfyTarget, storageType string) (string, bool) {
	var directory string
	switch storageType {
	case "input":
		directory = target.InputDirectory
		if directory == "" {
			directory = filepath.Join(target.WorkingDirectory, "input")
		}
	case "output":
		directory = target.OutputDirectory
		if directory == "" {
			directory = filepath.Join(target.WorkingDirectory, "output")
		}
	default:
		return "", false
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return directory, true
	}
	if err != nil || !info.IsDir() {
		return "", false
	}
	return directory, true
}

func deleteComfyAssetFile(ctx context.Context, directory string, file updates.ComfyAssetFile) (string, error) {
	return examineComfyAssetFile(ctx, directory, file, true)
}

func examineComfyAssetFile(ctx context.Context, directory string, file updates.ComfyAssetFile, remove bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	relative, valid := safeComfyAssetRelativePath(file)
	if !valid {
		return "rejected", nil
	}
	root, err := os.OpenRoot(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	return examineComfyAssetAtRoot(ctx, root, relative, file, remove)
}

func examineComfyAssetAtRoot(ctx context.Context, root *os.Root, relative string, file updates.ComfyAssetFile, remove bool) (string, error) {
	entry, err := root.Lstat(relative)
	if errors.Is(err, fs.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if entry.Mode()&fs.ModeSymlink != 0 || !entry.Mode().IsRegular() || entry.Size() != file.SizeBytes {
		return "mismatched", nil
	}
	matched, err := hasExpectedSHA256(ctx, root, relative, file.SHA256, entry)
	if err != nil {
		return "", err
	}
	if !matched {
		return "mismatched", nil
	}
	if !remove {
		return "matched", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := root.Remove(relative); err != nil {
		return "", err
	}
	return "deleted", nil
}

func safeComfyAssetRelativePath(file updates.ComfyAssetFile) (string, bool) {
	if file.StorageType != "output" && file.StorageType != "input" || file.SizeBytes < 0 || !validSHA256(file.SHA256) {
		return "", false
	}
	filename := strings.TrimSpace(file.Filename)
	if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, "\\/:\x00<>\"|?*") || filepath.Base(filename) != filename {
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
	relative := filepath.Join(cleanSubfolder, filename)
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return relative, true
}

func InspectComfyAssetFiles(ctx context.Context, target ComfyTarget, files []updates.ComfyAssetFile) (updates.ComfyAssetInspectResult, error) {
	result := updates.ComfyAssetInspectResult{Protocol: 1, Complete: true, Items: []updates.ComfyAssetDeleteOutcome{}}
	var inspectionErrors []error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.CheckedAt = time.Now().UTC()
			return result, errors.Join(append(inspectionErrors, err)...)
		}
		status := "rejected"
		if directory, valid := comfyAssetRoot(target, file.StorageType); valid {
			var err error
			status, err = examineComfyAssetFile(ctx, directory, file, false)
			if err != nil {
				status = "io_error"
				result.Complete = false
				inspectionErrors = append(inspectionErrors, err)
			}
		}
		result.Items = append(result.Items, comfyAssetOutcome(file, status))
	}
	result.CheckedAt = time.Now().UTC()
	return result, errors.Join(inspectionErrors...)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type comfyCleanupReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r comfyCleanupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func sameComfyCleanupEntry(before, after fs.FileInfo) bool {
	return before != nil && after != nil && before.Mode().IsRegular() && after.Mode().IsRegular() && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func hasExpectedSHA256(ctx context.Context, root *os.Root, relative, expected string, before fs.FileInfo) (bool, error) {
	file, err := root.Open(relative)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !sameComfyCleanupEntry(before, opened) {
		return false, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, comfyCleanupReader{ctx: ctx, reader: file}); err != nil {
		return false, err
	}
	after, err := file.Stat()
	if err != nil {
		return false, err
	}
	current, err := root.Lstat(relative)
	if err != nil {
		return false, err
	}
	if !sameComfyCleanupEntry(before, after) || !sameComfyCleanupEntry(before, current) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected), nil
}
