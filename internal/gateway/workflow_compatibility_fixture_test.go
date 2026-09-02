package gateway

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type objectInfoFixture struct {
	SchemaVersion     int                      `json:"schema_version"`
	Family            string                   `json:"family"`
	CapturedAt        time.Time                `json:"captured_at"`
	SourceFingerprint string                   `json:"source_fingerprint"`
	Nodes             map[string]comfyNodeInfo `json:"nodes"`
}

func TestWorkflowCompatibilityFixturesCoverCriticalNodes(t *testing.T) {
	expected := map[string][]string{
		"krea2.json":         {"AspectRatioSimplifier", "Krea2EditModelPatch", "Krea2EditGroundedEncode", "UltimateSDUpscale"},
		"flux2.json":         {"ClipLoaderGGUF", "LCReferenceLatent", "LCPipeEdit", "LCSamplerConfigureSimplePipeOut"},
		"minimax_h3_v4.json": {"MiniMaxH3ImageToVideo", "MiniMaxH3ReferenceToVideo", "RIFE VFI", "RTXVideoSuperResolution", "ImageSharpenKJ", "LCColorMatch"},
	}
	fingerprint := ""
	for filename, classTypes := range expected {
		fixture := loadObjectInfoFixture(t, filename)
		if fixture.SchemaVersion != 1 || fixture.CapturedAt.IsZero() || len(fixture.SourceFingerprint) != 64 {
			t.Fatalf("fixture metadata %s = %#v", filename, fixture)
		}
		if fingerprint == "" {
			fingerprint = fixture.SourceFingerprint
		} else if fixture.SourceFingerprint != fingerprint {
			t.Fatalf("fixture %s came from another object_info snapshot", filename)
		}
		for _, classType := range classTypes {
			if _, ok := fixture.Nodes[classType]; !ok {
				t.Fatalf("fixture %s is missing %s", filename, classType)
			}
		}
	}
}

func TestCurrentKreaFluxAndMiniMaxFixturesValidateRepresentativeGraphs(t *testing.T) {
	info := compatibilityFixtureObjectInfo(t)
	catalog := buildGenerationModelCatalog(info)
	catalog.ObjectInfo = comfyObjectInfoSnapshot{
		Info: info, Schema: buildComfySchemaCatalog(info, time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), "fixture"),
		Source: comfyObjectInfoLive, Fingerprint: "fixture",
	}
	results := buildWorkflowCompatibilityResults(catalog)
	if len(results) < 8 {
		t.Fatalf("representative matrix is unexpectedly small: %#v", results)
	}
	seen := map[string]bool{}
	var failures []string
	for _, result := range results {
		seen[result.Family] = true
		if result.Status != workflowCompatibilityOK {
			message := result.Description
			if len(result.Issues) > 0 {
				message = result.Issues[0].Message
			}
			failures = append(failures, result.Family+" / "+result.Scenario+": "+message)
		}
	}
	for _, family := range []string{"Krea2", "Flux2", "MiniMax H3 v5"} {
		if !seen[family] {
			t.Fatalf("matrix is missing %s: %#v", family, results)
		}
	}
	if len(failures) > 0 {
		t.Fatalf("fixture compatibility failures:\n%s", strings.Join(failures, "\n"))
	}
}

func TestAdminWorkflowCompatibilityTemplateRendersOperationalStates(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = templates.ExecuteTemplate(&output, "admin_workflows", map[string]any{
		"Title": "Совместимость workflow", "CSRF": "csrf", "AssetVersion": "asset", "PublicBaseURL": "https://ai.example.test",
		"Report": workflowCompatibilityReport{
			SourceLabel: "получен из ComfyUI", Fingerprint: "0123456789ab", NodeCount: 420, Compatible: 1, Failed: 1,
			Snapshot: comfyObjectInfoSnapshot{FetchedAt: time.Now()}, Results: []workflowCompatibilityResult{
				{Scenario: "Первый и последний кадр", Family: "MiniMax H3 v5", Model: "MiniMax H3", Status: workflowCompatibilityOK, NodeCount: 24},
				{Scenario: "Полная обработка видео", Family: "MiniMax H3 v5", Model: "MiniMax H3", Status: workflowCompatibilityErrorStatus, Issues: []workflowCompatibilityIssue{{Label: "Финальный апскейл", Message: "Не найден resize_type", ClassType: "RTXVideoSuperResolution", InputName: "resize_type"}}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, marker := range []string{"compatibility-source", "Обновить каталог и проверить", "Первый и последний кадр", "RTXVideoSuperResolution", "resize_type"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("compatibility template is missing %q", marker)
		}
	}
}

func compatibilityFixtureObjectInfo(t *testing.T) map[string]comfyNodeInfo {
	t.Helper()
	info := make(map[string]comfyNodeInfo)
	for _, filename := range []string{"krea2.json", "flux2.json", "minimax_h3_v4.json"} {
		for classType, node := range loadObjectInfoFixture(t, filename).Nodes {
			info[classType] = node
		}
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		for _, node := range definition.Nodes {
			if classType, ok := node["class_type"].(string); ok {
				addPermissiveFixtureNode(info, classType)
			}
		}
	}
	for _, classType := range []string{
		"BasicGuider", "BasicScheduler", "CR Apply LoRA Stack", "CR LoRA Stack", "H3AIMDOResidencyLimiter",
		"H3MemoryOptimization", "H3SparseAttentionAdvanced", "ImageSharpenKJ", "KSamplerSelect", "LCColorMatch",
		"Image Filter Adjustments", "Image Levels Adjustment", "LCGetImage", "LCImageMaskResize", "LCVRAMCacheClear", "LoadAudio", "LoadImage", "MiniMaxH3ImageToVideo",
		"MiniMaxChunkFeedForward", "MiniMaxH3MemoryEfficientSageAttentionPatch", "MiniMaxH3ReferenceToVideo", "MiniMaxH3SigmaShift", "MiniMaxLowVRAMAttention",
		"MiniMaxH3TurboLoRA", "MiniMaxH3TurboSampler", "PathchSageAttentionKJ", "RandomNoise", "RIFE VFI", "RTXVideoSuperResolution",
		"SamplerCustomAdvanced", "TrimAudioDuration", "UNETLoader", "UpscaleModelLoader", "UltimateSDUpscale", "VAEDecode", "VAEDecodeAudio", "VAELoader",
		"VHS_LoadVideo", "VHS_VideoCombine", "ComfyMathExpression", "SeedVR2LoadVAEModel", "SeedVR2LoadDiTModel", "SeedVR2VideoUpscaler",
	} {
		addPermissiveFixtureNode(info, classType)
	}
	return info
}

func addPermissiveFixtureNode(info map[string]comfyNodeInfo, classType string) {
	if _, exists := info[classType]; exists {
		return
	}
	info[classType] = comfyNodeInfo{DisplayName: classType, PythonModule: "fixture.permissive"}
}

func loadObjectInfoFixture(t *testing.T, filename string) objectInfoFixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "object_info", filename))
	if err != nil {
		t.Fatal(err)
	}
	var fixture objectInfoFixture
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
