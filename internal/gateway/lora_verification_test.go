package gateway

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestLoraVerificationValuesKeepPairsExactAndBaselineEmpty(t *testing.T) {
	base := url.Values{"model": {"krea2:test"}, "template_id": {"text-to-image"}, "generation_workflow": {"photoflow-krea2"}, "width": {"768"}, "input_image": {"owned-reference"}, "positive_prompt": {"old"}, "lora_1": {"old.safetensors"}, "lora_model_strength_1": {"2"}, "lora_clip_strength_1": {"1"}, "lora_2": {"other.safetensors"}, "lora_clip_strength_2": {"1"}, "batch_enabled": {"true"}, "assistant_enabled": {"true"}, "assistant_job_id": {"old-assistant"}, "parent_job_id": {"old-parent"}}
	original := cloneGenerationValues(base)
	plan := domain.LoraVerificationPlan{Version: 1, Cases: []domain.LoraVerificationCase{{Prompt: "scene one", Seed: "42"}, {Prompt: "scene two", Seed: "43"}}, Weights: []string{"0.5", "1"}}
	result, err := buildLoraVerificationValues(base, plan, "Trained/Krea2/owner/adapter.safetensors")
	if err != nil || len(result) != 6 {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(original, base) {
		t.Fatal("input form mutated")
	}
	for i, item := range result {
		values := item.Values
		if values.Get("positive_prompt") != plan.Cases[i/3].Prompt || values.Get("seed") != plan.Cases[i/3].Seed || values.Get("input_image") != "owned-reference" || values.Get("width") != "768" {
			t.Fatal("case settings changed", values)
		}
		if values.Get("loras_configured") != "true" || values.Get("lora_2") != "" || values.Get("lora_clip_strength_2") != "0" || values.Get("parent_job_id") != "" || values.Get("assistant_enabled") != "false" || values.Get("assistant_job_id") != "" || values.Get("batch_enabled") != "" {
			t.Fatal("unrelated state retained", values)
		}
		if i%3 == 0 {
			if values.Get("lora_1") != "" || values.Get("lora_model_strength_1") != "0" || values.Get("lora_clip_strength_1") != "0" {
				t.Fatal("baseline contains adapter contribution", values)
			}
		} else if values.Get("lora_1") != "Trained/Krea2/owner/adapter.safetensors" || values.Get("lora_model_strength_1") != item.Member.Weight || values.Get("lora_clip_strength_1") != item.Member.Weight {
			t.Fatal("wrong tested adapter", values)
		}
		pairBase := cloneGenerationValues(result[(i/3)*3].Values)
		candidate := cloneGenerationValues(values)
		for _, key := range []string{"lora_1", "lora_model_strength_1", "lora_clip_strength_1"} {
			pairBase.Del(key)
			candidate.Del(key)
		}
		if !reflect.DeepEqual(pairBase, candidate) {
			t.Fatal("non-adapter settings differ within a case")
		}
	}
}

func TestLoraVerificationCannotUseGenericBatchSubmission(t *testing.T) {
	if _, err := parseGenerationBatchSpec(url.Values{"batch_count": {"3"}, "batch_mode": {"lora_verification"}}); err == nil {
		t.Fatal("unconfirmed verification accepted by ordinary batch parser")
	}
	app := &App{}
	if _, _, err := app.buildGenerationBatchCandidates(context.Background(), nil, generationPreparation{}, nil, generationBatchSpec{Mode: domain.GenerationBatchLoraVerification, Count: 3}); err == nil {
		t.Fatal("verification silently became random seeds")
	}
}

func TestLoraVerificationBatchMissingMembersAreNotQueuedForever(t *testing.T) {
	batch := domain.GenerationBatch{Mode: domain.GenerationBatchLoraVerification, TotalCount: 3, CompletedCount: 2, MissingCount: 1}
	view := makeGenerationBatchView(batch, nil)
	if view.State != "partial" || view.FinishedCount != 3 || view.MissingCount != 1 || view.ProgressPercent != 100 {
		t.Fatal(view)
	}
	batch.CompletedCount = 0
	batch.MissingCount = 3
	if generationBatchState(batch) != "failed" {
		t.Fatal("all missing results presented as successful or still queued")
	}
}
