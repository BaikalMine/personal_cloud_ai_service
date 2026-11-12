package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func liveTopicRevision(state store.LiveRevisionSnapshot, topic string) (int64, bool) {
	scope := taskAccessScope(&state.User)
	switch topic {
	case "tasks":
		return state.Tasks, scope.Generation || scope.Training
	case "jobs":
		return state.Jobs, scope.Generation
	case "notifications":
		return state.Notifications, state.User.ID > 0
	case "content":
		return state.Content, state.User.Role == "admin"
	default:
		return 0, false
	}
}

func (a *App) serveLiveEvents(w http.ResponseWriter, r *http.Request, topic string, write func(io.Writer, string, int64) error) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	cookie, err := r.Cookie(sessionCookieName)
	if user == nil || err != nil || cookie.Value == "" {
		http.Error(w, "session_required", http.StatusUnauthorized)
		return
	}
	if _, ok := liveTopicRevision(store.LiveRevisionSnapshot{User: *user}, topic); !ok {
		http.Error(w, "stream_access_denied", http.StatusForbidden)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming_unavailable", http.StatusInternalServerError)
		return
	}
	hub := a.liveUpdates()
	sub, err := hub.subscribe(security.HashToken(cookie.Value))
	if err != nil {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "stream_unavailable", http.StatusServiceUnavailable)
		return
	}
	defer hub.unsubscribe(sub)
	var initial store.LiveRevisionSnapshot
	select {
	case <-r.Context().Done():
		return
	case state, ok := <-sub.states:
		if !ok {
			http.Error(w, "stream_unavailable", http.StatusServiceUnavailable)
			return
		}
		initial = state
	}
	valid := func(state store.LiveRevisionSnapshot) bool {
		_, allowed := liveTopicRevision(state, topic)
		return allowed && state.User.ID == user.ID && state.User.Role == user.Role && taskAccessScope(&state.User) == taskAccessScope(user)
	}
	if !valid(initial) {
		http.Error(w, "stream_access_denied", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	send := func(event string, revision int64, first bool) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if first && topic != "tasks" {
			if _, err := io.WriteString(w, "retry: 2000\n\n"); err != nil {
				return false
			}
		}
		return write(w, event, revision) == nil && controller.Flush() == nil
	}
	since, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("since")), 10, 64)
	if resume, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && resume >= 0 {
		since = resume
	}
	last, _ := liveTopicRevision(initial, topic)
	event := "ready"
	if since != last {
		event = topic
	}
	if !send(event, last, true) {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case state, ok := <-sub.states:
			if !ok || !valid(state) {
				return
			}
			revision, _ := liveTopicRevision(state, topic)
			if revision != last {
				if !send(topic, revision, false) {
					return
				}
				last = revision
			}
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
