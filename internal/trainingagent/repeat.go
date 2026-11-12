package trainingagent

import (
	"errors"
	"reflect"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func validateRepeatSpec(spec loratraining.JobSpec, profile ProfileConfig) error {
	c := spec.Repeat
	if c == nil {
		return nil
	}
	p := domain.LoraExecutionParameters{Resolution: spec.Resolution, MaxSteps: spec.MaxSteps,
		NetworkDim: spec.NetworkDim, NetworkAlpha: spec.NetworkAlpha, LearningRate: spec.LearningRate,
		Seed: spec.Seed, SampleCount: spec.SampleCount}
	if c.Validate() != nil || c.ProfileSHA256 != profile.ExecutionIdentity().Hash() || c.Parameters != p {
		return errors.New("repeat training profile or parameters differ from confirmed source")
	}
	return nil
}

func verifyRepeatProvenance(c *domain.LoraTrainingRepeatConstraint, actual *domain.LoraTrainingProvenance) error {
	if c == nil {
		return nil
	}
	if c.Validate() != nil || actual == nil || actual.Validate() != nil ||
		c.ProfileSHA256 != actual.ProfileSHA256 || c.Parameters != actual.Parameters {
		return errors.New("repeat training execution differs from confirmed source")
	}
	if len(c.Models) != 0 && (!reflect.DeepEqual(c.Models, actual.Models) || !reflect.DeepEqual(c.Tuner, &actual.Tuner)) {
		return errors.New("repeat training model or tuner changed since source training")
	}
	return nil
}
