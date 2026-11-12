package inference

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func (f *fixture) durableWorker(t *testing.T, endpoint, directory string) *Worker {
	t.Helper()
	u, _ := url.Parse(endpoint)
	w, err := NewDurableWorker(f.store, f.cipher, u, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

func (f *fixture) blockOutcomeCommit(t *testing.T) func() {
	t.Helper()
	_, err := f.db.Exec(`CREATE OR REPLACE FUNCTION fixture_block_outcome() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.state IN ('completed','not_dispatched') THEN RAISE EXCEPTION 'fixture outcome commit blocked'; END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER fixture_block_outcome BEFORE UPDATE ON gpu_inference_requests FOR EACH ROW EXECUTE FUNCTION fixture_block_outcome()`)
	if err != nil {
		t.Fatal(err)
	}
	unblock := func() {
		if _, err := f.db.Exec(`DROP TRIGGER IF EXISTS fixture_block_outcome ON gpu_inference_requests; DROP FUNCTION IF EXISTS fixture_block_outcome()`); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(unblock)
	return unblock
}

// A real child executor is killed only after its receipt reaches disk while the
// database rejects completion. No production process or real model is involved.
func TestOutcomeReceiptSurvivesExecutorProcessCrash(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	unblock := f.blockOutcomeCommit(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write(testResponse) }))
	defer server.Close()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestOutcomeExecutorCrashHelper$", "-test.v")
	command.Env = append(os.Environ(), "OUTCOME_CRASH_HELPER=1", "OUTCOME_TEST_DIR="+directory, "OUTCOME_TEST_ENDPOINT="+server.URL)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			command.Process.Kill()
			command.Wait()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(directory + string(os.PathSeparator) + outcomeName(job.ID)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			command.Process.Kill()
			command.Wait()
			waited = true
			t.Fatalf("child did not persist receipt: %s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("child did not terminate abnormally")
	}
	waited = true
	saved, err := f.store.GPUInference(context.Background(), job.ID)
	if err != nil || saved.State != "running" || saved.CompletionCode != "" {
		t.Fatalf("completion was committed before crash: %+v %v", saved, err)
	}
	unblock()
	restarted := f.durableWorker(t, server.URL, directory)
	if _, err = restarted.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, body, err := NewClient(f.store, f.cipher).Wait(context.Background(), job.ID, job.LeaseToken)
	if err != nil || !bytes.Equal(body, testResponse) || saved.State != "completed" || calls.Load() != 1 {
		t.Fatalf("restart repeated/lost result: %+v calls=%d error=%v", saved, calls.Load(), err)
	}
	work, err := f.store.GPUWorkBySource(context.Background(), domain.GPUWorkAssistant, f.work.JobKey)
	if err != nil || work.InferenceCompletion != "executor_terminal" {
		t.Fatalf("GPU evidence not restored: %+v %v", work, err)
	}
	entries, _ := fs.ReadDir(restarted.journal.root.FS(), ".")
	if len(entries) != 0 {
		t.Fatal("committed receipt not removed")
	}
}

func TestOutcomeExecutorCrashHelper(t *testing.T) {
	if os.Getenv("OUTCOME_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := contentcrypto.NewCipher("inference-fixture-secret-never-production")
	u, _ := url.Parse(os.Getenv("OUTCOME_TEST_ENDPOINT"))
	w, err := NewDurableWorker(store.New(db), cipher, u, os.Getenv("OUTCOME_TEST_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err = w.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOutcomeReceiptExpiryKeepsEvidenceWithoutExtendingRetention(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	claimed, err := f.store.ClaimGPUInference(context.Background(), "outcome-expired-run")
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().Add(-25 * time.Hour).UTC()
	if _, err = f.db.Exec(`UPDATE gpu_inference_requests SET started_at=$2 WHERE id=$1`, job.ID, observed.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	w := f.durableWorker(t, "http://127.0.0.1:1", t.TempDir())
	body, _ := f.cipher.EncryptBytesWithAAD(testResponse, responseAAD(job.ID))
	o := observedOutcome{Version: 1, ID: job.ID, RunToken: claimed.RunToken, LeaseToken: job.LeaseToken, RequestHash: job.RequestHash, Code: "executor_terminal", Status: 200, Response: body, ObservedAt: observed}
	if err = w.journal.save(o); err != nil {
		t.Fatal(err)
	}
	unblock := f.blockOutcomeCommit(t)
	if err = w.restoreOutcomes(context.Background()); err == nil {
		t.Fatal("storage failure ignored")
	}
	redacted, err := w.journal.read(outcomeName(job.ID))
	if err != nil || len(redacted.Response) != 0 || redacted.Code != "executor_terminal" {
		t.Fatalf("receipt body not expired: %+v %v", redacted, err)
	}
	unblock()
	if err = w.restoreOutcomes(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, _, err := NewClient(f.store, f.cipher).Wait(context.Background(), job.ID, job.LeaseToken)
	if !errors.Is(err, ErrResultExpired) || saved.CompletionCode != "executor_terminal" || len(saved.ResponseCipher) != 0 || saved.ResultExpiresAt.After(time.Now()) {
		t.Fatalf("expired receipt resurrected content: %+v %v", saved, err)
	}
}

func TestOutcomeRecoveryRejectsIdentityMismatchAndUnknownExecution(t *testing.T) {
	for _, kind := range []string{"token", "hash", "lease", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			job := f.submit(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(504) }))
			defer server.Close()
			w := f.durableWorker(t, server.URL, t.TempDir())
			if kind == "unknown" {
				if _, err := w.RunOne(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := w.RunOne(context.Background()); err != nil {
					t.Fatal(err)
				}
				saved, _ := f.store.GPUInference(context.Background(), job.ID)
				entries, _ := fs.ReadDir(w.journal.root.FS(), ".")
				if calls.Load() != 1 || saved.State != "uncertain" || saved.CompletionCode != "" || len(entries) != 0 {
					t.Fatal("unknown model execution treated as terminal")
				}
				return
			}
			claimed, err := f.store.ClaimGPUInference(context.Background(), "outcome-test-run")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := f.cipher.EncryptBytesWithAAD(testResponse, responseAAD(job.ID))
			o := observedOutcome{Version: 1, ID: job.ID, RunToken: claimed.RunToken, LeaseToken: job.LeaseToken, RequestHash: job.RequestHash, Code: "executor_terminal", Status: 200, Response: body, ObservedAt: time.Now().UTC()}
			switch kind {
			case "token":
				o.RunToken = "another-run"
			case "hash":
				o.RequestHash = strings.Repeat("f", 64)
			case "lease":
				o.LeaseToken = "another-lease"
			}
			if err = w.journal.save(o); err != nil {
				t.Fatal(err)
			}
			if _, err = w.RunOne(context.Background()); !errors.Is(err, store.ErrGPUWorkConflict) {
				t.Fatalf("identity mismatch ignored: %v", err)
			}
			if !w.journalBlocked.Load() || calls.Load() != 0 {
				t.Fatal("invalid receipt allowed execution")
			}
			saved, _ := f.store.GPUInference(context.Background(), job.ID)
			if saved.Settled() {
				t.Fatal("invalid receipt completed a job")
			}
		})
	}
}

func TestOutcomeRecoveryAfterCancellationAndDuplicateReplay(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	claimed, err := f.store.ClaimGPUInference(context.Background(), "outcome-cancel-run")
	if err != nil {
		t.Fatal(err)
	}
	w := f.durableWorker(t, "http://127.0.0.1:1", t.TempDir())
	if !w.journalBlocked.Load() {
		t.Fatal("executor advertised readiness before recovery")
	}
	body, _ := f.cipher.EncryptBytesWithAAD(testResponse, responseAAD(job.ID))
	o := observedOutcome{Version: 1, ID: job.ID, RunToken: claimed.RunToken, LeaseToken: job.LeaseToken,
		RequestHash: job.RequestHash, Code: "executor_terminal", Status: 200, Response: body, ObservedAt: time.Now().UTC()}
	if _, err = f.store.CancelGPUInference(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = w.journal.save(o); err != nil {
			t.Fatal(err)
		}
		if _, err = w.RunOne(context.Background()); err != nil {
			t.Fatal(err)
		}
		saved, err := f.store.GPUInference(context.Background(), job.ID)
		if err != nil || saved.State != "completed" || !saved.CancelRequested || saved.CompletionCode != "executor_terminal" ||
			saved.ResultExpiresAt == nil || saved.ResultExpiresAt.Sub(o.ObservedAt.Add(24*time.Hour)).Abs() > time.Microsecond {
			t.Fatalf("replay changed cancellation/evidence/retention: %+v %v", saved, err)
		}
		if w.journalBlocked.Load() {
			t.Fatal("successful recovery did not restore readiness")
		}
		entries, _ := fs.ReadDir(w.journal.root.FS(), ".")
		if len(entries) != 0 {
			t.Fatal("duplicate receipt not removed")
		}
	}
}

func TestOutcomeJournalFailurePreventsNewDispatch(t *testing.T) {
	f := newFixture(t)
	job := f.submit(t)
	w := f.durableWorker(t, "http://127.0.0.1:1", t.TempDir())
	w.journal.root.Close()
	if worked, err := w.RunOne(context.Background()); err == nil || worked || !w.journalBlocked.Load() {
		t.Fatalf("closed journal allowed dispatch: worked=%v error=%v", worked, err)
	}
	saved, err := f.store.GPUInference(context.Background(), job.ID)
	if err != nil || saved.State != "queued" || saved.RunToken != "" {
		t.Fatalf("job claimed with unavailable journal: %+v %v", saved, err)
	}
}
