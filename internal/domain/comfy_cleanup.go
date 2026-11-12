package domain

import "time"

const ComfyCleanupReceiptRetention = 90 * 24 * time.Hour

type ComfyOutputCleanupResult struct {
	ComfyOutputCleanupTombstone
	Outcome string
}

type ComfyInputCleanupResult struct {
	ExpiredComfyInputAsset
	ObservedSizeBytes int64
	ObservedHash      string
	Outcome           string
}

type ComfyInputCleanupReceipt struct {
	ComfyInputCleanupResult
	FreedBytes                        int64
	Attempts                          int
	QueuedAt, EligibleAt, ConfirmedAt time.Time
}

func ValidComfyCleanupOutcome(outcome string) bool {
	switch outcome {
	case "deleted", "missing", "legacy_confirmed", "mismatched", "rejected", "host_unavailable", "unconfirmed":
		return true
	default:
		return false
	}
}

func TerminalComfyCleanupOutcome(outcome string) bool {
	return outcome == "deleted" || outcome == "missing" || outcome == "legacy_confirmed"
}

type ComfyCleanupReceipt struct {
	ComfyOutputCleanupTombstone
	Outcome, Reason string
	FreedBytes      int64
	Attempts        int
	QueuedAt        time.Time
	EligibleAt      time.Time
	ConfirmedAt     time.Time
}
