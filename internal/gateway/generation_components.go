package gateway

import (
	"context"
	"errors"
	"fmt"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
)

func generationComponentRequest(jobID string, prompt map[string]any) (componentinspection.Request, error) {
	request := componentinspection.Request{Version: 1, RequestID: jobID, References: []componentinspection.Reference{}}
	seen := map[string]bool{}
	for _, component := range generationExecutionComponents(prompt) {
		name, ok := componentinspection.NormalizeName(component.Name)
		ref := componentinspection.Reference{Kind: component.Kind, Name: name}
		if !ok || !ref.Valid() {
			return request, fmt.Errorf("Не поддерживается проверка файла %s", component.Name)
		}
		if !seen[ref.Key()] {
			request.References = append(request.References, ref)
			seen[ref.Key()] = true
		}
	}
	if !request.Valid() {
		return request, errors.New("Не удалось определить файлы компонентов генерации")
	}
	return request, nil
}

func (a *App) inspectGenerationPreparation(ctx context.Context, job domain.GenerationJob, preparation *generationPreparation) (bool, error) {
	return a.inspectGenerationPreparationRequired(ctx, job, preparation, false)
}

func (a *App) inspectGenerationPreparationRequired(ctx context.Context, job domain.GenerationJob, preparation *generationPreparation, required bool) (bool, error) {
	if !a.cfg.GenerationComponentInspectionEnabled && !required {
		return false, nil
	}
	request, err := generationComponentRequest(job.PublicID, preparation.Prompt)
	if err != nil {
		return false, err
	}
	if !a.loraTraining.Configured() {
		return true, nil
	}
	response, err := a.loraTraining.InspectGenerationComponents(ctx, request)
	if err != nil {
		return true, nil
	}
	if response.State == "pending" {
		return true, nil
	}
	for _, item := range response.Items {
		switch item.State {
		case "verified":
		case "unavailable", "unstable":
			return true, nil
		case "missing":
			return false, fmt.Errorf("Файл %s не найден в настроенных каталогах компонентов", item.Name)
		case "ambiguous":
			return false, fmt.Errorf("Файл %s найден в нескольких каталогах; требуется уточнить источник", item.Name)
		case "unconfigured":
			return false, fmt.Errorf("Для компонента %s не настроен каталог проверки на хосте", item.Name)
		case "too_large":
			return false, fmt.Errorf("Файл %s превышает допустимый размер проверки", item.Name)
		default:
			return true, nil
		}
	}
	preparation.FileObservations = &response
	return false, nil
}
