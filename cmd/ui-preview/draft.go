package main

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This in-memory fixture exercises the shipped form without a database, GPU,
// user session, or ComfyUI process. It is only registered by cmd/ui-preview.
func registerDraftPreview(mux *http.ServeMux, now time.Time) {
	var mu sync.Mutex
	var revision int64
	type sessionState struct {
		draft  map[string]any
		assets map[string]map[string]any
	}
	sessions := map[string]*sessionState{}
	fields := []string{"input_image", "input_image_2", "input_image_3", "input_image_4", "input_audio", "input_video"}
	serve := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		cookie, _ := r.Cookie("preview_generation_draft")
		if cookie == nil {
			cookie = &http.Cookie{Name: "preview_generation_draft", Value: rand.Text(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode}
			http.SetCookie(w, cookie)
		}
		state := sessions[cookie.Value]
		if state == nil {
			state = &sessionState{assets: map[string]map[string]any{}}
			sessions[cookie.Value] = state
		}
		draft, assets := state.draft, state.assets
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			current := int64(0)
			if draft != nil {
				current = draft["revision"].(int64)
			}
			expected, err := strconv.ParseInt(r.Form.Get("draft_revision"), 10, 64)
			if err != nil || current != expected {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"draft": draft})
				return
			}
			if r.URL.Path == "/generate/draft/delete" {
				draft = nil
			} else {
				values := map[string]string{}
				for name := range r.Form {
					if name == "csrf" || name == "draft_revision" || strings.HasPrefix(name, "draft_asset_") || strings.HasPrefix(name, "draft_name_") || strings.HasPrefix(name, "draft_pending_") || strings.HasPrefix(name, "input_") {
						continue
					}
					values[name] = r.Form.Get(name)
				}
				references := []map[string]any{}
				for _, field := range fields {
					id := r.Form.Get("draft_asset_" + field)
					value := r.Form.Get(field)
					pending := r.Form.Get("draft_pending_" + field)
					if id == "" && value == "" && pending == "" {
						continue
					}
					if value != "" {
						id = "preview-" + field
						name := r.Form.Get("draft_name_" + field)
						if name == "" {
							name = value
						}
						assets[id] = map[string]any{"field": field, "id": id, "name": name, "available": true, "value": value, "url": "/preview/result.svg", "expires_at": now.Add(72 * time.Hour)}
					}
					if asset, ok := assets[id]; ok {
						references = append(references, asset)
					} else {
						references = append(references, map[string]any{"field": field, "id": id, "name": pending, "available": false})
					}
				}
				revision++
				draft = map[string]any{"revision": revision, "values": values, "assets": references, "updated_at": now, "expires_at": now.Add(30 * 24 * time.Hour)}
			}
		}
		state.draft = draft
		_ = json.NewEncoder(w).Encode(map[string]any{"draft": draft, "deleted": draft == nil})
	}
	mux.HandleFunc("/generate/draft", serve)
	mux.HandleFunc("/generate/draft/delete", serve)
}
