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

const AdminSecurityPageSize = 50

var ErrAdminSecurityFilter = errors.New("invalid security list filter")

type AdminSecurityPage struct {
	Total  int64
	Offset int
	At     time.Time
}

func validSecurityText(value string, limit int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, 0)
}

func normalizeSecurityOrder(search, order string, offset int) (string, string, error) {
	search = strings.TrimSpace(search)
	if order == "" {
		order = "newest"
	}
	if !validSecurityText(search, 120) || (order != "newest" && order != "oldest") || offset < 0 || offset > 1000000000 {
		return search, order, ErrAdminSecurityFilter
	}
	return search, order, nil
}

// Counts, time-dependent states and rows share the same read-only snapshot.
func beginAdminSecurityPage(ctx context.Context, db *sql.DB, offset int, from string, args []any) (*sql.Tx, AdminSecurityPage, error) {
	p := AdminSecurityPage{Offset: offset}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, p, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*),now() `+from, args...).Scan(&p.Total, &p.At); err != nil {
		tx.Rollback()
		return nil, p, err
	}
	if p.Total == 0 {
		p.Offset = 0
	} else if int64(p.Offset) >= p.Total {
		p.Offset = int((p.Total-1)/AdminSecurityPageSize) * AdminSecurityPageSize
	}
	return tx, p, nil
}

type AdminAuditFilter struct {
	Search, Action, Target, Sort string
	From, Until                  *time.Time
	Before                       time.Time
	Offset                       int
}

func (f AdminAuditFilter) Normalize() (AdminAuditFilter, error) {
	var err error
	f.Search, f.Sort, err = normalizeSecurityOrder(f.Search, f.Sort, f.Offset)
	if err != nil || !validSecurityText(f.Action, 128) || !validSecurityText(f.Target, 128) || f.Before.IsZero() ||
		(f.From != nil && f.Until != nil && !f.From.Before(*f.Until)) {
		return f, ErrAdminSecurityFilter
	}
	return f, nil
}

type AdminAuditRow struct {
	domain.AuditRow
	ActorID                  sql.NullInt64
	RequestID, CorrelationID string
}

type AdminAuditPage struct {
	AdminSecurityPage
	Filter           AdminAuditFilter
	Items            []AdminAuditRow
	Actions, Targets []string
}

func (s *Store) ListAdminAuditPage(ctx context.Context, filter AdminAuditFilter) (AdminAuditPage, error) {
	var out AdminAuditPage
	f, err := filter.Normalize()
	if err != nil {
		return out, err
	}
	from := `FROM audit_log a LEFT JOIN users u ON u.id=a.actor_user_id
	 WHERE a.created_at<$1 AND ($2::timestamptz IS NULL OR a.created_at >= $2)
	 AND ($3::timestamptz IS NULL OR a.created_at < $3)
	 AND ($4='' OR strpos(lower(concat_ws(' ',u.username,a.action,a.target_type,a.target_id::text,a.ip,a.user_agent,a.metadata::text,a.request_id,a.correlation_id,a.id::text)),lower($4))>0)
	 AND ($5='' OR a.action=$5) AND ($6='' OR a.target_type=$6)`
	args := []any{f.Before.UTC(), f.From, f.Until, f.Search, f.Action, f.Target}
	tx, page, err := beginAdminSecurityPage(ctx, s.db, f.Offset, from, args)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	f.Offset = page.Offset
	out.AdminSecurityPage, out.Filter, out.Items = page, f, []AdminAuditRow{}
	order := "a.created_at DESC,a.id DESC"
	if f.Sort == "oldest" {
		order = "a.created_at,a.id"
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,COALESCE(u.username,''),a.action,a.target_type,a.target_id,a.ip,a.user_agent,a.created_at,a.metadata::text,a.actor_user_id,a.request_id,a.correlation_id `+from+` ORDER BY `+order+` LIMIT $7 OFFSET $8`, append(args, AdminSecurityPageSize, page.Offset)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item AdminAuditRow
		if err = rows.Scan(&item.ID, &item.Actor, &item.Action, &item.TargetType, &item.TargetID, &item.IP, &item.UserAgent, &item.CreatedAt, &item.Metadata, &item.ActorID, &item.RequestID, &item.CorrelationID); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	options, err := tx.QueryContext(ctx, `SELECT kind,value FROM (
	 SELECT 'action' AS kind,action AS value FROM audit_log WHERE created_at<$1 GROUP BY action
	 UNION ALL SELECT 'target',target_type FROM audit_log WHERE created_at<$1 GROUP BY target_type
	) choices ORDER BY kind,value`, f.Before.UTC())
	if err != nil {
		return out, err
	}
	for options.Next() {
		var kind, value string
		if err = options.Scan(&kind, &value); err != nil {
			options.Close()
			return out, err
		}
		if kind == "action" {
			out.Actions = append(out.Actions, value)
		} else {
			out.Targets = append(out.Targets, value)
		}
	}
	err = options.Err()
	options.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type AdminSessionFilter struct {
	Search, State, Sort string
	Offset              int
}

func (f AdminSessionFilter) Normalize() (AdminSessionFilter, error) {
	var err error
	f.Search, f.Sort, err = normalizeSecurityOrder(f.Search, f.Sort, f.Offset)
	if f.State == "" {
		f.State = "valid"
	}
	if err != nil || (f.State != "valid" && f.State != "inactive" && f.State != "all") {
		return f, ErrAdminSecurityFilter
	}
	return f, nil
}

type AdminSessionRow struct {
	domain.SessionRow
	UserID  int64
	Current bool
	State   string
}

type AdminSessionPage struct {
	AdminSecurityPage
	Filter AdminSessionFilter
	Items  []AdminSessionRow
}

func (s *Store) ListAdminSessionsPage(ctx context.Context, filter AdminSessionFilter, currentHash string, idleTimeout time.Duration) (AdminSessionPage, error) {
	var out AdminSessionPage
	f, err := filter.Normalize()
	if err != nil || idleTimeout <= 0 {
		return out, ErrAdminSecurityFilter
	}
	from := `FROM sessions s JOIN users u ON u.id=s.user_id
	 CROSS JOIN LATERAL (SELECT CASE WHEN u.disabled THEN 'disabled'
	 WHEN u.account_expires_at<=now() THEN 'account_expired' WHEN s.expires_at<=now() THEN 'expired'
	 WHEN s.last_seen_at<=now()-($1::bigint*interval '1 second') THEN 'idle' ELSE 'valid' END AS state) status
	 WHERE ($2='' OR strpos(lower(concat_ws(' ',u.username,s.ip,s.user_agent,s.id::text)),lower($2))>0)
	 AND ($3='all' OR ($3='valid')=(status.state='valid'))`
	args := []any{int64(idleTimeout.Seconds()), f.Search, f.State}
	tx, page, err := beginAdminSecurityPage(ctx, s.db, f.Offset, from, args)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	f.Offset = page.Offset
	out.AdminSecurityPage, out.Filter, out.Items = page, f, []AdminSessionRow{}
	order := "s.last_seen_at DESC,s.id DESC"
	if f.Sort == "oldest" {
		order = "s.last_seen_at,s.id"
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,u.username,s.created_at,s.expires_at,s.last_seen_at,s.ip,s.user_agent,s.user_id,s.token_hash=$4,status.state `+from+` ORDER BY `+order+` LIMIT $5 OFFSET $6`, append(args, currentHash, AdminSecurityPageSize, page.Offset)...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item AdminSessionRow
		if err = rows.Scan(&item.ID, &item.Username, &item.CreatedAt, &item.ExpiresAt, &item.LastSeenAt, &item.IP, &item.UserAgent, &item.UserID, &item.Current, &item.State); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) RevokeAdminSession(ctx context.Context, id int64) (string, bool, error) {
	if id <= 0 {
		return "", false, ErrAdminSecurityFilter
	}
	var hash string
	err := s.db.QueryRowContext(ctx, `DELETE FROM sessions WHERE id=$1 RETURNING token_hash`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}
