package gateway

import (
	"context"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
)

// generationAccessPolicy is deliberately allow-list based. A missing or empty
// policy means no extra restriction, keeping all existing accounts compatible.
func (a *App) generationAccessPolicy(ctx context.Context, user *User) (domain.GenerationAccessPolicy, error) {
	if user == nil {
		return domain.GenerationAccessPolicy{}, errors.New("не удалось определить пользователя")
	}
	if a.store == nil || user.Role == "admin" {
		return domain.GenerationAccessPolicy{UserID: user.ID}, nil
	}
	return a.store.GenerationAccessPolicy(ctx, user.ID)
}

func policyAllows(items []string, value string) bool {
	if len(items) == 0 {
		return true
	}
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func filterGenerationPresets(presets []generationPreset, policy domain.GenerationAccessPolicy) []generationPreset {
	result := append([]generationPreset(nil), presets...)
	for index := range result {
		if !policyAllows(policy.PresetIDs, result[index].ID) {
			result[index].Restricted = true
			result[index].Restriction = "Недоступно для вашей учётной записи"
		}
	}
	return result
}

func filterGenerationModels(models []generationModel, policy domain.GenerationAccessPolicy) []generationModel {
	if len(policy.ModelIDs) == 0 {
		return models
	}
	result := make([]generationModel, 0, len(models))
	for _, model := range models {
		if policyAllows(policy.ModelIDs, model.ID) {
			result = append(result, model)
		}
	}
	return result
}

func filterGenerationLoraGroups(groups []generationLoraGroup, allowed []string) []generationLoraGroup {
	if len(allowed) == 0 {
		return groups
	}
	result := make([]generationLoraGroup, 0, len(groups))
	for _, group := range groups {
		if policyAllows(allowed, group.Name) {
			result = append(result, group)
		}
	}
	return result
}

func loraBelongsToAllowedGroup(groups []generationLoraGroup, allowed []string, name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if len(allowed) == 0 {
		return generationLoraAllowed(groups, name)
	}
	for _, group := range groups {
		if !policyAllows(allowed, group.Name) {
			continue
		}
		for _, lora := range group.Loras {
			if lora.Name == name {
				return true
			}
		}
	}
	return false
}

func (a *App) assertGenerationPolicy(ctx context.Context, user *User, preset generationPreset, model generationModel, input generationForm, catalog generationModelCatalog) error {
	policy, err := a.generationAccessPolicy(ctx, user)
	if err != nil {
		return errors.New("не удалось проверить права быстрой генерации")
	}
	if !policyAllows(policy.PresetIDs, preset.ID) {
		return errors.New("этот workflow отключён администратором для вашей учётной записи")
	}
	if !policyAllows(policy.ModelIDs, model.ID) {
		return errors.New("эта диффузионная модель отключена администратором для вашей учётной записи")
	}
	for _, lora := range input.LoraNames {
		if model.Family == modelFamilyKrea2 && !loraBelongsToAllowedGroup(catalog.LoraGroups, policy.KreaLoraGroups, lora) {
			return errors.New("выбранная LoRA отключена администратором для вашей учётной записи")
		}
		if model.Family == modelFamilyFlux2 && !loraBelongsToAllowedGroup(catalog.FluxLoraGroups, policy.FluxLoraGroups, lora) {
			return errors.New("выбранная LoRA отключена администратором для вашей учётной записи")
		}
	}
	return nil
}
