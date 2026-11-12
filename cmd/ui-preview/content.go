package main

import (
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
)

func registerAdminContentPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, base map[string]any, now time.Time) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		data := make(map[string]any, len(base))
		for key, value := range base {
			data[key] = value
		}
		events := append([]gateway.ContentEventView(nil), base["Events"].([]gateway.ContentEventView)...)
		if r.URL.Query().Get("scenario") == "mixed" {
			events = append(events,
				gateway.ContentEventView{Key: "video-preview", Version: "video-1", UserID: 2, Username: "video-author", Service: "comfyui", Model: "MiniMax H3", GenerationState: "completed", StateLabel: "Готово", StateClass: "is-complete", Prompt: "A camera follows a moving subject.", MediaCount: 1, Media: []domain.ContentMediaSummary{{ID: 42044, MediaType: "video", UpdatedAt: now}}, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)},
				gateway.ContentEventView{Key: "chat-preview", Version: "chat-1", UserID: 2, Username: "chat-author", Service: "openwebui", Model: "Qwen", StateLabel: "Ответ сохранён", Prompt: "Составь список кадров", Response: "Общий план, средний план, деталь.", CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)},
				gateway.ContentEventView{Key: "assistant-preview", Version: "assistant-1", UserID: 2, Username: "assistant-author", Service: "ollama", Kind: "prompt_assistant", Model: "Qwen VL", StateLabel: "Ассистент", Assistant: &gateway.ContentAssistantView{OriginalPrompt: "Исходная идея", Suggestion: "Уточнённая идея", Decision: "kept_original"}, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)},
				gateway.ContentEventView{Key: "queued-preview", Version: "queued-1", UserID: 2, Username: "queue-author", Service: "comfyui", GenerationState: "queued", StateLabel: "В очереди", StateClass: "is-pending", StatusMessage: "Ожидает GPU", CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)},
				gateway.ContentEventView{Key: "cancelled-preview", Version: "cancelled-1", UserID: 2, Username: "cancel-author", Service: "comfyui", GenerationState: "cancelled", StateLabel: "Отменено", StateClass: "is-cancelled", CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)},
			)
		}
		username, service, query := r.URL.Query().Get("user"), r.URL.Query().Get("service"), r.URL.Query().Get("q")
		filtered := make([]gateway.ContentEventView, 0, len(events))
		overview := gateway.ContentOverview{}
		for _, event := range events {
			if username != "" && !strings.Contains(strings.ToLower(event.Username), strings.ToLower(username)) {
				continue
			}
			if service != "" && event.Service != service {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{event.Prompt, event.Response, event.Model, event.ErrorMessage, event.Key}, " ")), strings.ToLower(query)) {
				continue
			}
			filtered = append(filtered, event)
			overview.Total++
			if event.Service == "comfyui" {
				overview.ComfyUI++
			}
			if event.Service == "openwebui" {
				overview.OpenWebUI++
			}
			if len(event.Media) > 0 {
				overview.WithMedia++
			}
		}
		data["Events"], data["Overview"] = filtered, overview
		data["Username"], data["Service"], data["Query"] = username, service, query
		render("admin_content", "AI-контент", data)(w, r)
	}
	mux.HandleFunc("/preview/content", handler)
	mux.HandleFunc("/admin/content", handler)
}
