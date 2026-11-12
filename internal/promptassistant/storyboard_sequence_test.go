package promptassistant

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestAIHubChronologicalPagesStaySeparateFromPictures(t *testing.T) {
	base, _ := url.Parse("http://localhost:11434")
	c := NewClient(base, "text").WithVisionModel("vision", 0, "0")
	refs := []ImageReference{{Number: 1, Role: ImageReferenceIdentity, MIMEType: "image/png", Image: []byte("face")}}
	v := VideoContext{Mode: VideoModeREF2VA, AIHubMode: "video", VideoReference: true, DurationSeconds: 5, VideoStoryboard: []byte("first"), VideoStoryboards: [][]byte{[]byte("first"), []byte("second")}, VideoStoryboardTimes: [][]float64{{0, .25, .5}, {3, 3.25, 3.5}}}
	prepared, err := c.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "Keep the motion", refs, v, false)
	if err != nil {
		t.Fatal(err)
	}
	var req chatRequest
	if err = json.Unmarshal(prepared.Body, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages[1].Images) != 3 {
		t.Fatal("missing pages or duplicate first page")
	}
	for i, want := range []string{"face", "first", "second"} {
		b, _ := base64.StdEncoding.DecodeString(req.Messages[1].Images[i])
		if string(b) != want {
			t.Fatal("image order changed")
		}
	}
	for _, want := range []string{"page 1/2", "page 2/2", "[3,3.25,3.5]", "NOT a Picture reference", "no heard or transcribed audio"} {
		if !strings.Contains(req.Messages[1].Content, want) {
			t.Fatal("missing context", want)
		}
	}
	if len(prepared.References) != 2 || prepared.References[1].Identifier != "Video 1" {
		t.Fatal("pages became separate references")
	}
	v.VideoStoryboardTimes = nil
	if _, err = c.PrepareEnhancement(ModeTextToVideo, ProfileMiniMaxH3REF2VA, "Keep the motion", refs, v, false); err == nil {
		t.Fatal("accepted pages without timestamps")
	}
}
