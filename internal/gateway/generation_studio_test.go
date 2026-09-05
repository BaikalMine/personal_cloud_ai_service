package gateway

import (
	"bytes"
	"strings"
	"testing"
)

func TestStudioMovedControlsKeepAdvancedPermissionBoundary(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		for _, part := range []struct{ name, wrapper string }{
			{"studio_image_size", `class="studio-basic-settings"`},
			{"studio_advanced", `class="ui-disclosure generation-advanced"`},
			{"studio_video_mode", `class="minimax-reference-field studio-audio-trim"`},
		} {
			var output bytes.Buffer
			if err := templates.ExecuteTemplate(&output, part.name, map[string]any{"CanUseAdvancedGenerationSettings": allowed}); err != nil {
				t.Fatal(err)
			}
			html := output.String()
			start := strings.Index(html, part.wrapper)
			if start < 0 {
				t.Fatalf("%s permission wrapper missing", part.name)
			}
			tag := html[start : start+strings.Index(html[start:], ">")]
			if got := strings.Contains(tag, "hidden inert"); got == allowed {
				t.Errorf("%s allowed=%t: unexpected permission wrapper %s", part.name, allowed, tag)
			}
		}
	}
}

func TestStudioPurposeGroupsAndMainSizeHaveSingleFormOwnership(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, "generate", map[string]any{"CanUseAdvancedGenerationSettings": true}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, name := range []string{"lora", "processing", "sampling", "frame", "memory", "export"} {
		if strings.Count(html, `data-studio-option-group="`+name+`"`) != 1 {
			t.Errorf("expected one %s purpose group", name)
		}
	}
	for _, name := range []string{"base_megapixels", "output_megapixels", "dimension_multiple", "max_longest_side", "video_audio_start", "krea_sage_enabled"} {
		if strings.Count(html, `name="`+name+`"`) != 1 {
			t.Errorf("moved control %s is missing or duplicated", name)
		}
	}
}
