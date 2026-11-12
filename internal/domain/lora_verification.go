package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrLoraVerification = errors.New("invalid LoRA verification plan")

type LoraVerificationCase struct {
	Prompt string `json:"prompt"`
	Seed   string `json:"seed"`
}

type LoraVerificationPlan struct {
	Version int                    `json:"version"`
	Cases   []LoraVerificationCase `json:"cases"`
	Weights []string               `json:"weights"`
}

type LoraVerificationMemberSpec struct {
	Position     int    `json:"position"`
	CaseIndex    int    `json:"case_index"`
	VariantIndex int    `json:"variant_index"`
	Seed         int64  `json:"-"`
	Weight       string `json:"weight"`
}

func LoraVerificationWeight(value string) (string, bool) {
	value = strings.TrimSpace(value)
	weight, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(weight) || math.IsInf(weight, 0) || math.Round(weight*20) == 0 || weight < -4 || weight > 4 || math.Abs(weight*20-math.Round(weight*20)) > 1e-8 {
		return "", false
	}
	return strconv.FormatFloat(math.Round(weight*20)/20, 'f', -1, 64), true
}

func NormalizeLoraVerificationPlan(input LoraVerificationPlan) (LoraVerificationPlan, error) {
	plan := LoraVerificationPlan{Version: 1, Cases: []LoraVerificationCase{}, Weights: []string{}}
	if input.Version != 1 || len(input.Cases) < 1 || len(input.Cases) > 10 || len(input.Weights) < 1 || len(input.Weights) > 4 || len(input.Cases)*(len(input.Weights)+1) > 20 {
		return plan, ErrLoraVerification
	}
	seenWeights := map[string]bool{}
	for _, value := range input.Weights {
		weight, ok := LoraVerificationWeight(value)
		if !ok || seenWeights[weight] {
			return LoraVerificationPlan{}, ErrLoraVerification
		}
		seenWeights[weight] = true
		plan.Weights = append(plan.Weights, weight)
	}
	seenCases := map[LoraVerificationCase]bool{}
	for _, item := range input.Cases {
		item.Prompt = strings.TrimSpace(item.Prompt)
		item.Seed = strings.TrimSpace(item.Seed)
		seed, err := strconv.ParseInt(item.Seed, 10, 64)
		if err != nil || seed < 0 || seed > 1<<50 || item.Seed != strconv.FormatInt(seed, 10) || item.Prompt == "" || len(item.Prompt) > 64<<10 || !utf8.ValidString(item.Prompt) || strings.ContainsRune(item.Prompt, 0) || seenCases[item] {
			return LoraVerificationPlan{}, ErrLoraVerification
		}
		seenCases[item] = true
		plan.Cases = append(plan.Cases, item)
	}
	return plan, nil
}

func (plan LoraVerificationPlan) Members() ([]LoraVerificationMemberSpec, error) {
	normalized, err := NormalizeLoraVerificationPlan(plan)
	if err != nil {
		return nil, err
	}
	members := make([]LoraVerificationMemberSpec, 0, len(normalized.Cases)*(len(normalized.Weights)+1))
	for caseIndex, item := range normalized.Cases {
		seed, _ := strconv.ParseInt(item.Seed, 10, 64)
		for variantIndex, weight := range append([]string{"0"}, normalized.Weights...) {
			members = append(members, LoraVerificationMemberSpec{Position: len(members) + 1, CaseIndex: caseIndex, VariantIndex: variantIndex, Seed: seed, Weight: weight})
		}
	}
	return members, nil
}

func (plan LoraVerificationPlan) Digest() (string, error) {
	normalized, err := NormalizeLoraVerificationPlan(plan)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

type CreateLoraVerificationParams struct {
	TrainingJobID    int64
	TrainingPublicID string
	Family           string
	ProfileSHA256    string
	Artifact         LoraFileIdentity
	ModelID          string
	// Binds the full request, including common generation settings and inputs.
	RequestSHA256 string
	PlanCipher    []byte
	Members       []LoraVerificationMemberSpec
}

type LoraVerification struct {
	BatchID             int64
	BatchPublicID       string
	TrainingJobID       *int64
	TrainingPublicID    string
	TrainingName        string
	TrainingProvenance  *LoraTrainingProvenance
	DatasetSnapshotID   string
	DatasetSnapshotHash string
	Family              string
	ProfileSHA256       string
	Artifact            LoraFileIdentity
	ModelID             string
	RequestSHA256       string
	PlanCipher          []byte
	CreatedAt           time.Time
	Members             []LoraVerificationMember
}

type LoraVerificationMember struct {
	LoraVerificationMemberSpec
	JobID       *int64
	JobPublicID string
	State       string
}

func (verification *CreateLoraVerificationParams) Validate() error {
	if verification == nil || verification.TrainingJobID <= 0 || verification.TrainingPublicID == "" || len(verification.TrainingPublicID) > 96 ||
		verification.Family != "krea2" && verification.Family != "flux2-klein" || !provenanceHash(verification.ProfileSHA256) || !verification.Artifact.Valid() ||
		verification.ModelID == "" || len(verification.ModelID) > 3000 || !provenanceHash(verification.RequestSHA256) || len(verification.PlanCipher) == 0 || len(verification.PlanCipher) > 4<<20 || len(verification.Members) < 2 || len(verification.Members) > 20 {
		return ErrLoraVerification
	}
	variants := 0
	for _, member := range verification.Members {
		if member.CaseIndex != 0 {
			break
		}
		variants++
	}
	if variants < 2 || variants > 5 || len(verification.Members)%variants != 0 {
		return ErrLoraVerification
	}
	for index, member := range verification.Members {
		if member.Position != index+1 || member.CaseIndex != index/variants || member.VariantIndex != index%variants || member.Seed < 0 || member.Seed > 1<<50 || member.Seed != verification.Members[member.CaseIndex*variants].Seed {
			return ErrLoraVerification
		}
		if member.VariantIndex == 0 {
			if member.Weight != "0" {
				return ErrLoraVerification
			}
			continue
		}
		weight, ok := LoraVerificationWeight(member.Weight)
		if !ok || weight != member.Weight || weight != verification.Members[member.VariantIndex].Weight {
			return ErrLoraVerification
		}
		for previous := 1; previous < member.VariantIndex; previous++ {
			if verification.Members[previous].Weight == weight {
				return ErrLoraVerification
			}
		}
	}
	return nil
}
