package gateway

import (
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/thumbnail"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAssistantVideoErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		text string
	}{
		{thumbnail.ErrInvalid, "не удалось прочитать кадры"},
		{thumbnail.ErrUnsupported, "не поддерживается"},
		{thumbnail.ErrLimit, "лимита обработки"},
		{context.DeadlineExceeded, "лимита обработки"},
		{context.Canceled, "отменена"},
	} {
		got := assistantVideoError(fmt.Errorf("private/path: %w", tc.err))
		if got == nil || !strings.Contains(got.Error(), tc.text) || strings.Contains(got.Error(), "private") || strings.Contains(got.Error(), "thumbnail:") {
			t.Fatalf("unexpected user message: %v", got)
		}
	}
	if assistantVideoError(nil) != nil {
		t.Fatal("success became error")
	}
	err := errors.New("видеореференс недоступен")
	if assistantVideoError(err) != err {
		t.Fatal("existing validation error changed")
	}
}

func TestAIHubAssistantPhotoExcludesInactiveMedia(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Form = url.Values{"generation_workflow": {aiHubWorkflowID}, "aihub_mode": {"photo"}, "aihub_audio_source": {"ltx"}, "aihub_audio_prompt": {"Hello."}, "input_video": {"foreign/file.mp4"}, "video_has_video": {"true"}, "video_has_audio": {"true"}}
	v := promptassistant.VideoContext{DurationSeconds: 5, VideoReference: true, AudioReference: true}
	if err := (&App{}).prepareAssistantVideo(context.Background(), 1, r, &v); err != nil {
		t.Fatal(err)
	}
	if v.VideoReference || v.AudioReference || v.VoiceReference || len(v.VideoStoryboard) > 0 || v.AudioPrompt != "Hello." {
		t.Fatal("inactive media leaked", v)
	}
}

func TestAIHubAssistantRejectsForeignVideoBeforeFetch(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.Form = url.Values{"generation_workflow": {aiHubWorkflowID}, "aihub_mode": {"video"}, "input_video": {"foreign/file.mp4"}}
	if err := (&App{}).prepareAssistantVideo(context.Background(), 1, r, &promptassistant.VideoContext{DurationSeconds: 5}); err == nil {
		t.Fatal("foreign source accepted")
	}
}
