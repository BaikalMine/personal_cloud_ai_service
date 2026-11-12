package updates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
)

var ErrComfyInputReconciliation = errors.New("invalid ComfyUI input reconciliation")

type ComfyInputObservation struct {
	Selection ComfyInputReconciliationSelection `json:"selection"`
	Outcome   string                            `json:"outcome"`
	SizeBytes *int64                            `json:"size_bytes"`
	SHA256    string                            `json:"sha256"`
}

// A valid shape is not authenticity or continued ownership of the window.
// Database reconciliation must separately bind the trusted executor and hold.
type ComfyInputReconciliationReceipt struct {
	Protocol        int                     `json:"protocol"`
	ID              string                  `json:"id"`
	RequestHash     string                  `json:"request_hash"`
	ExecutorID      string                  `json:"executor_id"`
	GuardJournalID  string                  `json:"guard_journal_id"`
	GuardInstanceID string                  `json:"guard_instance_id"`
	HeldAt          time.Time               `json:"held_at"`
	ObservedAt      time.Time               `json:"observed_at"`
	Items           []ComfyInputObservation `json:"items"`
}

func ValidateComfyInputReconciliationReceipt(request ComfyInputReconciliationRequest, receipt ComfyInputReconciliationReceipt) error {
	canonical, hash, err := CanonicalComfyInputReconciliationRequest(request)
	if err != nil || receipt.Protocol != 1 || receipt.ID != canonical.ID || receipt.RequestHash != hash || receipt.ExecutorID != canonical.ExecutorID || receipt.GuardJournalID != canonical.GuardJournalID || !ComfyStorageHex(receipt.GuardInstanceID, 16) || receipt.HeldAt.IsZero() || receipt.ObservedAt.IsZero() || receipt.ObservedAt.Before(receipt.HeldAt) || len(receipt.Items) != len(canonical.Items) {
		return ErrComfyInputReconciliation
	}
	for i, item := range receipt.Items {
		if item.Selection != canonical.Items[i] {
			return ErrComfyInputReconciliation
		}
		switch item.Outcome {
		case "matched":
			if item.SizeBytes == nil || *item.SizeBytes != item.Selection.File.SizeBytes || item.SHA256 != item.Selection.File.SHA256 {
				return ErrComfyInputReconciliation
			}
		case "changed":
			if item.SizeBytes == nil || *item.SizeBytes < 0 || *item.SizeBytes > 536870912 || !ComfyStorageHex(item.SHA256, 32) || *item.SizeBytes == item.Selection.File.SizeBytes && item.SHA256 == item.Selection.File.SHA256 {
				return ErrComfyInputReconciliation
			}
		case "missing", "unavailable":
			if item.SizeBytes != nil || item.SHA256 != "" {
				return ErrComfyInputReconciliation
			}
		default:
			return ErrComfyInputReconciliation
		}
	}
	return nil
}

// This request authorizes observation under a storage window, never deletion.
// Purpose remains part of the canonical hash to separate it from cleanup work.
type ComfyInputReconciliationRequest struct {
	Protocol       int                                 `json:"protocol"`
	Purpose        string                              `json:"purpose"`
	ID             string                              `json:"id"`
	ExecutorID     string                              `json:"executor_id"`
	GuardJournalID string                              `json:"guard_journal_id"`
	Items          []ComfyInputReconciliationSelection `json:"items"`
}

type ComfyInputReconciliationSelection struct {
	InputID string         `json:"input_id"`
	Version string         `json:"version"`
	File    ComfyAssetFile `json:"file"`
}

func CanonicalComfyInputReconciliationRequest(input ComfyInputReconciliationRequest) (ComfyInputReconciliationRequest, string, error) {
	fail := func() (ComfyInputReconciliationRequest, string, error) {
		return ComfyInputReconciliationRequest{}, "", ErrComfyInputReconciliation
	}
	if input.Protocol != 1 || input.Purpose != "reconcile_input" || !ValidComfyStorageID(input.ID) || !ComfyStorageHex(input.ExecutorID, 32) || !ComfyStorageHex(input.GuardJournalID, 16) || len(input.Items) == 0 || len(input.Items) > MaxComfyStorageSelection {
		return fail()
	}
	input.Items = append([]ComfyInputReconciliationSelection(nil), input.Items...)
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, item := range input.Items {
		digest, ok := strings.CutPrefix(item.InputID, "postprocess-input-")
		if !ok || !ComfyStorageHex(digest, 32) || ids[item.InputID] || !ComfyStorageHex(item.Version, 32) || !ValidComfyStorageFile(item.File) || item.File.StorageType != "input" || item.File.SizeBytes <= 0 || item.File.SizeBytes > 536870912 {
			return fail()
		}
		fileKey := strings.ToLower(path.Join(item.File.Subfolder, item.File.Filename))
		if paths[fileKey] {
			return fail()
		}
		paths[fileKey] = true
		ids[item.InputID] = true
	}
	sort.Slice(input.Items, func(i, j int) bool { return input.Items[i].InputID < input.Items[j].InputID })
	payload, err := json.Marshal(input)
	if err != nil {
		return fail()
	}
	digest := sha256.Sum256(payload)
	return input, hex.EncodeToString(digest[:]), nil
}
