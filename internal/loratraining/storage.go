package loratraining

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const StorageProtocolVersion = 1
const MaxStorageSelection = 50
const StorageReceiptRetention = 90 * 24 * time.Hour

var ErrStorageRequest = errors.New("invalid training storage request")

type StorageItem struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Label           string `json:"label"`
	JobID           string `json:"job_id,omitempty"`
	GatewayJobID    string `json:"gateway_job_id,omitempty"`
	ProfileID       string `json:"profile_id,omitempty"`
	State           string `json:"state"`
	ProtectedReason string `json:"protected_reason,omitempty"`
	CanCleanup      bool   `json:"can_cleanup"`
	SizeBytes       int64  `json:"size_bytes"`
	Files           int64  `json:"files"`
	Version         string `json:"version,omitempty"`
}

// Complete describes the returned page, not the unscanned remainder of a host.
// Sizes count regular-file logical lengths, not filesystem allocation.
type StorageInventory struct {
	ProtocolVersion int           `json:"protocol_version"`
	CheckedAt       time.Time     `json:"checked_at"`
	Complete        bool          `json:"complete"`
	TotalItems      int           `json:"total_items"`
	NextCursor      string        `json:"next_cursor,omitempty"`
	Items           []StorageItem `json:"items"`
}

type StoragePreviewRequest struct {
	IDs []string `json:"ids"`
}

type StorageSelection struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type StorageCleanupRequest struct {
	ID    string             `json:"id"`
	Items []StorageSelection `json:"items"`
}

type StorageCleanupOutcome struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	Version      string `json:"version"`
	Outcome      string `json:"outcome"`
	Reason       string `json:"reason,omitempty"`
	FreedBytes   int64  `json:"freed_bytes"`
	DeletedFiles int64  `json:"deleted_files"`
}

type StorageCleanupResult struct {
	ProtocolVersion int                     `json:"protocol_version"`
	ID              string                  `json:"id"`
	RequestHash     string                  `json:"request_hash"`
	State           string                  `json:"state"`
	StartedAt       time.Time               `json:"started_at"`
	FinishedAt      *time.Time              `json:"finished_at,omitempty"`
	Items           []StorageCleanupOutcome `json:"items"`
}

type StorageCleanupFenceResult struct {
	ProtocolVersion int                  `json:"protocol_version"`
	Fenced          bool                 `json:"fenced"`
	Settled         bool                 `json:"settled"`
	Result          StorageCleanupResult `json:"result"`
}

func ValidStorageKind(kind string) bool {
	switch kind {
	case "original_model", "normalized_model", "trained_artifact", "dataset_archive", "dataset_files", "training_cache", "training_output", "unmanaged_file":
		return true
	default:
		return false
	}
}

func RebuildableStorageKind(kind string) bool {
	return ValidStorageKind(kind) && kind != "original_model" && kind != "trained_artifact" && kind != "unmanaged_file"
}

func ValidStorageTargetID(id string) bool {
	kind, key, ok := strings.Cut(id, ":")
	return ok && ValidStorageKind(kind) && len(key) <= 96 && storageSafeKey(key)
}

func ValidStorageOperationID(id string) bool {
	return len(id) >= 32 && len(id) <= 96 && storageSafeKey(id)
}

func storageSafeKey(key string) bool {
	if key == "" {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func ValidStorageVersion(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func ValidateStorageIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > MaxStorageSelection {
		return ErrStorageRequest
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !ValidStorageTargetID(id) || seen[id] {
			return ErrStorageRequest
		}
		seen[id] = true
	}
	return nil
}

func CanonicalStorageCleanupRequest(request StorageCleanupRequest) (StorageCleanupRequest, string, error) {
	if !ValidStorageOperationID(request.ID) {
		return StorageCleanupRequest{}, "", ErrStorageRequest
	}
	ids := make([]string, 0, len(request.Items))
	for _, item := range request.Items {
		ids = append(ids, item.ID)
		if !ValidStorageVersion(item.Version) {
			return StorageCleanupRequest{}, "", ErrStorageRequest
		}
	}
	if err := ValidateStorageIDs(ids); err != nil {
		return StorageCleanupRequest{}, "", err
	}
	request.Items = append([]StorageSelection(nil), request.Items...)
	sort.Slice(request.Items, func(i, j int) bool { return request.Items[i].ID < request.Items[j].ID })
	payload, _ := json.Marshal(request)
	hash := sha256.Sum256(payload)
	return request, hex.EncodeToString(hash[:]), nil
}

func ValidateStorageInventory(inventory StorageInventory) error {
	if inventory.ProtocolVersion != StorageProtocolVersion || inventory.CheckedAt.IsZero() || inventory.TotalItems < len(inventory.Items) || len(inventory.Items) > 100 ||
		(inventory.NextCursor != "" && (!ValidStorageTargetID(inventory.NextCursor) || len(inventory.Items) == 0 || inventory.NextCursor != inventory.Items[len(inventory.Items)-1].ID)) {
		return ErrStorageRequest
	}
	seen := make(map[string]bool, len(inventory.Items))
	for _, item := range inventory.Items {
		if !ValidStorageTargetID(item.ID) || !strings.HasPrefix(item.ID, item.Kind+":") || seen[item.ID] || len(item.Label) > 512 || item.SizeBytes < 0 || item.SizeBytes > 1<<50 || item.Files < 0 || item.Files > 100000 {
			return ErrStorageRequest
		}
		seen[item.ID] = true
		switch item.State {
		case "present":
			if !ValidStorageVersion(item.Version) {
				return ErrStorageRequest
			}
		case "missing":
			if !ValidStorageVersion(item.Version) || item.SizeBytes != 0 || item.Files != 0 || item.CanCleanup {
				return ErrStorageRequest
			}
		case "unavailable":
			if inventory.Complete || item.Version != "" || item.CanCleanup {
				return ErrStorageRequest
			}
		default:
			return ErrStorageRequest
		}
		if item.CanCleanup && (item.State != "present" || !RebuildableStorageKind(item.Kind) || item.ProtectedReason != "") {
			return ErrStorageRequest
		}
	}
	return nil
}

func ValidateStorageCleanupResult(result StorageCleanupResult) error {
	if result.ProtocolVersion != StorageProtocolVersion || result.StartedAt.IsZero() || result.FinishedAt != nil && result.FinishedAt.Before(result.StartedAt) {
		return ErrStorageRequest
	}
	switch result.State {
	case "running", "unconfirmed":
	case "completed", "partial", "rejected", "closed_unconfirmed":
		if result.FinishedAt == nil {
			return ErrStorageRequest
		}
	default:
		return ErrStorageRequest
	}
	request := StorageCleanupRequest{ID: result.ID}
	for _, item := range result.Items {
		request.Items = append(request.Items, StorageSelection{ID: item.ID, Version: item.Version})
		if !strings.HasPrefix(item.ID, item.Kind+":") || !ValidStorageKind(item.Kind) || len(item.Label) > 512 || item.FreedBytes < 0 || item.FreedBytes > 1<<50 || item.DeletedFiles < 0 || item.DeletedFiles > 100000 {
			return ErrStorageRequest
		}
		switch item.Outcome {
		case "deleted", "partial":
			if !RebuildableStorageKind(item.Kind) {
				return ErrStorageRequest
			}
			if item.FreedBytes > 0 && item.DeletedFiles == 0 || item.Outcome == "partial" && item.DeletedFiles == 0 {
				return ErrStorageRequest
			}
		case "missing", "not_run", "changed", "protected", "error", "unconfirmed":
			if item.FreedBytes != 0 || item.DeletedFiles != 0 {
				return ErrStorageRequest
			}
		default:
			return ErrStorageRequest
		}
		if result.State == "completed" && item.Outcome != "deleted" && item.Outcome != "missing" {
			return ErrStorageRequest
		}
		if result.State == "rejected" && (item.Outcome == "deleted" || item.Outcome == "partial") {
			return ErrStorageRequest
		}
	}
	_, hash, err := CanonicalStorageCleanupRequest(request)
	if err != nil || hash != result.RequestHash {
		return ErrStorageRequest
	}
	return nil
}
