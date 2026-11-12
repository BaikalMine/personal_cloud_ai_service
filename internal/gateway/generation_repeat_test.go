package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
)

func TestGenerationRepeatGraphAndConfirmation(t *testing.T) {
	source := json.RawMessage(`{"1":{"class_type":"Sampler","inputs":{"seed":4611686018427387918}},"2":{"class_type":"SaveImage","inputs":{"filename_prefix":"portrait-source-repeat-001"}}}`)
	current := json.RawMessage(`{"1":{"class_type":"Sampler","inputs":{"seed":4611686018427387918}},"2":{"class_type":"SaveImage","inputs":{"filename_prefix":"portrait"}}}`)
	one, err := generationRepeatGraph(source, "source-repeat-001")
	if err != nil {
		t.Fatal(err)
	}
	two, err := generationRepeatGraph(current, "")
	if err != nil || string(one) != string(two) || !strings.Contains(string(one), "4611686018427387918") {
		t.Fatalf("normalization rounded seed or changed graph: %s %s %v", one, two, err)
	}
	changed := json.RawMessage(strings.Replace(string(current), "portrait", "changed", 1))
	three, _ := generationRepeatGraph(changed, "")
	if string(two) == string(three) {
		t.Fatal("real output config change ignored")
	}
	cipher, _ := contentcrypto.NewCipher("repeat-generation-confirmation-secret-at-least-32-characters")
	a := &App{contentCipher: cipher}
	value := generationRepeatConfirmation{Purpose: "generation-repeat-v1", UserID: 1, RequestID: "repeat-request-001", Constraint: generationRepeatConstraint{Version: 1, SourceID: "source-repeat-001", PreparedSHA256: strings.Repeat("a", 64)}, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	token, err := a.encodeGenerationRepeatConfirmation(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := a.decodeGenerationRepeatConfirmation(token)
	if err != nil || decoded.Constraint != value.Constraint {
		t.Fatal(decoded, err)
	}
	for _, bad := range []string{"", token[:len(token)/2], strings.Repeat("a", 4097), "not/base64"} {
		if _, err = a.decodeGenerationRepeatConfirmation(bad); err == nil {
			t.Fatal("invalid confirmation accepted")
		}
	}
	value.Purpose = "another-purpose"
	token, _ = a.encodeGenerationRepeatConfirmation(value)
	if _, err = a.decodeGenerationRepeatConfirmation(token); err == nil {
		t.Fatal("cross-purpose token accepted")
	}
}

func TestGenerationRepeatParameterAndGraphDifferences(t *testing.T) {
	old := generationForm{Seed: (1 << 62) + 14, Width: 512, Height: 512, ModelName: "old.safetensors", Positive: "private prompt"}
	current := generationPreparation{Input: old, Definition: workflowDefinition{ID: "current"}, Prompt: map[string]any{"1": map[string]any{"class_type": "Sampler", "inputs": map[string]any{"seed": old.Seed, "steps": 12}}}}
	current.Input.Width = 768
	current.Input.ModelName = "new.safetensors"
	original := &generationExecutionSnapshot{Graph: json.RawMessage(`{"1":{"class_type":"Sampler","inputs":{"seed":4611686018427387918,"steps":8}}}`), Workflow: generationExecutionWorkflow{DefinitionSHA256: strings.Repeat("0", 64)}}
	differences := generationRepeatDifferences(original, old, current, "source-repeat-001")
	seen := map[string]bool{}
	for _, difference := range differences {
		seen[difference.Label] = true
		if difference.Kind == "graph" && !strings.Contains(difference.Before, "4611686018427387918") {
			t.Fatal("diff rounded seed")
		}
	}
	if !seen["Модель"] || !seen["Ширина базы"] || !seen["Узел схемы 1"] || !seen["Определение схемы"] {
		t.Fatal(differences)
	}
	digest, err := generationRepeatPreparedDigest(current, "")
	if err != nil {
		t.Fatal(err)
	}
	current.Input.AssistantOriginal = "not executed"
	current.Input.AssistantApplied = true
	same, _ := generationRepeatPreparedDigest(current, "")
	if digest != same {
		t.Fatal("assistant audit changed execution binding")
	}
	current.Input.Width++
	different, _ := generationRepeatPreparedDigest(current, "")
	if digest == different {
		t.Fatal("changed input retained binding")
	}
	if err := (&App{}).validateGenerationRepeatDispatch(context.Background(), nil, domain.GenerationJob{}, generationSavedPayload{Repeat: &generationRepeatConstraint{Version: 1}}, current); err == nil {
		t.Fatal("incomplete durable constraint accepted")
	}
}

func TestGenerationRepeatUnknownHistoricalFilesAreNotChanges(t *testing.T) {
	input := generationForm{Seed: 42}
	p := generationPreparation{Input: input, Prompt: map[string]any{"1": map[string]any{"class_type": "Sampler", "inputs": map[string]any{"seed": 42}}}}
	graph, _ := json.Marshal(p.Prompt)
	definition, _ := json.Marshal(p.Definition)
	original := &generationExecutionSnapshot{Graph: graph, Workflow: generationExecutionWorkflow{DefinitionSHA256: generationEvidenceDigest(definition)}}
	p.FileObservations = &componentinspection.Response{Items: []componentinspection.Observation{{Reference: componentinspection.Reference{Kind: "diffusion_model", Name: "model.safetensors"}, State: "verified", SHA256: strings.Repeat("a", 64)}}}
	if differences := generationRepeatDifferences(original, input, p, "source-repeat-001"); len(differences) != 0 {
		t.Fatal("unknown prior bytes labelled changed", differences)
	}
	old := *p.FileObservations
	old.Items = append([]componentinspection.Observation(nil), old.Items...)
	old.Items[0].SHA256 = strings.Repeat("b", 64)
	original.FileObservations = &old
	differences := generationRepeatDifferences(original, input, p, "source-repeat-001")
	if len(differences) != 2 || differences[0].Technical || !differences[1].Technical || strings.Contains(differences[0].Before, strings.Repeat("b", 64)) {
		t.Fatal("hashes not separated from human explanation", differences)
	}
	if generationRepeatDisplayValue(json.RawMessage(`4611686018427387918`)) != "4611686018427387918" || generationRepeatDisplayValue(json.RawMessage(`"model.safetensors"`)) != "model.safetensors" {
		t.Fatal("display values rounded or quoted")
	}
}

func TestGenerationRepeatAssetsArePublished(t *testing.T) {
	for _, path := range []string{"static/generation-repeat.js", "static/generation-repeat-state.js", "static/generation-repeat.css"} {
		found := false
		for _, asset := range frontendAssetPaths {
			if asset == path {
				found = true
			}
		}
		if !found {
			t.Fatal("repeat asset missing from version hash", path)
		}
		if strings.HasSuffix(path, ".css") && staticCSSAssets["/"+path] != path {
			t.Fatal("repeat CSS has no production route")
		}
	}
}
