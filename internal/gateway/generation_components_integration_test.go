package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func TestGenerationComponentDispatchIntegration(t *testing.T) {
	for _, outcome := range []string{"replaced", "missing", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := gpuDispatchFixture(t)
			ctx := context.Background()
			job := f.enqueue(t)
			input, err := parseGenerationValues(ctx, f.form)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := f.app.prepareGeneration(ctx, &f.user, input, false)
			if err != nil {
				t.Fatal(err)
			}
			request, err := generationComponentRequest(job.PublicID, prepared.Prompt)
			if err != nil {
				t.Fatal(err)
			}
			roots := map[string][]string{}
			filenames := map[string]string{}
			for _, ref := range request.References {
				if roots[ref.Kind] == nil {
					roots[ref.Kind] = []string{t.TempDir()}
				}
				filename := filepath.Join(roots[ref.Kind][0], filepath.FromSlash(ref.Name))
				if err = os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filename, []byte("original fixture bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				filenames[ref.Key()] = filename
			}
			inspector, err := componentinspection.New(roots)
			if err != nil {
				t.Fatal(err)
			}
			defer inspector.Close()
			var release atomic.Bool
			var activityProbes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
					t.Error("missing agent auth")
				}
				if r.URL.Path == "/v1/activity" {
					activityProbes.Add(1)
					_ = json.NewEncoder(w).Encode(loratraining.Activity{ProtocolVersion: 1, State: "idle"})
					return
				}
				var request componentinspection.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				result, err := inspector.Inspect(request)
				if err != nil {
					t.Error(err)
					w.WriteHeader(503)
					return
				}
				// Hold the read-only observer response, never the GPU or Comfy send.
				if !release.Load() {
					result.State = "pending"
					for n := range result.Items {
						result.Items[n] = componentinspection.Observation{Reference: request.References[n], State: "pending", CheckedAt: time.Now().UTC()}
					}
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			f.app.cfg.GenerationComponentInspectionEnabled = true
			f.app.loraTraining = loratraining.NewClient(base, strings.Repeat("t", 32))
			if _, err = f.app.dispatchGenerationJobs(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := f.app.store.GenerationJobByID(ctx, job.ID)
			if err != nil || current.State != domain.GenerationJobWaitingForResources || current.SubmissionStartedAt != nil || current.QuotaReservedOn == nil || current.QuotaCommittedAt != nil || f.posts.Load() != 0 {
				t.Fatalf("pending dispatched: %+v %v", current, err)
			}
			var work int
			if err = f.db.QueryRow(`SELECT (SELECT count(*) FROM gpu_work)+(SELECT count(*) FROM quick_generation_mining_leases)`).Scan(&work); err != nil {
				t.Fatal(err)
			}
			if work != 0 || activityProbes.Load() != 0 {
				t.Fatal("hash wait admitted GPU work")
			}
			if _, err = f.app.store.GenerationExecutionForJob(ctx, f.user.ID, job.PublicID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("pending fabricated execution", err)
			}
			for deadline := time.Now().Add(5 * time.Second); ; {
				result, err := inspector.Inspect(request)
				if err != nil {
					t.Fatal(err)
				}
				if result.State == "complete" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("hash timeout")
				}
				time.Sleep(time.Millisecond)
			}
			changedRef := request.References[0]
			if outcome == "missing" {
				err = os.Remove(filenames[changedRef.Key()])
			} else if outcome == "replaced" {
				err = os.WriteFile(filenames[changedRef.Key()], []byte("replacement bytes with different size"), 0600)
			} else {
				_, err = f.db.Exec(`UPDATE generation_jobs SET cancellation_requested_at=now() WHERE id=$1`, job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			release.Store(true)
			for deadline := time.Now().Add(5 * time.Second); ; {
				if _, err = f.db.Exec(`UPDATE generation_jobs SET dispatch_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = f.app.dispatchGenerationJobs(ctx); err != nil {
					t.Fatal(err)
				}
				current, err = f.app.store.GenerationJobByID(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "cancelled" || current.State.Terminal() || f.posts.Load() > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("dispatch did not settle")
				}
				time.Sleep(time.Millisecond)
			}
			if outcome == "missing" {
				if current.State != domain.GenerationJobFailed || current.ResourcesReleasedAt == nil || current.QuotaReservedOn != nil || current.QuotaCommittedAt != nil || f.posts.Load() != 0 {
					t.Fatalf("missing file not released: %+v", current)
				}
				return
			}
			if outcome == "cancelled" {
				if f.posts.Load() != 0 {
					t.Fatal("cancelled job sent")
				}
				return
			}
			record, err := f.app.store.GenerationExecutionForJob(ctx, f.user.ID, job.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.app.decodeGenerationExecution(job, record)
			if err != nil {
				t.Fatal(err)
			}
			if f.posts.Load() != 1 || snapshot.FileObservations == nil {
				t.Fatal("missing execution", snapshot)
			}
			active, err := f.app.store.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
			if err != nil || active.JobKey != job.PublicID || active.Kind != domain.GPUWorkGeneration || activityProbes.Load() == 0 {
				t.Fatalf("verified submission bypassed GPU coordinator: %+v %v", active, err)
			}
			for _, item := range snapshot.FileObservations.Items {
				payload, err := os.ReadFile(filenames[item.Reference.Key()])
				if err != nil {
					t.Fatal(err)
				}
				if item.SHA256 != generationEvidenceDigest(payload) || item.Bytes != int64(len(payload)) {
					t.Fatal("snapshot did not observe current bytes", item)
				}
			}
			if _, err = f.app.dispatchGenerationJobs(ctx); err != nil || f.posts.Load() != 1 {
				t.Fatal("submission replayed", err)
			}
		})
	}
}
