package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrModelInstallConflict = errors.New("model install conflict")

type ModelInstall struct {
	ID        string          `json:"id"`
	OwnerID   int64           `json:"-"`
	ModelID   int64           `json:"model_id"`
	VersionID int64           `json:"version_id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Filename  string          `json:"filename"`
	SHA256    string          `json:"sha256"`
	Size      int64           `json:"size_bytes"`
	Request   json.RawMessage `json:"-"`
	State     string          `json:"state"`
	Progress  float64         `json:"progress"`
	Message   string          `json:"message"`
	CreatedAt time.Time       `json:"created_at"`
}

const modelInstallColumns = `id,coalesce(owner_id,0),model_id,version_id,kind,name,filename,sha256,size_bytes,request,state,progress,message,created_at`

func scanModelInstall(row interface{ Scan(...any) error }) (ModelInstall, error) {
	var j ModelInstall
	err := row.Scan(&j.ID, &j.OwnerID, &j.ModelID, &j.VersionID, &j.Kind, &j.Name, &j.Filename, &j.SHA256, &j.Size, &j.Request, &j.State, &j.Progress, &j.Message, &j.CreatedAt)
	return j, err
}
func (s *Store) EnqueueModelInstall(ctx context.Context, j ModelInstall) (ModelInstall, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	var actor int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND role='admin' AND NOT disabled AND (account_expires_at IS NULL OR account_expires_at>now()) FOR SHARE`, j.OwnerID).Scan(&actor); err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO model_install_requests(id,owner_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, j.ID, j.OwnerID); err != nil {
		return j, err
	}
	var owner int64
	var closed bool
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(owner_id,0),closed FROM model_install_requests WHERE id=$1 FOR UPDATE`, j.ID).Scan(&owner, &closed); err != nil {
		return j, err
	}
	if owner != j.OwnerID {
		return j, ErrModelInstallConflict
	}
	if closed {
		j.State = "cancelled"
		return j, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_install_jobs(id,owner_id,model_id,version_id,kind,name,filename,sha256,size_bytes,request) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, j.ID, j.OwnerID, j.ModelID, j.VersionID, j.Kind, j.Name, j.Filename, j.SHA256, j.Size, []byte(j.Request))
	if err != nil {
		return j, err
	}
	stored, err := scanModelInstall(tx.QueryRowContext(ctx, `SELECT `+modelInstallColumns+` FROM model_install_jobs WHERE id=$1`, j.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrModelInstallConflict
	}
	if err != nil {
		return j, err
	}
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT owner_id=$2 AND version_id=$3 AND request=$4::jsonb AND sha256=$5 FROM model_install_jobs WHERE id=$1`, j.ID, j.OwnerID, j.VersionID, []byte(j.Request), j.SHA256).Scan(&same)
	if err != nil {
		return j, err
	}
	if !same {
		return j, ErrModelInstallConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,target_type,metadata) SELECT $1,'model_install_requested','model',jsonb_build_object('job_id',$2::text,'version_id',$3::bigint) WHERE NOT EXISTS(SELECT 1 FROM audit_log WHERE action='model_install_requested' AND metadata->>'job_id'=$2)`, j.OwnerID, j.ID, j.VersionID); err != nil {
		return j, err
	}
	return stored, tx.Commit()
}
func (s *Store) ModelInstalls(ctx context.Context, active bool) ([]ModelInstall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+modelInstallColumns+` FROM model_install_jobs WHERE NOT $1 OR state IN ('running','reconciling') ORDER BY created_at DESC LIMIT 100`, active)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []ModelInstall{}
	for rows.Next() {
		j, err := scanModelInstall(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// The advisory-lock connection serializes downloads across Gateway processes.
// A claimed job is never re-submitted after an unknown transport outcome.
func (s *Store) ClaimModelInstall(ctx context.Context) (ModelInstall, func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return ModelInstall{}, nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(746218497)`)
		conn.Close()
	}
	var locked bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(746218497)`).Scan(&locked); err != nil || !locked {
		release()
		if err == nil {
			err = sql.ErrNoRows
		}
		return ModelInstall{}, nil, err
	}
	_, err = conn.ExecContext(ctx, `UPDATE model_install_jobs j SET state='failed',message='Права администратора отозваны.',updated_at=now() WHERE state='queued' AND NOT EXISTS(SELECT 1 FROM users u WHERE u.id=j.owner_id AND u.role='admin' AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now()))`)
	if err != nil {
		release()
		return ModelInstall{}, nil, err
	}
	j, err := scanModelInstall(conn.QueryRowContext(ctx, `UPDATE model_install_jobs SET state='running',updated_at=now() WHERE id=(SELECT id FROM model_install_jobs WHERE state='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+modelInstallColumns))
	if err != nil {
		release()
		return j, nil, err
	}
	return j, release, nil
}
func (s *Store) UpdateModelInstall(ctx context.Context, id, state, message string, progress float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE model_install_jobs SET state=$2,message=$3,progress=greatest(progress,$4),updated_at=now() WHERE id=$1 AND state IN ('running','reconciling') AND NOT (state='reconciling' AND $2='running')`, id, state, message, progress)
	return err
}
func (s *Store) CancelQueuedModelInstall(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE model_install_jobs SET state='cancelled',updated_at=now() WHERE id=$1 AND state='queued'`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrModelInstallConflict
	}
	return nil
}
