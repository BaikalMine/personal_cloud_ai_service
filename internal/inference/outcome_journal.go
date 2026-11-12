package inference

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
)

const maxOutcomeFileBytes = 2*domain.MaxGPUInferenceResponseBytes + 8192
const maxOutcomeFiles = 1024

type observedOutcome struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	RunToken    string    `json:"run_token"`
	LeaseToken  string    `json:"lease_token"`
	RequestHash string    `json:"request_hash"`
	Code        string    `json:"code"`
	Status      int       `json:"status"`
	Response    []byte    `json:"response,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

func (o observedOutcome) validate(now time.Time) error {
	hash, err := hex.DecodeString(o.RequestHash)
	if err != nil || len(hash) != 32 || o.Version != 1 || o.ID == "" || len(o.ID) > 128 ||
		o.RunToken == "" || len(o.RunToken) > 256 || o.LeaseToken == "" || len(o.LeaseToken) > 256 ||
		o.ObservedAt.IsZero() || o.ObservedAt.After(now.Add(time.Minute)) || len(o.Response) > domain.MaxGPUInferenceResponseBytes+64 {
		return errors.New("invalid inference outcome identity")
	}
	switch o.Code {
	case "executor_terminal":
		if o.Status < 200 || o.Status >= 300 || (len(o.Response) == 0 && o.ObservedAt.Add(24*time.Hour).After(now)) {
			return errors.New("invalid inference completion receipt")
		}
	case "request_not_dispatched", "executor_stopped":
		if o.Status != 0 || len(o.Response) != 0 {
			return errors.New("invalid inference non-dispatch receipt")
		}
	default:
		return errors.New("unknown inference execution is not a completion receipt")
	}
	return nil
}

type outcomeJournal struct {
	root   *os.Root
	cipher *contentcrypto.Cipher
	mu     sync.Mutex
}

func openOutcomeJournal(directory string, cipher *contentcrypto.Cipher) (*outcomeJournal, error) {
	if !filepath.IsAbs(directory) || cipher == nil {
		return nil, errors.New("absolute inference outcome directory and cipher are required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	j := &outcomeJournal{root: root, cipher: cipher}
	if err := j.checkWritable(); err != nil {
		root.Close()
		return nil, err
	}
	return j, nil
}

func outcomeName(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:]) + ".receipt"
}
func outcomeAAD(name string) []byte { return []byte("gpu-inference/outcome/v1/" + name) }

func (j *outcomeJournal) syncDirectory() error {
	// File.Sync + Rename protects process-restart recovery on Windows. Directory
	// fsync is not supported there; power-loss durability is not claimed.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := j.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (j *outcomeJournal) write(name string, payload []byte) error {
	temporary := ".outcome-" + rand.Text() + ".tmp"
	file, err := j.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer j.root.Remove(temporary)
	_, err = file.Write(payload)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := j.root.Rename(temporary, name); err != nil {
		return err
	}
	return j.syncDirectory()
}

func (j *outcomeJournal) checkWritable() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := fs.ReadDir(j.root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) >= maxOutcomeFiles {
		return errors.New("inference outcome journal is full; reconcile saved receipts before dispatch")
	}
	name := ".outcome-probe-" + rand.Text()
	if err := j.write(name, []byte("probe")); err != nil {
		return err
	}
	return j.root.Remove(name)
}

func (j *outcomeJournal) save(o observedOutcome) error {
	if err := o.validate(time.Now()); err != nil {
		return err
	}
	plain, err := json.Marshal(o)
	if err != nil {
		return err
	}
	defer clear(plain)
	name := outcomeName(o.ID)
	payload, err := j.cipher.EncryptBytesWithAAD(plain, outcomeAAD(name))
	if err != nil {
		return err
	}
	defer clear(payload)
	if len(payload) > maxOutcomeFileBytes {
		return errors.New("inference outcome exceeds journal limit")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.write(name, payload)
}

func (j *outcomeJournal) read(name string) (observedOutcome, error) {
	var outcome observedOutcome
	info, err := j.root.Lstat(name)
	if err != nil {
		return outcome, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxOutcomeFileBytes {
		return outcome, errors.New("invalid inference receipt file")
	}
	file, err := j.root.Open(name)
	if err != nil {
		return outcome, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxOutcomeFileBytes+1))
	if err != nil {
		return outcome, err
	}
	defer clear(payload)
	if len(payload) > maxOutcomeFileBytes {
		return outcome, errors.New("inference receipt file is too large")
	}
	plain, err := j.cipher.DecryptBytesWithAAD(payload, outcomeAAD(name))
	if err != nil {
		return outcome, errors.New("inference receipt cannot be decrypted")
	}
	defer clear(plain)
	if err := json.Unmarshal(plain, &outcome); err != nil {
		return outcome, errors.New("inference receipt is invalid")
	}
	if outcomeName(outcome.ID) != name {
		return outcome, errors.New("inference receipt identity does not match file")
	}
	return outcome, outcome.validate(time.Now())
}

func (j *outcomeJournal) remove(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.root.Remove(outcomeName(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return j.syncDirectory()
}

// Drain receipts before accepting more work. Unknown/mismatched state stays on
// disk for diagnosis and never grants permission to repeat the model request.
func (w *Worker) restoreOutcomes(ctx context.Context) error {
	if w.journal == nil {
		return nil
	}
	entries, err := fs.ReadDir(w.journal.root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) > maxOutcomeFiles {
		return errors.New("inference outcome journal exceeds entry limit")
	}
	var failures []error
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.HasSuffix(entry.Name(), ".receipt") {
			continue
		}
		outcome, err := w.journal.read(entry.Name())
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			if !outcome.ObservedAt.Add(24*time.Hour).After(time.Now()) && len(outcome.Response) > 0 {
				clear(outcome.Response)
				outcome.Response = nil
				err = w.journal.save(outcome)
			}
			if err == nil {
				persistCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = w.persistOutcome(persistCtx, outcome)
				cancel()
			}
			clear(outcome.Response)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("receipt %s: %w", entry.Name(), err))
			continue
		}
		if err = w.journal.remove(outcome.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (w *Worker) persistOutcome(ctx context.Context, o observedOutcome) error {
	return w.store.RestoreGPUInferenceOutcome(ctx, o.ID, o.RunToken, o.LeaseToken, o.RequestHash, o.Code, o.Status, o.Response, o.ObservedAt)
}
