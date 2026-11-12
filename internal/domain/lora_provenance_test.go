package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func provenanceFixture() *LoraTrainingProvenance {
	p := &LoraTrainingProvenance{Version: 1, GatewayJobID: "training-fixture", CapturedAt: time.Now().UTC(),
		Profile:        LoraExecutionProfile{ID: "krea-fixture", Family: "krea2", BaseModel: "base.safetensors"},
		Parameters:     LoraExecutionParameters{Resolution: 512, MaxSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 12, SampleCount: 2},
		Tuner:          LoraTunerIdentity{SourceScope: "python-and-dependencies-v1", SourceSHA256: strings.Repeat("a", 64)},
		DatasetArchive: LoraFileIdentity{Name: "dataset.zip", Bytes: 100, SHA256: strings.Repeat("b", 64)}}
	p.ProfileSHA256 = p.Profile.Hash()
	for _, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
		p.Models = append(p.Models, LoraModelIdentity{Role: role, LoraFileIdentity: LoraFileIdentity{Name: role + ".safetensors", Bytes: 50, SHA256: strings.Repeat("c", 64)}})
	}
	return p
}

func TestLoraProvenanceValidation(t *testing.T) {
	if err := provenanceFixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*LoraTrainingProvenance){
		"future version":                func(p *LoraTrainingProvenance) { p.Version++ },
		"wrong profile digest":          func(p *LoraTrainingProvenance) { p.Profile.FP8Base = true },
		"host path":                     func(p *LoraTrainingProvenance) { p.Models[0].Name = `C:\models\base.safetensors` },
		"missing component":             func(p *LoraTrainingProvenance) { p.Models = p.Models[:3] },
		"duplicate role":                func(p *LoraTrainingProvenance) { p.Models[2].Role = p.Models[1].Role },
		"fake digest":                   func(p *LoraTrainingProvenance) { p.Models[0].SHA256 = strings.Repeat("z", 64) },
		"unbounded seed":                func(p *LoraTrainingProvenance) { p.Parameters.Seed = 1 << 40 },
		"unlabelled scope":              func(p *LoraTrainingProvenance) { p.Tuner.SourceScope = "all" },
		"artifact without verification": func(p *LoraTrainingProvenance) { p.Artifact = &p.DatasetArchive },
		"unknown git cleanliness":       func(p *LoraTrainingProvenance) { p.Tuner.Revision = strings.Repeat("a", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			p := provenanceFixture()
			change(p)
			if !errors.Is(p.Validate(), ErrLoraProvenance) {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestLoraProvenanceMonotonicAndDetached(t *testing.T) {
	initial := provenanceFixture()
	completed := initial.Clone()
	now := initial.CapturedAt.Add(time.Minute)
	completed.VerifiedAt = &now
	completed.Artifact = &LoraFileIdentity{Name: "adapter.safetensors", Bytes: 5, SHA256: strings.Repeat("d", 64)}
	merged, err := MergeLoraProvenance(initial, completed)
	if err != nil || merged.Artifact == nil {
		t.Fatalf("completion: %+v %v", merged, err)
	}
	for _, stale := range []*LoraTrainingProvenance{nil, initial} {
		kept, err := MergeLoraProvenance(merged, stale)
		if err != nil || kept.Artifact == nil {
			t.Fatal("older response erased evidence", err)
		}
	}
	changed := completed.Clone()
	changed.Artifact.SHA256 = strings.Repeat("e", 64)
	if _, err := MergeLoraProvenance(completed, changed); !errors.Is(err, ErrLoraProvenance) {
		t.Fatal("adapter was replaced")
	}
	changed = completed.Clone()
	changed.Parameters.Seed++
	if _, err := MergeLoraProvenance(initial, changed); !errors.Is(err, ErrLoraProvenance) {
		t.Fatal("execution identity was replaced")
	}
	merged.Models[0].Name = "changed"
	merged.Artifact.Name = "changed"
	if initial.Models[0].Name == "changed" || completed.Artifact.Name == "changed" {
		t.Fatal("merge shares mutable evidence")
	}
}
