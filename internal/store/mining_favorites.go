package store

import (
	"context"
	"errors"
	"strings"
)

func MiningScriptKey(path string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "/", "\\"))
}

func (s *Store) MiningScriptFavorites(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.script_path FROM mining_script_favorites f
	 JOIN users u ON u.id=f.user_id WHERE f.user_id=$1 AND u.role='admin' ORDER BY f.created_at,f.path_key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return result, rows.Err()
}

func (s *Store) SetMiningScriptFavorite(ctx context.Context, userID int64, path string, enabled bool) error {
	path = strings.TrimSpace(path)
	if len(path) == 0 || len(path) > 1024 {
		return errors.New("invalid script path")
	}
	key := MiningScriptKey(path)
	if !enabled {
		_, err := s.db.ExecContext(ctx, `DELETE FROM mining_script_favorites f USING users u
		 WHERE f.user_id=$1 AND f.path_key=$2 AND u.id=f.user_id AND u.role='admin'`, userID, key)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mining_script_favorites(user_id,path_key,script_path)
	 SELECT id,$2,$3 FROM users WHERE id=$1 AND role='admin'
	 ON CONFLICT(user_id,path_key) DO UPDATE SET script_path=EXCLUDED.script_path`, userID, key, path)
	return err
}
