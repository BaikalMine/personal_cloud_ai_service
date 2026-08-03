package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"ai-access-gateway/internal/domain"
)

var (
	ErrComfyDataExists   = errors.New("ComfyUI userdata already exists")
	ErrComfyDataNotFound = errors.New("ComfyUI userdata not found")
	ErrComfyDataQuota    = errors.New("ComfyUI userdata quota exceeded")
)

func (s *Store) ComfySettings(ctx context.Context, userID int64) (json.RawMessage, error) {
	var settings []byte
	err := s.db.QueryRowContext(ctx, `SELECT settings FROM comfy_settings WHERE user_id=$1`, userID).Scan(&settings)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(settings), err
}

func (s *Store) MergeComfySettings(ctx context.Context, userID int64, patch json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO comfy_settings(user_id,settings) VALUES($1,$2::jsonb)
		ON CONFLICT(user_id) DO UPDATE
		SET settings=comfy_settings.settings || EXCLUDED.settings, updated_at=now()
	`, userID, []byte(patch))
	return err
}

func (s *Store) SetComfySetting(ctx context.Context, userID int64, key string, value json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO comfy_settings(user_id,settings) VALUES($1,jsonb_build_object($2,$3::jsonb))
		ON CONFLICT(user_id) DO UPDATE
		SET settings=jsonb_set(comfy_settings.settings,ARRAY[$2],$3::jsonb,true), updated_at=now()
	`, userID, key, []byte(value))
	return err
}

func (s *Store) ComfyUserData(ctx context.Context, userID int64, dataPath string) ([]byte, domain.ComfyUserDataEntry, error) {
	var payload []byte
	var entry domain.ComfyUserDataEntry
	err := s.db.QueryRowContext(ctx, `
		SELECT path,payload,size_bytes,created_at,modified_at
		FROM comfy_userdata WHERE user_id=$1 AND path=$2
	`, userID, dataPath).Scan(&entry.Path, &payload, &entry.Size, &entry.CreatedAt, &entry.ModifiedAt)
	return payload, entry, err
}

func (s *Store) ListComfyUserData(ctx context.Context, userID int64) ([]domain.ComfyUserDataEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT path,size_bytes,created_at,modified_at
		FROM comfy_userdata WHERE user_id=$1 ORDER BY path
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []domain.ComfyUserDataEntry
	for rows.Next() {
		var entry domain.ComfyUserDataEntry
		if err := rows.Scan(&entry.Path, &entry.Size, &entry.CreatedAt, &entry.ModifiedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) PutComfyUserData(ctx context.Context, userID int64, dataPath string, payload []byte, overwrite bool, quota int64) (domain.ComfyUserDataEntry, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	defer tx.Rollback()
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&lockedUserID); err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	var oldSize int64
	err = tx.QueryRowContext(ctx, `SELECT size_bytes FROM comfy_userdata WHERE user_id=$1 AND path=$2`, userID, dataPath).Scan(&oldSize)
	if err == nil && !overwrite {
		return domain.ComfyUserDataEntry{}, ErrComfyDataExists
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.ComfyUserDataEntry{}, err
	}
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM comfy_userdata WHERE user_id=$1`, userID).Scan(&used); err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	if used-oldSize+int64(len(payload)) > quota {
		return domain.ComfyUserDataEntry{}, ErrComfyDataQuota
	}
	var entry domain.ComfyUserDataEntry
	err = tx.QueryRowContext(ctx, `
		INSERT INTO comfy_userdata(user_id,path,payload,size_bytes) VALUES($1,$2,$3,$4)
		ON CONFLICT(user_id,path) DO UPDATE SET payload=EXCLUDED.payload,size_bytes=EXCLUDED.size_bytes,modified_at=now()
		RETURNING path,size_bytes,created_at,modified_at
	`, userID, dataPath, payload, len(payload)).Scan(&entry.Path, &entry.Size, &entry.CreatedAt, &entry.ModifiedAt)
	if err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	return entry, nil
}

func (s *Store) DeleteComfyUserData(ctx context.Context, userID int64, dataPath string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM comfy_userdata WHERE user_id=$1 AND path=$2`, userID, dataPath)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return ErrComfyDataNotFound
	}
	return err
}

func (s *Store) MoveComfyUserData(ctx context.Context, userID int64, source, destination string, overwrite bool) (domain.ComfyUserDataEntry, error) {
	if source == destination {
		_, entry, err := s.ComfyUserData(ctx, userID, source)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ComfyUserDataEntry{}, ErrComfyDataNotFound
		}
		return entry, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	defer tx.Rollback()
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&lockedUserID); err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM comfy_userdata WHERE user_id=$1 AND path=$2)`, userID, destination).Scan(&exists); err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	if exists && !overwrite {
		return domain.ComfyUserDataEntry{}, ErrComfyDataExists
	}
	if exists {
		if _, err := tx.ExecContext(ctx, `DELETE FROM comfy_userdata WHERE user_id=$1 AND path=$2`, userID, destination); err != nil {
			return domain.ComfyUserDataEntry{}, err
		}
	}
	var entry domain.ComfyUserDataEntry
	err = tx.QueryRowContext(ctx, `
		UPDATE comfy_userdata SET path=$3,modified_at=now()
		WHERE user_id=$1 AND path=$2
		RETURNING path,size_bytes,created_at,modified_at
	`, userID, source, destination).Scan(&entry.Path, &entry.Size, &entry.CreatedAt, &entry.ModifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ComfyUserDataEntry{}, ErrComfyDataNotFound
	}
	if err != nil {
		return domain.ComfyUserDataEntry{}, fmt.Errorf("move ComfyUI userdata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ComfyUserDataEntry{}, err
	}
	return entry, nil
}
