package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"ai-access-gateway/internal/domain"
)

var ErrGPUExecutorBusy = errors.New("GPU executor admission is busy")

const gpuExecutorPermitColumns = `id,resource_id,executor,instance_id,COALESCE(work_id,''),state,completion_code,created_at,updated_at,COALESCE(owner_id,''),owner_generation`

func scanGPUExecutorPermit(row datasetScanner) (p domain.GPUExecutorPermit, err error) {
	err = row.Scan(&p.ID, &p.ResourceID, &p.Executor, &p.InstanceID, &p.WorkID, &p.State, &p.CompletionCode, &p.CreatedAt, &p.UpdatedAt, &p.OwnerID, &p.OwnerGeneration)
	return
}

func permitHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes && hex.EncodeToString(decoded) == value
}
func permitHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func validGPUExecutorPermitRequest(p domain.GPUExecutorPermitRequest) bool {
	if !permitHex(p.ID, 16) || !permitHex(p.InstanceID, 16) || !permitHex(p.Token, 32) {
		return false
	}
	switch p.ExecutorKind() {
	case domain.GPUExecutorComfyUI:
		return p.OwnerID == "" && p.OwnerGeneration == 0 && p.OwnerToken == "" &&
			((p.WorkID == "" && p.WorkLease == "") || (p.WorkID != "" && gpuTextValid(p.WorkID, 96) && gpuTextValid(p.WorkLease, 96) && p.WorkLease != ""))
	case domain.GPUExecutorOllama:
		return p.WorkID == "" && p.WorkLease == "" && validInferenceExecutor(p.OwnedExecutor())
	default:
		return false
	}
}

func (s *Store) ActiveGPUExecutorPermit(ctx context.Context, resource string) (domain.GPUExecutorPermit, error) {
	return scanGPUExecutorPermit(s.db.QueryRowContext(ctx, `SELECT `+gpuExecutorPermitColumns+` FROM gpu_executor_permits WHERE resource_id=$1 AND state IN ('reserved','running')`, resource))
}

func (s *Store) GPUExecutorPermit(ctx context.Context, input domain.GPUExecutorPermitRequest) (domain.GPUExecutorPermit, error) {
	if !validGPUExecutorPermitRequest(input) {
		return domain.GPUExecutorPermit{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUExecutorPermit{}, err
	}
	defer tx.Rollback()
	p, err := lockGPUExecutorPermit(ctx, tx, input)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// External admission must first observe all executors while holding the same
// advisory guard used by Gateway dispatch and mining. The resource row closes
// races with claims even if a previously sampled idle observation is stale.
func (s *Store) ReserveGPUExecutorPermit(ctx context.Context, input domain.GPUExecutorPermitRequest) (domain.GPUExecutorPermit, error) {
	if !validGPUExecutorPermitRequest(input) {
		return domain.GPUExecutorPermit{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUExecutorPermit{}, err
	}
	defer tx.Rollback()
	resource, err := scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource))
	if err != nil {
		return domain.GPUExecutorPermit{}, err
	}
	existing, err := scanGPUExecutorPermit(tx.QueryRowContext(ctx, `SELECT `+gpuExecutorPermitColumns+` FROM gpu_executor_permits WHERE id=$1 FOR UPDATE`, input.ID))
	if err == nil {
		if err = checkGPUExecutorPermitIdentity(ctx, tx, input); err != nil {
			return existing, err
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	if blocked, err := trainingStorageAdmissionBlockedTx(ctx, tx, resource.ID); err != nil {
		return existing, err
	} else if blocked {
		return existing, ErrGPUExecutorBusy
	}
	var occupied bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gpu_executor_permits WHERE resource_id=$1 AND state IN ('reserved','running'))`, resource.ID).Scan(&occupied); err != nil {
		return existing, err
	}
	if occupied {
		return existing, ErrGPUExecutorBusy
	}
	work, err := scanGPUWork(tx.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE resource_id=$1 AND state IN ('held','uncertain') FOR UPDATE`, resource.ID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	var workID any
	leaseHash := ""
	if input.WorkID != "" {
		if err != nil || work.ID != input.WorkID || work.LeaseToken != input.WorkLease || (work.Kind != domain.GPUWorkGeneration && work.Kind != domain.GPUWorkHarnessUltra) ||
			(work.Phase != "dispatching" && work.Phase != "running") || work.CancellationRequested {
			return existing, ErrGPUWorkConflict
		}
		workID, leaseHash = input.WorkID, permitHash(input.WorkLease)
		var prior bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gpu_executor_permits WHERE work_id=$1 AND work_lease_hash=$2)`, input.WorkID, leaseHash).Scan(&prior); err != nil {
			return existing, err
		}
		if prior {
			return existing, ErrGPUWorkConflict
		}
	} else {
		if err == nil {
			return existing, ErrGPUExecutorBusy
		}
		var idle, waiting bool
		if err = tx.QueryRowContext(ctx, `SELECT $1::text='idle' AND $2::timestamptz>clock_timestamp(),
			EXISTS(SELECT 1 FROM gpu_work w JOIN users u ON u.id=w.user_id WHERE w.resource_id=$3 AND w.state='waiting'
			 AND w.ready_until>now() AND NOT w.cancellation_requested AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now()))`, resource.Observation, resource.ValidUntil, resource.ID).Scan(&idle, &waiting); err != nil {
			return existing, err
		}
		if !idle || waiting {
			return existing, ErrGPUExecutorBusy
		}
	}
	ownerHash := ""
	if input.ExecutorKind() == domain.GPUExecutorOllama {
		if err = checkInferenceExecutor(ctx, tx, input.OwnedExecutor()); err != nil {
			return existing, err
		}
		ownerHash = permitHash(input.OwnerToken)
	}
	p, err := scanGPUExecutorPermit(tx.QueryRowContext(ctx, `INSERT INTO gpu_executor_permits(id,resource_id,executor,instance_id,token_hash,work_id,work_lease_hash,owner_id,owner_generation,owner_token_hash)
		VALUES($1,$2,$7,$3,$4,$5,$6,NULLIF($8,''),$9,$10) RETURNING `+gpuExecutorPermitColumns, input.ID, resource.ID, input.InstanceID, permitHash(input.Token), workID, leaseHash, input.ExecutorKind(), input.OwnerID, input.OwnerGeneration, ownerHash))
	if err != nil {
		return p, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func checkGPUExecutorPermitIdentity(ctx context.Context, tx *sql.Tx, input domain.GPUExecutorPermitRequest) error {
	leaseHash := ""
	if input.WorkID != "" {
		leaseHash = permitHash(input.WorkLease)
	}
	ownerHash := ""
	if input.OwnerToken != "" {
		ownerHash = permitHash(input.OwnerToken)
	}
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT executor=$6 AND instance_id=$2 AND token_hash=$3 AND COALESCE(work_id,'')=$4 AND work_lease_hash=$5
		AND COALESCE(owner_id,'')=$7 AND owner_generation=$8 AND owner_token_hash=$9
		FROM gpu_executor_permits WHERE id=$1`, input.ID, input.InstanceID, permitHash(input.Token), input.WorkID, leaseHash, input.ExecutorKind(), input.OwnerID, input.OwnerGeneration, ownerHash).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrGPUWorkConflict
	}
	return nil
}

func lockGPUExecutorPermit(ctx context.Context, tx *sql.Tx, input domain.GPUExecutorPermitRequest) (domain.GPUExecutorPermit, error) {
	var resource string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM gpu_resources WHERE id=(SELECT resource_id FROM gpu_executor_permits WHERE id=$1) FOR UPDATE`, input.ID).Scan(&resource); err != nil {
		return domain.GPUExecutorPermit{}, err
	}
	p, err := scanGPUExecutorPermit(tx.QueryRowContext(ctx, `SELECT `+gpuExecutorPermitColumns+` FROM gpu_executor_permits WHERE id=$1 FOR UPDATE`, input.ID))
	if err != nil {
		return p, err
	}
	return p, checkGPUExecutorPermitIdentity(ctx, tx, input)
}

// A duplicate start is not a second execution authorization. A lost response
// must be closed by the caller that knows it never entered the local handler.
func (s *Store) StartGPUExecutorPermit(ctx context.Context, input domain.GPUExecutorPermitRequest) (bool, error) {
	if !validGPUExecutorPermitRequest(input) {
		return false, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	p, err := lockGPUExecutorPermit(ctx, tx, input)
	if err != nil {
		return false, err
	}
	if p.State != "reserved" {
		return false, tx.Commit()
	}
	if input.ExecutorKind() == domain.GPUExecutorOllama {
		if err = checkInferenceExecutor(ctx, tx, input.OwnedExecutor()); err != nil {
			return false, err
		}
	}
	if input.WorkID != "" {
		var valid bool
		if err = tx.QueryRowContext(ctx, `SELECT state IN ('held','uncertain') AND lease_token=$2 AND NOT cancellation_requested
			FROM gpu_work WHERE id=$1 FOR UPDATE`, input.WorkID, input.WorkLease).Scan(&valid); err != nil {
			return false, err
		}
		if !valid {
			return false, ErrGPUWorkConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gpu_executor_permits SET state='running',updated_at=now() WHERE id=$1`, input.ID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Advance/registration is physical ownership evidence: the native runtime has
// exclusively drained its previous process tree. Never recover a different
// owner, the current generation, or a ComfyUI permit from this evidence.
func (s *Store) RecoverOwnedGPUExecutorPermits(ctx context.Context, executor domain.GPUInferenceExecutor) (int, error) {
	if !validInferenceExecutor(executor) {
		return 0, ErrGPUWorkInput
	}
	if err := s.EnsurePrimaryGPUResource(ctx); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var resource string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM gpu_resources WHERE id=$1 FOR UPDATE`, domain.GPUPrimaryResource).Scan(&resource); err != nil {
		return 0, err
	}
	if err = checkInferenceExecutor(ctx, tx, executor); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE gpu_executor_permits SET state='closed',completion_code='owner_stopped',updated_at=now()
		WHERE resource_id=$1 AND executor='ollama' AND owner_id=$2 AND owner_generation<$3 AND state IN ('reserved','running')`, resource, executor.ID, executor.Generation)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		if err = invalidateGPUObservation(ctx, tx, resource); err != nil {
			return 0, err
		}
	}
	return int(count), tx.Commit()
}

// This is executor evidence, not a timeout or a queue-health inference. Keep
// terminal identities so late reserve/start messages cannot reopen the gate.
func (s *Store) FinishGPUExecutorPermit(ctx context.Context, input domain.GPUExecutorPermitRequest, code string) error {
	if !validGPUExecutorPermitRequest(input) || (code != "not_started" && code != "quiesced") {
		return ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lockGPUExecutorPermit(ctx, tx, input)
	if err != nil {
		return err
	}
	if p.State == "closed" {
		if p.CompletionCode != code {
			return ErrGPUWorkConflict
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gpu_executor_permits SET state='closed',completion_code=$2,updated_at=now() WHERE id=$1`, input.ID, code); err != nil {
		return err
	}
	if err = invalidateGPUObservation(ctx, tx, p.ResourceID); err != nil {
		return err
	}
	return tx.Commit()
}
