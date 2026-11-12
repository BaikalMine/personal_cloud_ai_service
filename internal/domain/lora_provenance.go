package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// These records contain public identities, never host paths or agent secrets.
type LoraFileIdentity struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type LoraModelIdentity struct {
	Role string `json:"role"`
	LoraFileIdentity
}

type LoraExecutionProfile struct {
	ID             string `json:"id"`
	Family         string `json:"family"`
	BaseModel      string `json:"base_model"`
	ModelVersion   string `json:"model_version,omitempty"`
	StripPrefix    string `json:"strip_prefix,omitempty"`
	BlocksToSwap   int    `json:"blocks_to_swap"`
	FP8Base        bool   `json:"fp8_base"`
	FP8TextEncoder bool   `json:"fp8_text_encoder"`
}

func (p LoraExecutionProfile) Hash() string {
	data, _ := json.Marshal(p)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type LoraExecutionParameters struct {
	Resolution   int     `json:"resolution"`
	MaxSteps     int     `json:"max_steps"`
	NetworkDim   int     `json:"network_dim"`
	NetworkAlpha int     `json:"network_alpha"`
	LearningRate float64 `json:"learning_rate"`
	Seed         int64   `json:"seed"`
	SampleCount  int     `json:"sample_count"`
}

type LoraTunerIdentity struct {
	SourceSHA256 string `json:"source_sha256"`
	SourceScope  string `json:"source_scope"`
	Revision     string `json:"revision,omitempty"`
	Dirty        *bool  `json:"dirty,omitempty"`
}

type LoraTrainingProvenance struct {
	Version        int                     `json:"version"`
	GatewayJobID   string                  `json:"gateway_job_id"`
	CapturedAt     time.Time               `json:"captured_at"`
	VerifiedAt     *time.Time              `json:"verified_at,omitempty"`
	Profile        LoraExecutionProfile    `json:"profile"`
	ProfileSHA256  string                  `json:"profile_sha256"`
	Parameters     LoraExecutionParameters `json:"parameters"`
	Models         []LoraModelIdentity     `json:"models"`
	Tuner          LoraTunerIdentity       `json:"tuner"`
	DatasetArchive LoraFileIdentity        `json:"dataset_archive"`
	Artifact       *LoraFileIdentity       `json:"artifact,omitempty"`
}

var ErrLoraProvenance = errors.New("invalid or conflicting LoRA execution provenance")

func provenanceText(value string, max int, required bool) bool {
	if (required && value == "") || len(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsAny(value, `/\:`) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func provenanceHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (f LoraFileIdentity) Valid() bool {
	return provenanceText(f.Name, 255, true) && f.Name != "." && f.Name != ".." && f.Bytes > 0 && provenanceHash(f.SHA256)
}

func (p *LoraTrainingProvenance) Validate() error {
	if p == nil {
		return nil
	}
	profile, params := p.Profile, p.Parameters
	if p.Version != 1 || !provenanceText(p.GatewayJobID, 96, true) || p.CapturedAt.IsZero() ||
		!provenanceText(profile.ID, 96, true) || (profile.Family != "krea2" && profile.Family != "flux2-klein") ||
		!provenanceText(profile.BaseModel, 255, true) || !provenanceText(profile.ModelVersion, 96, false) ||
		!provenanceText(profile.StripPrefix, 255, false) || profile.BlocksToSwap < 0 || profile.BlocksToSwap > 1000 ||
		p.ProfileSHA256 != profile.Hash() || len(p.Models) != 4 || !p.DatasetArchive.Valid() ||
		params.Resolution <= 0 || params.MaxSteps <= 0 || params.NetworkDim <= 0 || params.NetworkAlpha <= 0 ||
		params.LearningRate <= 0 || math.IsInf(params.LearningRate, 0) || math.IsNaN(params.LearningRate) ||
		params.Seed < 0 || params.Seed > 1<<32-1 || params.SampleCount < 1 ||
		!provenanceHash(p.Tuner.SourceSHA256) || p.Tuner.SourceScope != "python-and-dependencies-v1" {
		return ErrLoraProvenance
	}
	if p.Tuner.Revision != "" {
		if len(p.Tuner.Revision) != 40 && len(p.Tuner.Revision) != 64 {
			return ErrLoraProvenance
		}
		if _, err := hex.DecodeString(p.Tuner.Revision); err != nil || p.Tuner.Dirty == nil {
			return ErrLoraProvenance
		}
	} else if p.Tuner.Dirty != nil {
		return ErrLoraProvenance
	}
	for i, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
		if p.Models[i].Role != role || !p.Models[i].LoraFileIdentity.Valid() {
			return ErrLoraProvenance
		}
	}
	if (p.VerifiedAt == nil) != (p.Artifact == nil) || p.VerifiedAt != nil &&
		(p.VerifiedAt.Before(p.CapturedAt) || !p.Artifact.Valid()) {
		return ErrLoraProvenance
	}
	return nil
}

func (p *LoraTrainingProvenance) Clone() *LoraTrainingProvenance {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Models = append([]LoraModelIdentity(nil), p.Models...)
	if p.VerifiedAt != nil {
		value := *p.VerifiedAt
		copy.VerifiedAt = &value
	}
	if p.Artifact != nil {
		value := *p.Artifact
		copy.Artifact = &value
	}
	if p.Tuner.Dirty != nil {
		value := *p.Tuner.Dirty
		copy.Tuner.Dirty = &value
	}
	return &copy
}

// A missing/older agent response cannot erase evidence. Once captured, only the
// verified timestamp and final adapter may be added; completed evidence is fixed.
func MergeLoraProvenance(previous, next *LoraTrainingProvenance) (*LoraTrainingProvenance, error) {
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if previous == nil {
		return next.Clone(), nil
	}
	if next == nil {
		return previous.Clone(), nil
	}
	before, after := previous.Clone(), next.Clone()
	before.VerifiedAt, before.Artifact, after.VerifiedAt, after.Artifact = nil, nil, nil, nil
	if !reflect.DeepEqual(before, after) {
		return nil, ErrLoraProvenance
	}
	if previous.VerifiedAt != nil {
		if next.VerifiedAt != nil && !reflect.DeepEqual(previous, next) {
			return nil, ErrLoraProvenance
		}
		return previous.Clone(), nil
	}
	return next.Clone(), nil
}
