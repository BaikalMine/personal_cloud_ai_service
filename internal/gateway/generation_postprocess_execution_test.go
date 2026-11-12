package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func postprocessExecutionFixture(t *testing.T, video bool) (*App, domain.GenerationJob, postprocessDispatchPreparation) {
	t.Helper()
	a, q := postprocessQuoteFixture(t, video)
	job := domain.GenerationJob{ID: 999, PublicID: "postprocess-child-execution-0001", RequestID: q.RequestID,
		UserID: &q.UserID, TemplateID: q.TemplateID, WorkflowID: domain.PostprocessWorkflow}
	name := q.Namespace + "/postprocess-runtime.png"
	if video {
		name = q.Namespace + "/postprocess-runtime.mp4"
	}
	graph, _, err := buildPostprocessPrompt(postprocessMeasuredFacts(q.Sources[0].Measurement), q.Plan.Options, name, "", q.Namespace, postprocessTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	attachGenerationOutputPrefixes(graph, job.PublicID)
	p := postprocessDispatchPreparation{Quote: q, Prompt: graph, Inputs: []postprocessUploadedInput{{Source: q.Sources[0].Identity,
		InputID: "postprocess-input-test-0001", InputName: name, SizeBytes: q.Sources[0].Identity.SizeBytes,
		SHA256: q.Sources[0].Identity.SHA256, ReadAt: time.Now().UTC()}}}
	if video {
		observations := *q.Components
		request, err := generationComponentRequest(job.PublicID, graph)
		if err != nil {
			t.Fatal(err)
		}
		observations.RequestID, observations.RequestSHA256 = request.RequestID, request.Digest()
		p.FileObservations = &observations
	}
	return a, job, p
}

func TestPostprocessExecutionRoundTrip(t *testing.T) {
	for _, video := range []bool{false, true} {
		a, job, p := postprocessExecutionFixture(t, video)
		record, err := a.captureGenerationExecution(context.Background(), job, generationPreparation{Postprocess: &p, Prompt: p.Prompt})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := a.decodeGenerationExecution(job, record)
		if err != nil || snapshot.Postprocess == nil || snapshot.Postprocess.Inputs[0].InputName != p.Inputs[0].InputName {
			t.Fatal("execution roundtrip", err)
		}
		if video && (len(snapshot.Components) != 1 || snapshot.Components[0].HashState != "verified" || snapshot.Components[0].SHA256 == "") {
			t.Fatal("verified RIFE file missing from component evidence")
		}
		plain, _ := a.contentCipher.Decrypt(record.PayloadCipher)
		for _, forbidden := range []string{`"seed":`, `"sampler":`, `"resolved_input":`, `"model_family":`} {
			if strings.Contains(plain, forbidden) {
				t.Fatal("invented sampler input", forbidden)
			}
		}
		if !strings.Contains(string(snapshot.Graph), job.PublicID) || !strings.Contains(plain, `"media_id":"9007199254740997"`) {
			t.Fatal("output prefix or exact media ID missing")
		}
		job.TemplateID = "text-to-image"
		if _, err := a.decodeGenerationExecution(job, record); err == nil {
			t.Fatal("postprocess record interpreted as ordinary generation")
		}
	}
}

func TestPostprocessExecutionRejectsDrift(t *testing.T) {
	t.Run("different submitted graph", func(t *testing.T) {
		a, job, p := postprocessExecutionFixture(t, false)
		if _, err := a.captureGenerationExecution(context.Background(), job, generationPreparation{Postprocess: &p,
			Prompt: map[string]any{"1": map[string]any{"class_type": "KSampler", "inputs": map[string]any{}}}}); err == nil {
			t.Fatal("execution evidence ignored the actual submitted graph")
		}
	})
	for _, tc := range []struct {
		name string
		edit func(*generationExecutionSnapshot)
	}{
		{"version", func(s *generationExecutionSnapshot) { s.Version = 1 }},
		{"seed", func(s *generationExecutionSnapshot) { s.Seed = "0" }},
		{"family", func(s *generationExecutionSnapshot) { s.ModelFamily = "krea2" }},
		{"source", func(s *generationExecutionSnapshot) { s.Postprocess.Inputs[0].Source.MediaID++ }},
		{"input", func(s *generationExecutionSnapshot) { s.Postprocess.Inputs[0].InputName += "changed" }},
		{"input hash", func(s *generationExecutionSnapshot) { s.Postprocess.Inputs[0].SHA256 = strings.Repeat("b", 64) }},
		{"stale read", func(s *generationExecutionSnapshot) {
			s.Postprocess.Inputs[0].ReadAt = s.CapturedAt.Add(-2 * time.Minute)
		}},
		{"parent", func(s *generationExecutionSnapshot) { s.ParentJobID = "another-parent" }},
		{"sink", func(s *generationExecutionSnapshot) { s.Postprocess.OutputNode = "2" }},
		{"confirmation", func(s *generationExecutionSnapshot) { s.Postprocess.QuoteSHA256 = strings.Repeat("b", 64) }},
		{"graph", func(s *generationExecutionSnapshot) {
			s.Graph = json.RawMessage(`{"1":{"class_type":"KSampler","inputs":{}}}`)
			s.GraphSHA256 = generationEvidenceDigest(s.Graph)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, job, p := postprocessExecutionFixture(t, false)
			r, err := a.capturePostprocessExecution(job, p)
			if err != nil {
				t.Fatal(err)
			}
			s, err := a.decodeGenerationExecution(job, r)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&s)
			if err := validatePostprocessExecution(job, s); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}
