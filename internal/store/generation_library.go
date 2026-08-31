package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const maxGenerationRecipes = 40

func (s *Store) SaveGenerationRecipe(ctx context.Context, userID int64, name, templateID, workflowID string, payloadCipher []byte) (domain.GenerationRecipeRow, error) {
	name = strings.TrimSpace(name)
	var recipe domain.GenerationRecipeRow
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO quick_generation_recipes (user_id,name,template_id,workflow_id,payload_cipher)
		SELECT $1,$2,$3,$4,$5
		WHERE (SELECT COUNT(*) FROM quick_generation_recipes WHERE user_id=$1) < $6
		RETURNING id,name,template_id,workflow_id,payload_cipher,created_at,updated_at
	`, userID, name, strings.TrimSpace(templateID), strings.TrimSpace(workflowID), payloadCipher, maxGenerationRecipes).Scan(
		&recipe.ID, &recipe.Name, &recipe.TemplateID, &recipe.WorkflowID, &recipe.PayloadCipher, &recipe.CreatedAt, &recipe.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GenerationRecipeRow{}, errors.New("достигнут лимит сохранённых наборов: 40")
	}
	return recipe, err
}

func (s *Store) ListGenerationRecipes(ctx context.Context, userID int64) ([]domain.GenerationRecipeRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,name,template_id,workflow_id,payload_cipher,created_at,updated_at
		FROM quick_generation_recipes WHERE user_id=$1 ORDER BY updated_at DESC,id DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationRecipeRow, 0)
	for rows.Next() {
		var item domain.GenerationRecipeRow
		if err := rows.Scan(&item.ID, &item.Name, &item.TemplateID, &item.WorkflowID, &item.PayloadCipher, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteGenerationRecipe(ctx context.Context, userID, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM quick_generation_recipes WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *Store) InsertGenerationVariant(ctx context.Context, userID int64, promptID, templateID, workflowID, modelName string, seed int64, payloadCipher []byte) error {
	return s.insertGenerationVariant(ctx, 0, userID, promptID, templateID, workflowID, modelName, seed, payloadCipher)
}

func (s *Store) InsertGenerationVariantForJob(ctx context.Context, jobID, userID int64, promptID, templateID, workflowID, modelName string, seed int64, payloadCipher []byte) error {
	if jobID <= 0 {
		return errors.New("generation job id is required")
	}
	return s.insertGenerationVariant(ctx, jobID, userID, promptID, templateID, workflowID, modelName, seed, payloadCipher)
}

func (s *Store) insertGenerationVariant(ctx context.Context, jobID, userID int64, promptID, templateID, workflowID, modelName string, seed int64, payloadCipher []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quick_generation_variants (user_id,prompt_id,template_id,workflow_id,model_name,seed,payload_cipher,job_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0)) ON CONFLICT (prompt_id) DO NOTHING
	`, userID, strings.TrimSpace(promptID), strings.TrimSpace(templateID), strings.TrimSpace(workflowID), strings.TrimSpace(modelName), seed, payloadCipher, jobID)
	return err
}

func (s *Store) SetGenerationVariantState(ctx context.Context, userID int64, promptID, state string) error {
	finished := state == "completed" || state == "error" || state == "cancelled"
	_, err := s.db.ExecContext(ctx, `
		UPDATE quick_generation_variants
		SET state=$3,
			state_changed_at=now(),
			finished_at=CASE WHEN $4 THEN COALESCE(finished_at,now()) ELSE finished_at END,
			error_message=CASE WHEN $3 IN ('completed','cancelled') THEN '' ELSE error_message END
		WHERE user_id=$1 AND prompt_id=$2
	`, userID, strings.TrimSpace(promptID), strings.TrimSpace(state), finished)
	return err
}

func (s *Store) SetGenerationVariantError(ctx context.Context, userID int64, promptID, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE quick_generation_variants
		SET state='error', state_changed_at=now(), finished_at=COALESCE(finished_at,now()), error_message=$3
		WHERE user_id=$1 AND prompt_id=$2
	`, userID, strings.TrimSpace(promptID), strings.TrimSpace(message))
	return err
}

func (s *Store) ListGenerationVariants(ctx context.Context, userID int64, limit int, finishedAfter time.Time) ([]domain.GenerationVariantRow, error) {
	if finishedAfter.IsZero() {
		return nil, errors.New("generation history boundary is required")
	}
	limit = boundedLimit(limit, 1, 60)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,user_id,prompt_id,template_id,workflow_id,model_name,seed,payload_cipher,state,created_at,finished_at,state_changed_at,error_message
		FROM quick_generation_variants
		WHERE user_id=$1
		  AND (state IN ('queued','running') OR COALESCE(finished_at,created_at) > $3)
		ORDER BY created_at DESC,id DESC LIMIT $2
	`, userID, limit, finishedAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationVariantRow, 0)
	for rows.Next() {
		var item domain.GenerationVariantRow
		if err := rows.Scan(&item.ID, &item.UserID, &item.PromptID, &item.TemplateID, &item.WorkflowID, &item.ModelName, &item.Seed, &item.PayloadCipher, &item.State, &item.CreatedAt, &item.FinishedAt, &item.StateChangedAt, &item.ErrorMessage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteExpiredGenerationVariants(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM quick_generation_variants
		WHERE state NOT IN ('queued','running') AND COALESCE(finished_at,created_at) <= $1
	`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ListActiveGenerationVariants(ctx context.Context, limit int) ([]domain.GenerationVariantRow, error) {
	limit = boundedLimit(limit, 1, 500)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,user_id,prompt_id,template_id,workflow_id,model_name,seed,payload_cipher,state,created_at,finished_at,state_changed_at,error_message
		FROM quick_generation_variants WHERE state IN ('queued','running') ORDER BY created_at ASC,id ASC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationVariantRow, 0)
	for rows.Next() {
		var item domain.GenerationVariantRow
		if err := rows.Scan(&item.ID, &item.UserID, &item.PromptID, &item.TemplateID, &item.WorkflowID, &item.ModelName, &item.Seed, &item.PayloadCipher, &item.State, &item.CreatedAt, &item.FinishedAt, &item.StateChangedAt, &item.ErrorMessage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GenerationAccessPolicy(ctx context.Context, userID int64) (domain.GenerationAccessPolicy, error) {
	policy := domain.GenerationAccessPolicy{UserID: userID}
	var presets, models, kreaGroups, fluxGroups []byte
	err := s.db.QueryRowContext(ctx, `SELECT preset_ids,model_ids,krea_lora_groups,flux_lora_groups FROM quick_generation_access_policies WHERE user_id=$1`, userID).Scan(&presets, &models, &kreaGroups, &fluxGroups)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return policy, err
	}
	if err := json.Unmarshal(presets, &policy.PresetIDs); err != nil {
		return policy, err
	}
	if err := json.Unmarshal(models, &policy.ModelIDs); err != nil {
		return policy, err
	}
	if err := json.Unmarshal(kreaGroups, &policy.KreaLoraGroups); err != nil {
		return policy, err
	}
	if err := json.Unmarshal(fluxGroups, &policy.FluxLoraGroups); err != nil {
		return policy, err
	}
	return policy, nil
}

func (s *Store) SaveGenerationAccessPolicy(ctx context.Context, policy domain.GenerationAccessPolicy) error {
	presets, err := json.Marshal(uniqueStrings(policy.PresetIDs))
	if err != nil {
		return err
	}
	models, err := json.Marshal(uniqueStrings(policy.ModelIDs))
	if err != nil {
		return err
	}
	krea, err := json.Marshal(uniqueStrings(policy.KreaLoraGroups))
	if err != nil {
		return err
	}
	flux, err := json.Marshal(uniqueStrings(policy.FluxLoraGroups))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO quick_generation_access_policies(user_id,preset_ids,model_ids,krea_lora_groups,flux_lora_groups)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(user_id) DO UPDATE SET preset_ids=EXCLUDED.preset_ids,model_ids=EXCLUDED.model_ids,krea_lora_groups=EXCLUDED.krea_lora_groups,flux_lora_groups=EXCLUDED.flux_lora_groups,updated_at=now()
	`, policy.UserID, presets, models, krea, flux)
	return err
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func (s *Store) ListGenerationVariantMedia(ctx context.Context, userID int64, promptID string) ([]domain.GenerationVariantMedia, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id,m.media_type,m.original_name,m.expires_at,e.is_sensitive,
		       (m.media_type='image' AND m.visual_sensitivity_classified_at IS NULL)
		FROM content_media m JOIN content_events e ON e.id=m.event_id
		WHERE e.user_id=$1 AND e.service='comfyui' AND e.kind='comfyui_prompt' AND e.external_id=$2
		  AND e.expires_at > now() AND m.expires_at > now() AND m.profile_hidden_at IS NULL
		ORDER BY m.created_at DESC,m.id DESC
	`, userID, strings.TrimSpace(promptID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.GenerationVariantMedia, 0)
	for rows.Next() {
		var item domain.GenerationVariantMedia
		if err := rows.Scan(&item.ID, &item.MediaType, &item.Filename, &item.ExpiresAt, &item.Sensitive, &item.VisualPending); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) AverageGenerationDuration(ctx context.Context) (time.Duration, error) {
	var seconds float64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (finished_at-created_at))),0)
		FROM quick_generation_variants
		WHERE state='completed' AND finished_at IS NOT NULL AND created_at >= now()-interval '14 days'
	`).Scan(&seconds)
	if err != nil {
		return 0, err
	}
	if seconds < 1 {
		return 0, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
