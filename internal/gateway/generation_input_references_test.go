package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestGenerationJobInputNames(t *testing.T) {
	if names := generationJobInputNames(generationForm{}); names == nil || len(names) != 0 {
		t.Fatal("input-free request must be verified", names)
	}
	input := generationForm{InputImage: " owner/main.png ", ReferenceImages: [3]string{"", "owner/ref.png"}, InputAudio: " owner/audio.wav ", InputVideo: "owner/video.mp4"}
	if got := generationJobInputNames(input); !reflect.DeepEqual(got, []string{"owner/main.png", "owner/ref.png", "owner/audio.wav", "owner/video.mp4"}) {
		t.Fatal(got)
	}
}

func TestGenerationInputUnavailableResponses(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter, error){"repeat": writeGenerationRepeatError, "verification": writeLoraVerificationError} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRecorder()
			write(r, store.ErrGenerationInputUnavailable)
			if r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), generationInputUnavailableMessage) {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
}

func TestGenerationInputReferenceAdmissionHTTPIntegration(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%t/expired=%t", batch, expired), func(t *testing.T) {
				f := newDispatchFixture(t)
				ctx := context.Background()
				found := false
				for _, preset := range buildGenerationPresets(buildGenerationModelCatalog(compatibilityFixtureObjectInfo(t))) {
					if preset.TemplateID == "image-to-image" && preset.Family == modelFamilyKrea2 && preset.Available {
						f.form.Set("generation_workflow", preset.ID)
						f.form.Set("model", preset.ModelID)
						found = true
						break
					}
				}
				if !found {
					t.Fatal("image-to-image fixture preset unavailable")
				}
				f.app.csrfSigner = security.NewCSRFSigner("input-reference-csrf-test-secret")
				namespace := comfyUploadNamespace(f.app.comfyClientID(f.user.ID))
				expires := time.Now().Add(time.Hour)
				if expired {
					expires = time.Now().Add(-time.Hour)
				}
				if _, err := f.db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state,expires_at)
				 VALUES($1,$2,'photo.png',$3,42,repeat('a',64),'stored',$4)`, strings.Repeat("a", 32), f.user.ID, namespace, expires); err != nil {
					t.Fatal(err)
				}
				f.form.Set("input_image", namespace+"/photo.png")
				f.form.Set("template_id", "image-to-image")
				f.form.Set("client_request_id", newRequestID())
				f.form.Set("correlation_id", newRequestID())
				f.form.Set("csrf", f.app.csrfSigner.Token("fixture-session"))
				handler := f.app.handleGenerateRun
				wantJobs := 1
				if batch {
					handler = f.app.handleGenerationBatches
					wantJobs = 2
					f.form.Set("batch_count", "2")
					f.form.Set("batch_mode", string(domain.GenerationBatchSeeds))
				}
				r := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(f.form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "fixture-session"})
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &f.user))
				w := httptest.NewRecorder()
				handler(w, r)
				if expired {
					if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), generationInputUnavailableMessage) {
						t.Fatal(w.Code, w.Body.String())
					}
					wantJobs = 0
					if !batch {
						job, err := f.app.store.GenerationJobByRequest(ctx, f.user.ID, f.form.Get("client_request_id"))
						if err != nil || job.State != domain.GenerationJobFailed || job.ErrorCode != "generation_input_unavailable" {
							t.Fatal("input failure did not close original job", job, err)
						}
					}
				} else if w.Code < 200 || w.Code >= 300 {
					t.Fatal(w.Code, w.Body.String())
				}
				var refs int
				if err := f.db.QueryRow(`SELECT count(*) FROM generation_input_references`).Scan(&refs); err != nil || refs != wantJobs {
					t.Fatal(refs, wantJobs, err)
				}
				quota, err := f.app.store.QuickGenerationQuota(ctx, f.user.ID)
				if err != nil || quota.Image.TotalUsed != int64(wantJobs) || quota.Image.DailyUsed != wantJobs {
					t.Fatal(quota, err)
				}
				if f.posts.Load() != 0 {
					t.Fatal("admission unexpectedly started execution")
				}
			})
		}
	}
}
