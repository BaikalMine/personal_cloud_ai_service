package domain

import "time"

type ComfyStorageItem struct {
	Key                string     `json:"key"`
	Kind               string     `json:"kind"`
	Filename           string     `json:"filename"`
	Subfolder          string     `json:"subfolder"`
	StorageType        string     `json:"storage_type"`
	SizeBytes          int64      `json:"size_bytes"`
	SizeKnown          bool       `json:"size_known"`
	ContentHash        string     `json:"-"`
	OwnerID            *int64     `json:"owner_id,omitempty"`
	OwnerName          string     `json:"owner_name"`
	OwnerCount         int        `json:"owner_count"`
	State              string     `json:"state"`
	Pending            bool       `json:"pending"`
	Reason             string     `json:"reason"`
	CreatedAt          time.Time  `json:"created_at"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	EligibleAt         *time.Time `json:"eligible_at,omitempty"`
	NextAttemptAt      *time.Time `json:"next_attempt_at,omitempty"`
	LastAttemptAt      *time.Time `json:"last_attempt_at,omitempty"`
	Attempts           int        `json:"attempts"`
	LastOutcome        string     `json:"last_outcome"`
	ProfileHidden      bool       `json:"profile_hidden"`
	PostprocessJobID   *int64     `json:"postprocess_job_id,omitempty"`
	ReferenceProtected bool       `json:"reference_protected"`
	Protection         []string   `json:"protection"`
	CanInspect         bool       `json:"can_inspect"`
	Version            string     `json:"version"`
}

type ComfyStorageSummary struct {
	InputRecords         int64      `json:"input_records"`
	InputExpectedBytes   int64      `json:"input_expected_bytes"`
	OutputRecords        int64      `json:"output_records"`
	OutputExpectedBytes  int64      `json:"output_expected_bytes"`
	UnknownSizeRecords   int64      `json:"unknown_size_records"`
	PendingRecords       int64      `json:"pending_records"`
	PendingExpectedBytes int64      `json:"pending_expected_bytes"`
	RetryRecords         int64      `json:"retry_records"`
	MismatchedRecords    int64      `json:"mismatched_records"`
	UnavailableRecords   int64      `json:"unavailable_records"`
	UnconfirmedRecords   int64      `json:"unconfirmed_records"`
	UnconfirmedUploads   int64      `json:"unconfirmed_uploads"`
	ProtectedReferences  int64      `json:"protected_references"`
	OldestEligibleAt     *time.Time `json:"oldest_eligible_at,omitempty"`
	ReceiptCount         int64      `json:"receipt_count"`
	ConfirmedFreedBytes  int64      `json:"confirmed_freed_bytes"`
	MissingReceipts      int64      `json:"missing_receipts"`
	LegacyReceipts       int64      `json:"legacy_receipts"`
	LatestConfirmationAt *time.Time `json:"latest_confirmation_at,omitempty"`
	ReportingSince       time.Time  `json:"reporting_since"`
	GenerationActive     bool       `json:"generation_active"`
	ExecutorOccupied     bool       `json:"executor_occupied"`
	ExecutionUnconfirmed bool       `json:"execution_unconfirmed"`
	UnknownInputManifest bool       `json:"unknown_input_manifest"`
}

type ComfyStorageInventory struct {
	CheckedAt time.Time           `json:"checked_at"`
	Summary   ComfyStorageSummary `json:"summary"`
	Items     []ComfyStorageItem  `json:"items"`
	Total     int64               `json:"total"`
	Limit     int                 `json:"limit"`
	Offset    int                 `json:"offset"`
}
