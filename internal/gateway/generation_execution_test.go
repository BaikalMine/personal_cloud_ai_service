package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestGenerationExecutionSnapshot(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("generation-evidence-test-secret-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{contentCipher: cipher}
	job := domain.GenerationJob{ID: 7, PublicID: "generation-evidence-0001"}
	input := generationForm{TemplateID: "image-to-image", PresetID: "photoflow-krea2-edit", ModelFamily: modelFamilyKrea2,
		Seed: (1 << 62) + 14, Positive: "private execution prompt", AssistantSuggestion: "private assistant draft",
		InputImage: "owner-source.png", ReferenceImages: [3]string{"", "owner-third.png"},
		ReferenceMetadata: [maxGenerationReferenceSlots]generationReferenceMetadata{{Source: "gallery", SourceID: "42", Role: "base_scene"}, {}, {Role: "identity", Source: "upload"}},
		InputAudio:        "owner-audio.wav", InputVideo: "owner-video.mp4"}
	prompt := map[string]any{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": "exact-base.safetensors"}},
		"2": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": input.Seed, "steps": 8}},
		"3": map[string]any{"class_type": "Power Lora Loader (rgthree)", "inputs": map[string]any{
			"lora_1": map[string]any{"on": true, "lora": "active.safetensors", "strength": 0.75},
			"lora_2": map[string]any{"on": false, "lora": "disabled.safetensors", "strength": 1}}},
	}
	prepared := generationPreparation{Input: input, Definition: workflowDefinition{ID: "image-to-image-krea2"}, Prompt: prompt}
	record, err := a.captureGenerationExecution(context.Background(), job, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.PayloadCipher), input.Positive) {
		t.Fatal("execution stored unencrypted")
	}
	snapshot, err := a.decodeGenerationExecution(job, record)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Seed != "4611686018427387918" || !strings.Contains(string(snapshot.Graph), "4611686018427387918") || snapshot.Workflow.Version == "" || snapshot.Workflow.ManifestSHA256 == "" {
		t.Fatalf("lost execution identity: %+v", snapshot)
	}
	if strings.Contains(string(snapshot.ResolvedInput), input.AssistantSuggestion) {
		t.Fatal("unexecuted assistant draft included")
	}
	if len(snapshot.Assets) != 4 || snapshot.Assets[1].Number != 3 || snapshot.Assets[1].InputName != "owner-third.png" || snapshot.Assets[0].SourceID != "42" {
		t.Fatalf("asset roles lost: %+v", snapshot.Assets)
	}
	if len(snapshot.Components) != 2 || snapshot.Components[1].Name != "active.safetensors" || snapshot.Components[0].HashState != "not_observed" || snapshot.Components[0].SHA256 != "" {
		t.Fatalf("invented components or hashes: %+v", snapshot.Components)
	}
	prompt["1"].(map[string]any)["inputs"].(map[string]any)["unet_name"] = "changed.safetensors"
	if strings.Contains(string(snapshot.Graph), "changed.safetensors") {
		t.Fatal("stored graph aliases mutable preparation")
	}
	wrong := job
	wrong.PublicID = "another-evidence-0001"
	if _, err = a.decodeGenerationExecution(wrong, record); err == nil {
		t.Fatal("cross-job snapshot accepted")
	}
	corrupt := record
	corrupt.PayloadSHA256 = strings.Repeat("0", 64)
	if _, err = a.decodeGenerationExecution(job, corrupt); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	prepared.Input.ModelFamily = modelFamilyCheckpoint
	prepared.Input.PresetID = "old-checkpoint"
	record, err = a.captureGenerationExecution(context.Background(), job, prepared)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = a.decodeGenerationExecution(job, record)
	if err != nil || len(snapshot.Workflow.Manifest) != 0 {
		t.Fatal("legacy checkpoint borrowed a Krea manifest", err)
	}
}

func TestGenerationPassportBoundary(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		method string
		user   *User
		query  string
		status int
	}{
		{"POST", nil, "", 405}, {"GET", nil, "", 401}, {"GET", &User{ID: 1}, "", 403},
		{"GET", &User{ID: 1, Role: "admin", Disabled: true}, "", 401},
		{"GET", &User{ID: 1, Role: "admin", AccountExpiresAt: sql.NullTime{Valid: true, Time: time.Now().Add(-time.Hour)}}, "", 401},
		{"GET", &User{ID: 1, Role: "admin"}, "?job=bad", 400},
		{"GET", &User{ID: 1, Role: "admin"}, "?job=generation-fixture-001&job=generation-fixture-002", 400},
	} {
		r := httptest.NewRequest(tc.method, "/generate/jobs/passport"+tc.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
		w := httptest.NewRecorder()
		a.handleGenerationPassport(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("boundary: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestGenerationExecutionDispatchIntegration(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "lost-response"}[uncertain], func(t *testing.T) {
			f := newDispatchFixture(t)
			ctx := context.Background()
			job := f.enqueue(t)
			var parentID int64
			if err := f.db.QueryRow(`INSERT INTO generation_jobs(public_id,correlation_id,request_id,user_id,state) VALUES('execution-parent-001','execution-parent-001','execution-parent-001',$1,'completed') RETURNING id`, f.user.ID).Scan(&parentID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE generation_jobs SET parent_job_id=$2 WHERE id=$1`, job.ID, parentID); err != nil {
				t.Fatal(err)
			}
			if uncertain {
				f.status = 504
				f.body = `{"error":"response lost"}`
			}
			_, dispatchErr := f.app.dispatchGenerationJobs(ctx)
			if (dispatchErr != nil) != uncertain {
				t.Fatal("unexpected dispatch", dispatchErr)
			}
			record, err := f.app.store.GenerationExecutionForJob(ctx, f.user.ID, job.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.app.decodeGenerationExecution(job, record)
			if err != nil {
				t.Fatal(err)
			}
			if string(snapshot.Graph) != string(f.graph.Load().(json.RawMessage)) {
				t.Fatal("saved graph differs from actual HTTP submission")
			}
			if !strings.Contains(string(snapshot.Graph), job.PublicID) || len(snapshot.Workflow.Manifest) == 0 {
				t.Fatal("output prefix or manifest not captured")
			}
			var expected generationForm
			if err = json.Unmarshal(snapshot.ResolvedInput, &expected); err != nil {
				t.Fatal(err)
			}
			definition, recovered, err := f.app.generationProjectionParameters(ctx, job, url.Values{"template_id": {"removed-from-current-code"}, "seed": {"not-a-number"}})
			if err != nil || recovered.Seed != expected.Seed || recovered.Positive != expected.Positive || definition.ID != snapshot.Workflow.DefinitionID {
				t.Fatal("recovery reinterpreted the old form", err)
			}
			before := record.PayloadSHA256
			f.app = &App{store: store.New(f.db), contentCipher: f.app.contentCipher, cfg: f.app.cfg}
			if _, err = f.app.dispatchGenerationJobs(ctx); err != nil {
				t.Fatal(err)
			}
			record, err = f.app.store.GenerationExecutionForJob(ctx, f.user.ID, job.PublicID)
			if err != nil || record.PayloadSHA256 != before || f.posts.Load() != 1 {
				t.Fatal("restart replaced evidence or replayed send", err)
			}
			request := func(user User, publicID string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/generate/jobs/passport?job="+publicID, nil)
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
				w := httptest.NewRecorder()
				f.app.handleGenerationPassport(w, r)
				return w
			}
			w := request(f.user, job.PublicID)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"evidence_state":"submission_recorded"`) {
				t.Fatal("passport unavailable", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"parent_available":true`) || snapshot.ParentJobID != "execution-parent-001" {
				t.Fatal("source identity not recorded", w.Body.String())
			}
			if _, err = f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, parentID); err != nil {
				t.Fatal(err)
			}
			w = request(f.user, job.PublicID)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"parent_available":false`) || !strings.Contains(w.Body.String(), `"parent_job_id":"execution-parent-001"`) {
				t.Fatal("deleted source was lost", w.Body.String())
			}
			other := f.user
			other.ID++
			foreign := request(other, job.PublicID)
			missing := request(other, "missing-generation-0001")
			if foreign.Code != 404 || foreign.Body.String() != missing.Body.String() {
				t.Fatal("owner boundary leaked")
			}
			if _, err = f.app.store.GenerationExecutionForJob(ctx, other.ID, job.PublicID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("foreign execution readable", err)
			}
			if _, err = f.db.Exec(`UPDATE generation_jobs SET payload_cipher=NULL WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			if w = request(f.user, job.PublicID); w.Code != 200 {
				t.Fatal("passport depends on mutable recipe", w.Body.String())
			}
			if _, err = f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			var remaining int
			if err = f.db.QueryRow(`SELECT count(*) FROM generation_job_executions WHERE job_id=$1`, job.ID).Scan(&remaining); err != nil || remaining != 0 {
				t.Fatal("retention did not clear evidence", err)
			}
		})
	}
}

func TestGenerationExecutionVideoComponents(t *testing.T) {
	prompt := map[string]any{
		"stack":   miniMaxH3Node("CR LoRA Stack", map[string]any{"lora_name_1": "enabled.safetensors", "switch_1": "On", "lora_name_2": "disabled.safetensors", "switch_2": "Off"}),
		"rife":    miniMaxH3Node("RIFE VFI", map[string]any{"ckpt_name": "rife.pth"}),
		"upscale": miniMaxH3Node("UpscaleModelLoader", map[string]any{"model_name": "upscale.pth"}),
	}
	items := generationExecutionComponents(prompt)
	if len(items) != 3 || items[0].Kind != "interpolation_model" || items[1].Name != "enabled.safetensors" || items[2].Kind != "upscale_model" {
		t.Fatalf("video components: %+v", items)
	}
}

func TestGenerationExecutionAtomicFenceIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	job := f.enqueue(t)
	claimed, err := f.app.store.ClaimNextGenerationDispatch(ctx, "execution-fence", 2)
	if err != nil {
		t.Fatal(err)
	}
	record := domain.GenerationExecutionRecord{JobID: job.ID, PayloadCipher: []byte("fixture"), PayloadSHA256: strings.Repeat("a", 64)}
	if err = f.app.store.BeginGenerationJobSubmissionWithExecution(ctx, job.ID, "stale", record); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatal("stale worker wrote evidence", err)
	}
	if _, err = f.app.store.GenerationExecutionForJob(ctx, f.user.ID, job.PublicID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("orphan evidence", err)
	}
	if _, err = f.db.Exec(`INSERT INTO generation_job_executions(job_id,payload_cipher,payload_sha256) VALUES($1,$2,$3)`, job.ID, record.PayloadCipher, record.PayloadSHA256); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.BeginGenerationJobSubmissionWithExecution(ctx, job.ID, claimed.DispatchToken, record); err == nil {
		t.Fatal("overwrote existing evidence")
	}
	current, err := f.app.store.GenerationJobByID(ctx, job.ID)
	if err != nil || current.SubmissionStartedAt != nil {
		t.Fatal("partial send marker survived failed evidence write", err)
	}
	if _, err = f.db.Exec(`DELETE FROM generation_job_executions WHERE job_id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.BeginGenerationJobSubmissionWithExecution(ctx, job.ID, claimed.DispatchToken, record); err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.BeginGenerationJobSubmissionWithExecution(ctx, job.ID, claimed.DispatchToken, record); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatal("repeated marker accepted", err)
	}
}
