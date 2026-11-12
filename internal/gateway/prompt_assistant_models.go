package gateway

import (
	"context"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/promptassistant"
)

var errAssistantModelUnknown = errors.New("выбранная модель ассистента недоступна")
var errAssistantModelDenied = errors.New("использование этой модели должен разрешить администратор")

type assistantModelChoice struct {
	ID, Label string
	Vision    bool
}

func (a *App) assistantModelChoices(ctx context.Context, userID int64) ([]assistantModelChoice, error) {
	choices := []assistantModelChoice{}
	if !a.cfg.PromptAssistantDurableJobs {
		return choices, nil
	}
	for _, model := range a.cfg.PromptAssistantModels {
		allowed, err := a.store.PromptAssistantModelAllowed(ctx, userID, model.ID)
		if err != nil {
			return nil, err
		}
		if allowed {
			choices = append(choices, assistantModelChoice{ID: model.ID, Label: model.Label, Vision: model.VisionModel != ""})
		}
	}
	return choices, nil
}

func (a *App) assistantCatalogModel(id string) (domain.PromptAssistantModel, bool) {
	for _, model := range a.cfg.PromptAssistantModels {
		if model.ID == id {
			return model, true
		}
	}
	return domain.PromptAssistantModel{}, false
}

func (a *App) promptAssistantForUser(ctx context.Context, userID int64, id string) (*promptassistant.Client, error) {
	if id == "" {
		return a.promptAssistant, nil
	}
	model, ok := a.assistantCatalogModel(id)
	if !ok {
		return nil, errAssistantModelUnknown
	}
	if a.store == nil {
		return nil, errAssistantModelDenied
	}
	allowed, err := a.store.PromptAssistantModelAllowed(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errAssistantModelDenied
	}
	return a.promptAssistant.ForModels(model.Model, model.VisionModel, time.Duration(model.TimeoutSeconds)*time.Second), nil
}

func (a *App) assistantSnapshotMatchesCatalog(snapshot promptAssistantJobSnapshot) bool {
	if snapshot.ModelID == "" {
		return true
	}
	model, ok := a.assistantCatalogModel(snapshot.ModelID)
	if !ok {
		return false
	}
	expected := model.Model
	if snapshot.Request.Mode == promptassistant.ModeTextToVideo && model.VisionModel != "" {
		expected = model.VisionModel
	}
	for _, reference := range snapshot.Request.References {
		if reference.Kind == "image" || reference.Kind == "video" && reference.Inspected {
			expected = model.VisionModel
			break
		}
	}
	return expected != "" && snapshot.Request.Model == expected
}

func (a *App) checkAssistantModelExecutor(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return inference.ProbeAssistantModelReadiness(ctx, a.cfg.InferenceWorkerURL)
}
