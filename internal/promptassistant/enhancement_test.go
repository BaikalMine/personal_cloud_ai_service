package promptassistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPreparedEnhancementPreservesEveryWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    Mode
		profile Profile
		prompt  string
		roles   []ImageReferenceRole
		video   VideoContext
		think   bool
	}{
		{"krea_text", ModeTextToImage, ProfileWorkflowDefault, "A green vase on a desk", nil, VideoContext{}, false},
		{"krea_edit", ModeImageToImage, ProfileWorkflowDefault, "Use the second vase", []ImageReferenceRole{ImageReferenceBaseScene, ImageReferenceWardrobeObject}, VideoContext{}, false},
		{"flux_edit", ModeImageToImage, ProfileFluxEdit, "Keep the room and change the arrangement", []ImageReferenceRole{ImageReferenceBaseScene, ImageReferenceWardrobeObject, ImageReferenceStyle, ImageReferenceBackground}, VideoContext{}, true},
		{"minimax_text", ModeTextToVideo, ProfileMiniMaxH3FL2VA, "Move the camera around a vase", nil, VideoContext{Mode: VideoModeFL2VA, DurationSeconds: 10}, true},
		{"minimax_first", ModeTextToVideo, ProfileMiniMaxH3FL2VA, "", []ImageReferenceRole{ImageReferenceBaseScene}, VideoContext{Mode: VideoModeFL2VA, DurationSeconds: 5, ImageCount: 1}, false},
		{"minimax_first_last", ModeTextToVideo, ProfileMiniMaxH3FL2VA, "Move the vase", []ImageReferenceRole{ImageReferenceBaseScene, ImageReferenceDetails}, VideoContext{Mode: VideoModeFL2VA, DurationSeconds: 15, ImageCount: 2}, false},
		{"minimax_references", ModeTextToVideo, ProfileMiniMaxH3REF2VA, "", []ImageReferenceRole{ImageReferenceIdentity, ImageReferenceWardrobeObject, ImageReferenceBackground, ImageReferenceStyle}, VideoContext{Mode: VideoModeREF2VA, DurationSeconds: 15, ImageCount: 4, AudioReference: true, VideoReference: true}, true},
		{"minimax_audio", ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A vase beside a radio", nil, VideoContext{Mode: VideoModeREF2VA, DurationSeconds: 10, AudioReference: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := make([]ImageReference, len(tc.roles))
			modelReferences := []map[string]string{}
			for i, role := range tc.roles {
				refs[i] = ImageReference{Number: i + 1, Role: role, MIMEType: "image/png", Image: []byte(fmt.Sprintf("unique-image-payload-%d", i)), Correction: fmt.Sprintf("observation-%d", i)}
				modelReferences = append(modelReferences, map[string]string{"id": fmt.Sprintf("Picture %d", i+1), "summary": "A green vase", "use": "Keep its form"})
			}
			content, _ := json.Marshal(map[string]any{"prompt": "A green vase in daylight.", "references": modelReferences})
			response, _ := json.Marshal(map[string]any{"done": true, "message": Message{Role: "assistant", Content: string(content)}, "prompt_eval_count": 57, "eval_count": 12})
			var received []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received, _ = io.ReadAll(r.Body); w.Write(response) }))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := NewClient(base, "saved-text").WithVisionModel("saved-vision", 93*time.Second, "0")
			prepared, err := client.PrepareEnhancement(tc.mode, tc.profile, tc.prompt, refs, tc.video, tc.think)
			if err != nil {
				t.Fatal(err)
			}
			var request chatRequest
			if err = json.Unmarshal(prepared.Body, &request); err != nil {
				t.Fatal(err)
			}
			wantModel := "saved-text"
			if len(refs) > 0 || tc.mode == ModeTextToVideo {
				wantModel = "saved-vision"
			}
			if request.Model != wantModel || len(request.Messages) != 2 || len(request.Messages[1].Images) != len(refs) || request.Think != tc.think || request.Stream || request.Format != "json" || request.Options.NumPredict != prepared.Policy.NumPredict {
				t.Fatalf("changed request envelope: %+v", request)
			}
			for i, ref := range refs {
				if request.Messages[1].Images[i] != base64.StdEncoding.EncodeToString(ref.Image) || !strings.Contains(request.Messages[1].Content, ref.Correction) || strings.Contains(request.Messages[0].Content, ref.Correction) {
					t.Fatalf("reference %d lost its image or observation scope", i+1)
				}
			}
			direct, err := client.EnhanceVideoResult(context.Background(), tc.mode, tc.profile, tc.prompt, refs, tc.video, tc.think)
			if err != nil || !bytes.Equal(received, prepared.Body) {
				t.Fatalf("direct path changed: %v", err)
			}
			metadata, err := json.Marshal(prepared)
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range refs {
				if bytes.Contains(metadata, []byte(base64.StdEncoding.EncodeToString(ref.Image))) || bytes.Contains(metadata, []byte(ref.Correction)) {
					t.Fatal("response metadata duplicated private image/request data")
				}
			}
			var restored PreparedEnhancement
			if err = json.Unmarshal(metadata, &restored); err != nil || len(restored.Body) != 0 {
				t.Fatalf("cannot restore metadata without request body: %v", err)
			}
			for i := range refs {
				refs[i].Role, refs[i].Number = ImageReferenceStyle, 99
				clear(refs[i].Image)
			}
			clear(prepared.Body)
			client.model, client.visionModel = "changed-text", "changed-vision"
			saved, err := ParseEnhancementResponse(restored, 200, response)
			direct.Policy.Timeout = 0 // Duration is a runtime-only field; TimeoutMS is persisted.
			if err != nil || !reflect.DeepEqual(saved, direct) || saved.Model != wantModel || saved.Execution != ExecutionCompleted {
				t.Fatalf("recovered result changed: %+v, direct=%+v err=%v", saved, direct, err)
			}
			for i, role := range tc.roles {
				want := string(role)
				if tc.video.Mode == VideoModeFL2VA {
					want = []string{"first_frame", "last_frame"}[i]
				}
				if saved.References[i].Role != want || saved.References[i].Identifier != fmt.Sprintf("Picture %d", i+1) || !saved.References[i].Inspected {
					t.Fatalf("reference role/order changed: %+v", saved.References)
				}
			}
			for _, ref := range saved.References {
				if (ref.Kind == "audio" || ref.Kind == "video") && ref.Inspected {
					t.Fatalf("claimed unseen media inspection: %+v", ref)
				}
			}
			after, _ := json.Marshal(restored)
			if !bytes.Equal(metadata, after) {
				t.Fatal("parser mutated the persisted reference contract")
			}
			if len(tc.roles) == 0 && !tc.video.AudioReference && !tc.video.VideoReference {
				encoded, _ := json.Marshal(saved)
				if !bytes.Contains(encoded, []byte(`"references":[]`)) {
					t.Fatalf("empty reference array contract changed: %s", encoded)
				}
			}
		})
	}
}

func TestPreparedEnhancementRejectsMissingReferenceAndPreservesCompletion(t *testing.T) {
	base, _ := url.Parse("http://fixture.invalid")
	prepared, err := NewClient(base, "fixture").PrepareEnhancement(ModeImageToImage, ProfileFluxEdit, "Keep the vase", []ImageReference{{Number: 2, Role: ImageReferenceDetails, Image: []byte("fixture"), MIMEType: "image/png"}}, VideoContext{}, false)
	if err != nil {
		t.Fatal(err)
	}
	response := []byte(`{"done":true,"message":{"content":"{\"prompt\":\"A vase\",\"references\":[]}"}}`)
	result, err := ParseEnhancementResponse(prepared, 200, response)
	if err == nil || !strings.Contains(err.Error(), "Picture 2") || result.Execution != ExecutionCompleted {
		t.Fatalf("missing reference accepted or completion lost: %+v %v", result, err)
	}
	if prepared.References[0].Inspected || prepared.References[0].Summary != "" {
		t.Fatal("failed parser mutated original reference metadata")
	}
	if result, _ = ParseEnhancementResponse(prepared, 504, response); result.Execution != ExecutionUnconfirmed {
		t.Fatal("504 was treated as completed")
	}
}

func TestEnhancementResponsePreservesNegativeAcrossDirectAndDurablePaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    Mode
		profile Profile
		edit    EditContext
	}{
		{"krea_ultra", ModeTextToImage, ProfilePhotographic, EditContext{CFG: 2}},
		{"moss", ModeTextToImage, ProfileWorkflowDefault, EditContext{CFG: 3, ImageFamily: "moss"}},
		{"qwen_text", ModeTextToImage, ProfileAnime, EditContext{CFG: 1.5}},
		{"qwen_edit", ModeImageToImage, ProfileQwen21Edit, EditContext{CFG: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const negative = "watermark, blurry lettering"
			content, _ := json.Marshal(map[string]any{"prompt": "A blue ceramic teapot.", "negative_prompt": "  " + negative + "  ", "references": []any{}})
			body, _ := json.Marshal(map[string]any{"done": true, "message": map[string]any{"content": string(content)}, "prompt_eval_count": 23, "eval_count": 17})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := NewClient(base, "neutral-fixture")
			prepared, err := client.PrepareEnhancementWithEditContext(tc.mode, tc.profile, "A blue teapot", nil, VideoContext{}, tc.edit, false)
			if err != nil {
				t.Fatal(err)
			}
			metadata, _ := json.Marshal(prepared)
			var restored PreparedEnhancement
			if err = json.Unmarshal(metadata, &restored); err != nil {
				t.Fatal(err)
			}
			durable, err := ParseEnhancementResponse(restored, http.StatusOK, body)
			if err != nil || durable.NegativePrompt != negative {
				t.Fatalf("durable negative was lost: %+v, %v", durable, err)
			}
			direct, err := client.EnhanceEditResult(context.Background(), tc.mode, tc.profile, "A blue teapot", nil, tc.edit, false)
			if err != nil || direct.NegativePrompt != negative {
				t.Fatalf("direct negative was lost: %+v, %v", direct, err)
			}
			if direct.Prompt != durable.Prompt || direct.Usage != durable.Usage || direct.Execution != ExecutionCompleted || durable.Execution != ExecutionCompleted {
				t.Fatal("response metadata changed")
			}
		})
	}
}
