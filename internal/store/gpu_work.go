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

var (
	ErrGPUWorkConflict = errors.New("GPU work changed; refresh executor evidence")
	ErrGPUWorkInput    = errors.New("invalid GPU admission request")
)

const gpuWorkColumns = `id,resource_id,kind,job_key,user_id,priority,state,phase,lease_token,lease_until,external_id,cancellation_requested,ready_until,queued_at,created_at,updated_at,held_at,released_at`
const gpuResourceColumns = `id,revision,observation,message,observed_at,valid_until`

func scanGPUWork(row datasetScanner) (work domain.GPUWork, err error) {
	err = row.Scan(&work.ID, &work.ResourceID, &work.Kind, &work.JobKey, &work.UserID, &work.Priority, &work.State, &work.Phase, &work.LeaseToken, &work.LeaseUntil, &work.ExternalID, &work.CancellationRequested, &work.ReadyUntil, &work.QueuedAt, &work.CreatedAt, &work.UpdatedAt, &work.HeldAt, &work.ReleasedAt)
	return
}
func scanGPUResource(row datasetScanner) (resource domain.GPUResource, err error) {
	err = row.Scan(&resource.ID, &resource.Revision, &resource.Observation, &resource.Message, &resource.ObservedAt, &resource.ValidUntil)
	return
}
func gpuTextValid(value string, limit int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, 0)
}
func gpuLeaseValid(token string, duration time.Duration) bool {
	return token != "" && gpuTextValid(token, 96) && duration >= time.Second && duration <= domain.GPUMaxLeaseDuration
}
func gpuEvent(ctx context.Context, tx *sql.Tx, id string, state domain.GPUWorkState, code, detail string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO gpu_work_events(work_id,state,code,detail) VALUES($1,$2,$3,$4)`, id, state, code, detail)
	return err
}
func invalidateGPUObservation(ctx context.Context, tx *sql.Tx, resourceID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE gpu_resources SET revision=revision+1,observation='unknown',message='Executor check required',valid_until=now() WHERE id=$1`, resourceID)
	return err
}

func (s *Store) GPUResource(ctx context.Context, id string) (domain.GPUResource, error) {
	return scanGPUResource(s.db.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1`, id))
}
func (s *Store) GPUWork(ctx context.Context, id string) (domain.GPUWork, error) {
	return scanGPUWork(s.db.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE id=$1`, id))
}
func (s *Store) EnqueueGPUWork(ctx context.Context, input domain.GPUWorkRequest) (domain.GPUWork, error) {
	if input.ID == "" || input.ResourceID == "" || input.JobKey == "" || !input.Kind.Valid() || input.UserID <= 0 || !gpuTextValid(input.ID, 96) || !gpuTextValid(input.ResourceID, 64) || !gpuTextValid(input.JobKey, 96) {
		return domain.GPUWork{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO gpu_resources(id) VALUES($1) ON CONFLICT(id) DO NOTHING`, input.ResourceID); err != nil {
		return domain.GPUWork{}, err
	}
	if _, err = scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=$1 FOR UPDATE`, input.ResourceID)); err != nil {
		return domain.GPUWork{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO gpu_work(id,resource_id,kind,job_key,user_id,priority) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(kind,job_key) DO NOTHING`, input.ID, input.ResourceID, input.Kind, input.JobKey, input.UserID, input.Priority)
	if err != nil {
		return domain.GPUWork{}, err
	}
	inserted, _ := result.RowsAffected()
	work, err := scanGPUWork(tx.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE kind=$1 AND job_key=$2 FOR UPDATE`, input.Kind, input.JobKey))
	if err != nil {
		return work, err
	}
	if work.ResourceID != input.ResourceID || work.UserID == nil || *work.UserID != input.UserID {
		return work, ErrGPUWorkConflict
	}
	if inserted == 1 {
		if err = gpuEvent(ctx, tx, work.ID, work.State, "enqueued", ""); err != nil {
			return work, err
		}
	}
	if work.State == domain.GPUWorkWaiting {
		work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET ready_until=now()+($2::bigint*interval '1 second'),priority=$3 WHERE id=$1 RETURNING `+gpuWorkColumns, work.ID, int64(domain.GPUIntentLifetime.Seconds()), input.Priority))
		if err != nil {
			return work, err
		}
	}
	return work, tx.Commit()
}

// Capture Revision before probing executors, then publish with that revision.
// A late idle response may not override a claim, release or a newer observation.
func (s *Store) ObserveGPUResource(ctx context.Context, id string, revision int64, observation, message string, validFor time.Duration) (domain.GPUResource, error) {
	if (observation != "idle" && observation != "busy" && observation != "unknown") || !gpuTextValid(message, 1000) || validFor < time.Second || validFor > 30*time.Second {
		return domain.GPUResource{}, ErrGPUWorkInput
	}
	resource, err := scanGPUResource(s.db.QueryRowContext(ctx, `UPDATE gpu_resources SET revision=revision+1,observation=$3,message=$4,observed_at=now(),valid_until=now()+($5::bigint*interval '1 second') WHERE id=$1 AND revision=$2 RETURNING `+gpuResourceColumns, id, revision, observation, message, int64(validFor.Seconds())))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrGPUWorkConflict
	}
	return resource, err
}

func lockGPUWork(ctx context.Context, tx *sql.Tx, id string) (domain.GPUResource, domain.GPUWork, error) {
	resource, err := scanGPUResource(tx.QueryRowContext(ctx, `SELECT `+gpuResourceColumns+` FROM gpu_resources WHERE id=(SELECT resource_id FROM gpu_work WHERE id=$1) FOR UPDATE`, id))
	if err != nil {
		return resource, domain.GPUWork{}, err
	}
	work, err := scanGPUWork(tx.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE id=$1 FOR UPDATE`, id))
	return resource, work, err
}

func (s *Store) AcquireGPUWork(ctx context.Context, id, token string, duration time.Duration) (domain.GPUAdmission, error) {
	var result domain.GPUAdmission
	if !gpuLeaseValid(token, duration) {
		return result, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	resource, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return result, err
	}
	result.Work = work
	result.ResourceRevision = resource.Revision
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return result, err
	}
	if work.State == domain.GPUWorkWaiting {
		err = tx.QueryRowContext(ctx, `SELECT position FROM (SELECT w.id,row_number() OVER(ORDER BY w.queued_at-CASE WHEN w.priority THEN ($2::bigint*interval '1 second') ELSE interval '0 seconds' END,w.id) AS position FROM gpu_work w JOIN users u ON u.id=w.user_id WHERE w.resource_id=$1 AND w.state='waiting' AND w.ready_until>now() AND NOT w.cancellation_requested AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now())) ranked WHERE id=$3`, resource.ID, int64(domain.GPUPriorityHeadStart.Seconds()), id).Scan(&result.Position)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
	}
	holder, holderErr := scanGPUWork(tx.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE resource_id=$1 AND state IN ('held','uncertain') FOR UPDATE`, resource.ID))
	if holderErr != nil && !errors.Is(holderErr, sql.ErrNoRows) {
		return result, holderErr
	}
	if holderErr == nil {
		if holder.State == domain.GPUWorkHeld && !holder.LeaseUntil.After(now) {
			holder, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state='uncertain',updated_at=now() WHERE id=$1 RETURNING `+gpuWorkColumns, holder.ID))
			if err != nil {
				return result, err
			}
			if err = gpuEvent(ctx, tx, holder.ID, holder.State, "lease_expired", "Executor completion is unconfirmed"); err != nil {
				return result, err
			}
			if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
				return result, err
			}
			result.ResourceRevision++
		}
		if holder.ID == work.ID {
			result.Work = holder
		}
		result.Granted = holder.ID == work.ID && holder.State == domain.GPUWorkHeld && holder.LeaseToken == token && !holder.CancellationRequested
		if !result.Granted {
			result.WaitCode = "gpu_in_use"
			if holder.State == domain.GPUWorkUncertain {
				result.WaitCode = "executor_unconfirmed"
			}
		}
		return result, tx.Commit()
	}
	if work.State != domain.GPUWorkWaiting || work.CancellationRequested {
		result.WaitCode = "not_waiting"
		return result, tx.Commit()
	}
	if !work.ReadyUntil.After(now) {
		result.WaitCode = "intent_expired"
		return result, tx.Commit()
	}
	if resource.Observation != "idle" || !resource.ValidUntil.After(now) {
		result.WaitCode = "executor_unavailable"
		if resource.Observation == "busy" && resource.ValidUntil.After(now) {
			result.WaitCode = "external_work"
		}
		return result, tx.Commit()
	}
	var next string
	err = tx.QueryRowContext(ctx, `SELECT w.id FROM gpu_work w JOIN users u ON u.id=w.user_id WHERE w.resource_id=$1 AND w.state='waiting' AND w.ready_until>now() AND NOT w.cancellation_requested AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now()) ORDER BY w.queued_at-CASE WHEN w.priority THEN ($2::bigint*interval '1 second') ELSE interval '0 seconds' END,w.id LIMIT 1`, resource.ID, int64(domain.GPUPriorityHeadStart.Seconds())).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		result.WaitCode = "owner_unavailable"
		return result, tx.Commit()
	}
	if err != nil {
		return result, err
	}
	if next != id {
		result.WaitCode = "waiting_turn"
		return result, tx.Commit()
	}
	result.Work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state='held',phase='reserved',lease_token=$2,lease_until=now()+($3::bigint*interval '1 second'),held_at=now(),updated_at=now() WHERE id=$1 RETURNING `+gpuWorkColumns, id, token, int64(duration.Seconds())))
	if err != nil {
		return result, err
	}
	if err = gpuEvent(ctx, tx, id, domain.GPUWorkHeld, "admitted", ""); err != nil {
		return result, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return result, err
	}
	result.Granted = true
	result.ResourceRevision++
	return result, tx.Commit()
}

// Heartbeats cannot revive a lease after its deadline. Reconciliation owns it then.
func (s *Store) HeartbeatGPUWork(ctx context.Context, id, token string, duration time.Duration) (bool, error) {
	if !gpuLeaseValid(token, duration) {
		return false, ErrGPUWorkInput
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gpu_work SET lease_until=clock_timestamp()+($3::bigint*interval '1 second'),updated_at=now() WHERE id=$1 AND lease_token=$2 AND state='held' AND lease_until>clock_timestamp() AND NOT cancellation_requested`, id, token, int64(duration.Seconds()))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// Mark dispatching before network I/O; a lost response must retain the GPU lease.
func (s *Store) SetGPUWorkPhase(ctx context.Context, id, token, phase, externalID string) (domain.GPUWork, error) {
	if (phase != "dispatching" && phase != "running") || !gpuTextValid(externalID, 200) {
		return domain.GPUWork{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, err
	}
	defer tx.Rollback()
	resource, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, err
	}
	if work.State != domain.GPUWorkHeld || work.LeaseToken != token || work.CancellationRequested || (work.Phase == "running" && (phase != "running" || externalID != work.ExternalID)) || (work.Phase == "reserved" && phase == "running") {
		return work, ErrGPUWorkConflict
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET phase=$3,external_id=$4,updated_at=now() WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() RETURNING `+gpuWorkColumns, id, token, phase, externalID))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrGPUWorkConflict
	}
	if err != nil {
		return work, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, phase, externalID); err != nil {
		return work, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return work, err
	}
	return work, tx.Commit()
}

func (s *Store) RequestGPUWorkCancellation(ctx context.Context, id string) (domain.GPUWork, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, err
	}
	if work.State == domain.GPUWorkReleased || work.State == domain.GPUWorkCancelled || work.CancellationRequested {
		return work, tx.Commit()
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET cancellation_requested=true,state=CASE WHEN state='waiting' THEN 'cancelled' ELSE state END,released_at=CASE WHEN state='waiting' THEN now() ELSE released_at END,updated_at=now() WHERE id=$1 RETURNING `+gpuWorkColumns, id))
	if err != nil {
		return work, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, "cancellation_requested", ""); err != nil {
		return work, err
	}
	return work, tx.Commit()
}

func (s *Store) ReleaseGPUWork(ctx context.Context, id, token string, evidence domain.GPUReleaseEvidence) (domain.GPUWork, error) {
	if !gpuTextValid(evidence.Detail, 1000) {
		return domain.GPUWork{}, ErrGPUWorkInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, err
	}
	defer tx.Rollback()
	resource, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, err
	}
	if resource.Revision != evidence.ResourceRevision || work.LeaseToken != token || (work.State != domain.GPUWorkHeld && work.State != domain.GPUWorkUncertain) {
		return work, ErrGPUWorkConflict
	}
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz>clock_timestamp()`, work.LeaseUntil).Scan(&live); err != nil {
		return work, err
	}
	owned := work.State == domain.GPUWorkHeld && live
	valid := evidence.Code == "executor_terminal" || evidence.Code == "executor_idle" || (owned && evidence.Code == "not_dispatched" && work.Phase == "reserved") || (owned && work.Phase != "reserved" && evidence.Code == "request_completed" && (work.Kind == domain.GPUWorkCaption || work.Kind == domain.GPUWorkAssistant))
	if !valid {
		return work, ErrGPUWorkInput
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state=CASE WHEN cancellation_requested THEN 'cancelled' ELSE 'released' END,lease_token='',lease_until=NULL,released_at=now(),updated_at=now() WHERE id=$1 RETURNING `+gpuWorkColumns, id))
	if err != nil {
		return work, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, evidence.Code, evidence.Detail); err != nil {
		return work, err
	}
	if err = invalidateGPUObservation(ctx, tx, resource.ID); err != nil {
		return work, err
	}
	return work, tx.Commit()
}

func (s *Store) RetryGPUWork(ctx context.Context, id string) (domain.GPUWork, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.GPUWork{}, err
	}
	defer tx.Rollback()
	_, work, err := lockGPUWork(ctx, tx, id)
	if err != nil {
		return work, err
	}
	if work.State != domain.GPUWorkReleased && work.State != domain.GPUWorkCancelled {
		return work, ErrGPUWorkConflict
	}
	work, err = scanGPUWork(tx.QueryRowContext(ctx, `UPDATE gpu_work SET state='waiting',phase='reserved',external_id='',cancellation_requested=false,queued_at=now(),ready_until=now()+($2::bigint*interval '1 second'),held_at=NULL,released_at=NULL,updated_at=now() WHERE id=$1 RETURNING `+gpuWorkColumns, id, int64(domain.GPUIntentLifetime.Seconds())))
	if err != nil {
		return work, err
	}
	if err = gpuEvent(ctx, tx, id, work.State, "retry", ""); err != nil {
		return work, err
	}
	return work, tx.Commit()
}

func (s *Store) ActiveGPUWork(ctx context.Context, resourceID string) (domain.GPUWork, error) {
	return scanGPUWork(s.db.QueryRowContext(ctx, `SELECT `+gpuWorkColumns+` FROM gpu_work WHERE resource_id=$1 AND state IN ('held','uncertain')`, resourceID))
}
