package domain

import "time"

type ProxyRequestRecord struct {
	UserID          int64
	RequestID       string
	CorrelationID   string
	GenerationJobID *int64
	Service         string
	Method          string
	Path            string
	Status          int
	DurationMS      int64
	BytesIn         int64
	BytesOut        int64
	WebSocket       bool
	ClientIP        string
	UserAgent       string
}

type AuditEvent struct {
	ActorUserID     *int64
	RequestID       string
	CorrelationID   string
	GenerationJobID *int64
	Action          string
	TargetType      string
	TargetID        *int64
	IP              string
	UserAgent       string
	Metadata        map[string]any
}

type Activity struct {
	Service      string
	ServiceLabel string
	Summary      string
	Method       string
	Path         string
	Status       int
	Duration     int64
	Bytes        int64
	Count        int
	WebSocket    bool
	CreatedAt    time.Time
}

type ServiceUsage struct {
	Service  string
	Requests int64
	Users    int64
	Bytes    int64
	Errors   int64
}

type ServiceTrendPoint struct {
	Label          string
	Requests       int64
	Users          int64
	Errors         int64
	Bytes          int64
	RequestPercent int
}

type ServiceAnalytics struct {
	Service          string
	DisplayName      string
	Requests         int64
	Users            int64
	Bytes            int64
	Errors           int64
	AverageDuration  int64
	ActiveWebSockets int64
	Trend            []ServiceTrendPoint
}

type ChartPoint struct {
	Label   string
	Count   int64
	Percent int
}

type UserStats struct {
	TotalRequests int64
	TotalBytesOut int64
	TodayRequests int64
	WeekRequests  int64
	AvgDuration   int64
	LastService   string
	ByService     []ServiceUsage
	Chart         []ChartPoint
}

type TopUser struct {
	Username string
	Value    int64
}

type AdminStats struct {
	ActiveUsers      int64
	RequestsToday    int64
	Requests7Days    int64
	ActiveWebSockets int64
	AverageDuration  int64
	ErrorRate        string
	TopUsersRequests []TopUser
	TopUsersTraffic  []TopUser
	UsageByService   []ServiceUsage
	Trend            []ChartPoint
}

type OnlineUser struct {
	Username   string    `json:"username"`
	Role       string    `json:"role"`
	LastSeenAt time.Time `json:"last_seen_at"`
	IP         string    `json:"-"`
}

type HostMetric struct {
	RecordedAt          time.Time `json:"recorded_at"`
	CPUPercent          float64   `json:"cpu_percent"`
	MemoryUsedBytes     int64     `json:"memory_used_bytes"`
	MemoryTotalBytes    int64     `json:"memory_total_bytes"`
	GPUAvailable        bool      `json:"gpu_available"`
	GPUName             string    `json:"gpu_name,omitempty"`
	GPUPercent          float64   `json:"gpu_percent"`
	GPUMemoryUsedBytes  int64     `json:"gpu_memory_used_bytes"`
	GPUMemoryTotalBytes int64     `json:"gpu_memory_total_bytes"`
}
