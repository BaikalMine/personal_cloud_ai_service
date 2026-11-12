package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
)

const AdminLoraTrainingPageSize = 25

var ErrAdminLoraTrainingFilter = errors.New("invalid admin training filter")

type AdminLoraTrainingFilter struct {
	Search, State, Family, Sort string
	Offset                      int
}

func (f AdminLoraTrainingFilter) Normalize() (AdminLoraTrainingFilter, error) {
	f.Search = strings.TrimSpace(f.Search)
	if !utf8.ValidString(f.Search) || utf8.RuneCountInString(f.Search) > 120 || strings.ContainsRune(f.Search, 0) || f.Offset < 0 || f.Offset > 1000000000 {
		return f, ErrAdminLoraTrainingFilter
	}
	if f.State != "" && f.State != "active" && !domain.LoraTrainingState(f.State).Valid() {
		return f, ErrAdminLoraTrainingFilter
	}
	if f.Family != "" && f.Family != "krea2" && f.Family != "flux2-klein" {
		return f, ErrAdminLoraTrainingFilter
	}
	if f.Sort == "" {
		f.Sort = "active"
	}
	if f.Sort != "active" && f.Sort != "newest" && f.Sort != "oldest" && f.Sort != "artifact" {
		return f, ErrAdminLoraTrainingFilter
	}
	return f, nil
}

type AdminLoraTrainingPage struct {
	Filter                                          AdminLoraTrainingFilter
	Total, Active, Completed, Failed, ArtifactBytes int64
	Now                                             time.Time
	Jobs                                            []domain.LoraTrainingJob
}

const adminTrainingFrom = `FROM lora_training_jobs
 WHERE ($1='' OR strpos(lower(username_snapshot),lower($1))>0 OR strpos(lower(name),lower($1))>0
 OR strpos(lower(output_name),lower($1))>0 OR strpos(lower(profile_id),lower($1))>0
 OR strpos(lower(base_model),lower($1))>0 OR public_id=$1 OR user_id::text=$1)
 AND ($2='' OR state=$2 OR ($2='active' AND state NOT IN ('completed','failed','cancelled')))
 AND ($3='' OR family=$3)`

// Summary, page clamping and rows share a read-only snapshot. No fixed history
// window limits which runs can be found through the administrative list.
func (s *Store) ListAdminLoraTrainingPage(ctx context.Context, filter AdminLoraTrainingFilter) (AdminLoraTrainingPage, error) {
	var result AdminLoraTrainingPage
	f, err := filter.Normalize()
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	args := []any{f.Search, f.State, f.Family}
	err = tx.QueryRowContext(ctx, `SELECT count(*),
 count(*) FILTER (WHERE state NOT IN ('completed','failed','cancelled')),
 count(*) FILTER (WHERE state='completed'),count(*) FILTER (WHERE state='failed'),
 COALESCE(sum(artifact_bytes) FILTER (WHERE artifact_name<>''),0),now() `+adminTrainingFrom, args...).Scan(
		&result.Total, &result.Active, &result.Completed, &result.Failed, &result.ArtifactBytes, &result.Now)
	if err != nil {
		return result, err
	}
	if result.Total == 0 {
		f.Offset = 0
	} else if int64(f.Offset) >= result.Total {
		f.Offset = int((result.Total-1)/AdminLoraTrainingPageSize) * AdminLoraTrainingPageSize
	}
	result.Filter = f
	order := map[string]string{
		"active": "CASE WHEN state NOT IN ('completed','failed','cancelled') THEN 0 WHEN state='failed' THEN 1 ELSE 2 END,created_at DESC,id DESC",
		"newest": "created_at DESC,id DESC", "oldest": "created_at,id", "artifact": "artifact_bytes DESC,created_at DESC,id DESC",
	}[f.Sort]
	rows, err := tx.QueryContext(ctx, `SELECT `+loraTrainingColumns+` `+adminTrainingFrom+` ORDER BY `+order+` LIMIT $4 OFFSET $5`, append(args, AdminLoraTrainingPageSize, f.Offset)...)
	if err != nil {
		return result, err
	}
	result.Jobs = make([]domain.LoraTrainingJob, 0, AdminLoraTrainingPageSize)
	for rows.Next() {
		job, scanErr := scanLoraTrainingJob(rows)
		if scanErr != nil {
			rows.Close()
			return result, scanErr
		}
		result.Jobs = append(result.Jobs, job)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	return result, tx.Commit()
}
