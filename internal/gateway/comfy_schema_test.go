package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

const testObjectInfoJSON = `{
  "FrameSource": {
    "input": {"required": {}},
    "output": ["IMAGE"],
    "display_name": "Frame source",
    "python_module": "test.nodes",
    "category": "test"
  },
  "RTXVideoSuperResolution": {
    "input": {
      "required": {
        "images": ["IMAGE"],
        "resize_type": ["COMFY_DYNAMICCOMBO_V3", {
          "default": "scale by multiplier",
          "options": [
            {"key": "scale by multiplier", "inputs": {"required": {"scale": ["FLOAT", {"default": 2, "min": 1, "max": 4}]}}},
            {"key": "target dimensions", "inputs": {"required": {"width": ["INT", {"min": 64}], "height": ["INT", {"min": 64}]}}}
          ]
        }],
        "quality": [["LOW", "MEDIUM", "HIGH", "ULTRA"], {"default": "ULTRA"}]
      }
    },
    "output": ["IMAGE"],
    "display_name": "RTX Video Super Resolution",
    "python_module": "comfy_extras.nodes_nvidia",
    "category": "image/upscaling"
  }
}`

func TestParseComfyDynamicComboSchema(t *testing.T) {
	info := decodeTestObjectInfo(t, testObjectInfoJSON)
	catalog := buildComfySchemaCatalog(info, time.Time{}, "fixture")
	resize := catalog.Nodes["RTXVideoSuperResolution"].Required["resize_type"]
	if resize.Type != "COMFY_DYNAMICCOMBO_V3" || len(resize.Choices) != 2 {
		t.Fatalf("resize schema = %#v", resize)
	}
	scale := resize.DynamicOptions["scale by multiplier"].Required["scale"]
	if scale.Type != "FLOAT" || scale.Min == nil || *scale.Min != 1 || scale.Max == nil || *scale.Max != 4 {
		t.Fatalf("scale schema = %#v", scale)
	}
	quality := catalog.Nodes["RTXVideoSuperResolution"].Required["quality"]
	if quality.Type != "COMBO" || len(quality.Choices) != 4 {
		t.Fatalf("quality schema = %#v", quality)
	}
}

func TestComfyObjectInfoCacheFallsBackOnlyWithinMaxStale(t *testing.T) {
	var requests atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object_info" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		requests.Add(1)
		if fail.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testObjectInfoJSON))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	app := &App{cfg: Config{ComfyUIUpstream: upstream, ComfyObjectInfoCacheTTL: 30 * time.Second, ComfyObjectInfoMaxStale: 2 * time.Minute}}
	cache := app.comfySchemaCache()
	cache.now = func() time.Time { return now }

	live, err := app.comfyObjectInfo(context.Background(), false)
	if err != nil || live.Source != comfyObjectInfoLive || requests.Load() != 1 || live.Fingerprint == "" {
		t.Fatalf("live snapshot = %#v, requests=%d, err=%v", live, requests.Load(), err)
	}
	now = now.Add(15 * time.Second)
	fresh, err := app.comfyObjectInfo(context.Background(), false)
	if err != nil || fresh.Source != comfyObjectInfoFreshCache || requests.Load() != 1 {
		t.Fatalf("fresh snapshot = %#v, requests=%d, err=%v", fresh, requests.Load(), err)
	}

	fail.Store(true)
	now = now.Add(20 * time.Second)
	stale, err := app.comfyObjectInfo(context.Background(), false)
	if err != nil || stale.Source != comfyObjectInfoLastKnownGood || stale.LastError == "" || requests.Load() != 2 {
		t.Fatalf("last-known-good snapshot = %#v, requests=%d, err=%v", stale, requests.Load(), err)
	}

	now = live.FetchedAt.Add(2*time.Minute + time.Nanosecond)
	expired, err := app.comfyObjectInfo(context.Background(), true)
	if err == nil || len(expired.Schema.Nodes) != 0 || requests.Load() != 3 {
		t.Fatalf("expired snapshot = %#v, requests=%d, err=%v", expired, requests.Load(), err)
	}
}

func decodeTestObjectInfo(t *testing.T, raw string) map[string]comfyNodeInfo {
	t.Helper()
	var info map[string]comfyNodeInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	return info
}
