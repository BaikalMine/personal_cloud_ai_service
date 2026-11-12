package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
)

var ErrPromptAssistantModelAccess = errors.New("prompt assistant model access denied")

// Empty means the default model, never unrestricted access to alternatives.
const promptAssistantModelAllowedSQL = `($3::text='' OR u.role='admin' OR EXISTS
 (SELECT 1 FROM prompt_assistant_model_grants g WHERE g.user_id=u.id AND g.model_id=$3))`

func (s *Store) PromptAssistantModelAllowed(ctx context.Context, userID int64, modelID string) (bool, error) {
	if modelID == "" {
		return true, nil
	}
	if !domain.ValidPromptAssistantModelID(modelID) {
		return false, nil
	}
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1
	 AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now())
	 AND (u.role='admin' OR EXISTS(SELECT 1 FROM prompt_assistant_model_grants g WHERE g.user_id=u.id AND g.model_id=$2)))`, userID, modelID).Scan(&allowed)
	return allowed, err
}

func (s *Store) PromptAssistantModelGrants(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model_id FROM prompt_assistant_model_grants WHERE user_id=$1 ORDER BY model_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// Serialize grant changes with enqueue/claim so revocation fences every request
// not yet claimed by the executor. Running work still requires completion proof.
func (s *Store) SetPromptAssistantModelGrant(ctx context.Context, actorID, userID int64, modelID string, grant bool) error {
	if actorID <= 0 || userID <= 0 || !domain.ValidPromptAssistantModelID(modelID) {
		return ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT DO NOTHING`, domain.GPUPrimaryResource); err != nil {
		return err
	}
	if _, err = scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource)); err != nil {
		return err
	}
	var actor int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND role='admin' AND NOT disabled
	 AND (account_expires_at IS NULL OR account_expires_at>now()) FOR SHARE`, actorID).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPromptAssistantModelAccess
	}
	if err != nil {
		return err
	}
	var target int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR KEY SHARE`, userID).Scan(&target); err != nil {
		return err
	}
	if grant {
		_, err = tx.ExecContext(ctx, `INSERT INTO prompt_assistant_model_grants(user_id,model_id,granted_by) VALUES($1,$2,$3)
		 ON CONFLICT(user_id,model_id) DO NOTHING`, userID, modelID, actorID)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM prompt_assistant_model_grants WHERE user_id=$1 AND model_id=$2`, userID, modelID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,target_type,target_id,metadata)
	 VALUES($1,'prompt_assistant_model_access_updated','user',$2,jsonb_build_object('model_id',$3::text,'granted',$4::boolean))`, actorID, userID, modelID, grant); err != nil {
		return err
	}
	return tx.Commit()
}
