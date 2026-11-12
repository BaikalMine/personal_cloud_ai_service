package trainingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"ai-access-gateway/internal/loratraining"
)

type trainingBackupJournalFile struct {
	name    string
	payload []byte
}

func validTrainingBackupJournalName(name string) bool {
	for _, suffix := range []string{".json", ".closed"} {
		if strings.HasSuffix(name, suffix) {
			return loratraining.ValidStorageOperationID(strings.TrimSuffix(name, suffix))
		}
	}
	return false
}

// The standalone backup tool cannot take the live controller's mutex. Read the
// journal at both ends of export and reject a changed snapshot, including prunes.
func snapshotTrainingBackupJournal(ctx context.Context, rootDir string) ([]trainingBackupJournalFile, error) {
	root, err := os.OpenRoot(rootDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := openStorageDirectory(root, storageReceiptDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := fs.ReadDir(directory.FS(), ".")
	if err != nil {
		return nil, err
	}
	if len(entries) > 65536 {
		return nil, errStorageLimit
	}
	var files []trainingBackupJournalFile
	hashes := make(map[string]string)
	total := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unsafe cleanup journal entry %q", name)
		}
		// An interrupted atomic write is not a published receipt. Its ID is
		// protected by the preceding running record, if deletion ever started.
		if strings.HasSuffix(name, ".partial") {
			continue
		}
		if !validTrainingBackupJournalName(name) {
			return nil, fmt.Errorf("unknown cleanup journal entry %q", name)
		}
		var value any
		var id, hash string
		if strings.HasSuffix(name, ".json") {
			id = strings.TrimSuffix(name, ".json")
			result, err := readStorageCleanupResult(directory, id)
			if err != nil {
				return nil, err
			}
			value, hash = result, result.RequestHash
		} else {
			id = strings.TrimSuffix(name, ".closed")
			closed, err := readStorageClosedID(directory, id)
			if err != nil {
				return nil, err
			}
			value, hash = closed, closed.RequestHash
		}
		if previous, ok := hashes[id]; ok && previous != hash {
			return nil, ErrStorageOperationConflict
		}
		hashes[id] = hash
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		total += len(payload)
		if total > 64<<20 {
			return nil, errStorageLimit
		}
		files = append(files, trainingBackupJournalFile{name: name, payload: payload})
	}
	return files, nil
}

func sameTrainingBackupJournal(a, b []trainingBackupJournalFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].name != b[i].name || !bytes.Equal(a[i].payload, b[i].payload) {
			return false
		}
	}
	return true
}
