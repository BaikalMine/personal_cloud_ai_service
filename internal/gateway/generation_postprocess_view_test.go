package gateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestPostprocessVariantViewRoundTrip(t *testing.T) {
	for _, video := range []bool{false, true} {
		a, q := postprocessQuoteFixture(t, video)
		job := domain.GenerationJob{ID: 1, UserID: &q.UserID, PublicID: "postprocess-view-child-0001", RequestID: q.RequestID, PromptID: "postprocess-prompt-0001", TemplateID: q.TemplateID, WorkflowID: domain.PostprocessWorkflow}
		cipher, err := a.encodePostprocessVariant(job, q)
		if err != nil {
			t.Fatal(err)
		}
		row := domain.GenerationVariantRow{UserID: q.UserID, JobPublicID: job.PublicID, PromptID: job.PromptID, TemplateID: job.TemplateID, WorkflowID: job.WorkflowID, PayloadCipher: cipher}
		view, err := a.decodePostprocessVariant(row)
		if err != nil || !reflect.DeepEqual(*view, postprocessViewFromQuote(q)) {
			t.Fatal("round trip", view, err)
		}
		if view.Sources[0].MediaID != "9007199254740997" {
			t.Fatal("source ID lost precision")
		}
		data, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"input_name", "namespace", "graph", "schemas", "seed", "sampler", "execution_input", "values"} {
			if strings.Contains(string(data), `"`+forbidden+`"`) {
				t.Fatal("execution parameters leaked into view", forbidden)
			}
		}
		if _, err := a.decodeGenerationSavedPayload(cipher); err == nil {
			t.Fatal("postprocess variant accepted as editable generation")
		}
		deleted := row
		deleted.JobPublicID = ""
		if _, err := a.decodePostprocessVariant(deleted); err != nil {
			t.Fatal("deleting job lost retained-media provenance", err)
		}
		for _, mutate := range []func(*domain.GenerationVariantRow){
			func(r *domain.GenerationVariantRow) { r.UserID++ },
			func(r *domain.GenerationVariantRow) { r.PromptID = "another-prompt-0001" },
			func(r *domain.GenerationVariantRow) { r.JobPublicID = "another-child-0001" },
			func(r *domain.GenerationVariantRow) { r.TemplateID = "text-to-image" },
			func(r *domain.GenerationVariantRow) { r.WorkflowID = "photoflow-krea2" },
		} {
			wrong := row
			mutate(&wrong)
			if _, err := a.decodePostprocessVariant(wrong); err == nil {
				t.Fatal("cross-purpose/identity view accepted")
			}
		}
	}
}

func TestPostprocessSeedProjection(t *testing.T) {
	for _, operation := range []string{"", "postprocess"} {
		for _, value := range []any{generationVariantView{Operation: operation, Seed: 0}, generationJobView{Operation: operation, Seed: 0}} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			seed, found := decoded["seed"]
			if operation == "postprocess" && found || operation == "" && (!found || seed != float64(0)) {
				t.Fatal("placeholder/zero seed contract changed", string(data))
			}
			if _, found := decoded["state"]; !found {
				t.Fatal("custom marshal dropped ordinary fields")
			}
		}
	}
	a, q := postprocessQuoteFixture(t, false)
	job := domain.GenerationJob{UserID: &q.UserID, TemplateID: q.TemplateID, State: domain.GenerationJobCompleted, PayloadCipher: []byte("not-a-recipe")}
	view := a.generationJobView(job)
	if view.Retryable || view.Operation != "postprocess" || view.Prompt != "" {
		t.Fatal("postprocess acquires ordinary-generation actions", view)
	}
}
