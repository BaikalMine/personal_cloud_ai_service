package promptassistant

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMiniMaxLongPlanningContract(t *testing.T) {
	video := VideoContext{Mode: VideoModeREF2VA, LongParts: 6, DurationSeconds: 90}
	base, _ := url.Parse("http://unused")
	prepared, err := NewClient(base, "test").PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A potter finishes a cup.", nil, video, false)
	if err != nil {
		t.Fatal(err)
	}
	var request chatRequest
	if err := json.Unmarshal(prepared.Body, &request); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"6 sequential 15-second parts", "90 seconds in total", "[Shot 6] At 01:15.000", "NOT mandatory camera cuts", "Do not perform a later part's action early", "immediately preceding end state", "13600 characters"} {
		if !strings.Contains(request.Messages[0].Content, expected) {
			t.Fatal("long-video planning rule missing", expected)
		}
	}
	if prepared.Policy.NumPredict != 8704 || prepared.Policy.NumCtx != 131072 || request.Options.NumCtx != 131072 || prepared.Policy.Timeout != 14*time.Minute {
		t.Fatal("budget does not scale with the number of parts", prepared.Policy)
	}
	if strings.Contains(request.Messages[0].Content, "7000 characters") {
		t.Fatal("ordinary prompt cap conflicts with long-video instructions")
	}
}

func TestMiniMaxLongCinematicPlanningContract(t *testing.T) {
	video := VideoContext{Mode: VideoModeREF2VA, LongParts: 6, LongTransition: "cinematic"}
	base, _ := url.Parse("http://unused")
	prepared, err := NewClient(base, "test").PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A potter finishes a cup, with cinematic coverage.", nil, video, false)
	if err != nil {
		t.Fatal(err)
	}
	var request chatRequest
	if err := json.Unmarshal(prepared.Body, &request); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"CINEMATIC EDITING MODE", "hard cut", "30 degrees", "180-degree", "exact physical state", "[Shot 6] At 01:15.000"} {
		if !strings.Contains(request.Messages[0].Content, expected) {
			t.Fatal("cinematic continuity instruction missing", expected)
		}
	}
	if strings.Contains(request.Messages[0].Content, "NOT mandatory camera cuts") || strings.Contains(request.Messages[0].Content, "A boundary is not a fade") {
		t.Fatal("continuous-mode instructions conflict with cinematic edits")
	}
}

func TestMiniMaxLongBudgetsSurviveVisionSelectionAndPersistence(t *testing.T) {
	base, _ := url.Parse("http://unused")
	client := NewClient(base, "text").WithVisionModel("vision", 5*time.Minute, "0")
	for _, parts := range []int{2, 6, 20} {
		for _, think := range []bool{false, true} {
			prepared, err := client.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A potter finishes a cup.", nil, VideoContext{Mode: VideoModeREF2VA, LongParts: parts}, think)
			if err != nil {
				t.Fatal(err)
			}
			var body chatRequest
			if err := json.Unmarshal(prepared.Body, &body); err != nil {
				t.Fatal(err)
			}
			budget := 4096 + parts*768
			if think {
				budget += 8192
			}
			if prepared.Model != "vision" || body.Options.NumPredict != budget || body.Options.NumCtx != LongVideoContextTokens || prepared.Policy.Timeout < time.Duration(8+parts)*time.Minute {
				t.Fatalf("parts=%d think=%v: incorrect effective budget: %+v", parts, think, prepared)
			}
			metadata, _ := json.Marshal(prepared)
			var restored PreparedEnhancement
			if err := json.Unmarshal(metadata, &restored); err != nil || restored.LongParts != parts || !restored.LongAudio || restored.Policy.NumCtx != LongVideoContextTokens || restored.Policy.TimeoutMS != prepared.Policy.TimeoutMS || restored.ResponseByteLimit() != 512<<10 {
				t.Fatal("durable budget was lost", err)
			}
		}
	}
	ordinary, err := client.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "A cup on a table.", nil, VideoContext{Mode: VideoModeREF2VA}, false)
	if err != nil || ordinary.Policy.NumCtx != 0 || ordinary.Policy.NumPredict != DefaultVideoNumPredict || ordinary.Policy.Timeout != 5*time.Minute || ordinary.ResponseByteLimit() != maxResponseBytes {
		t.Fatal("ordinary video policy changed", ordinary.Policy, err)
	}
}

func TestMiniMaxLongSoundSectionsAreAssembledWithoutLosingTimeline(t *testing.T) {
	content, _ := json.Marshal(map[string]any{
		"prompt": longAssistantFixture(6, "The potter moves the cup gradually."), "references": []any{},
		"overall_soundscape": "Quiet workshop and synchronized ceramic contact. No speech.", "non_diegetic_music": "N/A",
	})
	body, _ := json.Marshal(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": string(content)}})
	prepared := PreparedEnhancement{Mode: ModeTextToVideo, LongParts: 6, LongAudio: true}
	result, err := ParseEnhancementResponse(prepared, http.StatusOK, body)
	if err != nil || !strings.HasSuffix(result.Prompt, "non_diegetic_music:\nN/A") || !strings.Contains(result.Prompt, "synchronized ceramic contact") || strings.Count(result.Prompt, "overall_soundscape:") != 1 {
		t.Fatal("sound sections were lost or duplicated", err)
	}
	content, _ = json.Marshal(map[string]any{"prompt": longAssistantFixture(6, "Cup."), "references": []any{}})
	body, _ = json.Marshal(map[string]any{"done": true, "message": map[string]string{"content": string(content)}})
	if _, err := ParseEnhancementResponse(prepared, http.StatusOK, body); err == nil {
		t.Fatal("missing structured soundscape accepted")
	}
}

func longAssistantFixture(parts int, text string) string {
	var prompt strings.Builder
	prompt.WriteString("subject_definitions:\nA potter.\nsummary:\nvideo continuation\nretention_analysis:\nN/A\ndetailed_description:\n")
	for index := 0; index < parts; index++ {
		fmt.Fprintf(&prompt, "[Shot %d] ", index+1)
		if index > 0 {
			fmt.Fprintf(&prompt, "At %02d:%02d.000, ", index*15/60, index*15%60)
		}
		prompt.WriteString(text + "\n")
	}
	prompt.WriteString("overall_soundscape:\nQuiet workshop.\nnon_diegetic_music:\nN/A")
	return prompt.String()
}

func TestMiniMaxLongResponseIsCompleteAndNotSilentlyTruncated(t *testing.T) {
	prompt := longAssistantFixture(20, strings.Repeat("\U0001f3a5", 900))
	content, _ := json.Marshal(map[string]any{"prompt": prompt, "references": []any{}})
	body, _ := json.Marshal(map[string]any{"done": true, "done_reason": "stop", "eval_count": 9000, "message": map[string]string{"content": string(content)}})
	if len(body) <= maxResponseBytes {
		t.Fatal("fixture must exercise the larger transport envelope")
	}
	prepared := PreparedEnhancement{Mode: ModeTextToVideo, LongParts: 20}
	result, err := ParseEnhancementResponse(prepared, http.StatusOK, body)
	if err != nil || result.Prompt != prompt || result.Usage.CompletionTokens != 9000 || result.LongParts != 20 {
		t.Fatal("complete long plan was rejected or truncated", err)
	}
	prepared.LongParts = 0
	if _, err := ParseEnhancementResponse(prepared, http.StatusOK, body); err == nil {
		t.Fatal("ordinary response cap was bypassed")
	}
	for _, tc := range []struct{ prompt, reason string }{
		{longAssistantFixture(2, "The potter turns the cup."), "length"},
		{"[Shot 1] A cup.", "stop"},
		{strings.Replace(longAssistantFixture(2, "Cup."), "00:15.000", "00:16.000", 1), "stop"},
	} {
		content, _ := json.Marshal(map[string]any{"prompt": tc.prompt, "references": []any{}})
		body, _ := json.Marshal(map[string]any{"done": true, "done_reason": tc.reason, "eval_count": 50, "message": map[string]string{"content": string(content)}})
		result, err := ParseEnhancementResponse(PreparedEnhancement{Mode: ModeTextToVideo, LongParts: 2}, http.StatusOK, body)
		if err == nil || result.Prompt != "" || result.Execution != ExecutionCompleted || result.Usage.CompletionTokens != 50 {
			t.Fatal("unfinished plan was accepted or execution evidence was lost", err)
		}
	}
}

func TestMiniMaxLongPromptLimitsDoNotLeakToOtherModes(t *testing.T) {
	for _, tc := range []struct {
		mode        Mode
		parts, want int
	}{
		{ModeTextToVideo, 2, 7200}, {ModeTextToVideo, 6, 13600}, {ModeTextToVideo, 20, 36000},
		{ModeTextToVideo, 0, 7000}, {ModeTextToVideo, 21, 7000}, {ModeImageToImage, 20, 4000},
	} {
		if got := PromptCharacterLimitForVideo(tc.mode, tc.parts); got != tc.want {
			t.Fatalf("%s/%d: got %d, want %d", tc.mode, tc.parts, got, tc.want)
		}
	}
	base, _ := url.Parse("http://unused")
	client := NewClient(base, "text")
	if _, err := client.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, strings.Repeat("a", 36000), nil, VideoContext{Mode: VideoModeREF2VA, LongParts: 20}, false); err != nil {
		t.Fatal("long input still has the ordinary byte limit", err)
	}
	if _, err := client.PrepareEnhancement(ModeTextToImage, ProfileWorkflowDefault, "A cup.", nil, VideoContext{Mode: VideoModeREF2VA, LongParts: 20}, false); err == nil {
		t.Fatal("long budgets allowed outside the MiniMax long workflow")
	}
}
