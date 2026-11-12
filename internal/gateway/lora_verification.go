package gateway

import (
	"net/url"
	"strconv"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

type loraVerificationCandidateValues struct {
	Member domain.LoraVerificationMemberSpec
	Values url.Values
}

// Only the tested adapter varies inside a case. Required workflow adapters,
// common image inputs and all other recipe settings remain the same.
func buildLoraVerificationValues(base url.Values, plan domain.LoraVerificationPlan, reference string) ([]loraVerificationCandidateValues, error) {
	normalized, err := domain.NormalizeLoraVerificationPlan(plan)
	if err != nil {
		return nil, err
	}
	if _, ok := loratraining.ModelRelativeName(reference); !ok {
		return nil, domain.ErrLoraVerification
	}
	members, err := normalized.Members()
	if err != nil {
		return nil, err
	}
	candidates := make([]loraVerificationCandidateValues, 0, len(members))
	for _, member := range members {
		values := cloneGenerationValues(base)
		for key := range values {
			if strings.HasPrefix(key, "batch_") || strings.HasPrefix(key, "assistant_") || strings.HasPrefix(key, "lora_") || key == "client_request_id" || key == "csrf" || key == "parent_job_id" {
				values.Del(key)
			}
		}
		values.Set("assistant_enabled", "false")
		values.Set("loras_configured", "true")
		values.Set("positive_prompt", normalized.Cases[member.CaseIndex].Prompt)
		values.Set("seed", strconv.FormatInt(member.Seed, 10))
		for slot := 1; slot <= maxGenerationLoraSlots; slot++ {
			suffix := strconv.Itoa(slot)
			values.Set("lora_"+suffix, "")
			values.Set("lora_model_strength_"+suffix, "0")
			values.Set("lora_clip_strength_"+suffix, "0")
		}
		if member.VariantIndex > 0 {
			values.Set("lora_1", reference)
			values.Set("lora_model_strength_1", member.Weight)
			values.Set("lora_clip_strength_1", member.Weight)
		}
		candidates = append(candidates, loraVerificationCandidateValues{Member: member, Values: values})
	}
	return candidates, nil
}
