package domain

import (
	"database/sql"
	"time"
)

type Miner struct {
	ID              int64
	Name            string
	ScriptPath      string
	ProcessName     string
	IconMIME        string
	IconData        []byte
	Enabled         bool
	Default         bool
	CreatedByUserID sql.NullInt64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MinerIcon struct {
	MIME string
	Data []byte
}
