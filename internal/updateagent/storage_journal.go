package updateagent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ai-access-gateway/internal/updates"
)

const maxComfyStorageRecordBytes = 256 << 10
const comfyStoragePendingFile = "pending.json"
const comfyStorageRestoreMarker = "RESTORE_BLOCKED"

type comfyStorageIdentity struct {
	Protocol     int    `json:"protocol"`
	ExecutorID   string `json:"executor_id"`
	JournalRoot  string `json:"journal_root"`
	ReceiptsRoot string `json:"receipts_root"`
	InputRoot    string `json:"input_root"`
	OutputRoot   string `json:"output_root"`
	GuardBinding string `json:"guard_binding"`
}

func storageRootIdentity(directory string) (string, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(ErrComfyStorageIdentity, err)
	}
	file, err := os.Open(directory)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.Join(ErrComfyStorageIdentity, err)
	}
	identity, err := comfyStorageFileIdentity(file)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("comfy-storage-root/v1\x00" + canonical + "\x00" + identity))
	return hex.EncodeToString(digest[:]), nil
}

func (e *ComfyStorageExecutor) currentIdentity() (comfyStorageIdentity, error) {
	identity := comfyStorageIdentity{Protocol: 1, ExecutorID: e.identity.ExecutorID}
	var err error
	identity.JournalRoot, err = storageRootIdentity(e.config.JournalDirectory)
	if err != nil {
		return identity, err
	}
	identity.ReceiptsRoot, err = storageRootIdentity(filepath.Join(e.config.JournalDirectory, "receipts"))
	if err != nil && !(e.identity.ExecutorID == "" && errors.Is(err, os.ErrNotExist)) {
		return identity, err
	}
	identity.InputRoot, err = storageRootIdentity(e.target.InputDirectory)
	if err != nil {
		return identity, err
	}
	identity.OutputRoot, err = storageRootIdentity(e.target.OutputDirectory)
	if err != nil {
		return identity, err
	}
	digest := sha256.Sum256([]byte("comfy-storage-guard/v1\x00" + e.config.GuardURL + "\x00" + e.config.GuardToken))
	identity.GuardBinding = hex.EncodeToString(digest[:])
	return identity, nil
}

func (e *ComfyStorageExecutor) openJournal() (*os.Root, *os.File, error) {
	root, err := os.OpenRoot(e.config.JournalDirectory)
	if err != nil {
		return nil, nil, err
	}
	if _, err := root.Lstat(comfyStorageRestoreMarker); !errors.Is(err, os.ErrNotExist) {
		root.Close()
		return nil, nil, ErrComfyStorageIdentity
	}
	flags := os.O_RDWR
	if !e.existingOnly {
		flags |= os.O_CREATE
	}
	lock, err := root.OpenFile(".lock", flags, 0o600)
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	closeOnError := func(err error) (*os.Root, *os.File, error) {
		lock.Close()
		root.Close()
		return nil, nil, err
	}
	info, statErr := root.Lstat(".lock")
	opened, openErr := lock.Stat()
	if statErr != nil || openErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, opened) {
		return closeOnError(ErrComfyStorageIdentity)
	}
	if err := lockComfyStorageFile(lock); err != nil {
		return closeOnError(err)
	}
	if e.identity.ExecutorID != "" {
		current, err := e.currentIdentity()
		var persisted comfyStorageIdentity
		if err != nil || current != e.identity || readComfyStorageJSON(root, "identity.json", &persisted) != nil || persisted != e.identity {
			return closeOnError(ErrComfyStorageIdentity)
		}
	}
	return root, lock, nil
}

func readComfyStorageJSON(root *os.Root, name string, target any) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxComfyStorageRecordBytes {
		return ErrComfyStorageIdentity
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameComfyCleanupEntry(info, opened) {
		return ErrComfyStorageIdentity
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxComfyStorageRecordBytes+1))
	if err != nil || len(payload) > maxComfyStorageRecordBytes {
		return errors.Join(ErrComfyStorageIdentity, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrComfyStorageIdentity
	}
	return nil
}

func writeComfyStorageJSON(root *os.Root, name string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) > maxComfyStorageRecordBytes {
		return errors.Join(ErrComfyStorageIdentity, err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	temporary := ".write-" + hex.EncodeToString(nonce) + ".partial"
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	written, writeErr := file.Write(payload)
	if written != len(payload) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	// Atomic replacement preserves the last complete record on a process crash.
	// Hardware failure and restoring an older journal require separate recovery.
	return root.Rename(temporary, name)
}

func writeComfyStorageRecord(root *os.Root, name string, result updates.ComfyStorageResult) error {
	if err := updates.ValidateComfyStorageResult(result); err != nil {
		return err
	}
	return writeComfyStorageJSON(root, name, result)
}

func readComfyStorageRecord(root *os.Root, name, id string) (updates.ComfyStorageResult, error) {
	var result updates.ComfyStorageResult
	if err := readComfyStorageJSON(root, name, &result); err != nil {
		return result, err
	}
	if id != "" && result.ID != id || updates.ValidateComfyStorageResult(result) != nil {
		return updates.ComfyStorageResult{}, ErrComfyStorageIdentity
	}
	return result, nil
}

func comfyStorageReceiptsRoot(root *os.Root) (*os.Root, error) {
	info, err := root.Lstat("receipts")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(ErrComfyStorageIdentity, err)
	}
	receipts, err := root.OpenRoot("receipts")
	if err != nil {
		return nil, err
	}
	opened, err := fs.Stat(receipts.FS(), ".")
	if err != nil || !os.SameFile(info, opened) {
		receipts.Close()
		return nil, ErrComfyStorageIdentity
	}
	return receipts, nil
}

func comfyStorageDirectoriesOverlap(a, b string) bool {
	if strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}
