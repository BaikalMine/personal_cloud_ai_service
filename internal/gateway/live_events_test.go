package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestLiveEventsBindSessionAndPermissionChanges(t *testing.T) {
	for _, topic := range []string{"tasks", "jobs", "notifications", "content"} {
		t.Run(topic, func(t *testing.T) {
			user := User{ID: 1, Role: "admin"}
			var changed atomic.Bool
			h := newLiveHub(func(context.Context, []string) (map[string]store.LiveRevisionSnapshot, error) {
				current := user
				if changed.Load() {
					current.Role = "user"
				}
				return map[string]store.LiveRevisionSnapshot{security.HashToken("test"): {User: current, Tasks: 42, Jobs: 42, Notifications: 42, Content: 42}}, nil
			}, 5*time.Millisecond)
			app := &App{live: h}
			app.liveOnce.Do(func() {})
			server := liveTestServer(app, user)
			defer server.Close()
			defer h.close()
			request, _ := http.NewRequest(http.MethodGet, server.URL+"/"+topic+"?since=42", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test"})
			client := &http.Client{Timeout: time.Second}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			if frame, err := readLiveFrame(reader); err != nil || !containsLiveReady(frame) {
				t.Fatalf("ready: %q %v", frame, err)
			}
			changed.Store(true)
			if _, err := io.ReadAll(reader); err != nil {
				t.Fatal("stream did not close on changed permission scope", err)
			}
		})
	}
}

func containsLiveReady(frame string) bool {
	return frame == "event: ready\ndata: 42\n\n" || frame == "event: ready\ndata: {\"revision\":42}\n\n" || frame == "id: 42\nevent: ready\ndata: 42\n\n"
}

func TestLiveEventsRejectCookieContextMismatch(t *testing.T) {
	user := User{ID: 1, Role: "admin"}
	h := newLiveHub(func(context.Context, []string) (map[string]store.LiveRevisionSnapshot, error) {
		return map[string]store.LiveRevisionSnapshot{security.HashToken("other"): {User: User{ID: 2, Role: "admin"}}}, nil
	}, time.Millisecond)
	defer h.close()
	app := &App{live: h}
	app.liveOnce.Do(func() {})
	for _, tc := range []struct {
		cookie string
		want   int
	}{{"", 401}, {"other", 403}} {
		r := httptest.NewRequest("GET", "/tasks", nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
		if tc.cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.cookie})
		}
		w := httptest.NewRecorder()
		app.handleTaskEvents(w, r)
		if w.Code != tc.want {
			t.Fatalf("cookie binding: got %d want %d", w.Code, tc.want)
		}
	}
}
