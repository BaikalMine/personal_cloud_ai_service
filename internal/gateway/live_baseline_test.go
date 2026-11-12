package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Replays the pre-F12 SQL polling paths against the same disposable database.
// Cadence is scaled by 100/750; the task timer retains its original 2s/750ms ratio.
func TestLiveLegacyPollingDatabaseBaseline(t *testing.T) {
	app, _, user, _ := liveIntegrationApp(t)
	for _, tabs := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(tabs), func(t *testing.T) {
			var queries atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				read := func(recheck bool) error {
					if r.URL.Path == "/tasks" && recheck {
						queries.Add(1)
						if _, err := app.store.UserByID(r.Context(), user.ID); err != nil {
							return err
						}
					}
					queries.Add(1)
					var err error
					switch r.URL.Path {
					case "/tasks":
						_, err = app.store.TaskRevision(r.Context(), user.ID)
					case "/jobs":
						_, err = app.store.GenerationJobRevision(r.Context())
					case "/notifications":
						_, err = app.store.UserNotificationRevision(r.Context(), user.ID)
					case "/content":
						_, err = app.store.ContentRevision(r.Context())
					}
					return err
				}
				if err := read(false); err != nil {
					http.Error(w, "unavailable", 500)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: ready\ndata: 0\n\n")
				w.(http.Flusher).Flush()
				interval := 100 * time.Millisecond
				if r.URL.Path == "/tasks" {
					interval = 100 * time.Millisecond * 8 / 3
				}
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
						if read(true) != nil {
							return
						}
					}
				}
			}))
			defer server.Close()
			client := &http.Client{Timeout: 8 * time.Second}
			var workers sync.WaitGroup
			var mu sync.Mutex
			var bodies []io.ReadCloser
			defer func() {
				for _, body := range bodies {
					body.Close()
				}
			}()
			for i := 0; i < tabs; i++ {
				for _, topic := range []string{"tasks", "jobs", "notifications", "content"} {
					workers.Add(1)
					go func(topic string) {
						defer workers.Done()
						response, err := client.Get(server.URL + "/" + topic)
						if err != nil {
							t.Error(err)
							return
						}
						mu.Lock()
						bodies = append(bodies, response.Body)
						mu.Unlock()
						if _, err := readLiveFrame(bufio.NewReader(response.Body)); err != nil {
							t.Error(err)
						}
					}(topic)
				}
			}
			workers.Wait()
			before := queries.Load()
			time.Sleep(550 * time.Millisecond)
			count := queries.Load() - before
			if count < int64(tabs*14) || count > int64(tabs*25) {
				t.Fatalf("baseline window outside expected bounds: %d", count)
			}
			t.Logf("legacy tabs=%d SSE=%d SQL calls=%d over 550ms at scaled cadence; handshake middleware excluded", tabs, tabs*4, count)
		})
	}
}
