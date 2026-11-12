package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
)

func TestLoraWorkspaceTemplateHasFourStagesAndOneLaunch(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = templates.ExecuteTemplate(&output, "lora_training", map[string]any{
		"Title": "Обучение LoRA", "AssetVersion": "test", "CurrentUser": &User{ID: 42},
		"ReadyProfiles": 0,
		"Form":          defaultLoraTrainingForm(), "Presets": loraTrainingPresets,
		"Message": "<script>untrusted</script>",
	})
	if err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, expected := range []string{
		`data-training-step="material"`, `data-training-step="captions"`,
		`data-training-step="training"`, `data-training-step="result"`,
		`data-caption-inline`, `data-caption-panel`, `data-training-connection`,
		`data-training-preset="balanced"`, `1600`, `0.0001`,
		`/static/lora-training-workspace.js`, `/static/lora-training-workspace.css`,
		`&lt;script&gt;untrusted&lt;/script&gt;`,
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	if strings.Count(html, "data-lora-submit") != 1 || strings.Contains(html, "lora-training-guide") || strings.Contains(html, "<script>untrusted</script>") {
		t.Fatal("duplicate launch, obsolete guide or unescaped message")
	}
}

func TestLoraWorkspaceCancellationReturnAndOwnershipIntegration(t *testing.T) {
	f := newDispatchFixture(t)
	f.app.csrfSigner = security.NewCSRFSigner("workspace-test-csrf")
	ctx := context.Background()
	for i, returnView := range []string{"workspace", "", "https://untrusted.example/"} {
		id := fmt.Sprintf("workspace-training-%d", i)
		job, err := f.app.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
			PublicID: id, RequestID: id, UserID: f.user.ID, ProfileID: "fixture", Family: "krea2", BaseModel: "fixture",
			Name: "Workspace fixture", OutputName: "workspace_fixture", TriggerWord: "fixture", ConceptType: "object",
			Preset: "quick", Resolution: 512, MaxTrainSteps: 800, NetworkDim: 16, NetworkAlpha: 16,
			LearningRate: 0.0001, Seed: 1, SampleCount: 5, DatasetBytes: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		call := func(user User, token string) *httptest.ResponseRecorder {
			values := url.Values{"csrf": {token}, "return_view": {returnView}}
			r := httptest.NewRequest(http.MethodPost, "/train-lora/"+job.PublicID+"/cancel", strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "workspace-session"})
			r = r.WithContext(context.WithValue(ctx, userCtxKey, &user))
			w := httptest.NewRecorder()
			f.app.handleLoraTrainingAction(w, r)
			return w
		}
		token := f.app.csrfSigner.Token("workspace-session")
		if w := call(User{ID: f.user.ID + 100, Role: "user"}, token); w.Code != 404 {
			t.Fatal("foreign cancellation", w.Code)
		}
		if w := call(f.user, "invalid"); w.Code != 403 {
			t.Fatal("CSRF rejection", w.Code)
		}
		unchanged, err := f.app.store.LoraTrainingJobByPublicID(ctx, id, f.user.ID, false)
		if err != nil || unchanged.State != domain.LoraTrainingQueued {
			t.Fatal("rejected request changed job", err)
		}
		w := call(f.user, token)
		key := "job"
		if returnView == "workspace" {
			key = "run"
		}
		if w.Code != 302 || w.Header().Get("Location") != "/train-lora?cancelled=1&"+key+"="+id {
			t.Fatal("incorrect return target", w.Code, w.Header().Get("Location"))
		}
	}
	if f.posts.Load() != 0 {
		t.Fatal("cancellation dispatched generation")
	}
}
