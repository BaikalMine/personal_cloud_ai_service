package promptassistant

import (
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestModelSelectionDoesNotMutateSharedClient(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:11434")
	client := NewClient(base, "default-text").WithVisionModel("default-vision", time.Minute, "0")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			selected := client.ForModels("powerful-text", "powerful-vision", 600*time.Second)
			refs := []ImageReference{{Number: 1, Role: ImageReferenceBaseScene, MIMEType: "image/png", Image: []byte("fixture")}}
			for _, withImages := range []bool{false, true} {
				input, want := []ImageReference(nil), "powerful-text"
				if withImages {
					input, want = refs, "powerful-vision"
				}
				prepared, err := selected.PrepareEnhancement(ModeTextToImage, ProfileWorkflowDefault, "A green vase", input, VideoContext{}, false)
				if err != nil || prepared.Model != want || prepared.Policy.TimeoutMS != 600000 {
					t.Errorf("selected model/policy lost: %+v %v", prepared, err)
				}
			}
			if _, err := client.ForModels("text-only", "", 0).PrepareEnhancement(ModeTextToImage, ProfileWorkflowDefault, "A green vase", refs, VideoContext{}, false); err == nil {
				t.Error("text-only choice silently used the default vision model")
			}
		}()
	}
	wg.Wait()
	if client.model != "default-text" || client.visionModel != "default-vision" || client.visionTimeout != time.Minute {
		t.Fatal("shared client was changed")
	}
}
