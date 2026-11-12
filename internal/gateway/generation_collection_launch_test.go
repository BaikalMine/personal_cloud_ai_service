package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ai-access-gateway/internal/security"
)

func TestGenerationCollectionLaunchHTTP(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(strconv.FormatBool(batch), func(t *testing.T) {
			f := newDispatchFixture(t)
			ctx := context.Background()
			collection, err := f.app.store.CreateGenerationMediaCollection(ctx, f.user.ID, "Launch collection")
			if err != nil {
				t.Fatal(err)
			}
			var other int64
			if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('other-collection-owner','hash','user') RETURNING id`).Scan(&other); err != nil {
				t.Fatal(err)
			}
			foreign, err := f.app.store.CreateGenerationMediaCollection(ctx, other, "Foreign collection")
			if err != nil {
				t.Fatal(err)
			}
			f.app.csrfSigner = security.NewCSRFSigner("collection-launch-csrf")
			f.form.Set("csrf", f.app.csrfSigner.Token("collection-launch-session"))
			f.form.Set("client_request_id", newRequestID())
			f.form.Set("correlation_id", newRequestID())
			path, want, count := "/generate/run", http.StatusAccepted, 1
			if batch {
				path, want, count = "/generate/batches", http.StatusCreated, 2
				f.form.Set("batch_count", "2")
				f.form.Set("batch_mode", "seeds")
			}
			request := func(status int) map[string]any {
				t.Helper()
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(f.form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "collection-launch-session"})
				r = r.WithContext(context.WithValue(ctx, userCtxKey, &f.user))
				w := httptest.NewRecorder()
				if batch {
					f.app.handleGenerationBatches(w, r)
				} else {
					f.app.handleGenerateRun(w, r)
				}
				if w.Code != status {
					t.Fatalf("%s: %d want %d: %s", path, w.Code, status, w.Body.String())
				}
				var response map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				return response
			}
			f.form.Set("collection_id", strconv.FormatInt(foreign.ID, 10))
			request(http.StatusNotFound)
			var jobs int
			if err := f.db.QueryRow(`SELECT count(*) FROM generation_jobs`).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatal("foreign collection created jobs", jobs, err)
			}
			f.form.Set("collection_id", strconv.FormatInt(collection.ID, 10))
			first := request(want)
			identity := func(response map[string]any) any {
				if batch {
					return response["batch"].(map[string]any)["batch_id"]
				}
				return response["job_id"]
			}
			if batch {
				want = http.StatusOK
			}
			f.form.Set("collection_id", strconv.FormatInt(foreign.ID, 10))
			if again := request(want); identity(again) != identity(first) {
				t.Fatal("replay changed job identity", first, again)
			}
			if err := f.db.QueryRow(`SELECT count(*) FROM generation_collection_jobs WHERE collection_id=$1`, collection.ID).Scan(&jobs); err != nil || jobs != count {
				t.Fatal("HTTP binding/replay", jobs, count, err)
			}
			if _, err := f.app.store.DeleteGenerationMediaCollection(ctx, f.user.ID, collection.ID); err != nil {
				t.Fatal(err)
			}
			f.form.Set("collection_id", strconv.FormatInt(collection.ID, 10))
			if again := request(want); identity(again) != identity(first) {
				t.Fatal("collection deletion broke replay", first, again)
			}
			quota, err := f.app.store.QuickGenerationQuota(ctx, f.user.ID)
			if err != nil || quota.Image.TotalUsed != int64(count) || f.posts.Load() != 0 {
				t.Fatal("collection replay re-consumed quota or dispatched", quota, err, f.posts.Load())
			}
		})
	}
}
