package gateway

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func ParseAccessListFilter(values url.Values, invites bool) (store.AccessListFilter, error) {
	for _, key := range []string{"q", "account", "state", "access", "sort", "soon", "offset"} {
		if len(values[key]) > 1 {
			return store.AccessListFilter{}, store.ErrAccessListFilter
		}
	}
	f := store.AccessListFilter{Search: values.Get("q"), AccountType: values.Get("account"), State: values.Get("state"), Access: values.Get("access"), Sort: values.Get("sort"), ExpiringSoon: values.Get("soon") == "1"}
	if values.Get("soon") != "" && values.Get("soon") != "1" {
		return f, store.ErrAccessListFilter
	}
	if raw := values.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || strconv.Itoa(value) != raw {
			return f, store.ErrAccessListFilter
		}
		f.Offset = value
	}
	return f.Normalize(invites)
}

func accessListURL(f store.AccessListFilter, invites bool, offset int) string {
	path := "/admin/users"
	if invites {
		path = "/admin/invites"
	}
	v := url.Values{}
	for key, value := range map[string]string{"q": f.Search, "account": f.AccountType, "state": f.State, "access": f.Access} {
		if value != "" {
			v.Set(key, value)
		}
	}
	if f.Sort != "" && f.Sort != "newest" {
		v.Set("sort", f.Sort)
	}
	if f.ExpiringSoon {
		v.Set("soon", "1")
	}
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	return path
}

type accessListView struct {
	store.AccessListPage
	Invitations                                        bool
	Action, CurrentURL, ResetURL, PreviousURL, NextURL string
	First, Last                                        int64
	Users                                              []userAccessView
	Invites                                            []inviteAccessView
}

type userAccessView struct {
	store.UserAccessRow
	Settings                       domain.AccessSettings
	State, StateLabel, AccessLabel string
	Quotas                         []accessQuotaView
}

type inviteAccessView struct {
	domain.InviteRow
	Settings                                        domain.AccessSettings
	AccessLabel, RestoreLabel, RevokeURL, DeleteURL string
	Quotas                                          []accessQuotaView
}

type accessQuotaView struct{ Family, Daily, Total string }

func newAccessListView(p store.AccessListPage, invites bool, size int) accessListView {
	v := accessListView{AccessListPage: p, Invitations: invites}
	v.Action = "/admin/users"
	if invites {
		v.Action = "/admin/invites"
	}
	v.ResetURL = v.Action + "#access-results"
	v.CurrentURL = accessListURL(p.Filter, invites, p.Filter.Offset)
	if p.Total > 0 {
		v.First = int64(p.Filter.Offset) + 1
		v.Last = int64(p.Filter.Offset + size)
	}
	if p.Filter.Offset > 0 {
		v.PreviousURL = accessListURL(p.Filter, invites, max(0, p.Filter.Offset-store.AccessListPageSize)) + "#access-results"
	}
	if v.Last < p.Total {
		v.NextURL = accessListURL(p.Filter, invites, p.Filter.Offset+store.AccessListPageSize) + "#access-results"
	}
	return v
}

func accessListLabel(s domain.AccessSettings) string {
	labels := []string{}
	for _, c := range []struct {
		allowed bool
		label   string
	}{{s.Harness, "Harness"}, {s.TextToImage || s.ImageToImage, "Фото"}, {s.Video, "Видео"}, {s.Training, "Обучение LoRA"}} {
		if c.allowed {
			labels = append(labels, c.label)
		}
	}
	if len(labels) == 0 {
		return "Нет доступа"
	}
	return strings.Join(labels, ", ")
}

func accessListQuotas(s domain.AccessSettings, imageToday, videoToday int, imageTotal, videoTotal int64, remaining bool) []accessQuotaView {
	rows := []accessQuotaView{}
	for _, q := range []struct {
		enabled          bool
		label            string
		daily, usedToday int
		total, usedTotal int64
	}{
		{s.TextToImage || s.ImageToImage, "Фото", s.ImageDailyLimit, imageToday, s.ImageTotalLimit, imageTotal},
		{s.Video, "Видео", s.VideoDailyLimit, videoToday, s.VideoTotalLimit, videoTotal},
	} {
		if !q.enabled {
			continue
		}
		r := accessQuotaView{Family: q.label, Daily: "Без суточного ограничения", Total: "Без общего ограничения"}
		if remaining {
			if q.daily > 0 {
				r.Daily = fmt.Sprintf("Осталось %d из %d сегодня", max(0, q.daily-q.usedToday), q.daily)
			}
			if q.total > 0 {
				r.Total = fmt.Sprintf("Осталось %d из %d всего", max(int64(0), q.total-q.usedTotal), q.total)
			}
		} else {
			if q.daily > 0 {
				r.Daily = fmt.Sprintf("%d в сутки", q.daily)
			}
			if q.total > 0 {
				r.Total = fmt.Sprintf("%d всего", q.total)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func BuildUserAccessList(p store.UserAccessPage) accessListView {
	v := newAccessListView(p.AccessListPage, false, len(p.Items))
	for _, row := range p.Items {
		effective := row.User.EffectiveAccess(p.Now)
		state := effective.State
		if state == "active" && effective.LoginLocked {
			state = "locked"
		}
		v.Users = append(v.Users, userAccessView{UserAccessRow: row, Settings: effective.Settings, State: state,
			StateLabel:  map[string]string{"active": "Активен", "disabled": "Отключён", "expired": "Срок истёк", "locked": "Вход заблокирован"}[state],
			AccessLabel: accessListLabel(effective.Settings), Quotas: accessListQuotas(effective.Settings, row.ImageUsedToday, row.VideoUsedToday, row.GenerationTotalUsed, row.VideoGenerationTotalUsed, true)})
	}
	return v
}

func BuildInviteAccessList(p store.InviteAccessPage) accessListView {
	v := newAccessListView(p.AccessListPage, true, len(p.Items))
	for _, row := range p.Items {
		s := domain.AccessSettings{ComfyUI: row.GrantComfyUI, OpenWebUI: row.GrantOpenWebUI, QuickGeneration: row.GrantQuickGeneration,
			Harness:     row.GrantHarness,
			TextToImage: row.GrantTextToImage, ImageToImage: row.GrantImageToImage, Video: row.GrantVideo, Training: row.GrantTrainImageLora,
			AdvancedSettings: row.GrantAdvancedGenerationSettings, PauseMining: row.PauseMiningForQuickGeneration, QueuePriority: row.QueuePriority,
			ImageDailyLimit: row.GenerationDailyLimit, ImageTotalLimit: row.GenerationTotalLimit, VideoDailyLimit: row.VideoGenerationDailyLimit,
			VideoTotalLimit: row.VideoGenerationTotalLimit, MaxVideoQuality: row.MaxVideoGenerationQuality}.Normalize()
		label := "Вернуть"
		if !row.ExpiresAt.After(p.Now) || row.UsedCount >= row.MaxUses {
			label = "Снять отзыв"
		}
		action := "revoke"
		if row.Revoked {
			action = "unrevoke"
		}
		link, _ := url.Parse(v.CurrentURL)
		link.Path = fmt.Sprintf("/admin/invites/%d/%s", row.ID, action)
		revokeURL := link.String()
		link.Path = fmt.Sprintf("/admin/invites/%d/delete", row.ID)
		v.Invites = append(v.Invites, inviteAccessView{InviteRow: row, Settings: s, AccessLabel: accessListLabel(s), RestoreLabel: label, RevokeURL: revokeURL, DeleteURL: link.String(), Quotas: accessListQuotas(s, 0, 0, 0, 0, false)})
	}
	return v
}
