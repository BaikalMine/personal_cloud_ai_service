package domain

import (
	"math"
	"strings"
	"testing"
)

func repeatSourceFixture() LoraTrainingJob {
	owner := int64(1)
	return LoraTrainingJob{PublicID: "repeat-source-0001", UserID: &owner, State: LoraTrainingCompleted,
		ProfileID: "krea-fixture", Family: "krea2", BaseModel: "base.safetensors", Name: "Fixture",
		OutputName: "fixture", TriggerWord: "fixture", ConceptType: "object", Preset: "balanced",
		Resolution: 768, MaxTrainSteps: 1234, NetworkDim: 16, NetworkAlpha: 8, LearningRate: 0.00012,
		Seed: 1<<40 + 19, SampleCount: 5, DatasetSnapshotID: "snapshot-fixture-01", DatasetSnapshotHash: strings.Repeat("a", 64)}
}

func TestRepeatRetainsExactRequestedOrExecutedParameters(t *testing.T) {
	source := repeatSourceFixture()
	profileHash := provenanceFixture().ProfileSHA256
	c, err := NewLoraTrainingRepeatConstraint(source, profileHash)
	if err != nil || c.Parameters.Seed != 19 || c.Parameters.MaxSteps != 1234 || c.Parameters.LearningRate != source.LearningRate || c.Tuner != nil {
		t.Fatalf("requested parameters changed: %+v %v", c, err)
	}
	p := provenanceFixture()
	p.GatewayJobID, p.Parameters.SampleCount = source.PublicID, 5
	source.Provenance = p
	c, err = NewLoraTrainingRepeatConstraint(source, profileHash)
	if err != nil || c.Parameters != p.Parameters || len(c.Models) != 4 {
		t.Fatalf("executed parameters lost: %+v %v", c, err)
	}
	p.Models[0].SHA256 = strings.Repeat("d", 64)
	if c.Models[0].SHA256 == p.Models[0].SHA256 {
		t.Fatal("constraint aliases source")
	}
}

func TestRepeatRejectsInvalidConstraintsAndChangedSources(t *testing.T) {
	source := repeatSourceFixture()
	c, err := NewLoraTrainingRepeatConstraint(source, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*LoraTrainingRepeatConstraint){
		"version":        func(c *LoraTrainingRepeatConstraint) { c.Version++ },
		"source":         func(c *LoraTrainingRepeatConstraint) { c.SourceJobID = "../bad" },
		"hash":           func(c *LoraTrainingRepeatConstraint) { c.ProfileSHA256 = "bad" },
		"seed":           func(c *LoraTrainingRepeatConstraint) { c.Parameters.Seed = 1 << 32 },
		"nan":            func(c *LoraTrainingRepeatConstraint) { c.Parameters.LearningRate = math.NaN() },
		"partial models": func(c *LoraTrainingRepeatConstraint) { c.Models = provenanceFixture().Models[:1] },
		"partial tuner":  func(c *LoraTrainingRepeatConstraint) { c.Tuner = &provenanceFixture().Tuner },
	} {
		t.Run(name, func(t *testing.T) {
			changed := c.Clone()
			edit(changed)
			if changed.Validate() == nil {
				t.Fatal("accepted")
			}
		})
	}
	params := LoraTrainingRepeatJob(source, c)
	params.PublicID, params.UserID, params.RequestID = "new-job-fixture-01", 1, "request-fixture-01"
	params.DatasetPath, params.DatasetBytes = "/private/new.zip", 100
	if !LoraTrainingRepeatMatchesSource(source, c, params) {
		t.Fatal("unchanged contract rejected")
	}
	params.MaxTrainSteps++
	if LoraTrainingRepeatMatchesSource(source, c, params) {
		t.Fatal("today's preset accepted")
	}
	before := LoraTrainingRepeatSourceHash(source)
	source.DatasetPath, source.Progress = "/other/spool.zip", 50
	if before != LoraTrainingRepeatSourceHash(source) {
		t.Fatal("mutable progress changed source hash")
	}
	source.DatasetSnapshotHash = strings.Repeat("c", 64)
	if before == LoraTrainingRepeatSourceHash(source) {
		t.Fatal("snapshot change ignored")
	}
	source.State = LoraTrainingQueued
	if _, err := NewLoraTrainingRepeatConstraint(source, c.ProfileSHA256); err == nil {
		t.Fatal("active source accepted")
	}
}
