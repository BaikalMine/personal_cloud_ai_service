package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"
)

var ErrLoraTrainingRepeat = errors.New("invalid LoRA training repeat")

// A repeat pins the effective profile even when an old source has no file hashes.
// Recorded model/tuner identities, when present, are verified before GPU commands.
type LoraTrainingRepeatConstraint struct {
	Version       int                     `json:"version"`
	SourceJobID   string                  `json:"source_job_id"`
	ProfileSHA256 string                  `json:"profile_sha256"`
	Parameters    LoraExecutionParameters `json:"parameters"`
	Models        []LoraModelIdentity     `json:"models,omitempty"`
	Tuner         *LoraTunerIdentity      `json:"tuner,omitempty"`
}

func (c *LoraTrainingRepeatConstraint) Validate() error {
	if c == nil {
		return nil
	}
	p := c.Parameters
	if c.Version != 1 || !loraRepeatID(c.SourceJobID) || !provenanceHash(c.ProfileSHA256) ||
		p.Resolution <= 0 || p.MaxSteps <= 0 || p.NetworkDim <= 0 || p.NetworkAlpha <= 0 ||
		p.LearningRate <= 0 || math.IsNaN(p.LearningRate) || math.IsInf(p.LearningRate, 0) ||
		p.Seed < 0 || p.Seed > 1<<32-1 || p.SampleCount < 5 || p.SampleCount > 100 ||
		(len(c.Models) != 0 && len(c.Models) != 4) || (len(c.Models) == 0) != (c.Tuner == nil) {
		return ErrLoraTrainingRepeat
	}
	for i, m := range c.Models {
		if m.Role != []string{"base_source", "base_execution", "vae", "text_encoder"}[i] || !m.LoraFileIdentity.Valid() {
			return ErrLoraTrainingRepeat
		}
	}
	if t := c.Tuner; t != nil {
		if !provenanceHash(t.SourceSHA256) || t.SourceScope != "python-and-dependencies-v1" {
			return ErrLoraTrainingRepeat
		}
		if t.Revision == "" {
			if t.Dirty != nil {
				return ErrLoraTrainingRepeat
			}
		} else {
			if (len(t.Revision) != 40 && len(t.Revision) != 64) || t.Dirty == nil {
				return ErrLoraTrainingRepeat
			}
			if _, err := hex.DecodeString(t.Revision); err != nil {
				return ErrLoraTrainingRepeat
			}
		}
	}
	return nil
}

func (c *LoraTrainingRepeatConstraint) Clone() *LoraTrainingRepeatConstraint {
	if c == nil {
		return nil
	}
	copy := *c
	copy.Models = append([]LoraModelIdentity(nil), c.Models...)
	if c.Tuner != nil {
		tuner := *c.Tuner
		if tuner.Dirty != nil {
			dirty := *tuner.Dirty
			tuner.Dirty = &dirty
		}
		copy.Tuner = &tuner
	}
	return &copy
}

func loraRepeatID(id string) bool {
	if len(id) < 16 || len(id) > 96 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func NewLoraTrainingRepeatConstraint(source LoraTrainingJob, profileHash string) (*LoraTrainingRepeatConstraint, error) {
	if !source.State.Terminal() || !loraRepeatID(source.DatasetSnapshotID) || !provenanceHash(source.DatasetSnapshotHash) {
		return nil, ErrLoraTrainingRepeat
	}
	params := LoraExecutionParameters{Resolution: source.Resolution, MaxSteps: source.MaxTrainSteps,
		NetworkDim: source.NetworkDim, NetworkAlpha: source.NetworkAlpha, LearningRate: source.LearningRate,
		Seed: source.Seed, SampleCount: source.SampleCount}
	if params.Seed > 1<<32-1 {
		params.Seed %= 1 << 32
	}
	result := &LoraTrainingRepeatConstraint{Version: 1, SourceJobID: source.PublicID, ProfileSHA256: profileHash, Parameters: params}
	if p := source.Provenance.Clone(); p != nil {
		if p.Validate() != nil || p.GatewayJobID != source.PublicID || p.Profile.ID != source.ProfileID ||
			p.Profile.Family != source.Family || p.Profile.BaseModel != source.BaseModel || p.ProfileSHA256 != profileHash || p.Parameters.SampleCount != source.SampleCount {
			return nil, ErrLoraTrainingRepeat
		}
		result.Parameters, result.Models, result.Tuner = p.Parameters, p.Models, &p.Tuner
	}
	return result, result.Validate()
}

// Fingerprint only the immutable source contract, not spool paths or progress.
func LoraTrainingRepeatSourceHash(source LoraTrainingJob) string {
	data, _ := json.Marshal(struct {
		PublicID, ProfileID, Family, BaseModel, Name, OutputName, TriggerWord, ConceptType, Preset, SnapshotID, SnapshotHash string
		UserID                                                                                                               *int64
		Parameters                                                                                                           LoraExecutionParameters
		Provenance                                                                                                           *LoraTrainingProvenance
	}{source.PublicID, source.ProfileID, source.Family, source.BaseModel, source.Name, source.OutputName,
		source.TriggerWord, source.ConceptType, source.Preset, source.DatasetSnapshotID, source.DatasetSnapshotHash, source.UserID,
		LoraExecutionParameters{Resolution: source.Resolution, MaxSteps: source.MaxTrainSteps, NetworkDim: source.NetworkDim,
			NetworkAlpha: source.NetworkAlpha, LearningRate: source.LearningRate, Seed: source.Seed, SampleCount: source.SampleCount}, source.Provenance})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func LoraTrainingRepeatRequestHash(sourceHash string, constraint *LoraTrainingRepeatConstraint) (string, error) {
	if !provenanceHash(sourceHash) || constraint == nil || constraint.Validate() != nil {
		return "", ErrLoraTrainingRepeat
	}
	data, err := json.Marshal(struct {
		Source     string
		Constraint *LoraTrainingRepeatConstraint
	}{sourceHash, constraint})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func LoraTrainingRepeatJob(source LoraTrainingJob, c *LoraTrainingRepeatConstraint) CreateLoraTrainingJobParams {
	p := c.Parameters
	return CreateLoraTrainingJobParams{ProfileID: source.ProfileID, Family: source.Family, BaseModel: source.BaseModel,
		Name: source.Name, OutputName: source.OutputName, TriggerWord: source.TriggerWord, ConceptType: source.ConceptType, Preset: source.Preset,
		Resolution: p.Resolution, MaxTrainSteps: p.MaxSteps, NetworkDim: p.NetworkDim, NetworkAlpha: p.NetworkAlpha,
		LearningRate: p.LearningRate, Seed: p.Seed, SampleCount: p.SampleCount,
		DatasetSnapshotID: source.DatasetSnapshotID, DatasetSnapshotHash: source.DatasetSnapshotHash}
}

func LoraTrainingRepeatMatchesSource(source LoraTrainingJob, c *LoraTrainingRepeatConstraint, params CreateLoraTrainingJobParams) bool {
	if c == nil {
		return false
	}
	expected, err := NewLoraTrainingRepeatConstraint(source, c.ProfileSHA256)
	if err != nil || !reflect.DeepEqual(c, expected) {
		return false
	}
	want := LoraTrainingRepeatJob(source, expected)
	want.PublicID, want.UserID, want.UsernameSnapshot, want.RequestID = params.PublicID, params.UserID, params.UsernameSnapshot, params.RequestID
	want.DatasetPath, want.DatasetBytes = params.DatasetPath, params.DatasetBytes
	return reflect.DeepEqual(params, want)
}

type CreateLoraTrainingRepeatParams struct {
	Job          CreateLoraTrainingJobParams
	SourceJobID  string
	SourceSHA256 string
	Constraint   *LoraTrainingRepeatConstraint
}

type LoraTrainingRepeatReceipt struct {
	UserID              int64
	RequestID           string
	RequestSHA256       string
	SourceJobID         *int64
	SourcePublicID      string
	SourceName          string
	SourceSHA256        string
	JobID               *int64
	JobPublicID         string
	DatasetSnapshotID   string
	DatasetSnapshotHash string
	Constraint          *LoraTrainingRepeatConstraint
	CreatedAt           time.Time
}
