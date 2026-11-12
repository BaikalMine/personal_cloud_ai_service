package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFlux2EditAgainstLiveComfySchema(t *testing.T) {
	if os.Getenv("FLUX2_LIVE_OBJECT_INFO") == "" {
		t.Skip("set FLUX2_LIVE_OBJECT_INFO=1 for read-only live schema validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8188/object_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ComfyUI object_info returned HTTP %d", response.StatusCode)
	}
	var info map[string]comfyNodeInfo
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	var checked int
	var failures []string
	for _, result := range buildWorkflowCompatibilityResults(catalog) {
		if result.DefinitionID != "image-to-image-flux2" || result.Status == workflowCompatibilityUnavailable {
			continue
		}
		checked++
		for _, issue := range result.Issues {
			failures = append(failures, fmt.Sprintf("%s: %s", result.Scenario, issue.Message))
		}
	}
	if checked == 0 {
		t.Fatal("no installed FLUX.2 edit model was available for validation")
	}
	if len(failures) > 0 {
		t.Fatal(strings.Join(failures, "\n"))
	}
	t.Logf("validated %d Flux2 edit scenarios against the live ComfyUI schema without submitting GPU work", checked)
}
