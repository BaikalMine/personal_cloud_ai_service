package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
)

const UserProfilePageSize = 50

var ErrUserProfileFilter = errors.New("invalid user profile filter")

type UserProfileFilter struct {
	Search, Service, Result, Sort string
	Offset                        int
}

func (f UserProfileFilter) Normalize(sessions bool) (UserProfileFilter, error) {
	f.Search = strings.TrimSpace(f.Search)
	if !utf8.ValidString(f.Search) || utf8.RuneCountInString(f.Search) > 120 || strings.ContainsRune(f.Search, 0) || f.Offset < 0 || f.Offset > 1000000000 {
		return f, ErrUserProfileFilter
	}
	if f.Sort == "" {
		f.Sort = "newest"
	}
	if f.Sort != "newest" && f.Sort != "oldest" {
		return f, ErrUserProfileFilter
	}
	if sessions {
		if f.Service != "" || (f.Result != "" && f.Result != "current" && f.Result != "others") {
			return f, ErrUserProfileFilter
		}
	} else if (f.Service != "" && f.Service != "comfyui" && f.Service != "openwebui") || (f.Result != "" && f.Result != "success" && f.Result != "error") {
		return f, ErrUserProfileFilter
	}
	return f, nil
}

type UserProfilePage struct {
	Filter UserProfileFilter
	Total  int64
	Now    time.Time
}

type UserSessionPage struct {
	UserProfilePage
	Items []domain.AccountSession
}

type UserActivityPage struct {
	UserProfilePage
	Items []domain.Activity
}

// The status clock, count and selected rows belong to the same snapshot.
func beginUserProfilePage(ctx context.Context, db *sql.DB, f UserProfileFilter, from string, args []any) (*sql.Tx, UserProfilePage, error) {
	p := UserProfilePage{Filter: f}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, p, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*),now() `+from, args...).Scan(&p.Total, &p.Now); err != nil {
		tx.Rollback()
		return nil, p, err
	}
	if p.Total == 0 {
		p.Filter.Offset = 0
	} else if int64(p.Filter.Offset) >= p.Total {
		p.Filter.Offset = int((p.Total-1)/UserProfilePageSize) * UserProfilePageSize
	}
	return tx, p, nil
}

func (s *Store) ListUserSessionsPage(ctx context.Context, userID int64, currentHash string, idleTimeout time.Duration, filter UserProfileFilter) (UserSessionPage, error) {
	var result UserSessionPage
	f, err := filter.Normalize(true)
	if err != nil || userID <= 0 || idleTimeout <= 0 {
		return result, ErrUserProfileFilter
	}
	from := `FROM sessions WHERE user_id=$1 AND expires_at>now()
	 AND last_seen_at>now()-($2::bigint*interval '1 second')
	 AND ($3='' OR strpos(lower(ip),lower($3))>0 OR strpos(lower(user_agent),lower($3))>0)
	 AND ($4='' OR ($4='current')=(token_hash=$5))`
	args := []any{userID, int64(idleTimeout.Seconds()), f.Search, f.Result, currentHash}
	tx, page, err := beginUserProfilePage(ctx, s.db, f, from, args)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result.UserProfilePage, result.Items = page, []domain.AccountSession{}
	order := "last_seen_at DESC,id DESC"
	if f.Sort == "oldest" {
		order = "last_seen_at,id"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,created_at,expires_at,last_seen_at,ip,user_agent,token_hash=$5 `+from+` ORDER BY `+order+` LIMIT $6 OFFSET $7`, append(args, UserProfilePageSize, page.Filter.Offset)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.AccountSession
		if err = rows.Scan(&item.ID, &item.CreatedAt, &item.ExpiresAt, &item.LastSeenAt, &item.IP, &item.UserAgent, &item.Current); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) ListUserActivityPage(ctx context.Context, userID int64, filter UserProfileFilter) (UserActivityPage, error) {
	var result UserActivityPage
	f, err := filter.Normalize(false)
	if err != nil || userID <= 0 {
		return result, ErrUserProfileFilter
	}
	from := `FROM proxy_requests WHERE user_id=$1
	 AND ($2='' OR strpos(lower(path),lower($2))>0)
	 AND ($3='' OR service=$3)
	 AND ($4='' OR ($4='error')=(status_code>=400))`
	args := []any{userID, f.Search, f.Service, f.Result}
	tx, page, err := beginUserProfilePage(ctx, s.db, f, from, args)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result.UserProfilePage, result.Items = page, []domain.Activity{}
	order := "created_at DESC,id DESC"
	if f.Sort == "oldest" {
		order = "created_at,id"
	}
	rows, err := tx.QueryContext(ctx, `SELECT service,method,path,status_code,duration_ms,bytes_out,is_websocket,created_at `+from+` ORDER BY `+order+` LIMIT $5 OFFSET $6`, append(args, UserProfilePageSize, page.Filter.Offset)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.Activity
		if err = rows.Scan(&item.Service, &item.Method, &item.Path, &item.Status, &item.Duration, &item.Bytes, &item.WebSocket, &item.CreatedAt); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
