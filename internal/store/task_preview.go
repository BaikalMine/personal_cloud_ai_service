package store

import (
	"context"
	"database/sql"

	"ai-access-gateway/internal/domain"
	"github.com/lib/pq"
)

// Enrich only the selected page, inside ListTasks' snapshot. No per-row queries,
// decryption, tags, collection lookups or GPU polling belong in the list path.
func loadTaskPreviews(ctx context.Context, tx *sql.Tx, userID int64, tasks []domain.Task) error {
	generationIDs, captionIDs, trainingIDs := []string{}, []string{}, []string{}
	indices := make(map[string]int, len(tasks))
	for i, task := range tasks {
		indices[task.Kind+":"+task.ID] = i
		switch task.Kind {
		case "generation":
			generationIDs = append(generationIDs, task.ID)
		case "lora_caption":
			captionIDs = append(captionIDs, task.ID)
		case "lora_training":
			trainingIDs = append(trainingIDs, task.ID)
		}
	}
	if len(generationIDs)+len(captionIDs)+len(trainingIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT 'generation',j.public_id,p.id,''::text,''::text,''::text,p.media_type,j.template_id,p.sensitive,p.expires_at
		FROM generation_jobs j
		CROSS JOIN LATERAL (
			SELECT m.id,m.media_type,(e.is_sensitive OR m.visual_sensitivity_classified_at IS NULL) AS sensitive,
				LEAST(e.expires_at,m.expires_at) AS expires_at
			FROM content_events e JOIN content_media m ON m.event_id=e.id
			WHERE e.user_id=$1 AND e.service='comfyui' AND e.kind='comfyui_prompt'
				AND e.external_id=j.prompt_id AND j.prompt_id<>''
				AND e.expires_at>now() AND m.expires_at>now()
				AND m.profile_hidden_at IS NULL AND m.media_type IN ('image','video')
			ORDER BY (m.media_type='image') DESC,m.created_at DESC,m.id DESC LIMIT 1
		) p
		WHERE j.user_id=$1 AND j.public_id=ANY($2)
		UNION ALL
		SELECT 'lora_caption',j.id,0,a.id,'','','image','',false,j.expires_at
		FROM lora_caption_jobs j JOIN lora_dataset_assets a ON a.id=j.asset_id AND a.user_id=j.user_id
		WHERE j.user_id=$1 AND j.id=ANY($3) AND j.expires_at>now()
		UNION ALL
		SELECT 'lora_training',j.public_id,0,'',s.id,s.manifest_hash,'dataset','',false,now()+interval '5 minutes'
		FROM lora_training_jobs j JOIN lora_dataset_snapshots s ON s.id=j.dataset_snapshot_id AND s.user_id=j.user_id
		WHERE j.user_id=$1 AND j.public_id=ANY($4) AND j.dataset_snapshot_hash=s.manifest_hash AND s.image_count>0
	`, userID, pq.Array(generationIDs), pq.Array(captionIDs), pq.Array(trainingIDs))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id string
		var preview domain.TaskPreview
		if err := rows.Scan(&kind, &id, &preview.MediaID, &preview.AssetID, &preview.SnapshotID, &preview.SnapshotHash, &preview.MediaType, &preview.TemplateID, &preview.Sensitive, &preview.ExpiresAt); err != nil {
			return err
		}
		if index, ok := indices[kind+":"+id]; ok {
			tasks[index].Preview = &preview
		}
	}
	return rows.Err()
}

// GenerationTaskPreviews shares the task list's cover selection in one read,
// including pending sensitivity classification and retained-source identity.
func (s *Store) GenerationTaskPreviews(ctx context.Context, userID int64, ids []string) (map[string]domain.Task, error) {
	result := make(map[string]domain.Task)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	if userID <= 0 {
		return nil, sql.ErrNoRows
	}
	tasks := make([]domain.Task, len(ids))
	for i, id := range ids {
		tasks[i] = domain.Task{Kind: "generation", ID: id}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := loadTaskPreviews(ctx, tx, userID, tasks); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.Preview != nil {
			result[task.ID] = task
		}
	}
	return result, nil
}

// TaskPreview uses exactly the list's selection/retention rules, including on
// cache hits. Source-family permissions come from the authenticated adapter.
func (s *Store) TaskPreview(ctx context.Context, scope domain.TaskScope, kind, id string) (*domain.TaskPreview, error) {
	if scope.UserID <= 0 || kind == "generation" && !scope.Generation ||
		(kind == "lora_training" || kind == "lora_caption") && !scope.Training ||
		(kind != "generation" && kind != "lora_training" && kind != "lora_caption") {
		return nil, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	tasks := []domain.Task{{Kind: kind, ID: id}}
	if err := loadTaskPreviews(ctx, tx, scope.UserID, tasks); err != nil {
		return nil, err
	}
	if tasks[0].Preview == nil {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return tasks[0].Preview, nil
}

// The manifest provides ordering, while the relational references prove that
// the selected retained asset belongs to this exact owner's snapshot.
func (s *Store) TaskTrainingPreviewAsset(ctx context.Context, userID int64, snapshotID string, orderedIDs []string) (domain.LoraDatasetAsset, error) {
	if len(orderedIDs) == 0 || len(orderedIDs) > domain.LoraDatasetMaxImages {
		return domain.LoraDatasetAsset{}, sql.ErrNoRows
	}
	return scanDatasetAsset(s.db.QueryRowContext(ctx, `SELECT `+datasetAssetColumns+` FROM lora_dataset_assets a
		WHERE a.user_id=$1 AND a.id=ANY($3) AND EXISTS(
			SELECT 1 FROM lora_dataset_asset_refs r JOIN lora_dataset_snapshots s ON s.id=r.snapshot_id
			WHERE r.asset_id=a.id AND s.id=$2 AND s.user_id=$1)
		ORDER BY array_position($3::text[],a.id) LIMIT 1`, userID, snapshotID, pq.Array(orderedIDs)))
}
