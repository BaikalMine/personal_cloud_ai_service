package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
)

func TestLoraTrainingHandoffIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, lookup, fence string
		cancel, submit      bool
		bound               bool
		want                domain.LoraTrainingState
		wantError           bool
	}{
		{name: "lost submit response", lookup: "running", submit: true, want: domain.LoraTrainingRunning},
		{name: "accepted after restart", lookup: "running", want: domain.LoraTrainingRunning},
		{name: "agent unavailable", lookup: "unavailable", want: domain.LoraTrainingUploading, wantError: true},
		{name: "old agent", lookup: "old", want: domain.LoraTrainingUploading, wantError: true},
		{name: "unknown status", lookup: "unexpected", want: domain.LoraTrainingUploading, wantError: true},
		{name: "restarted executor", lookup: "unconfirmed", want: domain.LoraTrainingUploading, wantError: true},
		{name: "bound executor uncertain", lookup: "unconfirmed", bound: true, want: domain.LoraTrainingPreparing, wantError: true},
		{name: "cancel accepted still running", lookup: "running", cancel: true, want: domain.LoraTrainingRunning},
		{name: "late submit fenced", lookup: "missing", fence: "settled", want: domain.LoraTrainingFailed},
		{name: "cancel late submit", lookup: "missing", fence: "settled", cancel: true, want: domain.LoraTrainingCancelled},
		{name: "completed between lookup and fence", lookup: "missing", fence: "completed", want: domain.LoraTrainingCompleted},
		{name: "fence not confirmed", lookup: "missing", fence: "pending", cancel: true, want: domain.LoraTrainingUploading, wantError: true},
		{name: "bad fence receipt", lookup: "missing", fence: "invalid", cancel: true, want: domain.LoraTrainingUploading, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGatewayIntegrationDatabase(t, db)
			repository := store.New(db)
			var userID, minerID int64
			if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('handoff-owner','disabled','admin') RETURNING id`).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `INSERT INTO miners(name,script_path,process_name,enabled,is_default,created_by_user_id) VALUES('fixture','test.cmd','fixture.exe',true,true,$1) RETURNING id`, userID).Scan(&minerID); err != nil {
				t.Fatal(err)
			}
			spool := t.TempDir()
			publicID := newRequestID()
			dataset := filepath.Join(spool, "lora-training", publicID, "dataset.zip")
			if err := os.MkdirAll(filepath.Dir(dataset), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dataset, []byte("dataset fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			job, err := repository.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
				PublicID: publicID, UserID: userID, UsernameSnapshot: "handoff-owner", RequestID: newRequestID(),
				ProfileID: "test-krea", Family: "krea2", BaseModel: "fixture", Name: "Handoff fixture", OutputName: "handoff_fixture",
				TriggerWord: "test_person", ConceptType: "character", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
				NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5, DatasetBytes: 15, DatasetPath: dataset,
			})
			if err != nil {
				t.Fatal(err)
			}
			leaseID := newRequestID()
			if err := repository.CreateQuickGenerationMiningLease(ctx, domain.QuickGenerationMiningLease{ID: leaseID, UserID: userID, MinerID: minerID, LoraTrainingJobID: job.ID, ScriptPath: "test.cmd", ProcessName: "fixture.exe", ResumeMining: false}); err != nil {
				t.Fatal(err)
			}
			var submissions atomic.Int32
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/jobs" {
					submissions.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					http.Error(w, "response lost after acceptance", 504)
					return
				}
				if r.URL.Path == "/v1/gateway-jobs/fence" {
					result := loratraining.SubmissionFenceResult{GatewayJobID: publicID, Fenced: true, Settled: tc.fence == "settled"}
					if tc.fence == "invalid" {
						result.GatewayJobID = "wrong-job"
					}
					if tc.fence == "completed" {
						result.Settled = true
						result.Job = &loratraining.JobStatus{ID: "agent-completed", GatewayJobID: publicID, State: "completed", ArtifactName: "fixture.safetensors", ArtifactBytes: 100}
					}
					_ = json.NewEncoder(w).Encode(result)
					return
				}
				switch tc.lookup {
				case "unavailable":
					http.Error(w, "offline", 503)
				case "old":
					http.NotFound(w, r)
				case "missing":
					w.WriteHeader(404)
					_, _ = io.WriteString(w, `{"code":"job_not_found"}`)
				default:
					status := loratraining.JobStatus{ID: "agent-job", GatewayJobID: publicID, State: tc.lookup}
					if tc.lookup == "unconfirmed" {
						status.State, status.ExecutionUnconfirmed = "failed", true
					}
					_ = json.NewEncoder(w).Encode(status)
				}
			}))
			defer agent.Close()
			endpoint, _ := url.Parse(agent.URL)
			app := &App{store: repository, cfg: Config{MediaSpoolDir: spool}, loraTraining: loratraining.NewClient(endpoint, "handoff-test-token-with-32-characters")}
			if tc.submit {
				if _, err := app.refreshLoraTrainingJobs(ctx); err == nil {
					t.Fatal("lost response was not reported")
				}
			} else {
				if _, err := repository.ClaimNextLoraTrainingJob(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := repository.BeginLoraTrainingSubmission(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bound {
				if err := repository.AttachLoraTrainingAgentJob(ctx, job.ID, "agent-job", "Preparing", "accepted", 3); err != nil {
					t.Fatal(err)
				}
			}
			if tc.cancel {
				if _, err := repository.RequestLoraTrainingCancellation(ctx, publicID, userID, false); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := repository.RequeueLoraTrainingJob(ctx, job.ID, "unsafe retry"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("uncertain handoff was requeued: %v", err)
			}
			if n, err := repository.RecoverLoraTrainingJobs(ctx); err != nil || n != 0 {
				t.Fatalf("restart discarded submission evidence: %d %v", n, err)
			}
			if _, err := repository.QuickGenerationMiningLeaseByLoraTrainingJobID(ctx, job.ID); err != nil {
				t.Fatal("lost response/cancel released mining before confirmation")
			}
			// Replace the App to ensure recovery does not depend on an in-memory request.
			app = &App{store: repository, cfg: Config{MediaSpoolDir: spool}, loraTraining: app.loraTraining}
			_, refreshErr := app.refreshLoraTrainingJobs(ctx)
			if (refreshErr != nil) != tc.wantError {
				t.Fatalf("refresh error = %v, wantError=%v", refreshErr, tc.wantError)
			}
			got, err := repository.LoraTrainingJobByID(ctx, job.ID)
			if err != nil || got.State != tc.want || got.AgentSubmissionStartedAt == nil {
				t.Fatalf("recovered job: %+v %v", got, err)
			}
			if got.State == domain.LoraTrainingCompleted && (got.AgentJobID != "agent-completed" || got.ArtifactName != "fixture.safetensors") {
				t.Fatalf("completed race lost its downloadable agent artifact: %+v", got)
			}
			if tc.submit && submissions.Load() != 1 || !tc.submit && submissions.Load() != 0 {
				t.Fatalf("recovery resubmitted the dataset: %d", submissions.Load())
			}
			_, leaseErr := repository.QuickGenerationMiningLeaseByLoraTrainingJobID(ctx, job.ID)
			_, fileErr := os.Stat(dataset)
			if got.State.Terminal() {
				if !errors.Is(leaseErr, sql.ErrNoRows) || !errors.Is(fileErr, os.ErrNotExist) {
					t.Fatalf("confirmed terminal work kept resources: lease=%v file=%v", leaseErr, fileErr)
				}
			} else if leaseErr != nil || fileErr != nil {
				t.Fatalf("unconfirmed/active work lost resources: lease=%v file=%v", leaseErr, fileErr)
			}
		})
	}
}
