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

const AccessListPageSize = 50

var ErrAccessListFilter = errors.New("invalid access list filter")

type AccessListFilter struct {
	Search, AccountType, State, Access, Sort string
	ExpiringSoon                             bool
	Offset                                   int
}

func (f AccessListFilter) Normalize(invites bool) (AccessListFilter, error) {
	f.Search = strings.TrimSpace(f.Search)
	if !utf8.ValidString(f.Search) || utf8.RuneCountInString(f.Search) > 120 || strings.ContainsRune(f.Search, 0) || f.Offset < 0 || f.Offset > 1000000000 {
		return f, ErrAccessListFilter
	}
	if f.Sort == "" {
		f.Sort = "newest"
	}
	allowed := func(value string, options ...string) bool {
		for _, option := range options {
			if value == option {
				return true
			}
		}
		return false
	}
	if !allowed(f.AccountType, "", "permanent", "temporary") || !allowed(f.Access, "", "photo", "video", "training", "none") {
		return f, ErrAccessListFilter
	}
	if invites {
		if !allowed(f.State, "", "active", "revoked", "expired", "used") || !allowed(f.Sort, "newest", "oldest", "expires", "creator", "uses") {
			return f, ErrAccessListFilter
		}
	} else if !allowed(f.State, "", "active", "disabled", "expired", "locked") || !allowed(f.Sort, "newest", "oldest", "expires", "name", "login") {
		return f, ErrAccessListFilter
	}
	return f, nil
}

type AccessListPage struct {
	Filter AccessListFilter
	Total  int64
	Now    time.Time
}

type UserAccessRow struct {
	domain.User
	ImageUsedToday, VideoUsedToday int
}

type UserAccessPage struct {
	AccessListPage
	Items []UserAccessRow
}

type InviteAccessPage struct {
	AccessListPage
	Items []domain.InviteRow
}

const userAccessListFrom = `FROM users u
 CROSS JOIN LATERAL (SELECT CASE WHEN u.disabled THEN 'disabled'
 WHEN u.account_expires_at<=now() THEN 'expired'
 WHEN u.locked_until>now() THEN 'locked' ELSE 'active' END AS state,
 NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now()) AS enabled) account
 CROSS JOIN LATERAL (SELECT
 account.enabled AND (u.role='admin' OR (u.can_use_quick_generation AND (u.can_generate_text_to_image OR u.can_generate_image_to_image))) AS photo,
 account.enabled AND (u.role='admin' OR (u.can_use_quick_generation AND u.can_generate_video)) AS video,
 account.enabled AND (u.role='admin' OR u.can_train_image_lora) AS training) access
 WHERE ($1='' OR strpos(lower(u.username),lower($1))>0 OR strpos(lower(COALESCE(u.email,'')),lower($1))>0 OR u.id::text=$1)
 AND ($2='' OR ($2='temporary')=(u.account_expires_at IS NOT NULL))
 AND ($3='' OR account.state=$3)
 AND ($4='' OR CASE $4 WHEN 'photo' THEN access.photo WHEN 'video' THEN access.video WHEN 'training' THEN access.training
 WHEN 'none' THEN NOT (access.photo OR access.video OR access.training) ELSE false END)
 AND (NOT $5 OR (u.account_expires_at>now() AND u.account_expires_at<=now()+interval '7 days'))`

const inviteAccessListFrom = `FROM invites i LEFT JOIN users u ON u.id=i.created_by_user_id
 CROSS JOIN LATERAL (SELECT CASE WHEN i.revoked THEN 'revoked' WHEN i.expires_at<=now() THEN 'expired'
 WHEN i.used_count>=i.max_uses THEN 'used' ELSE 'active' END AS state) invitation
 CROSS JOIN LATERAL (SELECT i.grant_quick_generation AND (i.grant_text_to_image OR i.grant_image_to_image) AS photo,
 i.grant_quick_generation AND i.grant_video AS video, i.grant_train_image_lora AS training) access
 WHERE ($1='' OR i.id::text=$1 OR strpos(lower(COALESCE(u.username,'')),lower($1))>0)
 AND ($2='' OR ($2='temporary')=(i.account_lifetime_seconds>0))
 AND ($3='' OR invitation.state=$3)
 AND ($4='' OR CASE $4 WHEN 'photo' THEN access.photo WHEN 'video' THEN access.video WHEN 'training' THEN access.training
 WHEN 'none' THEN NOT (access.photo OR access.video OR access.training) ELSE false END)
 AND (NOT $5 OR (i.expires_at>now() AND i.expires_at<=now()+interval '7 days'))`

func accessListArgs(f AccessListFilter) []any {
	return []any{f.Search, f.AccountType, f.State, f.Access, f.ExpiringSoon}
}

// Count, clamped page and status clock share one read-only snapshot. Deletions
// between requests may change the last page, but cannot blank an in-range page.
func beginAccessList(ctx context.Context, db *sql.DB, f AccessListFilter, from string) (*sql.Tx, AccessListPage, error) {
	p := AccessListPage{Filter: f}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, p, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*),now() `+from, accessListArgs(f)...).Scan(&p.Total, &p.Now); err != nil {
		tx.Rollback()
		return nil, p, err
	}
	if p.Total == 0 {
		p.Filter.Offset = 0
	} else if int64(p.Filter.Offset) >= p.Total {
		p.Filter.Offset = int((p.Total-1)/AccessListPageSize) * AccessListPageSize
	}
	return tx, p, nil
}

func (s *Store) ListUsersPage(ctx context.Context, filter AccessListFilter) (UserAccessPage, error) {
	var result UserAccessPage
	f, err := filter.Normalize(false)
	if err != nil {
		return result, err
	}
	tx, page, err := beginAccessList(ctx, s.db, f, userAccessListFrom)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result.AccessListPage, result.Items = page, []UserAccessRow{}
	order := map[string]string{"newest": "u.created_at DESC,u.id DESC", "oldest": "u.created_at,u.id", "name": "lower(u.username),u.id", "expires": "u.account_expires_at NULLS LAST,u.id", "login": "u.last_login_at DESC NULLS LAST,u.id DESC"}[f.Sort]
	args := append(accessListArgs(f), AccessListPageSize, page.Filter.Offset)
	rows, err := tx.QueryContext(ctx, `WITH page AS (SELECT u.* `+userAccessListFrom+` ORDER BY `+order+` LIMIT $6 OFFSET $7)
 SELECT u.id,u.username,u.email,u.role,u.disabled,u.can_use_comfyui,u.can_use_openwebui,u.can_use_quick_generation,
 u.can_generate_text_to_image,u.can_generate_image_to_image,u.can_generate_video,u.can_use_advanced_generation_settings,
 u.can_train_image_lora,u.can_manage_mining,u.pause_mining_for_quick_generation,u.queue_priority,
 u.generation_daily_limit,u.generation_total_limit,u.generation_total_used,
 u.video_generation_daily_limit,u.video_generation_total_limit,u.video_generation_total_used,u.max_video_generation_quality,
 u.failed_login_count,u.locked_until,u.account_expires_at,u.created_at,u.last_login_at,
 COALESCE(d.used_count,0),COALESCE(d.video_used_count,0),u.can_use_harness
 FROM page u LEFT JOIN quick_generation_daily_usage d ON d.user_id=u.id AND d.usage_date=timezone('Europe/Moscow',now())::date
 ORDER BY `+order, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var row UserAccessRow
		u := &row.User
		if err = rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Disabled, &u.CanUseComfyUI, &u.CanUseOpenWebUI, &u.CanUseQuickGeneration,
			&u.CanGenerateTextToImage, &u.CanGenerateImageToImage, &u.CanGenerateVideo, &u.CanUseAdvancedGenerationSettings,
			&u.CanTrainImageLora, &u.CanManageMining, &u.PauseMiningForQuickGeneration, &u.QueuePriority,
			&u.GenerationDailyLimit, &u.GenerationTotalLimit, &u.GenerationTotalUsed,
			&u.VideoGenerationDailyLimit, &u.VideoGenerationTotalLimit, &u.VideoGenerationTotalUsed, &u.MaxVideoGenerationQuality,
			&u.FailedLoginCount, &u.LockedUntil, &u.AccountExpiresAt, &u.CreatedAt, &u.LastLoginAt, &row.ImageUsedToday, &row.VideoUsedToday, &u.CanUseHarness); err != nil {
			return result, err
		}
		result.Items = append(result.Items, row)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) ListInvitesPage(ctx context.Context, filter AccessListFilter) (InviteAccessPage, error) {
	var result InviteAccessPage
	f, err := filter.Normalize(true)
	if err != nil {
		return result, err
	}
	tx, page, err := beginAccessList(ctx, s.db, f, inviteAccessListFrom)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result.AccessListPage, result.Items = page, []domain.InviteRow{}
	order := map[string]string{"newest": "i.created_at DESC,i.id DESC", "oldest": "i.created_at,i.id", "expires": "i.expires_at,i.id", "creator": "lower(COALESCE(u.username,'')),i.id", "uses": "i.used_count DESC,i.id DESC"}[f.Sort]
	args := append(accessListArgs(f), AccessListPageSize, page.Filter.Offset)
	rows, err := tx.QueryContext(ctx, `SELECT i.id,COALESCE(u.username,''),i.max_uses,i.used_count,i.expires_at,i.revoked,
 i.grant_comfyui,i.grant_openwebui,i.grant_quick_generation,i.grant_text_to_image,i.grant_image_to_image,i.grant_video,
 i.grant_advanced_generation_settings,i.grant_train_image_lora,i.pause_mining_for_quick_generation,i.generation_daily_limit,
 i.generation_total_limit,i.video_generation_daily_limit,i.video_generation_total_limit,i.max_video_generation_quality,
 i.account_lifetime_seconds,invitation.state,i.created_at,i.queue_priority,i.grant_harness `+inviteAccessListFrom+` ORDER BY `+order+` LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var i domain.InviteRow
		if err = rows.Scan(&i.ID, &i.CreatedBy, &i.MaxUses, &i.UsedCount, &i.ExpiresAt, &i.Revoked, &i.GrantComfyUI, &i.GrantOpenWebUI,
			&i.GrantQuickGeneration, &i.GrantTextToImage, &i.GrantImageToImage, &i.GrantVideo, &i.GrantAdvancedGenerationSettings,
			&i.GrantTrainImageLora, &i.PauseMiningForQuickGeneration, &i.GenerationDailyLimit, &i.GenerationTotalLimit,
			&i.VideoGenerationDailyLimit, &i.VideoGenerationTotalLimit, &i.MaxVideoGenerationQuality, &i.AccountLifetimeSeconds,
			&i.Status, &i.CreatedAt, &i.QueuePriority, &i.GrantHarness); err != nil {
			return result, err
		}
		result.Items = append(result.Items, i)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
