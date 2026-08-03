package domain

import (
	"database/sql"
	"time"
)

type SessionRow struct {
	ID         int64
	Username   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	IP         string
	UserAgent  string
}

type AuditRow struct {
	ID         int64
	Actor      string
	Action     string
	TargetType string
	TargetID   sql.NullInt64
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	Metadata   string
}
