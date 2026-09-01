package domain

import "time"

type ServiceObservationRecord struct {
	Component       string
	Operation       string
	Outcome         string
	LatencyMS       int64
	GenerationJobID *int64
	CorrelationID   string
	ErrorCode       string
	Detail          string
	ObservedAt      time.Time
}

type ServiceLatencySummary struct {
	Component      string
	Operation      string
	Samples        int64
	Failures       int64
	P50MS          int64
	P95MS          int64
	LastLatencyMS  int64
	LastOutcome    string
	LastErrorCode  string
	LastDetail     string
	LastObservedAt time.Time
}

type GatewayObservation struct {
	ID                       int64
	DatabaseBytes            int64
	ActiveJobs               int64
	OverdueJobs              int64
	ActiveLeases             int64
	ContentModerationBacklog int64
	MediaModerationBacklog   int64
	CleanupStatus            string
	CleanupAgeSeconds        int64
	RecordedAt               time.Time
}

type GatewayObservationSummary struct {
	Latest                GatewayObservation
	DatabaseGrowth24Hours int64
}

type GenerationObservabilitySummary struct {
	ActiveJobs       int64
	OverdueJobs      int64
	Completed        int64
	Failed           int64
	Cancelled        int64
	Expired          int64
	SuccessRate      int
	QueueP50MS       int64
	QueueP95MS       int64
	ExecutionP50MS   int64
	ExecutionP95MS   int64
	ObservationHours int
}

type GenerationOutcomeGroup struct {
	WorkflowID  string
	ModelName   string
	Total       int64
	Completed   int64
	Failed      int64
	Cancelled   int64
	SuccessRate int
}

type GenerationFailureSummary struct {
	JobPublicID   string
	CorrelationID string
	Username      string
	WorkflowID    string
	ModelName     string
	ErrorCode     string
	ErrorMessage  string
	FailedAt      time.Time
}

type GenerationJobMarker struct {
	PublicID   string             `json:"public_id"`
	WorkflowID string             `json:"workflow_id"`
	ModelName  string             `json:"model_name"`
	State      GenerationJobState `json:"state"`
	CreatedAt  time.Time          `json:"created_at"`
	StartedAt  *time.Time         `json:"started_at,omitempty"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
}

type TraceProxyRequest struct {
	ID            int64
	RequestID     string
	CorrelationID string
	Service       string
	Method        string
	Path          string
	Status        int
	DurationMS    int64
	BytesIn       int64
	BytesOut      int64
	CreatedAt     time.Time
}

type TraceAuditEvent struct {
	ID            int64
	RequestID     string
	CorrelationID string
	Action        string
	TargetType    string
	Metadata      string
	CreatedAt     time.Time
}

type TraceContentEvent struct {
	ID              int64
	CorrelationID   string
	Service         string
	Kind            string
	ExternalID      string
	Model           string
	GenerationState string
	MediaCount      int64
	CreatedAt       time.Time
}

type GenerationJobTrace struct {
	Job                 GenerationJob
	Transitions         []GenerationJobTransition
	ServiceObservations []ServiceObservationRecord
	ProxyRequests       []TraceProxyRequest
	AuditEvents         []TraceAuditEvent
	ContentEvents       []TraceContentEvent
	MiningLease         *QuickGenerationMiningLease
}
