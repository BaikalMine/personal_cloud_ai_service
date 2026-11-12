package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"ai-access-gateway/internal/domain"
)

// The identity describes resolved attempts, not a dataset or a transient HTTP call.
func captionSeriesID(userID int64, datasetID string, jobs []domain.LoraCaptionJob) string {
	type attempt struct {
		ID      string
		Version int64
	}
	attempts := make([]attempt, 0, len(jobs))
	for _, job := range jobs {
		attempts = append(attempts, attempt{job.ID, job.RunVersion})
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].ID < attempts[j].ID })
	data, _ := json.Marshal(struct {
		Owner    int64
		Dataset  string
		Attempts []attempt
	}{userID, datasetID, attempts})
	hash := sha256.Sum256(data)
	return "caption-series-" + hex.EncodeToString(hash[:])
}

func ensureCaptionSeries(ctx context.Context, tx *sql.Tx, userID int64, datasetID string, jobs []domain.LoraCaptionJob) (string, error) {
	if len(jobs) == 0 {
		return "", nil
	}
	unique := make([]domain.LoraCaptionJob, 0, len(jobs))
	seen := make(map[string]bool)
	for _, job := range jobs {
		if !seen[job.ID] {
			unique = append(unique, job)
			seen[job.ID] = true
		}
	}
	id := captionSeriesID(userID, datasetID, unique)
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lora_caption_series WHERE id=$1 AND user_id=$2)`, id, userID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return id, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lora_caption_series WHERE user_id=$1 AND state<>'running' AND expires_at<=now()`, userID); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM lora_caption_series WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return "", err
	}
	if count >= domain.LoraCaptionMaxJobs {
		return "", ErrLoraCaptionQuota
	}
	name := "Описание кадров"
	if datasetID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT name FROM lora_datasets WHERE id=$1 AND user_id=$2`, datasetID, userID).Scan(&name); err != nil {
			return "", err
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Описание кадров"
	}
	name = truncateNotificationText(name, 200)
	if _, err := tx.ExecContext(ctx, `INSERT INTO lora_caption_series(id,user_id,dataset_id,name,total_count) VALUES($1,$2,NULLIF($3,''),$4,$5)`, id, userID, datasetID, name, len(unique)); err != nil {
		return "", err
	}
	for index, job := range unique {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lora_caption_series_members(series_id,job_id,source_job_id,run_version,image_id,position,state) VALUES($1,$2,$2,$3,$4,$5,$6)`, id, job.ID, job.RunVersion, job.ImageID, index+1, job.State); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `SELECT refresh_caption_series($1)`, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) LatestLoraCaptionSeries(ctx context.Context, userID int64, jobID string, version int64) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT m.series_id FROM lora_caption_series_members m JOIN lora_caption_series s ON s.id=m.series_id WHERE m.source_job_id=$1 AND m.run_version=$2 AND s.user_id=$3 ORDER BY s.created_at DESC,s.id DESC LIMIT 1`, jobID, version, userID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *Store) LoraCaptionSeries(ctx context.Context, userID int64, id string) (domain.LoraCaptionSeries, error) {
	series := domain.LoraCaptionSeries{Members: []domain.LoraCaptionSeriesMember{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return series, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT id,COALESCE(dataset_id,''),name,state,total_count,completed_count,failed_count,cancelled_count,unavailable_count,created_at,updated_at,finished_at,expires_at FROM lora_caption_series WHERE id=$1 AND user_id=$2`, id, userID).Scan(&series.ID, &series.DatasetID, &series.Name, &series.State, &series.TotalCount, &series.CompletedCount, &series.FailedCount, &series.CancelledCount, &series.UnavailableCount, &series.CreatedAt, &series.UpdatedAt, &series.FinishedAt, &series.ExpiresAt)
	if err != nil {
		return series, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.source_job_id,m.image_id,m.position,m.state,(j.id IS NOT NULL AND j.run_version=m.run_version) FROM lora_caption_series_members m LEFT JOIN lora_caption_jobs j ON j.id=m.job_id AND j.user_id=$2 WHERE m.series_id=$1 ORDER BY m.position`, id, userID)
	if err != nil {
		return series, err
	}
	defer rows.Close()
	for rows.Next() {
		var member domain.LoraCaptionSeriesMember
		if err := rows.Scan(&member.JobID, &member.ImageID, &member.Position, &member.State, &member.Available); err != nil {
			return series, err
		}
		series.Members = append(series.Members, member)
	}
	if err := rows.Err(); err != nil {
		return series, err
	}
	if err := rows.Close(); err != nil {
		return series, err
	}
	return series, tx.Commit()
}
