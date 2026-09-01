package promptassistant

import (
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFixedEvaluationCasesCoverProjectFamiliesAndDimensions(t *testing.T) {
	cases := FixedEvaluationCases()
	if len(cases) != 7 {
		t.Fatalf("fixed evaluation case count = %d, want 7", len(cases))
	}
	families := map[string]bool{}
	dimensions := map[string]bool{}
	caseIDs := map[string]bool{}
	for _, item := range cases {
		if caseIDs[item.ID] {
			t.Fatalf("duplicate evaluation case %q", item.ID)
		}
		caseIDs[item.ID] = true
		families[item.Family] = true
		for _, dimension := range item.Dimensions {
			dimensions[dimension] = true
		}
		expected := expectedReferenceMap(item.Mode, item.References, item.Video)
		if !referenceIdentifiersEqual(item.ExpectedReferenceIDs, expected) {
			t.Fatalf("case %q expected reference IDs do not match production mapping: expected=%v actual=%+v", item.ID, item.ExpectedReferenceIDs, expected)
		}
		for _, reference := range item.References {
			if reference.MIMEType != "image/png" || len(reference.Image) == 0 {
				t.Fatalf("case %q contains an empty visual fixture", item.ID)
			}
			image, err := png.Decode(strings.NewReader(string(reference.Image)))
			if err != nil || image.Bounds().Dx() != 512 || image.Bounds().Dy() != 512 {
				t.Fatalf("case %q fixture is not a 512px PNG: bounds=%v err=%v", item.ID, image.Bounds(), err)
			}
		}
	}
	for _, family := range []string{"krea2", "flux2", "minimax_h3"} {
		if !families[family] {
			t.Fatalf("fixed evaluation suite misses family %q", family)
		}
	}
	for _, dimension := range []string{"appearance", "clothing", "object", "style", "background", "first_frame", "last_frame", "sound", "motion"} {
		if !dimensions[dimension] {
			t.Fatalf("fixed evaluation suite misses dimension %q", dimension)
		}
	}
	if fingerprint := FixedEvaluationFingerprint(cases); len(fingerprint) != 64 {
		t.Fatalf("fixed evaluation fingerprint = %q", fingerprint)
	}
}

func TestScoreEvaluationCaseTracksTermsAndReferenceOrder(t *testing.T) {
	item := EvaluationCase{
		ExpectedReferenceIDs: []string{"Picture 1", "Picture 2"},
		RequiredPromptTerms:  [][]string{{"red coat", "crimson coat"}, {"greenhouse"}},
	}
	result := Result{
		Prompt: "Keep Picture 1 in a crimson coat inside the original room.",
		References: []ReferenceUnderstanding{
			{Identifier: "Picture 1"},
			{Identifier: "Picture 2"},
		},
	}
	checks, score := scoreEvaluationCase(item, result)
	if !checks.ValidPrompt || !checks.ReferenceMap || checks.TermCoverage != 0.5 || score != 80 {
		t.Fatalf("unexpected evaluation score: checks=%+v score=%.2f", checks, score)
	}
	result.References[0], result.References[1] = result.References[1], result.References[0]
	checks, score = scoreEvaluationCase(item, result)
	if checks.ReferenceMap || score != 45 {
		t.Fatalf("wrong reference order must reduce score: checks=%+v score=%.2f", checks, score)
	}
}

func TestCompareEvaluationReportsRequiresSameFixedSuite(t *testing.T) {
	baseline := EvaluationReport{
		SuiteID: FixedEvaluationSuiteID, SuiteFingerprint: "same", Label: "baseline",
		Summary: EvaluationSummary{MeanScore: 75, MeanLatencyMS: 2100, PassedCount: 1},
		Cases:   []EvaluationCaseReport{{ID: "case-a", Score: 75, LatencyMS: 2100, Status: "passed"}},
	}
	current := EvaluationReport{
		SuiteID: FixedEvaluationSuiteID, SuiteFingerprint: "same", Label: "candidate",
		Summary: EvaluationSummary{MeanScore: 90, MeanLatencyMS: 1800, PassedCount: 1},
		Cases:   []EvaluationCaseReport{{ID: "case-a", Score: 90, LatencyMS: 1800, Status: "passed"}},
	}
	comparison, err := CompareEvaluationReports(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.ScoreDelta != 15 || comparison.LatencyDeltaMS != -300 || comparison.PassedDelta != 0 || len(comparison.Cases) != 1 {
		t.Fatalf("unexpected evaluation comparison: %+v", comparison)
	}
	current.SuiteFingerprint = "different"
	if _, err := CompareEvaluationReports(baseline, current); err == nil {
		t.Fatal("expected fixed-suite mismatch error")
	}
}

func TestRunFixedEvaluationUsesProductionAssistantContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		const marker = "The references array must contain exactly these identifiers in this order: "
		start := strings.Index(request.Messages[0].Content, marker)
		if start < 0 {
			t.Fatal("structured reference contract is missing")
		}
		value := request.Messages[0].Content[start+len(marker):]
		value = strings.SplitN(value, ". Do not add", 2)[0]
		references := make([]map[string]string, 0, 4)
		if value != "none" {
			for _, identifier := range strings.Split(value, ", ") {
				references = append(references, map[string]string{
					"id": identifier, "summary": "A concrete fixed evaluation reference.", "use": "Use the assigned reference role.",
				})
			}
		}
		content, err := json.Marshal(map[string]any{"prompt": request.Messages[1].Content, "references": references})
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(chatResponse{
			Message: Message{Role: "assistant", Content: string(content)}, PromptEvalCount: 10, EvalCount: 20,
		}); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	progressCount := 0
	report := RunFixedEvaluation(context.Background(), NewClient(baseURL, "test:e4b"), "test:e4b", "candidate", false, func(_, _ int, _ string) {
		progressCount++
	})
	if progressCount != 7 || report.Summary.CaseCount != 7 || report.Summary.PassedCount != 7 || report.Summary.MeanScore != 100 {
		t.Fatalf("unexpected fixed evaluation report: progress=%d summary=%+v", progressCount, report.Summary)
	}
	if report.Summary.PromptTokens != 70 || report.Summary.CompletionTokens != 140 {
		t.Fatalf("evaluation usage was not aggregated: %+v", report.Summary)
	}
}
