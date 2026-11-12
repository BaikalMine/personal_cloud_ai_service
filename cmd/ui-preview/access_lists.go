package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
	"ai-access-gateway/internal/store"
)

func previewAccessListData(w http.ResponseWriter, r *http.Request, name string, data map[string]any, now time.Time) bool {
	if name != "admin_users" && name != "admin_invites" {
		return true
	}
	f, err := gateway.ParseAccessListFilter(r.URL.Query(), name == "admin_invites")
	if err != nil {
		http.Error(w, "Проверьте фильтры списка", 400)
		return false
	}
	page := func(size int) store.AccessListPage {
		filter := f
		if size == 0 {
			filter.Offset = 0
		} else if filter.Offset >= size {
			filter.Offset = (size - 1) / 50 * 50
		}
		return store.AccessListPage{Filter: filter, Total: int64(size), Now: now}
	}
	hasAccess := func(s domain.AccessSettings) bool {
		switch f.Access {
		case "photo":
			return s.TextToImage || s.ImageToImage
		case "video":
			return s.Video
		case "training":
			return s.Training
		case "comfyui":
			return s.ComfyUI
		case "openwebui":
			return s.OpenWebUI
		case "none":
			return !s.TextToImage && !s.ImageToImage && !s.Video && !s.Training && !s.ComfyUI && !s.OpenWebUI
		}
		return true
	}
	q := strings.ToLower(f.Search)
	if name == "admin_users" {
		rows, _ := data["Users"].([]store.UserAccessRow)
		if old, ok := data["Users"].([]domain.UserRow); ok {
			for _, u := range old {
				rows = append(rows, store.UserAccessRow{User: domain.User{ID: u.ID, Username: u.Username, Email: sql.NullString{String: u.Email, Valid: u.Email != ""}, Role: u.Role, Disabled: u.Disabled,
					CanUseComfyUI: u.CanUseComfyUI, CanUseOpenWebUI: u.CanUseOpenWebUI, CanUseQuickGeneration: u.CanUseQuickGeneration, CanGenerateTextToImage: u.CanGenerateTextToImage,
					CanGenerateImageToImage: u.CanGenerateImageToImage, CanGenerateVideo: u.CanGenerateVideo, CanUseAdvancedGenerationSettings: u.CanUseAdvancedGenerationSettings, CanTrainImageLora: u.CanTrainImageLora,
					PauseMiningForQuickGeneration: u.PauseMiningForQuickGeneration, QueuePriority: u.QueuePriority, GenerationDailyLimit: u.GenerationDailyLimit, GenerationTotalLimit: u.GenerationTotalLimit, GenerationTotalUsed: u.GenerationTotalUsed,
					VideoGenerationDailyLimit: u.VideoGenerationDailyLimit, VideoGenerationTotalLimit: u.VideoGenerationTotalLimit, VideoGenerationTotalUsed: u.VideoGenerationTotalUsed, MaxVideoGenerationQuality: u.MaxVideoGenerationQuality,
					LockedUntil: u.LockedUntil, AccountExpiresAt: u.AccountExpiresAt, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt}})
			}
		}
		matches := []store.UserAccessRow{}
		for _, u := range rows {
			e := u.User.EffectiveAccess(now)
			state := e.State
			if state == "active" && e.LoginLocked {
				state = "locked"
			}
			if q != "" && !strings.Contains(strings.ToLower(u.Username), q) && !strings.Contains(strings.ToLower(u.Email.String), q) && strconv.FormatInt(u.ID, 10) != q {
				continue
			}
			if f.AccountType != "" && (f.AccountType == "temporary") != u.AccountExpiresAt.Valid {
				continue
			}
			if f.State != "" && f.State != state || !hasAccess(e.Settings) {
				continue
			}
			if f.ExpiringSoon && (!u.AccountExpiresAt.Valid || !u.AccountExpiresAt.Time.After(now) || u.AccountExpiresAt.Time.After(now.Add(7*24*time.Hour))) {
				continue
			}
			matches = append(matches, u)
		}
		sort.Slice(matches, func(i, j int) bool {
			a, b := matches[i], matches[j]
			switch f.Sort {
			case "name":
				if a.Username != b.Username {
					return strings.ToLower(a.Username) < strings.ToLower(b.Username)
				}
			case "expires":
				if a.AccountExpiresAt.Valid != b.AccountExpiresAt.Valid {
					return a.AccountExpiresAt.Valid
				}
				if !a.AccountExpiresAt.Time.Equal(b.AccountExpiresAt.Time) {
					return a.AccountExpiresAt.Time.Before(b.AccountExpiresAt.Time)
				}
			case "login":
				if a.LastLoginAt.Valid != b.LastLoginAt.Valid {
					return a.LastLoginAt.Valid
				}
				if !a.LastLoginAt.Time.Equal(b.LastLoginAt.Time) {
					return a.LastLoginAt.Time.After(b.LastLoginAt.Time)
				}
			case "oldest":
				if !a.CreatedAt.Equal(b.CreatedAt) {
					return a.CreatedAt.Before(b.CreatedAt)
				}
			default:
				if !a.CreatedAt.Equal(b.CreatedAt) {
					return a.CreatedAt.After(b.CreatedAt)
				}
			}
			if f.Sort == "newest" || f.Sort == "login" {
				return a.ID > b.ID
			}
			return a.ID < b.ID
		})
		p := page(len(matches))
		data["AccessList"] = gateway.BuildUserAccessList(store.UserAccessPage{AccessListPage: p, Items: matches[p.Filter.Offset:min(p.Filter.Offset+50, len(matches))]})
	} else {
		rows, _ := data["Invites"].([]domain.InviteRow)
		matches := []domain.InviteRow{}
		for _, row := range rows {
			row.Status = "active"
			if row.Revoked {
				row.Status = "revoked"
			} else if !row.ExpiresAt.After(now) {
				row.Status = "expired"
			} else if row.UsedCount >= row.MaxUses {
				row.Status = "used"
			}
			v := gateway.BuildInviteAccessList(store.InviteAccessPage{AccessListPage: store.AccessListPage{Now: now}, Items: []domain.InviteRow{row}}).Invites[0]
			if q != "" && !strings.Contains(strings.ToLower(row.CreatedBy), q) && strconv.FormatInt(row.ID, 10) != q {
				continue
			}
			if f.AccountType != "" && (f.AccountType == "temporary") != (row.AccountLifetimeSeconds > 0) {
				continue
			}
			if f.State != "" && f.State != row.Status || !hasAccess(v.Settings) {
				continue
			}
			if f.ExpiringSoon && (!row.ExpiresAt.After(now) || row.ExpiresAt.After(now.Add(7*24*time.Hour))) {
				continue
			}
			matches = append(matches, row)
		}
		sort.Slice(matches, func(i, j int) bool {
			a, b := matches[i], matches[j]
			switch f.Sort {
			case "creator":
				if a.CreatedBy != b.CreatedBy {
					return strings.ToLower(a.CreatedBy) < strings.ToLower(b.CreatedBy)
				}
			case "uses":
				if a.UsedCount != b.UsedCount {
					return a.UsedCount > b.UsedCount
				}
			case "expires":
				if !a.ExpiresAt.Equal(b.ExpiresAt) {
					return a.ExpiresAt.Before(b.ExpiresAt)
				}
			case "oldest":
				if !a.CreatedAt.Equal(b.CreatedAt) {
					return a.CreatedAt.Before(b.CreatedAt)
				}
			default:
				if !a.CreatedAt.Equal(b.CreatedAt) {
					return a.CreatedAt.After(b.CreatedAt)
				}
			}
			if f.Sort == "newest" || f.Sort == "uses" {
				return a.ID > b.ID
			}
			return a.ID < b.ID
		})
		p := page(len(matches))
		v := gateway.BuildInviteAccessList(store.InviteAccessPage{AccessListPage: p, Items: matches[p.Filter.Offset:min(p.Filter.Offset+50, len(matches))]})
		data["AccessList"], data["InviteAction"] = v, v.CurrentURL
	}
	return true
}

func previewLongAccessUsers(now time.Time) []store.UserAccessRow {
	rows := []store.UserAccessRow{}
	for i := 1; i <= 125; i++ {
		u := domain.User{ID: 9007199254741000 + int64(i), Username: fmt.Sprintf("member-%03d", i), Email: sql.NullString{String: fmt.Sprintf("member-%03d@example.test", i), Valid: true}, Role: "user", CreatedAt: now.Add(-time.Duration(i) * time.Hour), CanUseQuickGeneration: true, CanGenerateTextToImage: true, CanGenerateVideo: i%2 == 0, CanTrainImageLora: i%3 == 0, GenerationDailyLimit: 12, GenerationTotalLimit: 50, GenerationTotalUsed: 17, VideoGenerationDailyLimit: 2, VideoGenerationTotalLimit: 8, VideoGenerationTotalUsed: 3, MaxVideoGenerationQuality: 720}
		if i%5 == 0 {
			u.AccountExpiresAt = sql.NullTime{Time: now.Add(24 * time.Hour), Valid: true}
		}
		if i%7 == 0 {
			u.Disabled = true
		}
		if i%11 == 0 {
			u.AccountExpiresAt = sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
		}
		if i%13 == 0 {
			u.LockedUntil = sql.NullTime{Time: now.Add(time.Hour), Valid: true}
		}
		if i == 1 {
			u.Username = "member-001-длинное-имя-пользователя-для-проверки-переноса"
		}
		rows = append(rows, store.UserAccessRow{User: u, ImageUsedToday: 3, VideoUsedToday: 1})
	}
	return rows
}

func previewLongAccessInvites(now time.Time) []domain.InviteRow {
	rows := []domain.InviteRow{}
	for i := 1; i <= 225; i++ {
		r := domain.InviteRow{ID: 9007199254742000 + int64(i), CreatedBy: fmt.Sprintf("team-%02d", i%3), CreatedAt: now.Add(-time.Duration(i) * time.Hour), ExpiresAt: now.Add(10 * 24 * time.Hour), MaxUses: 3, UsedCount: i % 4, GrantQuickGeneration: true, GrantTextToImage: true, GrantVideo: i%2 == 0, GrantTrainImageLora: i%3 == 0, GenerationDailyLimit: 12, GenerationTotalLimit: 50, VideoGenerationDailyLimit: 2, VideoGenerationTotalLimit: 8, MaxVideoGenerationQuality: 720}
		if i%5 == 0 {
			r.AccountLifetimeSeconds = 168 * 3600
			r.ExpiresAt = now.Add(24 * time.Hour)
		}
		if i%7 == 0 {
			r.Revoked = true
		}
		if i%11 == 0 {
			r.ExpiresAt = now.Add(-time.Hour)
		}
		rows = append(rows, r)
	}
	return rows
}
