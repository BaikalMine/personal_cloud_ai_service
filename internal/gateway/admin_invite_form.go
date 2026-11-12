package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type adminAccessForm struct{ values url.Values }

func inviteForm(value any) adminAccessForm {
	values, ok := value.(url.Values)
	if !ok || values == nil {
		values = url.Values{"grant_quick_generation": {"on"}, "grant_text_to_image": {"on"}, "account_type": {"permanent"}, "temporary_account_hours": {"168"}, "invite_ttl_hours": {"24"}, "max_uses": {"1"}, "max_video_generation_quality": {"720"}}
	}
	copy := url.Values{}
	for key, items := range values {
		copy[key] = append([]string(nil), items...)
	}
	if copy.Get("limit_form_version") != "2" {
		for _, name := range []string{"image_generation_daily", "image_generation_total", "video_generation_daily", "video_generation_total"} {
			if raw := copy.Get(name + "_limit"); raw == "" || raw == "0" {
				copy.Set(name+"_unlimited", "on")
			}
		}
	}
	return adminAccessForm{values: copy}
}

func (f adminAccessForm) Value(name string) string { return f.values.Get(name) }
func (f adminAccessForm) Checked(name string) bool { return f.Value(name) == "on" }

func (f adminAccessForm) SupportedVideoQuality() bool {
	switch f.Value("max_video_generation_quality") {
	case "480", "720", "1080", "1440":
		return true
	}
	return false
}

type accessQuotaField struct {
	Name      string
	Label     string
	Value     string
	Maximum   int
	Unlimited bool
}

func (f adminAccessForm) Quotas() []accessQuotaField {
	result := []accessQuotaField{}
	for _, family := range []struct{ key, label string }{{"image_generation", "Фото"}, {"video_generation", "Видео"}} {
		for _, period := range []struct {
			key, label string
			maximum    int
		}{{"daily", "в сутки", 100000}, {"total", "всего", 10000000}} {
			name := family.key + "_" + period.key
			value := f.Value(name + "_limit")
			unlimited := f.Checked(name + "_unlimited")
			if unlimited && (value == "" || value == "0") {
				value = "1"
			}
			result = append(result, accessQuotaField{Name: name, Label: family.label + ": " + period.label, Value: value, Maximum: period.maximum, Unlimited: unlimited})
		}
	}
	return result
}

func inviteDefaultsJSON() string { raw, _ := json.Marshal(invitationDefaults()); return string(raw) }

func inviteReviewDigest(settings domain.InviteTemplateSettings) string {
	raw, _ := json.Marshal(settings)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// InvitationSettingsFromForm is shared by the live handlers and the isolated
// UI preview. Authorization and CSRF remain the responsibility of the caller.
func InvitationSettingsFromForm(form url.Values) (domain.InviteTemplateSettings, error) {
	r := &http.Request{Form: form}
	access, err := parseAccessSettings(r, true)
	if err != nil {
		return domain.InviteTemplateSettings{}, err
	}
	lifetime, err := parseInviteAccountLifetime(r)
	if err != nil {
		return domain.InviteTemplateSettings{}, err
	}
	hours, hoursErr := strconv.Atoi(form.Get("invite_ttl_hours"))
	uses, usesErr := strconv.Atoi(form.Get("max_uses"))
	settings := domain.InviteTemplateSettings{Version: 1, Access: access, LinkLifetimeHours: hours, AccountLifetimeSeconds: lifetime, MaxUses: uses}
	if hoursErr != nil || usesErr != nil || settings.Validate() != nil {
		return domain.InviteTemplateSettings{}, fmt.Errorf("Проверьте сроки ссылки, аккаунта и число активаций.")
	}
	return settings, nil
}

func (a *App) renderAdminInvites(w http.ResponseWriter, r *http.Request, data map[string]any) {
	if r.Method == http.MethodPost && data["InviteLink"] == nil {
		data["InviteForm"] = r.PostForm
	}
	filter, err := ParseAccessListFilter(r.URL.Query(), true)
	if err != nil {
		http.Error(w, "Проверьте фильтры списка", http.StatusBadRequest)
		return
	}
	page, err := a.store.ListInvitesPage(r.Context(), filter)
	if err != nil {
		// A successfully issued token must remain visible even when the list read
		// fails. Do not invite the administrator to repeat the creating POST.
		data["ListError"] = "Список приглашений не загрузился. Обновите список."
		data["AccessList"] = BuildInviteAccessList(store.InviteAccessPage{AccessListPage: store.AccessListPage{Filter: filter}})
	} else {
		data["AccessList"] = BuildInviteAccessList(page)
	}
	data["InviteAction"] = accessListURL(filter, true, filter.Offset)
	a.render(w, r, "admin_invites", data)
}

type accessSummaryRow struct{ Key, Label, Value string }

func accessSummary(s domain.AccessSettings) []accessSummaryRow {
	s = s.Normalize()
	yes := func(v bool) string {
		if v {
			return "Разрешено"
		}
		return "Нет доступа"
	}
	quota := func(allowed bool, daily int, total int64) string {
		if !allowed {
			return "Нет доступа"
		}
		d, t := "без ограничения в сутки", "без общего ограничения"
		if daily > 0 {
			d = strconv.Itoa(daily) + " в сутки"
		}
		if total > 0 {
			t = strconv.FormatInt(total, 10) + " всего"
		}
		return d + "; " + t
	}
	quality := "Нет доступа"
	if s.Video {
		quality = strconv.Itoa(s.MaxVideoQuality) + "p"
	}
	return []accessSummaryRow{
		{"harness", "Harness", yes(s.Harness)},
		{"photo", "Фото", quota(s.TextToImage || s.ImageToImage, s.ImageDailyLimit, s.ImageTotalLimit)},
		{"video", "Видео", quota(s.Video, s.VideoDailyLimit, s.VideoTotalLimit)},
		{"quality", "Базовое качество видео", quality},
		{"text", "По тексту", yes(s.TextToImage)}, {"edit", "Редактирование фото", yes(s.ImageToImage)},
		{"training", "Обучение LoRA", yes(s.Training)}, {"advanced", "Точные настройки", yes(s.AdvancedSettings)},
		{"priority", "Приоритетная очередь", yes(s.QueuePriority)}, {"mining", "Пауза майнинга", yes(s.PauseMining)},
	}
}
