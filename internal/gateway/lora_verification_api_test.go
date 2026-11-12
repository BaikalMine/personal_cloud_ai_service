package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestLoraVerificationConfirmationIsAuthenticatedAndPurposeBound(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("lora-verification-test-secret-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{contentCipher: cipher}
	value := loraVerificationConfirmation{Purpose: "lora-verification-v1", UserID: 123, RequestID: "request-id-fixture", SourceID: "source-id-fixture", Digest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute).Unix()}
	token, err := app.encodeLoraVerificationConfirmation(value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.decodeLoraVerificationConfirmation(token)
	if err != nil || got != value {
		t.Fatal(got, err)
	}
	for _, invalid := range []string{token[:len(token)/2] + "changed", "not-base64", strings.Repeat("a", 4097)} {
		if _, err := app.decodeLoraVerificationConfirmation(invalid); err == nil {
			t.Fatal("invalid confirmation accepted")
		}
	}
	value.Purpose = "unrelated-token"
	token, _ = app.encodeLoraVerificationConfirmation(value)
	if _, err := app.decodeLoraVerificationConfirmation(token); err == nil {
		t.Fatal("another-purpose token accepted")
	}
}

func TestLoraVerificationRequestBindsCommonSettingsAndNormalizesPlan(t *testing.T) {
	values := url.Values{"client_request_id": {"verification-request-001"}, "lora_verification_source": {"verification-source-001"}, "lora_verification_plan": {`{"version":1,"cases":[{"prompt":" scene ","seed":"42"}],"weights":["0.50"]}`}, "width": {"512"}, "input_image": {"owned-reference"}, "csrf": {"token"}}
	plan, first, err := loraVerificationRequest(values)
	if err != nil || plan.Cases[0].Prompt != "scene" || plan.Weights[0] != "0.5" {
		t.Fatal(plan, err)
	}
	values.Set("csrf", "rotated")
	values.Set("lora_verification_confirmation", "encrypted")
	encoded, _ := json.Marshal(plan)
	values.Set("lora_verification_plan", string(encoded))
	_, second, err := loraVerificationRequest(values)
	if err != nil || first != second {
		t.Fatal("unstable canonical digest", err)
	}
	for _, field := range []string{"width", "input_image", "model", "negative_prompt"} {
		changed := cloneGenerationValues(values)
		changed.Set(field, "changed")
		_, digest, err := loraVerificationRequest(changed)
		if err != nil || digest == first {
			t.Fatal("unbound field", field, err)
		}
	}
	values.Add("width", "1024")
	if _, _, err := loraVerificationRequest(values); err == nil {
		t.Fatal("ambiguous form accepted")
	}
}

func TestLoraVerificationQuoteEnqueueAndReadIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	fixture := loraUsageFixture(t)
	fixture.PublicID = "verification-api-source-001"
	fixture.UsernameSnapshot = f.user.Username
	fixture.Provenance.GatewayJobID = fixture.PublicID
	p := fixture.Provenance
	source, err := f.app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: fixture.PublicID, RequestID: fixture.PublicID, UserID: f.user.ID, UsernameSnapshot: f.user.Username, ProfileID: fixture.ProfileID, Family: fixture.Family, BaseModel: fixture.BaseModel, Name: "Verification source", OutputName: "adapter", TriggerWord: "fixture", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 1, SampleCount: 5, DatasetBytes: 100, DatasetPath: "fixture-private-path"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.app.store.UpdateLoraTrainingJob(ctx, source.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingCompleted, Provenance: p, ArtifactName: p.Artifact.Name, ArtifactBytes: p.Artifact.Bytes}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE lora_training_jobs SET agent_job_id=$2 WHERE id=$1`, source.ID, fixture.AgentJobID); err != nil {
		t.Fatal(err)
	}
	info := compatibilityFixtureObjectInfo(t)
	reference, _ := loratraining.ArtifactRelativeName(f.user.Username, fixture.Family, fixture.ArtifactName)
	loader := info["LoraLoader"]
	var options []json.RawMessage
	if err := json.Unmarshal(loader.Input.Required["lora_name"], &options); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(options[0], &names); err != nil {
		t.Fatal(err)
	}
	names = append(names, reference)
	options[0], _ = json.Marshal(names)
	loader.Input.Required["lora_name"], _ = json.Marshal(options)
	info["LoraLoader"] = loader
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/object_info" {
			t.Errorf("unexpected GPU execution call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	}))
	defer comfy.Close()
	f.app.cfg.ComfyUIUpstream, _ = url.Parse(comfy.URL)
	var inspectionState atomic.Value
	inspectionState.Store("verified")
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/jobs/"+fixture.AgentJobID+"/inspect" {
			t.Errorf("unexpected training call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		var req loratraining.ArtifactInspectionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		result := loratraining.ArtifactInspection{Version: 1, AgentJobID: fixture.AgentJobID, GatewayJobID: source.PublicID, Owner: f.user.Username, Family: source.Family, ProfileSHA256: p.ProfileSHA256, Artifact: p.Artifact, RelativeName: reference, State: inspectionState.Load().(string), CheckedAt: time.Now()}
		for _, name := range req.Models {
			name, _ = loratraining.ModelRelativeName(name)
			result.Models = append(result.Models, loratraining.ModelInspection{Name: name, State: "compatible", Architecture: "krea2"})
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer agent.Close()
	agentURL, _ := url.Parse(agent.URL)
	f.app.loraTraining = loratraining.NewClient(agentURL, strings.Repeat("test", 8))
	f.app.csrfSigner = security.NewCSRFSigner("verification-csrf-secret")
	form := cloneGenerationValues(f.form)
	form.Set("client_request_id", "verification-api-request-001")
	form.Set("lora_verification_source", source.PublicID)
	form.Set("lora_verification_plan", `{"version":1,"cases":[{"prompt":"A ceramic teapot on a table in daylight.","seed":"42"}],"weights":["0.5","1"]}`)
	post := func(path string, values url.Values, user *User) *httptest.ResponseRecorder {
		t.Helper()
		values = cloneGenerationValues(values)
		values.Set("csrf", f.app.csrfSigner.Token("session-fixture"))
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-fixture"})
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, user))
		w := httptest.NewRecorder()
		f.app.handleLoraVerificationWrite(w, r)
		return w
	}
	read := func(query string, user *User) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/lora-training/verifications?"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, user))
		w := httptest.NewRecorder()
		f.app.handleLoraVerificationRead(w, r)
		return w
	}
	assertCounts := func(want int64) {
		t.Helper()
		var batches, charged int64
		if err := f.db.QueryRow(`SELECT count(*) FROM generation_batches`).Scan(&batches); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT generation_total_used FROM users WHERE id=$1`, f.user.ID).Scan(&charged); err != nil {
			t.Fatal(err)
		}
		if batches != want || charged != 3*want {
			t.Fatalf("batches %d quota %d, want %d batches", batches, charged, want)
		}
	}
	quoteURL := "/api/lora-training/verifications/quote"
	enqueueURL := "/api/lora-training/verifications/enqueue"
	liveSource, err := f.app.store.LoraTrainingJobByPublicID(ctx, source.PublicID, f.user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	usage, _, err := f.app.loraUseOptions(ctx, &f.user, liveSource)
	if err != nil || usage.State != "ready" {
		t.Fatalf("source options: %+v %v", usage, err)
	}
	quote := post(quoteURL, form, &f.user)
	if quote.Code != 200 {
		t.Fatalf("quote %d: %s", quote.Code, quote.Body.String())
	}
	var q struct {
		Confirmation string `json:"confirmation"`
		Count        int    `json:"count"`
	}
	if json.Unmarshal(quote.Body.Bytes(), &q) != nil || q.Confirmation == "" || q.Count != 3 {
		t.Fatal(quote.Body.String())
	}
	assertCounts(0)
	form.Set("lora_verification_confirmation", q.Confirmation)
	changed := cloneGenerationValues(form)
	changed.Set("width", "768")
	if w := post(enqueueURL, changed, &f.user); w.Code != 409 {
		t.Fatalf("changed body %d: %s", w.Code, w.Body.String())
	}
	inspectionState.Store("artifact_changed")
	if w := post(enqueueURL, form, &f.user); w.Code != 409 {
		t.Fatalf("changed artifact %d: %s", w.Code, w.Body.String())
	}
	inspectionState.Store("verified")
	expiredConfirmation, err := f.app.decodeLoraVerificationConfirmation(q.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	expiredConfirmation.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	expiredToken, err := f.app.encodeLoraVerificationConfirmation(expiredConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	expiredForm := cloneGenerationValues(form)
	expiredForm.Set("lora_verification_confirmation", expiredToken)
	if w := post(enqueueURL, expiredForm, &f.user); w.Code != 409 {
		t.Fatalf("expired initial confirmation %d: %s", w.Code, w.Body.String())
	}
	if _, err := f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if w := post(enqueueURL, form, &f.user); w.Code != 403 {
		t.Fatal("disabled owner submitted work", w.Code)
	}
	if _, err := f.db.Exec(`UPDATE users SET disabled=false WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	assertCounts(0)
	w := post(enqueueURL, form, &f.user)
	if w.Code != 201 {
		t.Fatalf("enqueue %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		BatchID string `json:"batch_id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.BatchID == "" {
		t.Fatal(w.Body.String())
	}
	assertCounts(1)
	if w := post(enqueueURL, form, &f.user); w.Code != 200 {
		t.Fatalf("retry %d: %s", w.Code, w.Body.String())
	}
	assertCounts(1)
	stored, err := f.app.store.LoraVerificationByBatch(ctx, f.user.ID, response.BatchID)
	if err != nil || strings.Contains(string(stored.PlanCipher), "ceramic") {
		t.Fatal("plan not encrypted", err)
	}
	batch, err := f.app.store.GenerationBatchByPublicID(ctx, f.user.ID, response.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	children, err := f.app.store.GenerationBatchJobs(ctx, f.user.ID, batch.ID)
	if err != nil || len(children) != 3 {
		t.Fatalf("stored children: %d %v", len(children), err)
	}
	for i, child := range children {
		payload, err := f.app.decodeGenerationSavedPayload(child.PayloadCipher)
		if err != nil {
			t.Fatal(err)
		}
		wantName, wantWeight := reference, []string{"0", "0.5", "1"}[i]
		if i == 0 {
			wantName = ""
		}
		if payload.Values["lora_1"] != wantName || payload.Values["lora_model_strength_1"] != wantWeight || payload.Values["lora_clip_strength_1"] != wantWeight {
			t.Fatalf("child %d adapter mismatch: %+v", i, payload.Values)
		}
		if child.Seed != 42 || payload.Values["seed"] != "42" || payload.Values["positive_prompt"] != "A ceramic teapot on a table in daylight." || payload.Values["model"] != form.Get("model") {
			t.Fatalf("child %d pair settings changed", i)
		}
		if child.PublicID != stored.Members[i].JobPublicID {
			t.Fatalf("child %d membership mismatch", i)
		}
	}
	if w := read("batch="+response.BatchID, &f.user); w.Code != 200 || !strings.Contains(w.Body.String(), `"seed":"42"`) || strings.Contains(w.Body.String(), "fixture-private-path") {
		t.Fatalf("read %d: %s", w.Code, w.Body.String())
	}
	if w := read("source="+source.PublicID, &f.user); w.Code != 200 || !strings.Contains(w.Body.String(), response.BatchID) {
		t.Fatalf("history %d: %s", w.Code, w.Body.String())
	}
	if w := read("request="+form.Get("client_request_id"), &f.user); w.Code != 200 {
		t.Fatalf("recover %d: %s", w.Code, w.Body.String())
	}
	var otherID int64
	if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation) VALUES('verification-other','disabled','admin',true) RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	other, _ := f.app.store.UserByID(ctx, otherID)
	if w := read("batch="+response.BatchID, &other); w.Code != 404 {
		t.Fatal("foreign comparison leaked", w.Code)
	}
	if w := post(enqueueURL, form, &other); w.Code != 409 {
		t.Fatal("foreign confirmation accepted", w.Code)
	}
	if _, err = f.db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, *stored.Members[0].JobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`DELETE FROM lora_training_jobs WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if w := read("batch="+response.BatchID, &f.user); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"missing"`) || !strings.Contains(w.Body.String(), `"source_available":false`) {
		t.Fatalf("deleted source read %d: %s", w.Code, w.Body.String())
	}
	confirmation, _ := f.app.decodeLoraVerificationConfirmation(q.Confirmation)
	confirmation.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	expired, _ := f.app.encodeLoraVerificationConfirmation(confirmation)
	form.Set("lora_verification_confirmation", expired)
	if w := post(enqueueURL, form, &f.user); w.Code != 200 {
		t.Fatalf("committed retry after deletion/expiry %d: %s", w.Code, w.Body.String())
	}
	assertCounts(1)
	if _, err = f.db.Exec(`UPDATE users SET disabled=true WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if w := read("batch="+response.BatchID, &f.user); w.Code != 403 {
		t.Fatal("disabled owner retained access", w.Code)
	}
}
