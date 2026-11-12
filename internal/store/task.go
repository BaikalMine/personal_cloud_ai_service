package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
)

var ErrTaskQuery = errors.New("invalid task query or cursor")

func (s *Store) TaskRevision(ctx context.Context, userID int64) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM user_task_revisions WHERE user_id=$1),0)`, userID).Scan(&revision)
	return revision, err
}

type taskCursor struct {
	Version int       `json:"v"`
	Binding string    `json:"b"`
	Created time.Time `json:"t"`
	Kind    string    `json:"k"`
	ID      string    `json:"i"`
}

func normalizeTaskFilter(f domain.TaskFilter) (domain.TaskFilter, error) {
	f.Search = strings.TrimSpace(f.Search)
	if !utf8.ValidString(f.Search) || utf8.RuneCountInString(f.Search) > 200 || strings.ContainsRune(f.Search, 0) {
		return f, ErrTaskQuery
	}
	switch f.Kind {
	case "", "generation", "lora_training", "lora_caption", "lora_caption_series":
	default:
		return f, ErrTaskQuery
	}
	if f.State != "" && !domain.GenerationJobState(f.State).Valid() && !domain.LoraTrainingState(f.State).Valid() && !(f.Kind == "lora_caption_series" && f.State == "partial") {
		return f, ErrTaskQuery
	}
	for _, value := range []*time.Time{f.From, f.Before} {
		if value != nil && (value.IsZero() || value.Year() < 1 || value.Year() > 9999) {
			return f, ErrTaskQuery
		}
	}
	if f.From != nil {
		utc := f.From.UTC()
		f.From = &utc
	}
	if f.Before != nil {
		utc := f.Before.UTC()
		f.Before = &utc
	}
	if f.From != nil && f.Before != nil && !f.From.Before(*f.Before) {
		return f, ErrTaskQuery
	}
	return f, nil
}

func taskQueryBinding(scope domain.TaskScope, filter domain.TaskFilter) string {
	encoded, _ := json.Marshal(struct {
		Scope  domain.TaskScope
		Filter domain.TaskFilter
	}{scope, filter})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// Cursors are not credentials: every union branch independently enforces owner
// and permissions. The binding rejects accidental reuse with different filters.
func decodeTaskCursor(raw, binding string) (*taskCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 2048 {
		return nil, ErrTaskQuery
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrTaskQuery
	}
	var cursor taskCursor
	if json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Binding != binding || cursor.Created.IsZero() || cursor.Created.Year() < 1 || cursor.Created.Year() > 9999 ||
		cursor.ID == "" || len(cursor.ID) > 200 || strings.ContainsRune(cursor.ID, 0) {
		return nil, ErrTaskQuery
	}
	switch cursor.Kind {
	case "generation", "lora_training", "lora_caption", "lora_caption_series":
	default:
		return nil, ErrTaskQuery
	}
	return &cursor, nil
}

const taskProjectionSQL = `WITH tasks AS (
	SELECT 'generation'::text COLLATE "C" AS kind,j.public_id COLLATE "C" AS public_id,
		CASE j.template_id WHEN 'postprocess-image-v1' THEN 'Постобработка изображения' WHEN 'postprocess-video-v1' THEN 'Постобработка видео' ELSE j.model_name END AS name,j.state,''::text AS stage,j.status_message AS message,
		NULL::integer AS progress,j.cancellation_requested_at IS NOT NULL AND j.state NOT IN ('completed','failed','cancelled','expired') AS cancellation_pending,
		j.created_at,j.updated_at,j.finished_at,NULL::timestamptz AS expires_at,
		COALESCE(b.public_id,'') AS batch_id,CASE WHEN b.id IS NULL THEN 0 ELSE j.batch_position END AS batch_position,''::text AS dataset_id,''::text AS image_id,
		(COALESCE(octet_length(j.payload_cipher),0)>0 AND j.template_id NOT IN ('postprocess-image-v1','postprocess-video-v1')) AS source_available,false AS artifact_available,
		0 AS total_count,0 AS completed_count,0 AS failed_count,0 AS cancelled_count,0 AS unavailable_count
	FROM generation_jobs j LEFT JOIN generation_batches b ON b.id=j.batch_id AND b.user_id=j.user_id
	WHERE j.user_id=$1 AND $2
	UNION ALL
	SELECT 'lora_training',j.public_id COLLATE "C",j.name,j.state,j.stage,j.message,
		CASE WHEN j.state IN ('preparing','caching','running','installing','completed') THEN j.progress END,
		j.cancellation_requested_at IS NOT NULL AND j.state NOT IN ('completed','failed','cancelled'),j.created_at,j.updated_at,j.finished_at,NULL::timestamptz,
		'',0,'','',EXISTS(SELECT 1 FROM lora_dataset_snapshots s WHERE s.id=j.dataset_snapshot_id AND s.user_id=j.user_id),
		j.artifact_name<>'',0,0,0,0,0
	FROM lora_training_jobs j WHERE j.user_id=$1 AND $3
	UNION ALL
	SELECT 'lora_caption',j.id COLLATE "C",COALESCE(d.name,''),j.state,'',j.status,NULL::integer,
		j.cancel_requested AND j.state IN ('queued','running'),j.created_at,j.updated_at,NULL::timestamptz,j.expires_at,'',0,COALESCE(j.dataset_id,''),j.image_id,
		d.id IS NOT NULL,false,0,0,0,0,0
	FROM lora_caption_jobs j LEFT JOIN lora_datasets d ON d.id=j.dataset_id AND d.user_id=j.user_id
	WHERE j.user_id=$1 AND $3
	UNION ALL
	SELECT 'lora_caption_series',s.id COLLATE "C",s.name,s.state,'','',NULL::integer,
		false,s.created_at,s.updated_at,s.finished_at,s.expires_at,'',0,COALESCE(s.dataset_id,''),'',false,false,
		s.total_count,s.completed_count,s.failed_count,s.cancelled_count,s.unavailable_count
	FROM lora_caption_series s WHERE s.user_id=$1 AND $3 AND $4='lora_caption_series'
), filtered AS (
	SELECT * FROM tasks WHERE ($4='' OR kind=$4) AND ($5='' OR state=$5)
	AND ($6='' OR strpos(lower(name),lower($6))>0 OR strpos(lower(public_id),lower($6))>0)
	AND ($7::timestamptz IS NULL OR created_at >= $7)
	AND ($8::timestamptz IS NULL OR created_at < $8)
) `

// ListTasks reads the revision, total and page in one MVCC snapshot. Each new
// page is a fresh snapshot; immutable creation ordering avoids update duplicates.
func (s *Store) ListTasks(ctx context.Context, scope domain.TaskScope, filter domain.TaskFilter, limit int, rawCursor string) (domain.TaskPage, error) {
	page := domain.TaskPage{Tasks: []domain.Task{}}
	if scope.UserID <= 0 || limit < 1 || limit > 100 {
		return page, ErrTaskQuery
	}
	filter, err := normalizeTaskFilter(filter)
	if err != nil {
		return page, err
	}
	binding := taskQueryBinding(scope, filter)
	cursor, err := decodeTaskCursor(rawCursor, binding)
	if err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM user_task_revisions WHERE user_id=$1),0)`, scope.UserID).Scan(&page.Revision); err != nil {
		return page, err
	}
	args := []any{scope.UserID, scope.Generation, scope.Training, filter.Kind, filter.State, filter.Search, filter.From, filter.Before}
	if err = tx.QueryRowContext(ctx, taskProjectionSQL+`SELECT count(*) FROM filtered`, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	var created *time.Time
	kind, id := "", ""
	if cursor != nil {
		created, kind, id = &cursor.Created, cursor.Kind, cursor.ID
	}
	args = append(args, created, kind, id, limit+1)
	rows, err := tx.QueryContext(ctx, taskProjectionSQL+`SELECT * FROM filtered
		WHERE ($9::timestamptz IS NULL OR (created_at,kind,public_id)<($9,$10 COLLATE "C",$11 COLLATE "C"))
		ORDER BY created_at DESC,kind DESC,public_id DESC LIMIT $12`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var task domain.Task
		var summary domain.TaskSeriesSummary
		if err = rows.Scan(&task.Kind, &task.ID, &task.Name, &task.State, &task.Stage, &task.Message,
			&task.Progress, &task.CancellationPending, &task.CreatedAt, &task.UpdatedAt, &task.FinishedAt,
			&task.ExpiresAt, &task.BatchID, &task.BatchPosition, &task.DatasetID, &task.ImageID,
			&task.SourceAvailable, &task.ArtifactAvailable, &summary.Total, &summary.Completed,
			&summary.Failed, &summary.Cancelled, &summary.Unavailable); err != nil {
			return page, err
		}
		if task.Kind == "lora_caption_series" {
			task.Series = &summary
		}
		page.Tasks = append(page.Tasks, task)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if len(page.Tasks) > limit {
		page.Tasks = page.Tasks[:limit]
		last := page.Tasks[limit-1]
		encoded, err := json.Marshal(taskCursor{1, binding, last.CreatedAt, last.Kind, last.ID})
		if err != nil {
			return page, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	if err = loadTaskPreviews(ctx, tx, scope.UserID, page.Tasks); err != nil {
		return domain.TaskPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.TaskPage{}, err
	}
	return page, nil
}
