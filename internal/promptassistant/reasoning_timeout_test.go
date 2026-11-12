package promptassistant

import (
	"net/url"
	"testing"
	"time"
)

func TestReasoningTimeoutSurvivesVisionOverride(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:11434")
	c := NewClient(base, "text").WithVisionModel("vision", 5*time.Minute, "0")
	for _, images := range []bool{false, true} {
		p := c.PolicyForRequest(ModeTextToImage, ProfileWorkflowDefault, true, images)
		if p.Timeout != 15*time.Minute || p.TimeoutMS != 900000 {
			t.Fatalf("reasoning deadline: %+v", p)
		}
	}
	if p := c.PolicyForRequest(ModeTextToImage, ProfileWorkflowDefault, false, true); p.Timeout != 5*time.Minute {
		t.Fatalf("ordinary vision changed: %+v", p)
	}
	if p := c.ForModels("admin-text", "admin-vision", 10*time.Minute).PolicyForRequest(ModeTextToImage, ProfileWorkflowDefault, true, true); p.Timeout != 10*time.Minute {
		t.Fatalf("admin model limit changed: %+v", p)
	}
}
