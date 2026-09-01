package gateway

import (
	"net/url"
	"reflect"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestParseGenerationBatchSpec(t *testing.T) {
	seedSpec, err := parseGenerationBatchSpec(url.Values{"batch_mode": {"seeds"}, "batch_count": {"4"}})
	if err != nil || seedSpec.Mode != domain.GenerationBatchSeeds || seedSpec.Count != 4 {
		t.Fatalf("seed batch spec=%+v err=%v", seedSpec, err)
	}
	parameterSpec, err := parseGenerationBatchSpec(url.Values{
		"batch_mode": {"parameter"}, "batch_count": {"3"}, "batch_parameter": {"cfg"},
		"batch_from": {"1.0"}, "batch_to": {"2.0"},
	})
	if err != nil || parameterSpec.Mode != domain.GenerationBatchParameter || parameterSpec.ParameterName != "cfg" || parameterSpec.From != 1 || parameterSpec.To != 2 {
		t.Fatalf("parameter batch spec=%+v err=%v", parameterSpec, err)
	}
	for name, values := range map[string]url.Values{
		"too small": {"batch_mode": {"seeds"}, "batch_count": {"1"}},
		"too large": {"batch_mode": {"seeds"}, "batch_count": {"21"}},
		"bad mode":  {"batch_mode": {"random"}, "batch_count": {"2"}},
		"no range":  {"batch_mode": {"parameter"}, "batch_count": {"2"}, "batch_parameter": {"cfg"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGenerationBatchSpec(values); err == nil {
				t.Fatal("invalid generation batch spec was accepted")
			}
		})
	}
}

func TestGenerationBatchParameterValues(t *testing.T) {
	values, err := generationBatchParameterValues(generationBatchSpec{Count: 3, From: 1, To: 2}, generationBatchParameterSpec{
		Label: "CFG", Minimum: 0, Maximum: 10, Step: 0.1,
	})
	if err != nil || !reflect.DeepEqual(values, []string{"1.0", "1.5", "2.0"}) {
		t.Fatalf("parameter values=%v err=%v", values, err)
	}
	if _, err := generationBatchParameterValues(generationBatchSpec{Count: 3, From: 5, To: 6}, generationBatchParameterSpec{
		Label: "Шаги", Minimum: 1, Maximum: 100, Step: 1, Integer: true,
	}); err == nil {
		t.Fatal("narrow integer range was accepted")
	}
	if _, err := generationBatchParameterValues(generationBatchSpec{Count: 2, From: -1, To: 2}, generationBatchParameterSpec{
		Label: "CFG", Minimum: 0, Maximum: 10, Step: 0.1,
	}); err == nil {
		t.Fatal("out-of-range values were accepted")
	}
}

func TestGenerationBatchSeeds(t *testing.T) {
	seeds, err := generationBatchSeeds(41, 4, false)
	if err != nil || !reflect.DeepEqual(seeds, []int64{41, 42, 43, 44}) {
		t.Fatalf("seeds=%v err=%v", seeds, err)
	}
}

func TestGenerationBatchParameterAvailability(t *testing.T) {
	krea := generationForm{TemplateID: "text-to-image", ModelFamily: modelFamilyKrea2}
	if _, ok := generationBatchParameter(krea, "cfg"); !ok {
		t.Fatal("Krea2 CFG is unavailable")
	}
	if _, ok := generationBatchParameter(krea, "video_steps"); ok {
		t.Fatal("video steps are available for Krea2")
	}
	flux := generationForm{TemplateID: "text-to-image", ModelFamily: modelFamilyFlux2}
	if _, ok := generationBatchParameter(flux, "flux_guidance"); !ok {
		t.Fatal("Flux guidance is unavailable")
	}
	video := generationForm{TemplateID: "minimax-h3-video", ModelFamily: modelFamilyMiniMaxH3}
	if _, ok := generationBatchParameter(video, "video_steps"); !ok {
		t.Fatal("MiniMax video steps are unavailable")
	}
	if _, ok := generationBatchParameter(video, "video_rife_multiplier"); ok {
		t.Fatal("RIFE multiplier is available while RIFE is disabled")
	}
	video.VideoRIFEEnabled = true
	if _, ok := generationBatchParameter(video, "video_rife_multiplier"); !ok {
		t.Fatal("RIFE multiplier is unavailable while RIFE is enabled")
	}
}

func TestGenerationBatchStateAndDifferences(t *testing.T) {
	batch := domain.GenerationBatch{Mode: domain.GenerationBatchParameter, ParameterName: "cfg", TotalCount: 3, DraftCount: 2, ActiveCount: 1}
	if state := generationBatchState(batch); state != "running" {
		t.Fatalf("running batch state=%q", state)
	}
	jobs := []generationJobView{
		{JobID: "job-1", BatchPosition: 1, ExperimentValue: "1.0"},
		{JobID: "job-2", BatchPosition: 2, ExperimentValue: "1.5"},
		{JobID: "job-3", BatchPosition: 3, ExperimentValue: "2.0"},
	}
	differences := generationBatchDifferences(batch, jobs)
	if len(differences) != 1 || differences[0].Name != "cfg" || len(differences[0].Values) != 3 {
		t.Fatalf("batch differences=%+v", differences)
	}
	batch.DraftCount = 0
	batch.ActiveCount = 0
	batch.CompletedCount = 2
	batch.FailedCount = 1
	if state := generationBatchState(batch); state != "partial" {
		t.Fatalf("partial batch state=%q", state)
	}
	jobs[1].ExperimentValue = "1.0"
	jobs[2].ExperimentValue = "1.0"
	if differences := generationBatchDifferences(batch, jobs); differences != nil {
		t.Fatalf("identical values were reported as differences: %+v", differences)
	}
}
