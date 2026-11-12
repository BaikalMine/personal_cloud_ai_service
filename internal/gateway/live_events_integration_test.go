package gateway

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func liveIntegrationApp(t *testing.T) (*App, *sql.DB, User, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	user := User{Role: "admin", CanUseQuickGeneration: true, CanTrainImageLora: true}
	if err = db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_train_image_lora)
		VALUES('live-owner','disabled','admin',true,true) RETURNING id`).Scan(&user.ID); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store.New(db), cfg: Config{SessionIdleTimeout: time.Hour}}
	const token = "synthetic-live-session"
	if err = app.store.CreateSession(context.Background(), user.ID, security.HashToken(token), time.Now().Add(time.Hour), "test", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.liveUpdates().close() })
	return app, db, user, token
}

func liveTestServer(app *App, user User) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
		switch r.URL.Path {
		case "/tasks":
			app.handleTaskEvents(w, r)
		case "/jobs":
			app.handleGenerationJobEvents(w, r)
		case "/notifications":
			app.handleUserNotificationEvents(w, r)
		case "/content":
			app.handleAdminContentEvents(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

func readLiveFrame(reader *bufio.Reader) (string, error) {
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		frame.WriteString(line)
		if line == "\n" {
			if strings.Contains(frame.String(), "data:") {
				return frame.String(), nil
			}
			frame.Reset()
		}
	}
}

func TestLiveRevisionsSessionAndOwnerIsolation(t *testing.T) {
	app, db, user, token := liveIntegrationApp(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	hash := security.HashToken(token)
	var lastSeen time.Time
	if err := db.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash=$1`, hash).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO user_task_revisions(user_id,revision) VALUES($1,123)`, user.ID)
	exec(`INSERT INTO user_notification_revision(user_id,revision) VALUES($1,456)`, user.ID)
	states, err := app.store.LiveRevisions(ctx, []string{hash, "missing", "x') OR true--"}, time.Hour)
	if err != nil || len(states) != 1 || states[hash].Tasks != 123 || states[hash].Notifications != 456 || states[hash].User.ID != user.ID {
		t.Fatalf("snapshot: %+v %v", states, err)
	}
	if states[hash].Jobs != 123 {
		t.Fatal("generation cursor is not owner-scoped")
	}
	var other int64
	if err = db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('live-other','disabled','user') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, _, err = app.store.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: "live-other-generation-job", RequestID: "live-other-generation-job", UserID: other}); err != nil {
		t.Fatal(err)
	}
	afterOther, err := app.store.LiveRevisions(ctx, []string{hash}, time.Hour)
	if err != nil || afterOther[hash].Jobs != states[hash].Jobs || afterOther[hash].Tasks != states[hash].Tasks {
		t.Fatalf("another owner invalidated private streams: %+v %v", afterOther, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/generate/jobs", nil)
	request = request.WithContext(context.WithValue(request.Context(), userCtxKey, &user))
	_, cursor, err := app.loadGenerationJobViews(request)
	if err != nil || cursor != states[hash].Jobs {
		t.Fatalf("HTTP and SSE cursors differ: %d %v", cursor, err)
	}
	var after time.Time
	if err = db.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash=$1`, hash).Scan(&after); err != nil || !after.Equal(lastSeen) {
		t.Fatal("poll extended idle session")
	}
	if _, err = app.store.LiveRevisions(ctx, make([]string, store.LiveRevisionBatchLimit+1), time.Hour); err == nil {
		t.Fatal("unbounded store batch")
	}
	for _, tc := range []struct{ name, change, restore string }{
		{"expired", `UPDATE sessions SET expires_at=now()-interval '1 second'`, `UPDATE sessions SET expires_at=now()+interval '1 hour'`},
		{"idle", `UPDATE sessions SET last_seen_at=now()-interval '2 hours'`, `UPDATE sessions SET last_seen_at=now()`},
		{"disabled", `UPDATE users SET disabled=true`, `UPDATE users SET disabled=false`},
		{"account_expired", `UPDATE users SET account_expires_at=now()-interval '1 second'`, `UPDATE users SET account_expires_at=NULL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(tc.change)
			states, err := app.store.LiveRevisions(ctx, []string{hash}, time.Hour)
			if err != nil || len(states) != 0 {
				t.Fatalf("invalid session published: %+v %v", states, err)
			}
			exec(tc.restore)
		})
	}
	exec(`DELETE FROM sessions`)
	states, err = app.store.LiveRevisions(ctx, []string{hash}, time.Hour)
	if err != nil || len(states) != 0 {
		t.Fatal("revoked session published")
	}
}

func TestLiveEventsDatabaseLoadOneTenFiftyTabs(t *testing.T) {
	app, _, user, token := liveIntegrationApp(t)
	initial, err := app.store.LiveRevisions(context.Background(), []string{security.HashToken(token)}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tabs := range []int{1, 10, 50} {
		t.Run(fmt.Sprint(tabs), func(t *testing.T) {
			var queries atomic.Int64
			h := newLiveHub(func(ctx context.Context, hashes []string) (map[string]store.LiveRevisionSnapshot, error) {
				queries.Add(1)
				if len(hashes) != 1 {
					t.Errorf("same session not deduplicated: %d", len(hashes))
				}
				return app.store.LiveRevisions(ctx, hashes, time.Hour)
			}, 100*time.Millisecond)
			app.liveOnce.Do(func() {})
			app.live = h
			server := liveTestServer(app, user)
			defer server.Close()
			defer h.close()
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
						since, _ := liveTopicRevision(initial[security.HashToken(token)], topic)
						request, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/%s?since=%d", server.URL, topic, since), nil)
						request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
						response, err := client.Do(request)
						if err != nil {
							t.Error(err)
							return
						}
						mu.Lock()
						bodies = append(bodies, response.Body)
						mu.Unlock()
						frame, err := readLiveFrame(bufio.NewReader(response.Body))
						if err != nil || response.StatusCode != 200 || !strings.Contains(frame, "event: ready") {
							t.Errorf("%s initial: %d %q %v", topic, response.StatusCode, frame, err)
						}
					}(topic)
				}
			}
			workers.Wait()
			if t.Failed() {
				return
			}
			before := queries.Load()
			time.Sleep(550 * time.Millisecond)
			steady := queries.Load() - before
			if steady < 4 || steady > 7 {
				t.Fatalf("unbatched steady-state SQL calls: %d", steady)
			}
			t.Logf("tabs=%d SSE=%d shared SQL calls=%d over 550ms at 100ms test cadence; handshake middleware excluded", tabs, tabs*4, steady)
		})
	}
}

func TestLiveEventsRecoveryAndSessionRevocation(t *testing.T) {
	app, db, user, token := liveIntegrationApp(t)
	server := liveTestServer(app, user)
	defer server.Close()
	defer app.liveUpdates().close()
	client := &http.Client{Timeout: 8 * time.Second}
	for _, topic := range []string{"tasks", "jobs", "notifications", "content"} {
		t.Run(topic, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodGet, server.URL+"/"+topic+"?since=999", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			frame, err := readLiveFrame(reader)
			if err != nil || !strings.Contains(frame, "event: "+topic) {
				t.Fatalf("missing state recovery: %q %v", frame, err)
			}
			if _, err = db.Exec(`UPDATE sessions SET expires_at=now()-interval '1 second'`); err != nil {
				t.Fatal(err)
			}
			if payload, err := io.ReadAll(reader); err != nil || len(payload) != 0 {
				t.Fatalf("expired stream remained open: %q %v", payload, err)
			}
			if _, err = db.Exec(`UPDATE sessions SET expires_at=now()+interval '1 hour'`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLiveGenerationCursorPrecedesConcurrentListRead(t *testing.T) {
	app, db, user, _ := liveIntegrationApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	before, err := app.store.TaskRevision(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err = lock.ExecContext(ctx, `LOCK TABLE generation_jobs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		revision int64
		err      error
	}
	done := make(chan result, 1)
	request := httptest.NewRequest(http.MethodGet, "/generate/jobs", nil)
	request = request.WithContext(context.WithValue(ctx, userCtxKey, &user))
	go func() { _, revision, err := app.loadGenerationJobViews(request); done <- result{revision, err} }()
	for {
		var waiting bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock'
			AND query LIKE '%FROM generation_jobs%' AND query LIKE '%ORDER BY created_at DESC,id DESC LIMIT%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO user_task_revisions(user_id,revision) VALUES($1,1)
		ON CONFLICT(user_id) DO UPDATE SET revision=user_task_revisions.revision+1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if err = lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.revision != before {
		t.Fatalf("list acknowledged newer concurrent revision: %d want %d: %v", got.revision, before, got.err)
	}
	after, err := app.store.TaskRevision(ctx, user.ID)
	if err != nil || after <= got.revision {
		t.Fatal("concurrent invalidation was lost", err)
	}
}
