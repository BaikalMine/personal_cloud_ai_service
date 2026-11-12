package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
)

func registerInviteTemplatesPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, invites []domain.InviteRow) {
	type item struct {
		ID       string                        `json:"id"`
		Revision string                        `json:"revision"`
		Name     string                        `json:"name"`
		Settings domain.InviteTemplateSettings `json:"settings"`
	}
	settings := domain.InviteTemplateSettings{Version: 1, Access: domain.AccessSettings{QuickGeneration: true, TextToImage: true, ImageToImage: true, Video: true, Training: true, QueuePriority: true, ImageDailyLimit: 12, ImageTotalLimit: 50, VideoDailyLimit: 2, VideoTotalLimit: 8, MaxVideoQuality: 720}, LinkLifetimeHours: 24, AccountLifetimeSeconds: 168 * 3600, MaxUses: 1}
	items := map[string]item{}
	for i := 0; i < 55; i++ {
		id := strconv.FormatInt(9007199254741000+int64(i), 10)
		items[id] = item{ID: id, Revision: "1", Name: fmt.Sprintf("Шаблон %02d: фото, видео и обучение", i+1), Settings: settings}
	}
	var mutex sync.Mutex
	next := int64(9007199254742000)
	mux.HandleFunc("/admin/invite-templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		write := func(code int, data any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(data) }
		mutex.Lock()
		defer mutex.Unlock()
		if r.Method == http.MethodGet {
			if id := r.URL.Query().Get("id"); id != "" {
				value, ok := items[id]
				if !ok {
					write(404, map[string]any{"error": "Шаблон не найден"})
					return
				}
				write(200, value)
				return
			}
			query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
			matches := []item{}
			for _, value := range items {
				if strings.Contains(strings.ToLower(value.Name), query) {
					matches = append(matches, value)
				}
			}
			sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if offset < 0 {
				offset = 0
			}
			if offset >= len(matches) {
				offset = max(0, ((len(matches)-1)/50)*50)
			}
			write(200, map[string]any{"templates": matches[offset:min(offset+50, len(matches))], "total": len(matches), "offset": offset, "page_size": 50})
			return
		}
		if r.Method != http.MethodPost {
			write(405, map[string]any{"error": "Метод не поддерживается"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		if err := r.ParseForm(); err != nil {
			write(400, map[string]any{"error": "Некорректная форма"})
			return
		}
		id, revision := r.PostForm.Get("template_id"), r.PostForm.Get("revision")
		if id != "" && id != "0" {
			if old, ok := items[id]; !ok || old.Revision != revision {
				write(409, map[string]any{"error": "Шаблон изменён или удалён. Загрузите актуальную версию."})
				return
			}
		}
		if r.PostForm.Get("action") == "delete" {
			delete(items, id)
			write(200, map[string]any{"deleted": true, "id": id})
			return
		}
		value, err := gateway.InvitationSettingsFromForm(r.PostForm)
		if err != nil {
			write(400, map[string]any{"error": err.Error()})
			return
		}
		if r.PostForm.Get("action") == "preview" {
			write(200, map[string]any{"settings": value})
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		if len([]rune(name)) < 1 || len([]rune(name)) > 80 {
			write(400, map[string]any{"error": "Укажите имя до 80 символов"})
			return
		}
		for key, old := range items {
			if key != id && strings.EqualFold(old.Name, name) {
				write(409, map[string]any{"error": "Шаблон с таким именем уже существует"})
				return
			}
		}
		if id == "" || id == "0" {
			id = strconv.FormatInt(next, 10)
			next++
			revision = "0"
		}
		rev, _ := strconv.ParseInt(revision, 10, 64)
		result := item{ID: id, Revision: strconv.FormatInt(rev+1, 10), Name: name, Settings: value}
		items[id] = result
		write(200, result)
	})
	mux.HandleFunc("/admin/invites", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Invites": invites}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
			_ = r.ParseForm()
			data["InviteForm"] = r.PostForm
			value, err := gateway.InvitationSettingsFromForm(r.PostForm)
			if err != nil {
				data["Error"] = err.Error()
			} else {
				raw, _ := json.Marshal(value)
				sum := sha256.Sum256(raw)
				digest := hex.EncodeToString(sum[:])
				data["InviteReview"], data["ReviewDigest"], data["InviteReviewed"] = value, digest, true
				if r.PostForm.Get("action") == "create" && r.PostForm.Get("invite_review") == digest {
					data["Error"] = "В предпросмотре создание настоящих приглашений отключено."
				}
			}
		}
		render("admin_invites", "Приглашения: предпросмотр", data)(w, r)
	})
}
