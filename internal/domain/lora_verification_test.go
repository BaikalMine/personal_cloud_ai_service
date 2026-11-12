package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoraVerificationPlanLayoutAndExactSeeds(t *testing.T) {
	input := LoraVerificationPlan{Version: 1, Cases: []LoraVerificationCase{{Prompt: " scene one ", Seed: "1125899906842624"}, {Prompt: "scene two", Seed: "42"}}, Weights: []string{"0.50", "1.00"}}
	plan, err := NormalizeLoraVerificationPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cases[0].Prompt != "scene one" || plan.Weights[0] != "0.5" || input.Weights[0] != "0.50" {
		t.Fatal("normalization mutated input or failed", plan)
	}
	members, err := plan.Members()
	if err != nil || len(members) != 6 {
		t.Fatal(members, err)
	}
	for i, member := range members {
		if member.Position != i+1 || member.CaseIndex != i/3 || member.VariantIndex != i%3 {
			t.Fatal(member)
		}
		if i < 3 && member.Seed != 1<<50 {
			t.Fatal("seed precision lost")
		}
	}
	if members[0].Weight != "0" || members[1].Weight != "0.5" || members[2].Weight != "1" {
		t.Fatal(members)
	}
	first, _ := input.Digest()
	second, _ := plan.Digest()
	if first != second || len(first) != 64 {
		t.Fatal("digest not canonical")
	}
	another, _ := plan.Members()
	if !reflect.DeepEqual(members, another) {
		t.Fatal("nondeterministic layout")
	}
}

func TestLoraVerificationRejectsAmbiguousOrExcessivePlans(t *testing.T) {
	valid := func() LoraVerificationPlan {
		return LoraVerificationPlan{Version: 1, Cases: []LoraVerificationCase{{Prompt: "scene", Seed: "0"}}, Weights: []string{"1"}}
	}
	for name, mutate := range map[string]func(*LoraVerificationPlan){
		"version":                   func(p *LoraVerificationPlan) { p.Version = 0 },
		"empty":                     func(p *LoraVerificationPlan) { p.Cases = nil },
		"random seed":               func(p *LoraVerificationPlan) { p.Cases[0].Seed = "-1" },
		"float seed":                func(p *LoraVerificationPlan) { p.Cases[0].Seed = "1e3" },
		"noncanonical seed":         func(p *LoraVerificationPlan) { p.Cases[0].Seed = "01" },
		"overflow":                  func(p *LoraVerificationPlan) { p.Cases[0].Seed = "1125899906842625" },
		"empty prompt":              func(p *LoraVerificationPlan) { p.Cases[0].Prompt = " " },
		"oversized prompt":          func(p *LoraVerificationPlan) { p.Cases[0].Prompt = strings.Repeat("a", (64<<10)+1) },
		"duplicate case":            func(p *LoraVerificationPlan) { p.Cases = append(p.Cases, p.Cases[0]) },
		"duplicate weight":          func(p *LoraVerificationPlan) { p.Weights = []string{"1", "1.00"} },
		"baseline weight":           func(p *LoraVerificationPlan) { p.Weights = []string{"0"} },
		"rounded baseline":          func(p *LoraVerificationPlan) { p.Weights = []string{"0.000000000001"} },
		"negative rounded baseline": func(p *LoraVerificationPlan) { p.Weights = []string{"-0.000000000001"} },
		"nonfinite":                 func(p *LoraVerificationPlan) { p.Weights = []string{"NaN"} },
		"off step":                  func(p *LoraVerificationPlan) { p.Weights = []string{"0.123"} },
		"range":                     func(p *LoraVerificationPlan) { p.Weights = []string{"4.05"} },
		"count": func(p *LoraVerificationPlan) {
			p.Weights = []string{"0.25", "0.5", "1", "2"}
			p.Cases = []LoraVerificationCase{{"a", "1"}, {"b", "1"}, {"c", "1"}, {"d", "1"}, {"e", "1"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := valid()
			mutate(&p)
			if _, err := NormalizeLoraVerificationPlan(p); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}

func TestLoraVerificationRejectsInvalidMemberLayout(t *testing.T) {
	valid := func() *CreateLoraVerificationParams {
		plan := LoraVerificationPlan{Version: 1, Cases: []LoraVerificationCase{{"first", "42"}, {"second", "43"}}, Weights: []string{"-0.5", "1"}}
		members, err := plan.Members()
		if err != nil {
			t.Fatal(err)
		}
		return &CreateLoraVerificationParams{TrainingJobID: 1, TrainingPublicID: "training-fixture", Family: "krea2",
			ProfileSHA256: strings.Repeat("a", 64), Artifact: LoraFileIdentity{Name: "adapter.safetensors", Bytes: 1, SHA256: strings.Repeat("b", 64)},
			ModelID: "model", RequestSHA256: strings.Repeat("c", 64), PlanCipher: []byte("opaque"), Members: members}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CreateLoraVerificationParams){
		"no members":             func(p *CreateLoraVerificationParams) { p.Members = nil },
		"partial case":           func(p *CreateLoraVerificationParams) { p.Members = p.Members[:5] },
		"case outside layout":    func(p *CreateLoraVerificationParams) { p.Members[0].CaseIndex = 100 },
		"negative case":          func(p *CreateLoraVerificationParams) { p.Members[3].CaseIndex = -1 },
		"position":               func(p *CreateLoraVerificationParams) { p.Members[2].Position = 1 },
		"variant":                func(p *CreateLoraVerificationParams) { p.Members[2].VariantIndex = 100 },
		"seed mismatch":          func(p *CreateLoraVerificationParams) { p.Members[2].Seed++ },
		"random seed":            func(p *CreateLoraVerificationParams) { p.Members[0].Seed = -1 },
		"baseline":               func(p *CreateLoraVerificationParams) { p.Members[0].Weight = "1" },
		"noncanonical weight":    func(p *CreateLoraVerificationParams) { p.Members[1].Weight = "-0.50" },
		"duplicate weight":       func(p *CreateLoraVerificationParams) { p.Members[2].Weight = "-0.5" },
		"different case weights": func(p *CreateLoraVerificationParams) { p.Members[4].Weight = "0.75" },
		"missing cipher":         func(p *CreateLoraVerificationParams) { p.PlanCipher = nil },
		"bad digest":             func(p *CreateLoraVerificationParams) { p.RequestSHA256 = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid()
			mutate(p)
			if p.Validate() == nil {
				t.Fatal("invalid verification accepted")
			}
		})
	}
}
