package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIHubExportLiveCandidate(t *testing.T) {
	dir := os.Getenv("AIHUB_LIVE_EXPORT")
	if dir == "" {
		t.Skip("explicit offline graph export only")
	}
	response, err := http.Get("http://127.0.0.1:8188/object_info")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var info map[string]comfyNodeInfo
	if err = json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	resolve := func(node, field, wanted string) string {
		for _, name := range comfyChoiceStrings(info, node, field) {
			if strings.EqualFold(filepath.Base(strings.ReplaceAll(name, "\\", "/")), filepath.Base(wanted)) {
				return name
			}
		}
		t.Fatalf("missing model %s", wanted)
		return ""
	}
	for _, scenario := range []string{"video", "photo"} {
		in := aiHubTestInput(t)
		in.Seed = 424242
		in.Width = 384
		in.Height = 704
		in.InputImage = "portrait.png"
		in.InputVideo = "reference.mp4"
		in.AIHubMode = scenario
		in.AIHubAudioSource = "original"
		in.AIHubDLSSEnabled = true
		in.AIHubDLSSStrength = 1
		in.VideoRTXEnabled = true
		in.VideoRTXScale = 2
		in.AIHubFGEnabled = true
		in.AIHubFGMultiplier = 2
		in.Positive = "<Picture 1> supplies the identity of a clearly adult woman with dark shoulder-length hair. <Video 1> supplies the street, beige coat, camera framing and waving action. Replace only the woman's face and hair with the identity from <Picture 1>. Preserve the scene, clothing, natural motion and timing. One continuous photorealistic shot."
		if scenario == "photo" {
			in.InputVideo = ""
			in.AIHubAudioSource = "ltx"
			in.AIHubAudioPrompt = "An adult woman says in English: Hello, have a lovely day. Quiet room tone. No music."
			in.Positive = "A clearly adult woman from <Picture 1> in a green blazer and white blouse stands beside a window, gives a small friendly wave and says: Hello, have a lovely day. Natural blinking and visible speech, one continuous medium portrait shot."
		}
		in.ModelName = resolve("UNETLoader", "unet_name", in.ModelName)
		in.TextEncoder = resolve("CLIPLoader", "clip_name", in.TextEncoder)
		in.VAE = resolve("VAELoader", "vae_name", in.VAE)
		in.AudioVAE = resolve("VAELoader", "vae_name", in.AudioVAE)
		if err = resolveAIHubModels(&in, info); err != nil {
			t.Fatal(err)
		}
		if err = normalizeAIHub(&in); err != nil {
			t.Fatal(err)
		}
		graph, err := buildAIHubPrompt(in)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(graph, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, scenario+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
