package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

const (
	pingTimeout  = 3 * time.Second
	retryDelay   = 2 * time.Second
	maxOpenConns = 20
	maxIdleConns = 10
)

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	var lastErr error
	for {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			db.SetMaxOpenConns(maxOpenConns)
			db.SetMaxIdleConns(maxIdleConns)
			db.SetConnMaxIdleTime(5 * time.Minute)
			db.SetConnMaxLifetime(30 * time.Minute)
			return db, nil
		}

		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = db.Close()
			return nil, fmt.Errorf("connect database: %w", errors.Join(lastErr, ctx.Err()))
		case <-timer.C:
		}
	}
}
