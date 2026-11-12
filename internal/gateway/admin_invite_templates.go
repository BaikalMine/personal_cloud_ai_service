package gateway

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func inviteTemplateJSON(item domain.InviteTemplate) map[string]any {
	return map[string]any{"id": strconv.FormatInt(item.ID, 10), "revision": strconv.FormatInt(item.Revision, 10),
		"name": item.Name, "settings": item.Settings, "updated_at": item.UpdatedAt}
}

func invitationDefaults() []map[string]any {
	photo := domain.InviteTemplateSettings{Version: 1, Access: domain.AccessSettings{QuickGeneration: true, TextToImage: true, ImageToImage: true, MaxVideoQuality: 720}, LinkLifetimeHours: 24, MaxUses: 1}
	video := photo
	video.Access.Video = true
	training := photo
	training.Access.Training = true
	harness := domain.InviteTemplateSettings{Version: 1, Access: domain.AccessSettings{Harness: true, MaxVideoQuality: 720}, LinkLifetimeHours: 24, MaxUses: 1}
	return []map[string]any{{"key": "photo", "name": "Фото", "settings": photo}, {"key": "photo-video", "name": "Фото и видео", "settings": video}, {"key": "training", "name": "Обучение", "settings": training}, {"key": "harness", "name": "Только Harness", "settings": harness}}
}

func (a *App) handleAdminInviteTemplates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Доступ запрещён"})
		return
	}
	fail := func(err error) {
		status, message := http.StatusInternalServerError, "Не удалось прочитать или сохранить шаблон. Повторите попытку."
		switch {
		case errors.Is(err, store.ErrInviteTemplateDenied):
			status, message = http.StatusForbidden, "Доступ администратора изменился. Обновите страницу."
		case errors.Is(err, sql.ErrNoRows):
			status, message = http.StatusNotFound, "Шаблон удалён или не найден."
		case errors.Is(err, store.ErrInviteTemplateConflict):
			status, message = http.StatusConflict, "Шаблон уже изменён или удалён. Загрузите актуальную версию; ваши поля не сохранены."
		case errors.Is(err, store.ErrInviteTemplateName):
			status, message = http.StatusConflict, "Шаблон с таким именем уже существует."
		}
		writeJSON(w, status, map[string]any{"error": message})
	}
	bad := func(message string) { writeJSON(w, http.StatusBadRequest, map[string]any{"error": message}) }
	if r.Method == http.MethodGet {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(r.URL.RawQuery) > 2048 {
			bad("Некорректный запрос шаблонов")
			return
		}
		for key, items := range values {
			if len(items) != 1 || (key != "id" && key != "q" && key != "offset") {
				bad("Некорректный запрос шаблонов")
				return
			}
		}
		if raw := values.Get("id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
				bad("Некорректный номер шаблона")
				return
			}
			item, err := a.store.InviteTemplate(r.Context(), actor.ID, id)
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, http.StatusOK, inviteTemplateJSON(item))
			return
		}
		offset := 0
		if raw := values.Get("offset"); raw != "" {
			var err error
			offset, err = strconv.Atoi(raw)
			if err != nil || offset < 0 || offset > 1000000 {
				bad("Некорректная страница")
				return
			}
		}
		search := strings.TrimSpace(values.Get("q"))
		if !utf8.ValidString(search) || len([]rune(search)) > 80 {
			bad("Поиск: не больше 80 символов")
			return
		}
		page, err := a.store.ListInviteTemplates(r.Context(), actor.ID, search, offset)
		if err != nil {
			fail(err)
			return
		}
		items := make([]map[string]any, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, inviteTemplateJSON(item))
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": items, "defaults": invitationDefaults(), "total": page.Total, "offset": page.Offset, "page_size": 50})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Метод не поддерживается"})
		return
	}
	if r.URL.RawQuery != "" {
		bad("Параметры должны быть в форме")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		bad("Некорректная или слишком большая форма")
		return
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			bad("Поле передано несколько раз")
			return
		}
	}
	if !a.validCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Неверный защитный токен"})
		return
	}
	var id, revision int64
	if raw := r.PostForm.Get("template_id"); raw != "" && raw != "0" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
			bad("Некорректный номер шаблона")
			return
		}
		rawRevision := r.PostForm.Get("revision")
		revision, err = strconv.ParseInt(rawRevision, 10, 64)
		if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != rawRevision {
			bad("Отсутствует версия шаблона")
			return
		}
	} else if raw := r.PostForm.Get("revision"); raw != "" && raw != "0" {
		bad("Новый шаблон не имеет версии")
		return
	}
	switch r.PostForm.Get("action") {
	case "delete":
		if id == 0 {
			bad("Выберите шаблон")
			return
		}
		if err := a.store.DeleteInviteTemplate(r.Context(), actor.ID, id, revision); err != nil {
			fail(err)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_template_deleted", "invite_template", &id, a.clientIP(r), r.UserAgent(), map[string]any{"revision": revision})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": strconv.FormatInt(id, 10)})
	case "save", "preview":
		value, err := InvitationSettingsFromForm(r.PostForm)
		if err != nil {
			bad(err.Error())
			return
		}
		if r.PostForm.Get("action") == "preview" {
			// A preview is read-only, but still uses fresh administrator authority.
			fresh, err := a.store.UserByID(r.Context(), actor.ID)
			if err != nil {
				fail(err)
				return
			}
			if fresh.Role != "admin" || fresh.EffectiveAccess(time.Now()).State != "active" {
				fail(store.ErrInviteTemplateDenied)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"settings": value})
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		if !utf8.ValidString(name) || len([]rune(name)) < 1 || len([]rune(name)) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			bad("Имя шаблона: от 1 до 80 символов, одной строкой")
			return
		}
		item, err := a.store.SaveInviteTemplate(r.Context(), actor.ID, domain.InviteTemplate{ID: id, Revision: revision, Name: name, Settings: value})
		if err != nil {
			fail(err)
			return
		}
		a.audit(r.Context(), &actor.ID, "invite_template_saved", "invite_template", &item.ID, a.clientIP(r), r.UserAgent(), map[string]any{"name": item.Name, "revision": item.Revision})
		writeJSON(w, http.StatusOK, inviteTemplateJSON(item))
	default:
		bad("Неизвестное действие с шаблоном")
	}
}
