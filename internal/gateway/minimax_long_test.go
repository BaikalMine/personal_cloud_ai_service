package gateway

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const miniMaxLongTestPrompt = "subject_definitions:\nAn adult potter in a navy jacket.\ndetailed_description:\n[Shot 1] The potter slowly turns a blue cup.\n[Shot 2] At 00:15.000, Continuing from the same position, the potter puts that cup on the table.\noverall_soundscape:\nContinuous quiet studio ambience."

func TestMiniMaxLongRecipeAndDraftRetainFullPlan(t *testing.T) {
	prompt := strings.Repeat("\U0001f3a5", 35000)
	form := url.Values{"template_id": {"minimax-h3-video"}, "video_long_enabled": {"true"}, "video_long_parts": {"20"}, "positive_prompt": {prompt}}
	values := generationRecipeValues(form, 42)
	if values["positive_prompt"] != prompt {
		t.Fatal("long prompt was silently dropped from saved settings")
	}
	encoded, err := encodeGenerationSavedPayload(generationSavedPayload{Version: 1, Values: values})
	if err != nil {
		t.Fatal(err)
	}
	var restored generationSavedPayload
	if json.Unmarshal(encoded, &restored) != nil || restored.Values["positive_prompt"] != prompt {
		t.Fatal("saved plan was truncated")
	}
	if len(encoded) > maxGenerationDraftPayload {
		t.Fatal("long plan cannot fit in the draft envelope")
	}
	for _, name := range []string{"assistant_original_prompt", "assistant_suggestion", "assistant_draft"} {
		form.Set(name, prompt)
	}
	draftValues, err := generationDraftValues(form)
	if err != nil {
		t.Fatal("draft still has a smaller per-field byte limit", err)
	}
	draft, _ := json.Marshal(generationDraftPayload{Version: 1, Values: draftValues})
	if len(draft) > maxGenerationDraftPayload || draftValues["assistant_draft"] != prompt {
		t.Fatal("original, proposal and edited prompt cannot be saved together")
	}
	form.Set("video_long_enabled", "false")
	if _, err := generationDraftValues(form); err == nil {
		t.Fatal("ordinary draft field limit changed")
	}
	if generationRecipeValues(form, 42)["positive_prompt"] != "" {
		t.Fatal("ordinary video prompt limit changed")
	}
	form.Set("video_long_enabled", "true")
	form.Set("template_id", "text-to-image")
	if generationRecipeValues(form, 42)["positive_prompt"] != "" {
		t.Fatal("long video budget leaked to image generation")
	}
}

func TestMiniMaxLongTimeline(t *testing.T) {
	for _, prompt := range []string{miniMaxLongTestPrompt, strings.Replace(miniMaxLongTestPrompt, "[Shot 1]", "[Shot 1] At 00:00.000,", 1)} {
		if err := validateMiniMaxLongTimeline(prompt, 2); err != nil {
			t.Fatal(err)
		}
	}
	for _, prompt := range []string{
		"A cup on a table.",
		strings.Replace(miniMaxLongTestPrompt, "At 00:15.000,", "", 1),
		strings.Replace(miniMaxLongTestPrompt, "00:15.000", "00:14.000", 1),
		strings.Replace(miniMaxLongTestPrompt, "[Shot 2]", "[Shot 3]", 1),
		miniMaxLongTestPrompt + "\n[Shot 3] At 00:30.000, Repeated cup.",
		"[Shot 1]\n[Shot 2] At 00:15.000, Cup.",
	} {
		if validateMiniMaxLongTimeline(prompt, 2) == nil {
			t.Error("invalid timeline accepted", prompt)
		}
	}
}

func TestMiniMaxLongGraphUsesSharedReferencesAndLatentContinuation(t *testing.T) {
	input := generationForm{Positive: miniMaxLongTestPrompt, ModelName: "MiniMax/h3ErosMax_beta6.safetensors",
		ReferenceModel: "MiniMax/h3ErosMax_beta6.safetensors", TextEncoder: "encoder", VAE: "video-vae", AudioVAE: "audio-vae",
		Width: 256, Height: 352, VideoQuality: 480, VideoIntegratedTurbo: true,
		VideoLongEnabled: true, VideoLongParts: 2, InputImage: "identity.png", ReferenceImages: [3]string{"scene.png"}, InputAudio: "voice.wav"}
	if err := normalizeMiniMaxH3(&input); err != nil {
		t.Fatal(err)
	}
	if input.VideoMode != "references" || input.VideoDurationSeconds != 15 || input.VideoLongContext != 39 || input.VideoSteps != 10 || input.VideoLongTransition != "continuous" {
		t.Fatal("incorrect long-video normalization", input)
	}
	if err := validateWorkflowManifestInput(miniMaxH3WorkflowManifest(), input); err != nil {
		t.Fatal(err)
	}
	graph, err := buildMiniMaxH3LongPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	long := graph["long_video"].(map[string]any)["inputs"].(map[string]any)
	if long["length"] != 720 || long["context_frames"] != "39" || long["max_raw_frames"] != 362 || long["noise_seed"] != input.Seed {
		t.Fatal("incorrect continuation controls", long)
	}
	if long["transition_mode"] != "continuous" {
		t.Fatal("transition mode was not passed to the sampler")
	}
	input.VideoLongTransition = "cinematic"
	graph, err = buildMiniMaxH3LongPrompt(input)
	if err != nil || graph["long_video"].(map[string]any)["inputs"].(map[string]any)["transition_mode"] != "cinematic" {
		t.Fatal("cinematic mode was not passed to the sampler", err)
	}
	long = graph["long_video"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"ref_images.ref_image_0", "ref_images.ref_image_1", "ref_audios.ref_audio_0"} {
		if long[name] == nil {
			t.Fatal("shared source lost", name)
		}
	}
	for _, node := range graph {
		kind := node.(map[string]any)["class_type"]
		if kind == "VHS_VideoCombine" || kind == "VAEDecode" || kind == "SamplerCustomAdvanced" || kind == "MiniMaxH3ReferenceToVideo" {
			t.Fatal("whole-film in-memory graph retained", kind)
		}
	}
	attachGenerationOutputPrefixes(graph, "owned-job")
	if long["cache_name"] != "gateway-minimax-long/owned-job" {
		t.Fatal("checkpoint directory is not job-isolated", long)
	}
	input.VideoRTXEnabled = true
	if normalizeMiniMaxH3(&input) == nil {
		t.Fatal("unbounded postprocessing accepted")
	}
}

func TestMiniMaxLongTransitionValidation(t *testing.T) {
	input := generationForm{Positive: miniMaxLongTestPrompt, VideoLongEnabled: true, VideoLongParts: 2, VideoLongTransition: "cinematic"}
	if err := normalizeMiniMaxLong(&input); err != nil {
		t.Fatal(err)
	}
	input.Positive += "\n[STATIC_CAMERA]"
	if err := normalizeMiniMaxLong(&input); err == nil {
		t.Fatal("cinematic edits conflict with a fixed camera directive")
	}
	input.VideoLongTransition = "continuous"
	if err := normalizeMiniMaxLong(&input); err != nil {
		t.Fatal(err)
	}
	input.VideoLongTransition = "unknown"
	if err := normalizeMiniMaxLong(&input); err == nil {
		t.Fatal("unknown transition accepted")
	}
}
