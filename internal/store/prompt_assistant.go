package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
)

func (s *Store) InsertPromptAssistantRun(ctx context.Context, run domain.PromptAssistantRunRecord) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO prompt_assistant_runs
			(content_event_id,user_id,correlation_id,mode,profile,model,status,latency_ms,prompt_tokens,completion_tokens,
			 total_duration_ms,load_duration_ms,eval_duration_ms,num_predict,timeout_ms,keep_alive,reference_count,error_code)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (content_event_id) DO NOTHING
		RETURNING id
	`, run.ContentEventID, run.UserID, run.CorrelationID, run.Mode, run.Profile, run.Model, run.Status,
		run.LatencyMS, run.PromptTokens, run.CompletionTokens, run.TotalDurationMS, run.LoadDurationMS,
		run.EvalDurationMS, run.NumPredict, run.TimeoutMS, run.KeepAlive, run.ReferenceCount, run.ErrorCode).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM prompt_assistant_runs WHERE content_event_id=$1`, run.ContentEventID).Scan(&id)
	}
	return id, err
}

func (s *Store) PromptAssistantEventMetadata(ctx context.Context, userID int64, correlationID string) (int64, []byte, error) {
	var id int64
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id,metadata_cipher
		FROM content_events
		WHERE user_id=$1 AND correlation_id=$2 AND service='ollama' AND kind='prompt_assistant' AND expires_at > now()
		ORDER BY created_at DESC,id DESC
		LIMIT 1
	`, userID, correlationID).Scan(&id, &metadata)
	return id, metadata, err
}

func (s *Store) SetPromptAssistantDecision(ctx context.Context, userID, contentEventID int64, decision string, metadataCipher []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE content_events
		SET metadata_cipher=$3
		WHERE id=$1 AND user_id=$2 AND service='ollama' AND kind='prompt_assistant' AND expires_at > now()
	`, contentEventID, userID, metadataCipher)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE prompt_assistant_runs
		SET decision=$2,decided_at=now()
		WHERE content_event_id=$1
	`, contentEventID, decision)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}
