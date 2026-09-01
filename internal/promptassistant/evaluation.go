package promptassistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const FixedEvaluationSuiteID = "prompt-assistant-fixed-v1"

type EvaluationCase struct {
	ID                   string
	Family               string
	Dimensions           []string
	Mode                 Mode
	Profile              Profile
	Prompt               string
	References           []ImageReference
	Video                VideoContext
	FixtureIDs           []string
	ExpectedReferenceIDs []string
	RequiredPromptTerms  [][]string
}

type EvaluationChecks struct {
	ValidPrompt          bool     `json:"valid_prompt"`
	ReferenceMap         bool     `json:"reference_map"`
	TermCoverage         float64  `json:"term_coverage"`
	MissingTermGroups    []string `json:"missing_term_groups,omitempty"`
	ExpectedReferenceIDs []string `json:"expected_reference_ids"`
}

type EvaluationCaseReport struct {
	ID         string                   `json:"id"`
	Family     string                   `json:"family"`
	Dimensions []string                 `json:"dimensions"`
	Status     string                   `json:"status"`
	Score      float64                  `json:"score"`
	LatencyMS  int64                    `json:"latency_ms"`
	Prompt     string                   `json:"prompt,omitempty"`
	References []ReferenceUnderstanding `json:"references"`
	Usage      Usage                    `json:"usage"`
	Policy     RequestPolicy            `json:"policy"`
	Checks     EvaluationChecks         `json:"checks"`
	Error      string                   `json:"error,omitempty"`
}

type EvaluationSummary struct {
	CaseCount        int     `json:"case_count"`
	PassedCount      int     `json:"passed_count"`
	MeanScore        float64 `json:"mean_score"`
	MeanLatencyMS    int64   `json:"mean_latency_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
}

type EvaluationCaseDelta struct {
	ID             string  `json:"id"`
	ScoreDelta     float64 `json:"score_delta"`
	LatencyDeltaMS int64   `json:"latency_delta_ms"`
	PassedBefore   bool    `json:"passed_before"`
	PassedNow      bool    `json:"passed_now"`
}

type EvaluationComparison struct {
	BaselineLabel  string                `json:"baseline_label"`
	CurrentLabel   string                `json:"current_label"`
	ScoreDelta     float64               `json:"score_delta"`
	LatencyDeltaMS int64                 `json:"latency_delta_ms"`
	PassedDelta    int                   `json:"passed_delta"`
	Cases          []EvaluationCaseDelta `json:"cases"`
}

type EvaluationReport struct {
	SchemaVersion    int                    `json:"schema_version"`
	SuiteID          string                 `json:"suite_id"`
	SuiteFingerprint string                 `json:"suite_fingerprint"`
	Label            string                 `json:"label"`
	Model            string                 `json:"model"`
	Think            bool                   `json:"think"`
	GeneratedAt      time.Time              `json:"generated_at"`
	Summary          EvaluationSummary      `json:"summary"`
	Cases            []EvaluationCaseReport `json:"cases"`
	Comparison       *EvaluationComparison  `json:"comparison,omitempty"`
}

// FixedEvaluationCases returns a deterministic, non-sensitive suite spanning
// every prompt-assistant dimension in the product roadmap. Raster references
// are generated in memory so the same suite runs on any host without downloads.
func FixedEvaluationCases() []EvaluationCase {
	return []EvaluationCase{
		{
			ID: "krea2-appearance", Family: "krea2", Dimensions: []string{"appearance"},
			Mode: ModeTextToImage, Profile: ProfilePhotographic,
			Prompt:              "Create a cinematic close-up portrait of an adult woman with a short auburn bob, green eyes, freckles across her nose, and a calm expression. Preserve every appearance detail.",
			RequiredPromptTerms: [][]string{{"auburn", "copper"}, {"green eyes", "emerald eyes"}, {"freckles"}},
		},
		{
			ID: "krea2-clothing", Family: "krea2", Dimensions: []string{"appearance", "clothing"},
			Mode: ModeImageToImage, Profile: ProfileRealistic,
			Prompt: "Keep the adult subject and composition from Picture 1. Replace only the outerwear with the mustard-yellow raincoat shown in Picture 2.",
			References: []ImageReference{
				evaluationReference(1, ImageReferenceBaseScene, "adult-subject-a", "ADULT SUBJECT A", "AUBURN BOB / GREEN EYES", color.RGBA{24, 70, 76, 255}, color.RGBA{177, 86, 48, 255}),
				evaluationReference(2, ImageReferenceWardrobeObject, "mustard-raincoat", "WARDROBE REFERENCE", "MUSTARD YELLOW RAINCOAT", color.RGBA{48, 53, 45, 255}, color.RGBA{218, 166, 47, 255}),
			},
			FixtureIDs: []string{"adult-subject-a", "mustard-raincoat"}, ExpectedReferenceIDs: []string{"Picture 1", "Picture 2"},
			RequiredPromptTerms: [][]string{{"mustard-yellow raincoat", "mustard yellow raincoat", "yellow raincoat"}, {"Picture 1", "<Picture 1>"}, {"Picture 2", "<Picture 2>"}},
		},
		{
			ID: "flux2-object", Family: "flux2", Dimensions: []string{"object"},
			Mode: ModeImageToImage, Profile: ProfileFluxEdit,
			Prompt: "Preserve Picture 1 as the base scene. Place the glossy red ceramic vase from Picture 2 on the wooden table without changing the room layout.",
			References: []ImageReference{
				evaluationReference(1, ImageReferenceBaseScene, "wooden-table-room", "BASE ROOM", "WOODEN TABLE / SOFT WINDOW LIGHT", color.RGBA{54, 64, 68, 255}, color.RGBA{128, 91, 57, 255}),
				evaluationReference(2, ImageReferenceWardrobeObject, "red-ceramic-vase", "OBJECT REFERENCE", "GLOSSY RED CERAMIC VASE", color.RGBA{49, 51, 55, 255}, color.RGBA{191, 48, 55, 255}),
			},
			FixtureIDs: []string{"wooden-table-room", "red-ceramic-vase"}, ExpectedReferenceIDs: []string{"Picture 1", "Picture 2"},
			RequiredPromptTerms: [][]string{{"red ceramic vase", "red vase"}, {"wooden table", "wood table"}},
		},
		{
			ID: "flux2-style-background", Family: "flux2", Dimensions: []string{"style", "background"},
			Mode: ModeImageToImage, Profile: ProfileFluxEdit,
			Prompt: "Keep the subject from Picture 1, render the result in the soft watercolor style of Picture 2, and use the bright botanical greenhouse from Picture 3 as the background.",
			References: []ImageReference{
				evaluationReference(1, ImageReferenceBaseScene, "neutral-subject-b", "BASE SUBJECT", "ADULT SUBJECT / NEUTRAL POSE", color.RGBA{54, 61, 66, 255}, color.RGBA{95, 151, 160, 255}),
				evaluationReference(2, ImageReferenceStyle, "soft-watercolor", "STYLE REFERENCE", "SOFT WATERCOLOR / PAPER TEXTURE", color.RGBA{224, 218, 203, 255}, color.RGBA{105, 145, 170, 255}),
				evaluationReference(3, ImageReferenceBackground, "bright-greenhouse", "BACKGROUND REFERENCE", "BRIGHT BOTANICAL GREENHOUSE", color.RGBA{39, 74, 57, 255}, color.RGBA{95, 183, 116, 255}),
			},
			FixtureIDs: []string{"neutral-subject-b", "soft-watercolor", "bright-greenhouse"}, ExpectedReferenceIDs: []string{"Picture 1", "Picture 2", "Picture 3"},
			RequiredPromptTerms: [][]string{{"watercolor"}, {"botanical greenhouse", "greenhouse"}},
		},
		{
			ID: "minimax-first-last-frame", Family: "minimax_h3", Dimensions: []string{"first_frame", "last_frame"},
			Mode: ModeTextToVideo, Profile: ProfileMiniMaxH3FL2VA,
			Prompt: "Create a smooth ten-second transition from the exact first frame in Picture 1 to Picture 2. The adult subject walks from the left side of the blue studio to the window and turns toward camera in the final frame.",
			References: []ImageReference{
				evaluationReference(1, ImageReferenceBaseScene, "blue-studio-first", "EXACT FIRST FRAME", "SUBJECT LEFT / BLUE STUDIO", color.RGBA{28, 57, 91, 255}, color.RGBA{83, 157, 205, 255}),
				evaluationReference(2, ImageReferenceBaseScene, "window-last", "EXACT LAST FRAME", "SUBJECT AT WINDOW / FACING CAMERA", color.RGBA{87, 62, 41, 255}, color.RGBA{229, 177, 98, 255}),
			},
			Video:      VideoContext{Mode: "frames", DurationSeconds: 10, ImageCount: 2},
			FixtureIDs: []string{"blue-studio-first", "window-last"}, ExpectedReferenceIDs: []string{"Picture 1", "Picture 2"},
			RequiredPromptTerms: [][]string{{"Picture 1", "<Picture 1>"}, {"Picture 2", "<Picture 2>"}, {"first frame", "opening frame"}, {"final frame", "last frame"}},
		},
		{
			ID: "minimax-sound", Family: "minimax_h3", Dimensions: []string{"sound"},
			Mode: ModeTextToVideo, Profile: ProfileMiniMaxH3REF2VA,
			Prompt: "An adult presenter delivers one calm whispered sentence. Use Audio 1 as the voice reference and keep lip movement synchronized with the spoken line.",
			Video:  VideoContext{Mode: "references", DurationSeconds: 10, AudioReference: true}, ExpectedReferenceIDs: []string{"Audio 1"},
			RequiredPromptTerms: [][]string{{"Audio 1", "<Audio 1>"}, {"whisper", "whispered"}, {"synchron", "lip-sync", "lip sync"}},
		},
		{
			ID: "minimax-motion", Family: "minimax_h3", Dimensions: []string{"motion"},
			Mode: ModeTextToVideo, Profile: ProfileMiniMaxH3REF2VA,
			Prompt: "Use Picture 1 for the adult subject and Video 1 only for motion timing. The subject performs the same measured turn and camera-relative movement while keeping the appearance from Picture 1.",
			References: []ImageReference{
				evaluationReference(1, ImageReferenceIdentity, "motion-subject", "IDENTITY REFERENCE", "ADULT SUBJECT / SILVER JACKET", color.RGBA{44, 48, 56, 255}, color.RGBA{174, 186, 194, 255}),
			},
			Video:      VideoContext{Mode: "references", DurationSeconds: 10, ImageCount: 1, VideoReference: true},
			FixtureIDs: []string{"motion-subject"}, ExpectedReferenceIDs: []string{"Picture 1", "Video 1"},
			RequiredPromptTerms: [][]string{{"Picture 1", "<Picture 1>"}, {"Video 1", "<Video 1>"}, {"motion", "movement"}, {"timing", "tempo"}},
		},
	}
}

func FixedEvaluationFingerprint(cases []EvaluationCase) string {
	type fingerprintCase struct {
		ID                   string
		Family               string
		Dimensions           []string
		Mode                 Mode
		Profile              Profile
		Prompt               string
		FixtureIDs           []string
		ExpectedReferenceIDs []string
		RequiredPromptTerms  [][]string
		Video                VideoContext
	}
	values := make([]fingerprintCase, 0, len(cases))
	for _, item := range cases {
		values = append(values, fingerprintCase{
			ID: item.ID, Family: item.Family, Dimensions: item.Dimensions, Mode: item.Mode, Profile: item.Profile,
			Prompt: item.Prompt, FixtureIDs: item.FixtureIDs, ExpectedReferenceIDs: item.ExpectedReferenceIDs,
			RequiredPromptTerms: item.RequiredPromptTerms, Video: item.Video,
		})
	}
	payload, _ := json.Marshal(values)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func RunFixedEvaluation(ctx context.Context, client *Client, model, label string, think bool, progress func(index, total int, caseID string)) EvaluationReport {
	cases := FixedEvaluationCases()
	report := EvaluationReport{
		SchemaVersion: 1, SuiteID: FixedEvaluationSuiteID, SuiteFingerprint: FixedEvaluationFingerprint(cases),
		Label: strings.TrimSpace(label), Model: strings.TrimSpace(model), Think: think, GeneratedAt: time.Now().UTC(),
		Cases: make([]EvaluationCaseReport, 0, len(cases)),
	}
	if report.Label == "" {
		report.Label = report.Model
	}
	for index, item := range cases {
		if progress != nil {
			progress(index+1, len(cases), item.ID)
		}
		started := time.Now()
		var result Result
		var err error
		if item.Mode == ModeTextToVideo {
			result, err = client.EnhanceVideoResult(ctx, item.Mode, item.Profile, item.Prompt, item.References, item.Video, think)
		} else {
			result, err = client.EnhanceResult(ctx, item.Mode, item.Profile, item.Prompt, item.References, think)
		}
		latency := time.Since(started).Milliseconds()
		caseReport := EvaluationCaseReport{
			ID: item.ID, Family: item.Family, Dimensions: append([]string(nil), item.Dimensions...),
			Status: "error", LatencyMS: latency, Prompt: result.Prompt, References: result.References,
			Usage: result.Usage, Policy: result.Policy,
		}
		if err != nil {
			caseReport.Error = err.Error()
		} else {
			caseReport.Checks, caseReport.Score = scoreEvaluationCase(item, result)
			caseReport.Status = "failed"
			if caseReport.Checks.ValidPrompt && caseReport.Checks.ReferenceMap && caseReport.Checks.TermCoverage == 1 {
				caseReport.Status = "passed"
			}
		}
		report.Cases = append(report.Cases, caseReport)
		for referenceIndex := range item.References {
			clear(item.References[referenceIndex].Image)
		}
	}
	report.Summary = summarizeEvaluation(report.Cases)
	return report
}

func CompareEvaluationReports(baseline, current EvaluationReport) (*EvaluationComparison, error) {
	if baseline.SuiteID == "" || baseline.SuiteID != current.SuiteID || baseline.SuiteFingerprint == "" || baseline.SuiteFingerprint != current.SuiteFingerprint {
		return nil, errors.New("eval reports use different fixed suites")
	}
	if len(baseline.Cases) != len(current.Cases) {
		return nil, errors.New("eval reports contain different case counts")
	}
	baselineCases := make(map[string]EvaluationCaseReport, len(baseline.Cases))
	for _, item := range baseline.Cases {
		baselineCases[item.ID] = item
	}
	comparison := &EvaluationComparison{
		BaselineLabel: baseline.Label, CurrentLabel: current.Label,
		ScoreDelta:     roundScore(current.Summary.MeanScore - baseline.Summary.MeanScore),
		LatencyDeltaMS: current.Summary.MeanLatencyMS - baseline.Summary.MeanLatencyMS,
		PassedDelta:    current.Summary.PassedCount - baseline.Summary.PassedCount,
		Cases:          make([]EvaluationCaseDelta, 0, len(current.Cases)),
	}
	for _, item := range current.Cases {
		previous, ok := baselineCases[item.ID]
		if !ok {
			return nil, fmt.Errorf("baseline is missing eval case %q", item.ID)
		}
		comparison.Cases = append(comparison.Cases, EvaluationCaseDelta{
			ID: item.ID, ScoreDelta: roundScore(item.Score - previous.Score),
			LatencyDeltaMS: item.LatencyMS - previous.LatencyMS,
			PassedBefore:   previous.Status == "passed", PassedNow: item.Status == "passed",
		})
	}
	return comparison, nil
}

func scoreEvaluationCase(item EvaluationCase, result Result) (EvaluationChecks, float64) {
	checks := EvaluationChecks{
		ValidPrompt:          strings.TrimSpace(result.Prompt) != "",
		ReferenceMap:         referenceIdentifiersEqual(item.ExpectedReferenceIDs, result.References),
		ExpectedReferenceIDs: append([]string(nil), item.ExpectedReferenceIDs...),
	}
	matched := 0
	for _, group := range item.RequiredPromptTerms {
		if containsAnyFold(result.Prompt, group) {
			matched++
			continue
		}
		checks.MissingTermGroups = append(checks.MissingTermGroups, strings.Join(group, " | "))
	}
	checks.TermCoverage = 1
	if len(item.RequiredPromptTerms) > 0 {
		checks.TermCoverage = float64(matched) / float64(len(item.RequiredPromptTerms))
	}
	score := 40 * checks.TermCoverage
	if checks.ValidPrompt {
		score += 25
	}
	if checks.ReferenceMap {
		score += 35
	}
	return checks, roundScore(score)
}

func summarizeEvaluation(cases []EvaluationCaseReport) EvaluationSummary {
	summary := EvaluationSummary{CaseCount: len(cases)}
	var totalScore float64
	var totalLatency int64
	for _, item := range cases {
		if item.Status == "passed" {
			summary.PassedCount++
		}
		totalScore += item.Score
		totalLatency += item.LatencyMS
		summary.PromptTokens += item.Usage.PromptTokens
		summary.CompletionTokens += item.Usage.CompletionTokens
	}
	if len(cases) > 0 {
		summary.MeanScore = roundScore(totalScore / float64(len(cases)))
		summary.MeanLatencyMS = totalLatency / int64(len(cases))
	}
	return summary
}

func referenceIdentifiersEqual(expected []string, actual []ReferenceUnderstanding) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index] != actual[index].Identifier {
			return false
		}
	}
	return true
}

func containsAnyFold(value string, candidates []string) bool {
	value = strings.ToLower(value)
	for _, candidate := range candidates {
		if strings.Contains(value, strings.ToLower(candidate)) {
			return true
		}
	}
	return false
}

func roundScore(value float64) float64 {
	return math.Round(value*100) / 100
}

func evaluationReference(number int, role ImageReferenceRole, fixtureID, title, detail string, background, accent color.RGBA) ImageReference {
	return ImageReference{Number: number, Role: role, MIMEType: "image/png", Image: evaluationFixturePNG(fixtureID, title, detail, background, accent)}
}

func evaluationFixturePNG(fixtureID, title, detail string, background, accent color.RGBA) []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, 512, 512))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(36, 36, 476, 118), image.NewUniform(color.RGBA{12, 18, 20, 220}), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(72, 162, 440, 398), image.NewUniform(color.RGBA{18, 24, 27, 155}), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(198, 184, 314, 300), image.NewUniform(accent), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(154, 300, 358, 380), image.NewUniform(accent), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(36, 426, 476, 476), image.NewUniform(color.RGBA{12, 18, 20, 220}), image.Point{}, draw.Src)
	drawer := &font.Drawer{Dst: canvas, Src: image.NewUniform(color.White), Face: basicfont.Face7x13}
	drawer.Dot = fixed.P(52, 69)
	drawer.DrawString(strings.ToUpper(title))
	drawer.Dot = fixed.P(52, 94)
	drawer.DrawString(strings.ToUpper(detail))
	drawer.Dot = fixed.P(52, 457)
	drawer.DrawString("FIXED EVAL: " + strings.ToUpper(fixtureID))
	var output bytes.Buffer
	_ = png.Encode(&output, canvas)
	return output.Bytes()
}
