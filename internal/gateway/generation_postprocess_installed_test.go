package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in observation only: GET object_info for the allowlisted classes. This
// test never uploads a file, submits a prompt or exercises the GPU.
func TestPostprocessInstalledSchema(t *testing.T) {
	base := strings.TrimRight(os.Getenv("GATEWAY_POSTPROCESS_SCHEMA_URL"), "/")
	if base == "" {
		t.Skip("set GATEWAY_POSTPROCESS_SCHEMA_URL for a read-only installed-schema check")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	info := map[string]comfyNodeInfo{}
	for _, class := range []string{"LoadImage", "SaveImage", "VHS_LoadVideo", "VHS_VideoCombine", "RTXVideoSuperResolution", "RIFE VFI", "LCColorMatch", "ImageFromBatch", "RepeatImageBatch", "ImageBatch"} {
		response, err := client.Get(base + "/object_info/" + url.PathEscape(class))
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		response.Body.Close()
		if readErr != nil || len(data) > 1<<20 || response.StatusCode != http.StatusOK {
			t.Fatalf("schema read failed for %s: %d", class, response.StatusCode)
		}
		var nodes map[string]comfyNodeInfo
		if err := json.Unmarshal(data, &nodes); err != nil {
			t.Fatal(err)
		}
		node, exists := nodes[class]
		if !exists {
			t.Fatalf("class not installed: %s", class)
		}
		info[class] = node
	}
	catalog := buildComfySchemaCatalog(info, time.Now(), "")
	for _, kind := range []string{"image", "video"} {
		for _, color := range []bool{false, true} {
			source, options, name, reference := postprocessTestSource(), postprocessTestOptions(), "source.png", ""
			if kind == "video" {
				source = postprocessVideoSource()
				options.Interpolate, options.RIFECheckpoint, name = 2, "rife49.pth", "source.mp4"
			}
			if color {
				options.ColorMethod, options.ColorStrength, reference = "adain", 0.5, "gateway/gateway-schema-test/reference.png"
			}
			if _, _, err := buildPostprocessPrompt(source, options, "gateway/gateway-schema-test/"+name, reference, "gateway/gateway-schema-test", catalog); err != nil {
				t.Fatalf("%s color=%t: %v", kind, color, err)
			}
		}
	}
}

func TestPostprocessVideoFormatMetadata(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `[]`, `["COMBO"]`, `["COMBO",null]`,
		`[[],{"formats":{"video/h264-mp4":{}}}]`,
		`[[],{"formats":{"video/h264-mp4":[[]]}}]`,
		`[[],{"formats":{"video/h264-mp4":[[7,"INT"]]}}]`,
		`[[],{"formats":{"video/h264-mp4":[["crf",{}]]}}]`,
		`[[],{"formats":{"video/h264-mp4":[["crf","INT"],["crf","FLOAT"]]}}]`,
	} {
		if got := parseComfyVideoFormats(json.RawMessage(raw)); len(got["video/h264-mp4"]) != 0 {
			t.Fatalf("accepted malformed metadata: %s", raw)
		}
	}
	got := parseComfyVideoFormats(json.RawMessage(`[[],{"formats":{"other":{},"video/h264-mp4":[["crf","INT",{"default":19,"min":0,"max":100}],["pix_fmt",["yuv420p"]]]}}]`))
	if len(got) != 1 || got["video/h264-mp4"]["crf"].Max == nil || *got["video/h264-mp4"]["crf"].Max != 100 || !choiceContains(got["video/h264-mp4"]["pix_fmt"].Choices, "yuv420p") {
		t.Fatalf("wrong metadata: %+v", got)
	}
}
