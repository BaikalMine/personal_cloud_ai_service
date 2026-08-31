package domain

import "time"

// DatabaseRetentionCutoffs contains absolute boundaries calculated from the
// runtime retention policy. Store code only receives timestamps, so policy
// configuration remains owned by the Gateway layer.
type DatabaseRetentionCutoffs struct {
	ProxyRequests       time.Time
	WebSocketSessions   time.Time
	GenerationRequests  time.Time
	GenerationJobs      time.Time
	DailyUsage          time.Time
	InviteHistory       time.Time
	AuditLog            time.Time
	HostMetrics         time.Time
	ServiceObservations time.Time
	GatewayObservations time.Time
	GenerationVariants  time.Time
	OutputOwnerships    time.Time
}

type DatabaseCleanupReport struct {
	StartedAt   time.Time
	FinishedAt  time.Time
	Status      string
	DeletedRows map[string]int64
	Errors      map[string]string
	DurationMS  int64
}

func (r DatabaseCleanupReport) TotalDeleted() int64 {
	var total int64
	for _, count := range r.DeletedRows {
		total += count
	}
	return total
}

type DatabaseCleanupState struct {
	LastStartedAt  *time.Time
	LastFinishedAt *time.Time
	LastSuccessAt  *time.Time
	Status         string
	DeletedRows    map[string]int64
	Errors         map[string]string
	DurationMS     int64
}

type DatabaseTableStat struct {
	Name          string
	EstimatedRows int64
	TotalBytes    int64
	OldestAt      *time.Time
}
