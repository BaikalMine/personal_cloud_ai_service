package updates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ComfyStorageProtocol = 1
const MaxComfyStorageSelection = 50
const ComfyStorageReceiptRetention = 90 * 24 * time.Hour

var ErrComfyStorageRequest = errors.New("invalid ComfyUI storage operation")
var ErrComfyStorageBusy = errors.New("ComfyUI storage operation is busy")
var ErrComfyStorageConflict = errors.New("ComfyUI storage operation ID conflicts")
var ErrComfyStorageNotFound = errors.New("ComfyUI storage operation was not found")

type ComfyStorageSelection struct {
	Key     string         `json:"key"`
	Version string         `json:"version"`
	File    ComfyAssetFile `json:"file"`
}

type ComfyStorageRequest struct {
	Protocol       int                     `json:"protocol"`
	ID             string                  `json:"id"`
	ExecutorID     string                  `json:"executor_id"`
	GuardJournalID string                  `json:"guard_journal_id"`
	Items          []ComfyStorageSelection `json:"items"`
}

type ComfyStorageOutcome struct {
	Selection  ComfyStorageSelection `json:"selection"`
	Outcome    string                `json:"outcome"`
	Reason     string                `json:"reason,omitempty"`
	FreedBytes int64                 `json:"freed_bytes"`
}

// ExecutorFenced proves only local execution closure. GuardClosed is separate:
// neither a transport timeout nor a terminal file outcome releases its window.
type ComfyStorageResult struct {
	Protocol        int                   `json:"protocol"`
	ID              string                `json:"id"`
	ExecutorID      string                `json:"executor_id"`
	GuardJournalID  string                `json:"guard_journal_id"`
	RequestHash     string                `json:"request_hash"`
	State           string                `json:"state"`
	MutationStarted bool                  `json:"mutation_started"`
	ExecutorFenced  bool                  `json:"executor_fenced"`
	GuardClosed     bool                  `json:"guard_closed"`
	StartedAt       time.Time             `json:"started_at"`
	FinishedAt      *time.Time            `json:"finished_at,omitempty"`
	Items           []ComfyStorageOutcome `json:"items"`
}

type ComfyStorageStatus struct {
	Protocol       int    `json:"protocol"`
	ExecutorID     string `json:"executor_id"`
	GuardJournalID string `json:"guard_journal_id"`
	GuardEnforced  bool   `json:"guard_enforced"`
	GuardHeld      bool   `json:"guard_held"`
	PendingID      string `json:"pending_id,omitempty"`
}

func ValidComfyStorageID(id string) bool {
	if len(id) < 32 || len(id) > 96 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func ComfyStorageHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes && hex.EncodeToString(decoded) == value
}

func CanonicalComfyStorageRequest(input ComfyStorageRequest) (ComfyStorageRequest, string, error) {
	if input.Protocol != ComfyStorageProtocol || !ValidComfyStorageID(input.ID) || !ComfyStorageHex(input.ExecutorID, 32) || !ComfyStorageHex(input.GuardJournalID, 16) || len(input.Items) == 0 || len(input.Items) > MaxComfyStorageSelection {
		return ComfyStorageRequest{}, "", ErrComfyStorageRequest
	}
	input.Items = append([]ComfyStorageSelection(nil), input.Items...)
	keys, paths := map[string]bool{}, map[string]bool{}
	for _, item := range input.Items {
		kind, keyID, ok := strings.Cut(item.Key, ":")
		id, err := strconv.ParseInt(keyID, 10, 64)
		if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != keyID || kind != item.File.StorageType || keys[item.Key] || !ComfyStorageHex(item.Version, 32) || !ValidComfyStorageFile(item.File) {
			return ComfyStorageRequest{}, "", ErrComfyStorageRequest
		}
		keys[item.Key] = true
		fileKey := strings.ToLower(item.File.StorageType + "/" + path.Join(item.File.Subfolder, item.File.Filename))
		if paths[fileKey] {
			return ComfyStorageRequest{}, "", ErrComfyStorageRequest
		}
		paths[fileKey] = true
	}
	sort.Slice(input.Items, func(i, j int) bool { return input.Items[i].Key < input.Items[j].Key })
	payload, _ := json.Marshal(input)
	digest := sha256.Sum256(payload)
	return input, hex.EncodeToString(digest[:]), nil
}

func ValidComfyStorageFile(file ComfyAssetFile) bool {
	if file.StorageType != "input" && file.StorageType != "output" || file.SizeBytes < 0 || file.SizeBytes > 1<<40 || !ComfyStorageHex(file.SHA256, 32) || !comfyStoragePathPart(file.Filename) || len(file.Subfolder) > 1024 {
		return false
	}
	if file.Subfolder == "" {
		return true
	}
	if path.Clean(file.Subfolder) != file.Subfolder {
		return false
	}
	for _, part := range strings.Split(file.Subfolder, "/") {
		if !comfyStoragePathPart(part) {
			return false
		}
	}
	return true
}

func comfyStoragePathPart(part string) bool {
	if part == "" || part == "." || part == ".." || len(part) > 255 || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") || strings.ContainsAny(part, "\\/:\x00<>\"|?*") {
		return false
	}
	for _, ch := range part {
		if ch < 32 || ch == 127 {
			return false
		}
	}
	base, _, _ := strings.Cut(strings.ToUpper(part), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if base == prefix+"\u00b9" || base == prefix+"\u00b2" || base == prefix+"\u00b3" {
			return false
		}
	}
	return !(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9')
}

func ValidateComfyStorageResult(result ComfyStorageResult) error {
	if result.Protocol != ComfyStorageProtocol || !ValidComfyStorageID(result.ID) || !ComfyStorageHex(result.ExecutorID, 32) || !ComfyStorageHex(result.GuardJournalID, 16) || !ComfyStorageHex(result.RequestHash, 32) || result.StartedAt.IsZero() || result.FinishedAt != nil && result.FinishedAt.Before(result.StartedAt) || result.GuardClosed && !result.ExecutorFenced {
		return ErrComfyStorageRequest
	}
	switch result.State {
	case "running", "unconfirmed":
		if result.ExecutorFenced || result.FinishedAt != nil {
			return ErrComfyStorageRequest
		}
	case "completed", "partial", "rejected", "closed_unconfirmed", "expired":
		if !result.ExecutorFenced || result.FinishedAt == nil {
			return ErrComfyStorageRequest
		}
	default:
		return ErrComfyStorageRequest
	}
	if result.State == "expired" {
		if len(result.Items) != 0 || !result.GuardClosed {
			return ErrComfyStorageRequest
		}
		return nil
	}
	request := ComfyStorageRequest{Protocol: result.Protocol, ID: result.ID, ExecutorID: result.ExecutorID, GuardJournalID: result.GuardJournalID}
	for _, item := range result.Items {
		request.Items = append(request.Items, item.Selection)
		if len(item.Reason) > 64 || item.FreedBytes < 0 || item.FreedBytes > item.Selection.File.SizeBytes {
			return ErrComfyStorageRequest
		}
		switch item.Outcome {
		case "deleted":
			if !result.MutationStarted || item.FreedBytes != item.Selection.File.SizeBytes {
				return ErrComfyStorageRequest
			}
		case "missing", "changed", "rejected", "not_run", "io_error", "unconfirmed":
			if item.FreedBytes != 0 {
				return ErrComfyStorageRequest
			}
		default:
			return ErrComfyStorageRequest
		}
		if result.State == "completed" && item.Outcome != "deleted" && item.Outcome != "missing" || result.State == "rejected" && item.Outcome == "deleted" {
			return ErrComfyStorageRequest
		}
	}
	canonical, hash, err := CanonicalComfyStorageRequest(request)
	if err != nil || hash != result.RequestHash {
		return ErrComfyStorageRequest
	}
	for i := range canonical.Items {
		if canonical.Items[i] != result.Items[i].Selection {
			return ErrComfyStorageRequest
		}
	}
	return nil
}

func MatchComfyStorageResult(result ComfyStorageResult, request ComfyStorageRequest) error {
	_, hash, err := CanonicalComfyStorageRequest(request)
	if err != nil || ValidateComfyStorageResult(result) != nil || result.ID != request.ID || result.ExecutorID != request.ExecutorID || result.GuardJournalID != request.GuardJournalID || result.RequestHash != hash {
		return ErrComfyStorageRequest
	}
	return nil
}
