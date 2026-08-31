package gateway

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateComfyPromptAcceptsRTXDynamicBranch(t *testing.T) {
	catalog := buildComfySchemaCatalog(decodeTestObjectInfo(t, testObjectInfoJSON), time.Time{}, "fixture")
	prompt := testRTXPrompt()
	if issues := validateComfyPrompt(catalog, prompt); len(issues) != 0 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestComfyScalarIntAcceptsNegativeSeed(t *testing.T) {
	if !comfyScalarTypeMatches("INT", -1) {
		t.Fatal("negative ComfyUI seed was rejected")
	}
	if _, ok := comfyPromptReference([]any{"1", -1}); ok {
		t.Fatal("negative output index was accepted")
	}
}

func TestComfyNumericComboAcceptsSelectedNumber(t *testing.T) {
	issues := validateComfyInput(comfySchemaCatalog{}, nil, "22", comfyNodeSchema{ClassType: "RIFE VFI"}, comfyInputSchema{Type: "COMBO", Choices: []any{0.5, 1.0, 2.0}}, "scale_factor", 1.0)
	if len(issues) != 0 {
		t.Fatalf("numeric combo issues = %#v", issues)
	}
}

func TestValidateComfyPromptReportsActionableRTXIssues(t *testing.T) {
	catalog := buildComfySchemaCatalog(decodeTestObjectInfo(t, testObjectInfoJSON), time.Time{}, "fixture")
	tests := []struct {
		name      string
		mutate    func(map[string]any)
		wantCode  string
		wantField string
	}{
		{
			name: "missing selected branch value",
			mutate: func(prompt map[string]any) {
				delete(prompt["20"].(map[string]any)["inputs"].(map[string]any), "resize_type.scale")
			},
			wantCode: "missing_dynamic_input", wantField: "video_rtx_scale",
		},
		{
			name: "unsupported quality",
			mutate: func(prompt map[string]any) {
				prompt["20"].(map[string]any)["inputs"].(map[string]any)["quality"] = "MAX"
			},
			wantCode: "invalid_choice", wantField: "video_rtx_quality",
		},
		{
			name: "scale above schema maximum",
			mutate: func(prompt map[string]any) {
				prompt["20"].(map[string]any)["inputs"].(map[string]any)["resize_type.scale"] = 5.0
			},
			wantCode: "above_maximum", wantField: "video_rtx_scale",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := testRTXPrompt()
			test.mutate(prompt)
			issues := validateComfyPrompt(catalog, prompt)
			if !hasCompatibilityIssue(issues, test.wantCode, test.wantField) {
				t.Fatalf("issues = %#v", issues)
			}
		})
	}
}

func TestValidateComfyPromptDetectsSchemaEvolution(t *testing.T) {
	info := decodeTestObjectInfo(t, testObjectInfoJSON)
	node := info["RTXVideoSuperResolution"]
	node.Input.Required["required_after_update"] = json.RawMessage(`["BOOLEAN", {"default": true}]`)
	info["RTXVideoSuperResolution"] = node
	catalog := buildComfySchemaCatalog(info, time.Time{}, "updated")
	issues := validateComfyPrompt(catalog, testRTXPrompt())
	if !hasCompatibilityIssue(issues, "missing_required_input", "") {
		t.Fatalf("schema change was not detected: %#v", issues)
	}
}

func TestValidateComfyPromptChecksNodesAndConnections(t *testing.T) {
	info := decodeTestObjectInfo(t, testObjectInfoJSON)
	info["ImageConsumer"] = comfyNodeInfoFromJSON(t, `{
	  "input":{"required":{"image":["IMAGE"]}},
	  "output":[],"display_name":"Image consumer"
	}`)
	catalog := buildComfySchemaCatalog(info, time.Time{}, "fixture")
	tests := []struct {
		name     string
		prompt   map[string]any
		wantCode string
	}{
		{
			name:     "unknown class",
			prompt:   map[string]any{"9": map[string]any{"class_type": "RemovedCustomNode", "inputs": map[string]any{}}},
			wantCode: "unknown_node",
		},
		{
			name:     "missing source",
			prompt:   map[string]any{"9": map[string]any{"class_type": "ImageConsumer", "inputs": map[string]any{"image": []any{"404", 0}}}},
			wantCode: "missing_source_node",
		},
		{
			name: "invalid output index",
			prompt: map[string]any{
				"1": map[string]any{"class_type": "FrameSource", "inputs": map[string]any{}},
				"9": map[string]any{"class_type": "ImageConsumer", "inputs": map[string]any{"image": []any{"1", 1}}},
			},
			wantCode: "invalid_output_index",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues := validateComfyPrompt(catalog, test.prompt)
			if !hasCompatibilityIssue(issues, test.wantCode, "") {
				t.Fatalf("issues = %#v", issues)
			}
		})
	}

	audioSource := info["FrameSource"]
	audioSource.Output = []json.RawMessage{json.RawMessage(`"AUDIO"`)}
	info["FrameSource"] = audioSource
	catalog = buildComfySchemaCatalog(info, time.Time{}, "audio")
	issues := validateComfyPrompt(catalog, map[string]any{
		"1": map[string]any{"class_type": "FrameSource", "inputs": map[string]any{}},
		"9": map[string]any{"class_type": "ImageConsumer", "inputs": map[string]any{"image": []any{"1", 0}}},
	})
	if !hasCompatibilityIssue(issues, "incompatible_connection", "") {
		t.Fatalf("connection type mismatch was not detected: %#v", issues)
	}
}

func testRTXPrompt() map[string]any {
	return map[string]any{
		"1": map[string]any{"class_type": "FrameSource", "inputs": map[string]any{}},
		"20": map[string]any{
			"class_type": "RTXVideoSuperResolution",
			"inputs": map[string]any{
				"images": []any{"1", 0}, "resize_type": "scale by multiplier", "resize_type.scale": 2.0, "quality": "ULTRA",
			},
		},
	}
}

func comfyNodeInfoFromJSON(t *testing.T, raw string) comfyNodeInfo {
	t.Helper()
	var info comfyNodeInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func hasCompatibilityIssue(issues []workflowCompatibilityIssue, code, field string) bool {
	for _, issue := range issues {
		if issue.Code == code && (field == "" || issue.Field == field) {
			return true
		}
	}
	return false
}
