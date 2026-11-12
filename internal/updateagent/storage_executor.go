package updateagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-access-gateway/internal/updates"
)

// ComfyStorageExecutor owns only tracked-file operations admitted by Gateway.
// It never launches ComfyUI, executes GPU work, or discovers arbitrary files.
type ComfyStorageExecutor struct {
	config       ComfyStorageConfig
	target       ComfyTarget
	identity     comfyStorageIdentity
	guard        *comfyStorageGuard
	persist      func(*os.Root, string, updates.ComfyStorageResult) error
	remove       func(context.Context, string, updates.ComfyAssetFile, bool) (string, error)
	existingOnly bool
}

func NewComfyStorageExecutor(target ComfyTarget) (*ComfyStorageExecutor, error) {
	return newComfyStorageExecutor(target, true)
}

func newComfyStorageExecutor(target ComfyTarget, initialize bool) (*ComfyStorageExecutor, error) {
	if target.Storage == nil {
		return nil, updates.ErrUnavailable
	}
	config := *target.Storage
	if err := config.normalize(); err != nil {
		return nil, err
	}
	if target.InputDirectory == "" {
		target.InputDirectory = filepath.Join(target.WorkingDirectory, "input")
	}
	if target.OutputDirectory == "" {
		target.OutputDirectory = filepath.Join(target.WorkingDirectory, "output")
	}
	for _, directory := range []string{target.InputDirectory, target.OutputDirectory, config.JournalDirectory} {
		if !filepath.IsAbs(directory) {
			return nil, ErrComfyStorageIdentity
		}
		if _, err := storageRootIdentity(directory); err != nil {
			return nil, err
		}
	}
	journal, err := filepath.EvalSymlinks(config.JournalDirectory)
	if err != nil {
		return nil, err
	}
	input, err := filepath.EvalSymlinks(target.InputDirectory)
	if err != nil {
		return nil, err
	}
	output, err := filepath.EvalSymlinks(target.OutputDirectory)
	if err != nil {
		return nil, err
	}
	if comfyStorageDirectoriesOverlap(journal, input) || comfyStorageDirectoriesOverlap(journal, output) || comfyStorageDirectoriesOverlap(input, output) {
		return nil, ErrComfyStorageIdentity
	}
	if target.WorkingDirectory != "" {
		working, err := filepath.EvalSymlinks(target.WorkingDirectory)
		if err != nil || comfyStorageDirectoriesOverlap(journal, working) {
			return nil, ErrComfyStorageIdentity
		}
	}
	e := &ComfyStorageExecutor{config: config, target: target, guard: newComfyStorageGuard(config), persist: writeComfyStorageRecord, remove: examineManualComfyStorageFile}
	e.existingOnly = !initialize
	if !initialize {
		info, err := os.Lstat(filepath.Join(config.JournalDirectory, "identity.json"))
		if err != nil || !info.Mode().IsRegular() {
			return nil, ErrComfyStorageIdentity
		}
	}
	root, lock, err := e.openJournal()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	defer lock.Close()
	var saved comfyStorageIdentity
	if err := readComfyStorageJSON(root, "identity.json", &saved); errors.Is(err, os.ErrNotExist) {
		if !initialize {
			return nil, ErrComfyStorageIdentity
		}
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() != ".lock" {
				return nil, ErrComfyStorageIdentity
			}
		}
		if err := root.Mkdir("receipts", 0o700); err != nil {
			return nil, err
		}
		saved, err = e.currentIdentity()
		if err != nil {
			return nil, err
		}
		nonce := make([]byte, 32)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		saved.ExecutorID = hex.EncodeToString(nonce)
		if err := writeComfyStorageJSON(root, "identity.json", saved); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if saved.Protocol != 1 || !updates.ComfyStorageHex(saved.ExecutorID, 32) {
		return nil, ErrComfyStorageIdentity
	}
	e.identity = saved
	current, err := e.currentIdentity()
	if err != nil || current != saved {
		return nil, ErrComfyStorageIdentity
	}
	return e, nil
}

func (e *ComfyStorageExecutor) Status(ctx context.Context) (updates.ComfyStorageStatus, error) {
	root, lock, err := e.openJournal()
	if err != nil {
		return updates.ComfyStorageStatus{}, err
	}
	defer root.Close()
	defer lock.Close()
	status := updates.ComfyStorageStatus{Protocol: 1, ExecutorID: e.identity.ExecutorID}
	pending, pendingErr := readComfyStorageRecord(root, comfyStoragePendingFile, "")
	if pendingErr == nil {
		status.PendingID = pending.ID
	} else if !errors.Is(pendingErr, os.ErrNotExist) {
		return status, pendingErr
	}
	guard, err := e.guard.status(ctx)
	if err != nil {
		return status, err
	}
	status.GuardEnforced, status.GuardHeld, status.GuardJournalID = guard.Enforced, guard.Held, guard.JournalID
	if pendingErr == nil {
		closed, err := e.readReceipt(root, pending.ID)
		if err == nil {
			if closed.RequestHash != pending.RequestHash || closed.ExecutorID != pending.ExecutorID || closed.GuardJournalID != pending.GuardJournalID {
				return status, ErrComfyStorageIdentity
			}
			// A published closed receipt supersedes a leftover pending file. Leave
			// removal to the next write operation; a status read stays read-only.
			if closed.GuardJournalID == guard.JournalID {
				status.PendingID = ""
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return status, err
		}
	}
	return status, nil
}

func (e *ComfyStorageExecutor) Result(ctx context.Context, id string) (updates.ComfyStorageResult, error) {
	if !updates.ValidComfyStorageID(id) {
		return updates.ComfyStorageResult{}, updates.ErrComfyStorageRequest
	}
	if err := ctx.Err(); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	root, lock, err := e.openJournal()
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	defer root.Close()
	defer lock.Close()
	result, err := e.readReceipt(root, id)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	result, err = readComfyStorageRecord(root, comfyStoragePendingFile, "")
	if errors.Is(err, os.ErrNotExist) {
		return updates.ComfyStorageResult{}, updates.ErrComfyStorageNotFound
	}
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	if result.ID != id {
		return updates.ComfyStorageResult{}, updates.ErrComfyStorageNotFound
	}
	return observedComfyStorageResult(result), nil
}

func (e *ComfyStorageExecutor) Execute(ctx context.Context, input updates.ComfyStorageRequest) (updates.ComfyStorageResult, error) {
	request, hash, err := updates.CanonicalComfyStorageRequest(input)
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	if request.ExecutorID != e.identity.ExecutorID {
		return updates.ComfyStorageResult{}, ErrComfyStorageIdentity
	}
	root, lock, err := e.openJournal()
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	defer root.Close()
	defer lock.Close()
	if result, found, err := e.existing(root, request, hash); found || err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	result := newComfyStorageResult(request, hash)
	// The complete selection is durable before even requesting a host hold.
	if err := e.persist(root, comfyStoragePendingFile, result); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	state, holdErr := e.guard.window(ctx, "hold", result)
	if holdErr != nil || state != "held" {
		result.State = "rejected"
		for i := range result.Items {
			result.Items[i].Reason = "guard_unconfirmed"
			if state == "closed" {
				result.Items[i].Outcome, result.Items[i].Reason = "unconfirmed", "guard_already_closed"
				result.State = "closed_unconfirmed"
			}
		}
		return e.finish(ctx, root, result)
	}
	preflightRejected := false
	for i := range result.Items {
		item := &result.Items[i]
		outcome, err := e.examine(ctx, item.Selection.File, false)
		switch {
		case err != nil:
			item.Outcome, item.Reason = "io_error", comfyStorageErrorReason(err)
			preflightRejected = true
		case outcome == "missing":
			item.Outcome = "missing"
		case outcome != "matched":
			item.Outcome, item.Reason = "changed", "file_changed"
			preflightRejected = true
		}
	}
	if preflightRejected {
		result.State = "rejected"
		for i := range result.Items {
			if result.Items[i].Outcome == "not_run" {
				result.Items[i].Reason = "plan_rejected"
			}
		}
		return e.finish(ctx, root, result)
	}
	result.MutationStarted = true
	if err := e.persist(root, comfyStoragePendingFile, result); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	result.State = "completed"
	for i := range result.Items {
		item := &result.Items[i]
		if item.Outcome == "missing" {
			continue
		}
		if err := ctx.Err(); err != nil {
			item.Reason = "canceled"
			result.State = "partial"
			break
		}
		outcome, err := e.examine(ctx, item.Selection.File, true)
		switch {
		case err != nil:
			item.Outcome, item.Reason = "io_error", comfyStorageErrorReason(err)
			result.State = "partial"
		case outcome == "deleted":
			item.Outcome, item.FreedBytes = "deleted", item.Selection.File.SizeBytes
		case outcome == "missing":
			item.Outcome = "missing"
		default:
			item.Outcome, item.Reason = "changed", "file_changed"
			result.State = "partial"
		}
		// Publish per-file evidence while the operation is still running. A failed
		// journal write stops the loop; the old on-disk outcome remains uncertain.
		snapshot := result
		snapshot.State = "running"
		if err := e.persist(root, comfyStoragePendingFile, snapshot); err != nil {
			return updates.ComfyStorageResult{}, err
		}
	}
	return e.finish(ctx, root, result)
}

func (e *ComfyStorageExecutor) Fence(ctx context.Context, input updates.ComfyStorageRequest) (updates.ComfyStorageResult, error) {
	request, hash, err := updates.CanonicalComfyStorageRequest(input)
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	if request.ExecutorID != e.identity.ExecutorID {
		return updates.ComfyStorageResult{}, ErrComfyStorageIdentity
	}
	root, lock, err := e.openJournal()
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	defer root.Close()
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	result, found, err := e.existing(root, request, hash)
	if err != nil {
		return result, err
	}
	if found && result.GuardClosed {
		return result, nil
	}
	if !found {
		result = newComfyStorageResult(request, hash)
		result.State = "rejected"
		for i := range result.Items {
			result.Items[i].Reason = "request_fenced"
		}
	} else if !result.ExecutorFenced {
		result.State = "rejected"
		for i := range result.Items {
			item := &result.Items[i]
			if item.Outcome == "unconfirmed" {
				result.State = "closed_unconfirmed"
			} else if item.Outcome == "not_run" {
				item.Reason = "request_fenced"
			}
		}
		if result.State != "closed_unconfirmed" {
			complete, deleted := true, false
			for _, item := range result.Items {
				complete = complete && (item.Outcome == "deleted" || item.Outcome == "missing")
				deleted = deleted || item.Outcome == "deleted"
			}
			if complete {
				result.State = "completed"
			} else if deleted {
				result.State = "partial"
			}
		}
	}
	return e.finish(ctx, root, result)
}

func newComfyStorageResult(request updates.ComfyStorageRequest, hash string) updates.ComfyStorageResult {
	result := updates.ComfyStorageResult{Protocol: 1, ID: request.ID, ExecutorID: request.ExecutorID, GuardJournalID: request.GuardJournalID, RequestHash: hash, State: "running", StartedAt: time.Now().UTC(), Items: []updates.ComfyStorageOutcome{}}
	for _, item := range request.Items {
		result.Items = append(result.Items, updates.ComfyStorageOutcome{Selection: item, Outcome: "not_run"})
	}
	return result
}

func observedComfyStorageResult(result updates.ComfyStorageResult) updates.ComfyStorageResult {
	if result.State == "running" {
		result.State = "unconfirmed"
		for i := range result.Items {
			if result.MutationStarted && result.Items[i].Outcome == "not_run" {
				result.Items[i].Outcome, result.Items[i].Reason = "unconfirmed", "operation_interrupted"
			}
		}
	}
	return result
}

func (e *ComfyStorageExecutor) readReceipt(root *os.Root, id string) (updates.ComfyStorageResult, error) {
	receipts, err := comfyStorageReceiptsRoot(root)
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	defer receipts.Close()
	result, err := readComfyStorageRecord(receipts, id+".json", id)
	if err == nil && (!result.ExecutorFenced || !result.GuardClosed || result.ExecutorID != e.identity.ExecutorID) {
		return updates.ComfyStorageResult{}, ErrComfyStorageIdentity
	}
	return result, err
}

func (e *ComfyStorageExecutor) existing(root *os.Root, request updates.ComfyStorageRequest, hash string) (updates.ComfyStorageResult, bool, error) {
	result, err := e.readReceipt(root, request.ID)
	if err == nil {
		if updates.MatchComfyStorageResult(result, request) != nil {
			return updates.ComfyStorageResult{}, false, ErrComfyStorageConflict
		}
		return result, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return updates.ComfyStorageResult{}, false, err
	}
	pending, err := readComfyStorageRecord(root, comfyStoragePendingFile, "")
	if errors.Is(err, os.ErrNotExist) {
		return updates.ComfyStorageResult{}, false, nil
	}
	if err != nil {
		return updates.ComfyStorageResult{}, false, err
	}
	if pending.ID != request.ID {
		// A crash between receipt publication and removing pending.json must not
		// block all later operations once both execution and guard are closed.
		closed, err := e.readReceipt(root, pending.ID)
		if err != nil || closed.RequestHash != pending.RequestHash || closed.ExecutorID != pending.ExecutorID || closed.GuardJournalID != pending.GuardJournalID {
			return updates.ComfyStorageResult{}, false, ErrComfyStorageBusy
		}
		if err := root.Remove(comfyStoragePendingFile); err != nil {
			return updates.ComfyStorageResult{}, false, err
		}
		return updates.ComfyStorageResult{}, false, nil
	}
	if pending.RequestHash != hash || updates.MatchComfyStorageResult(pending, request) != nil {
		return updates.ComfyStorageResult{}, false, ErrComfyStorageConflict
	}
	return observedComfyStorageResult(pending), true, nil
}

func (e *ComfyStorageExecutor) finish(ctx context.Context, root *os.Root, result updates.ComfyStorageResult) (updates.ComfyStorageResult, error) {
	result.ExecutorFenced = true
	if result.FinishedAt == nil {
		now := time.Now().UTC()
		result.FinishedAt = &now
	}
	if err := e.persist(root, comfyStoragePendingFile, result); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	state, err := e.guard.window(closeCtx, "close", result)
	if err != nil || state != "closed" {
		return result, ErrComfyStorageGuard
	}
	result.GuardClosed = true
	receipts, err := comfyStorageReceiptsRoot(root)
	if err != nil {
		return updates.ComfyStorageResult{}, err
	}
	defer receipts.Close()
	if err := e.persist(receipts, result.ID+".json", result); err != nil {
		return updates.ComfyStorageResult{}, err
	}
	if err := root.Remove(comfyStoragePendingFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	return result, nil
}

func (e *ComfyStorageExecutor) examine(ctx context.Context, file updates.ComfyAssetFile, remove bool) (string, error) {
	current, err := e.currentIdentity()
	if err != nil || current != e.identity {
		return "", ErrComfyStorageIdentity
	}
	directory := e.target.OutputDirectory
	if file.StorageType == "input" {
		directory = e.target.InputDirectory
	}
	return e.remove(ctx, directory, file, remove)
}

func examineManualComfyStorageFile(ctx context.Context, directory string, file updates.ComfyAssetFile, remove bool) (string, error) {
	if !updates.ValidComfyStorageFile(file) {
		return "rejected", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	relative, ok := safeComfyAssetRelativePath(file)
	if !ok {
		return "rejected", nil
	}
	parent := ""
	if file.Subfolder != "" {
		for _, part := range strings.Split(file.Subfolder, "/") {
			parent = filepath.Join(parent, part)
			info, err := root.Lstat(parent)
			if errors.Is(err, os.ErrNotExist) {
				return "missing", nil
			}
			if err != nil {
				return "", err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "rejected", nil
			}
		}
	}
	return examineComfyAssetAtRoot(ctx, root, relative, file, remove)
}

func comfyStorageErrorReason(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if errors.Is(err, ErrComfyStorageIdentity) {
		return "host_identity_changed"
	}
	return "filesystem_error"
}

func (e *ComfyStorageExecutor) Prune(ctx context.Context, now time.Time) error {
	root, lock, err := e.openJournal()
	if err != nil {
		return err
	}
	defer root.Close()
	defer lock.Close()
	receipts, err := comfyStorageReceiptsRoot(root)
	if err != nil {
		return err
	}
	defer receipts.Close()
	entries, err := fs.ReadDir(receipts.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if id == entry.Name() || !updates.ValidComfyStorageID(id) {
			continue
		}
		result, err := readComfyStorageRecord(receipts, entry.Name(), id)
		if err != nil {
			return err
		}
		if result.State == "expired" || !result.GuardClosed || !result.ExecutorFenced || result.FinishedAt == nil || !result.FinishedAt.Before(now.Add(-updates.ComfyStorageReceiptRetention)) {
			continue
		}
		result.State, result.Items = "expired", []updates.ComfyStorageOutcome{}
		if err := e.persist(receipts, entry.Name(), result); err != nil {
			return err
		}
	}
	return nil
}
