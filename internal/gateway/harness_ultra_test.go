package gateway

import (
	"ai-access-gateway/internal/config"
	"ai-access-gateway/internal/domain"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const ultraTestID = "10000000-0000-4000-8000-000000000001"

func ultraReadyRelayInfo() map[string]comfyNodeInfo {
	info := map[string]comfyNodeInfo{"LCRelayVSRUpscale": {}, "RelayVSRConditioning": {}, "VSRDecode": {}}
	for _, loader := range []struct {
		node, field string
		names       []string
	}{
		{"UNETLoader", "unet_name", []string{`RelayVSR\relayvsr_x2_lightx2v_int8_convrot.safetensors`, "RelayVSR/relayvsr_x4_int8_convrot.safetensors"}},
		{"VAELoader", "vae_name", []string{`RelayVSR\relayvsr_flash_vae_x2_fp32.safetensors`, "RelayVSR/relayvsr_flash_vae_x4_fp32.safetensors"}},
	} {
		var n comfyNodeInfo
		raw, _ := json.Marshal([]any{loader.names})
		n.Input.Required = map[string]json.RawMessage{loader.field: raw}
		info[loader.node] = n
	}
	return info
}

func TestHarnessUltraRelayCapabilityRequiresLocalCoreAndAssets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*generationModelCatalog)
		want   []int
	}{
		{"both", func(*generationModelCatalog) {}, []int{2, 4}},
		{"missing-core", func(c *generationModelCatalog) { delete(c.ObjectInfo.Info, "RelayVSRConditioning") }, []int{}},
		{"missing-decode", func(c *generationModelCatalog) { delete(c.ObjectInfo.Info, "VSRDecode") }, []int{}},
		{"missing-node", func(c *generationModelCatalog) { delete(c.ObjectInfo.Info, "LCRelayVSRUpscale") }, []int{}},
		{"missing-model-loader", func(c *generationModelCatalog) { delete(c.ObjectInfo.Info, "UNETLoader") }, []int{}},
		{"missing-vae-loader", func(c *generationModelCatalog) { delete(c.ObjectInfo.Info, "VAELoader") }, []int{}},
		{"offline", func(c *generationModelCatalog) { c.Online = false }, []int{}},
		{"stale", func(c *generationModelCatalog) { c.ObjectInfo.Source = comfyObjectInfoLastKnownGood }, []int{}},
		{"two-only", func(c *generationModelCatalog) {
			n := c.ObjectInfo.Info["VAELoader"]
			n.Input.Required["vae_name"] = json.RawMessage(`[["relayvsr_flash_vae_x2_fp32.safetensors"]]`)
			c.ObjectInfo.Info["VAELoader"] = n
		}, []int{2}},
		{"partial-download", func(c *generationModelCatalog) {
			n := c.ObjectInfo.Info["UNETLoader"]
			n.Input.Required["unet_name"] = json.RawMessage(`[["relayvsr_x2_lightx2v_int8_convrot.safetensors.part","relayvsr_x4_int8_convrot.safetensors.download"]]`)
			c.ObjectInfo.Info["UNETLoader"] = n
		}, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := buildGenerationModelCatalog(ultraReadyRelayInfo())
			tc.change(&catalog)
			got := harnessUltraRelayCapability(catalog)
			if got.Available != (len(tc.want) > 0) || !reflect.DeepEqual(got.ReadyScales, tc.want) {
				t.Fatalf("capability=%+v, want scales=%v", got, tc.want)
			}
		})
	}
}

func TestHarnessUltraCatalogIncludesRelayCapability(t *testing.T) {
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	for name, node := range ultraReadyRelayInfo() {
		if _, exists := fixture.Nodes[name]; !exists || name == "UNETLoader" || name == "VAELoader" {
			fixture.Nodes[name] = node
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(fixture.Nodes) }))
	defer server.Close()
	upstream, _ := url.Parse(server.URL)
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	w := httptest.NewRecorder()
	app.harnessUltraCatalog(w, httptest.NewRequest("GET", "/catalog", nil))
	var reply struct {
		Capabilities struct {
			Relay harnessUltraRelayVSRCapability `json:"relay_vsr"`
		} `json:"capabilities"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || !reply.Capabilities.Relay.Available || !reflect.DeepEqual(reply.Capabilities.Relay.ReadyScales, []int{2, 4}) {
		t.Fatalf("catalog capability missing: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHarnessUltraExecutorCatalogReusesSchemaAndRejectsStaleExecutor(t *testing.T) {
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	offline, requests := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if offline {
			http.Error(w, "offline", 503)
			return
		}
		json.NewEncoder(w).Encode(fixture.Nodes)
	}))
	defer server.Close()
	upstream, _ := url.Parse(server.URL)
	app := &App{cfg: Config{ComfyUIUpstream: upstream, ComfyObjectInfoCacheTTL: 30 * time.Second, ComfyObjectInfoMaxStale: 2 * time.Minute}}
	now := time.Now()
	app.comfySchemaCache().now = func() time.Time { return now }
	full := app.comfyGenerationModels(context.Background())
	compact := app.harnessUltraExecutorCatalog(context.Background())
	if !compact.Online || requests != 1 || compact.ObjectInfo.Fingerprint != full.ObjectInfo.Fingerprint || compact.byID != nil {
		t.Fatal("Ultra rebuilt the website catalog or lost its schema")
	}
	expected, _ := json.Marshal(harnessUltraModels(full))
	actual, _ := json.Marshal(harnessUltraModels(compact))
	if string(expected) != string(actual) {
		t.Fatal("Ultra model choices changed")
	}
	expected, _ = json.Marshal(harnessUltraCompanions(full))
	actual, _ = json.Marshal(harnessUltraCompanions(compact))
	if string(expected) != string(actual) {
		t.Fatal("Ultra companion choices changed")
	}
	offline = true
	now = now.Add(31 * time.Second)
	if app.harnessUltraExecutorCatalog(context.Background()).Online {
		t.Fatal("last-known-good schema admitted an offline executor")
	}
	now = now.Add(3 * time.Minute)
	if app.harnessUltraExecutorCatalog(context.Background()).Online {
		t.Fatal("expired schema admitted an offline executor")
	}
}

func TestHarnessUltraResolutionMatchesWebsiteProfile(t *testing.T) {
	type sizeCase struct {
		AspectRatio string  `json:"aspect_ratio"`
		Megapixels  float64 `json:"megapixels"`
		Width       int     `json:"width"`
		Height      int     `json:"height"`
	}
	var cases []sizeCase
	for _, aspect := range []string{"1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", "21:9"} {
		for _, mp := range []float64{.5, 1.9, 4.27} {
			width, height, err := generationDimensions(aspect, mp, 16, 4096)
			if err != nil {
				t.Fatal(err)
			}
			graph, catalog := ultraGraphFixture(t, "text")
			dims := graph["3481"].(map[string]any)["inputs"].(map[string]any)
			dims["custom_width"], dims["custom_height"] = float64(width), float64(height)
			if err := validateHarnessUltraGraph(graph, ultraTestID, catalog); err != nil {
				t.Fatalf("website size %s at %g MP was rejected: %v", aspect, mp, err)
			}
			cases = append(cases, sizeCase{aspect, mp, width, height})
		}
	}
	graph, catalog := ultraGraphFixture(t, "text")
	dims := graph["3481"].(map[string]any)["inputs"].(map[string]any)
	dims["custom_width"], dims["custom_height"] = float64(2048), float64(2208)
	if validateHarnessUltraGraph(graph, ultraTestID, catalog) == nil {
		t.Fatal("oversized base canvas accepted")
	}
	if path := os.Getenv("ULTRA_RESOLUTION_FIXTURES"); path != "" {
		body, err := json.MarshalIndent(map[string]any{"maximum_pixels": qwen21MaxOutputPixels, "megapixel_pixels": 1024 * 1024, "cases": cases}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHarnessUltraOutputAcceptsWindowsSeparatorsOnlyInsideOwnNamespace(t *testing.T) {
	for _, folder := range []string{"harness-ultra/" + ultraTestID, "harness-ultra\\" + ultraTestID} {
		if !validUltraOutput(generationOutput{Type: "output", Filename: "result_00001.png", Subfolder: folder}, ultraTestID) {
			t.Fatal(folder)
		}
	}
	for _, folder := range []string{"other/" + ultraTestID, "harness-ultra/../" + ultraTestID, "harness-ultra\\..\\" + ultraTestID} {
		if validUltraOutput(generationOutput{Type: "output", Filename: "result.png", Subfolder: folder}, ultraTestID) {
			t.Fatal("foreign path accepted", folder)
		}
	}
}

func ultraGraphFixture(t *testing.T, mode string) (map[string]any, generationModelCatalog) {
	t.Helper()
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	catalog := buildGenerationModelCatalog(fixture.Nodes)
	input := kreaProTestInput()
	input.KreaProMode = mode
	if mode != "text" {
		input.InputImage = "harness-ultra/" + ultraTestID + ".png"
	}
	if mode == "inpaint" {
		input.ReferenceImages[0] = input.InputImage
	}
	graph, err := buildKreaProPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	fields := graph["4071"].(map[string]any)["inputs"].(map[string]any)
	fields["path"], fields["filename"], fields["filename_prefix"], fields["format"] = "harness-ultra/"+ultraTestID, "result", "Harness-Ultra", "png"
	data, _ := json.Marshal(graph)
	_ = json.Unmarshal(data, &graph)
	return graph, catalog
}

func TestHarnessUltraLegacyCompletionNeedsHistoryAndBoundedIdleReservation(t *testing.T) {
	for _, test := range []struct {
		name        string
		known       bool
		reserved    int64
		want        bool
		errorStatus bool
		cacheLimit  int64
	}{
		{"history_missing", false, 0, false, false, 256 << 20}, {"model_still_loaded", true, 1 << 30, false, false, 256 << 20}, {"terminal_and_unloaded", true, 0, true, false, 256 << 20}, {"idle_cuda_async_pool", true, 256 << 20, true, false, 256 << 20}, {"error_history_is_terminal", true, 256 << 20, true, true, 256 << 20},
		{"full_pipeline_idle_pool", true, 512 << 20, true, false, 512 << 20}, {"full_pipeline_model_not_released", true, 1 << 30, false, false, 512 << 20}, {"pool_is_not_proof_without_history", false, 512 << 20, false, false, 512 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/history/" + ultraTestID:
					if test.known {
						status := "success"
						if test.errorStatus {
							status = "error"
						}
						json.NewEncoder(w).Encode(map[string]any{ultraTestID: map[string]any{"status": map[string]any{"completed": !test.errorStatus, "status_str": status}}})
					} else {
						w.Write([]byte(`{}`))
					}
				case "/queue":
					w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
				case "/free":
					w.Write([]byte(`{}`))
				case "/system_stats":
					json.NewEncoder(w).Encode(map[string]any{"devices": []any{map[string]any{"type": "cuda", "torch_vram_total": test.reserved, "vram_total": int64(32 << 30), "vram_free": int64(30 << 30)}}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			upstream, _ := url.Parse(server.URL)
			a := &App{cfg: Config{ComfyUIUpstream: upstream, Harness: config.HarnessConfig{UltraIdleCacheBytes: test.cacheLimit}}}
			ok, err := a.harnessUltraLegacyCompletion(t.Context(), domain.GPUWork{JobKey: ultraTestID, Phase: "running"})
			if err != nil || ok != test.want {
				t.Fatal(ok, err)
			}
		})
	}
}

func TestHarnessUltraFullGraphNamespaces(t *testing.T) {
	for _, mode := range []string{"text", "references", "identity", "control", "inpaint", "outpaint"} {
		t.Run(mode, func(t *testing.T) {
			graph, catalog := ultraGraphFixture(t, mode)
			if err := validateHarnessUltraGraph(graph, ultraTestID, catalog); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHarnessUltraV29RelayAndAllocatorBounds(t *testing.T) {
	for _, kind := range []string{"none", "vosr", "relay"} {
		for _, scale := range []int{2, 4} {
			t.Run(fmt.Sprintf("%s-%d", kind, scale), func(t *testing.T) {
				input := kreaProTestInput()
				input.KreaProUpscaler, input.KreaProRelayScale = kind, scale
				graph, err := buildKreaProPrompt(input)
				if err != nil {
					t.Fatal(err)
				}
				fields := graph["4071"].(map[string]any)["inputs"].(map[string]any)
				fields["path"], fields["filename"], fields["filename_prefix"] = "harness-ultra/"+ultraTestID, "result", "Harness-Ultra"
				data, _ := json.Marshal(graph)
				if err := json.Unmarshal(data, &graph); err != nil {
					t.Fatal(err)
				}
				_, catalog := ultraGraphFixture(t, "text")
				for name, node := range ultraReadyRelayInfo() {
					if _, exists := catalog.ObjectInfo.Info[name]; !exists || name == "UNETLoader" || name == "VAELoader" {
						catalog.ObjectInfo.Info[name] = node
					}
				}
				if err := validateHarnessUltraGraph(graph, ultraTestID, catalog); err != nil {
					t.Fatal(err)
				}
				if kind == "relay" {
					delete(catalog.ObjectInfo.Info, "VAELoader")
					if validateHarnessUltraGraph(graph, ultraTestID, catalog) == nil {
						t.Fatal("Relay without a local VAE was allowed to dispatch")
					}
				}
			})
		}
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"cascade", func(g map[string]any) {
			g["4150"] = map[string]any{"class_type": "LCRelayVSRUpscale", "inputs": map[string]any{"image": []any{"3621", float64(0)}, "source": "image", "upscale": "4x", "seed": float64(42), "detail": .95}}
		}},
		{"extra-relay", func(g map[string]any) {
			g["9998"] = map[string]any{"class_type": "LCRelayVSRUpscale", "inputs": map[string]any{"image": []any{"3621", float64(0)}, "source": "image", "upscale": "4x", "seed": float64(42), "detail": .95}}
		}},
		{"huge-resize", func(g map[string]any) {
			g["9999"] = map[string]any{"class_type": "ImageScale", "inputs": map[string]any{"image": []any{"3621", float64(0)}, "upscale_method": "lanczos", "width": float64(16000), "height": float64(16000), "crop": "disabled"}}
		}},
		{"large-batch", func(g map[string]any) {
			g["3481"].(map[string]any)["inputs"].(map[string]any)["batch_size"] = float64(100)
		}},
		{"oversize-source-cap", func(g map[string]any) {
			g["3481"].(map[string]any)["inputs"].(map[string]any)["max_resolution"] = float64(16000)
		}},
		{"linked-vosr-scale", func(g map[string]any) {
			g["4046"].(map[string]any)["inputs"].(map[string]any)["upscale_by"] = []any{"3761", float64(0)}
		}},
		{"vosr-four", func(g map[string]any) { g["4047"].(map[string]any)["inputs"].(map[string]any)["value"] = "4x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph, catalog := ultraGraphFixture(t, "text")
			tc.change(graph)
			if validateHarnessUltraGraph(graph, ultraTestID, catalog) == nil {
				t.Fatal("unsafe allocator accepted")
			}
		})
	}
}

func TestHarnessUltraRelayOutputProfileIncludesOutpaint(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		width, height, scale int
		outpaint, valid      bool
	}{
		{"side-exact", 3072, 256, 4, false, true}, {"side-over", 3088, 256, 4, false, false},
		{"pixels-below", 1584, 1584, 4, false, true}, {"pixels-over", 1600, 1584, 4, false, false},
		{"two-allowed", 2048, 2048, 2, false, true}, {"four-rejected", 2048, 2048, 4, false, false},
		{"outpaint-below", 1312, 1584, 4, true, true}, {"outpaint-over", 1328, 1584, 4, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := "text"
			if tc.outpaint {
				mode = "outpaint"
			}
			graph, catalog := ultraGraphFixture(t, mode)
			for name, node := range ultraReadyRelayInfo() {
				if _, exists := catalog.ObjectInfo.Info[name]; !exists || name == "UNETLoader" || name == "VAELoader" {
					catalog.ObjectInfo.Info[name] = node
				}
			}
			original := graph["4046"].(map[string]any)["inputs"].(map[string]any)
			graph["4046"] = map[string]any{"class_type": "GatewayKreaProBypass", "inputs": map[string]any{"v0": original["pipe"], "v1": original["image"]}}
			graph["4150"] = map[string]any{"class_type": "LCRelayVSRUpscale", "inputs": map[string]any{"image": []any{"3621", float64(0)}, "source": "image", "upscale": fmt.Sprintf("%dx", tc.scale), "seed": float64(42), "detail": .95}}
			dims := graph["3481"].(map[string]any)["inputs"].(map[string]any)
			dims["custom_width"], dims["custom_height"] = float64(tc.width), float64(tc.height)
			for _, raw := range graph {
				if n := raw.(map[string]any); n["class_type"] == "ImageScale" {
					inputs := n["inputs"].(map[string]any)
					inputs["width"], inputs["height"] = float64(tc.width), float64(tc.height)
				}
			}
			err := validateHarnessUltraGraph(graph, ultraTestID, catalog)
			if (err == nil) != tc.valid {
				t.Fatalf("broker output guard: valid=%v err=%v", tc.valid, err)
			}
			if !tc.valid && !strings.Contains(err.Error(), "38,43") {
				t.Fatalf("rejected before final-output guard: %v", err)
			}
		})
	}
}

func TestHarnessUltraRejectsOtherWorkflowsAndPaths(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(g map[string]any) { g["4071"].(map[string]any)["inputs"].(map[string]any)["path"] = "../other" },
		func(g map[string]any) {
			g["3857"].(map[string]any)["inputs"].(map[string]any)["model_file"] = "other-model"
		},
		func(g map[string]any) {
			g["9999"] = map[string]any{"class_type": "ExecuteShell", "inputs": map[string]any{}}
		},
		func(g map[string]any) {
			g["3481"].(map[string]any)["inputs"].(map[string]any)["custom_width"] = float64(16000)
		},
	} {
		graph, catalog := ultraGraphFixture(t, "text")
		change(graph)
		if validateHarnessUltraGraph(graph, ultraTestID, catalog) == nil {
			t.Fatal("unsafe graph accepted")
		}
	}
}

func TestHarnessUltraBrokerRequiresOwnKeyAndCanonicalPath(t *testing.T) {
	upstream, _ := url.Parse("http://harness:3080")
	a := &App{cfg: Config{Harness: config.HarnessConfig{Upstream: upstream, UltraKey: strings.Repeat("u", 32)}}}
	for _, path := range []string{"catalog", "jobs/../catalog", "jobs/" + ultraTestID + "?secret=x"} {
		r := httptest.NewRequest(http.MethodGet, harnessUltraMount+path, nil)
		w := httptest.NewRecorder()
		a.harnessUltraBroker().ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatal("unauthenticated broker exposed", w.Code)
		}
	}
}
