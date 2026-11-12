package store

import (
	"context"
	"database/sql"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

// TaskGPUStatus resolves the source and current attempt inside one read snapshot.
// It never probes an executor, renews an intent, or changes an expired lease.
func (s *Store) TaskGPUStatus(ctx context.Context, scope domain.TaskScope, kind, id string) (*domain.TaskGPUStatus, error) {
	if scope.UserID <= 0 || id == "" || !gpuTextValid(id, 200) {
		return nil, ErrTaskQuery
	}
	if (kind != "generation" || !scope.Generation) &&
		(kind != "lora_training" || !scope.Training) && (kind != "lora_caption" || !scope.Training) {
		return nil, sql.ErrNoRows
	}
	snapshot, err := s.TaskGPUSnapshots(ctx, scope, []domain.TaskGPUKey{{Kind: kind, ID: id}})
	if err != nil {
		return nil, err
	}
	if !snapshot.Tasks[0].Available {
		return nil, sql.ErrNoRows
	}
	return snapshot.Tasks[0].GPU, nil
}

// TaskGPUSnapshots reads a bounded set in one statement without decrypting source
// payloads. Resource-partitioned ranks include eligible intents of other owners,
// but only requested, owner-scoped source identities leave this read model.
func (s *Store) TaskGPUSnapshots(ctx context.Context, scope domain.TaskScope, keys []domain.TaskGPUKey) (domain.TaskGPUSnapshots, error) {
	return s.taskGPUSnapshots(ctx, scope, keys, false)
}

// Administrative training reads are explicit and cannot widen the public task
// scope. Generation and caption identities are never accepted by this adapter.
func (s *Store) AdminTrainingGPUSnapshots(ctx context.Context, ids []string) (domain.TaskGPUSnapshots, error) {
	keys := make([]domain.TaskGPUKey, len(ids))
	for i, id := range ids {
		keys[i] = domain.TaskGPUKey{Kind: "lora_training", ID: id}
	}
	return s.taskGPUSnapshots(ctx, domain.TaskScope{Training: true}, keys, true)
}

func (s *Store) taskGPUSnapshots(ctx context.Context, scope domain.TaskScope, keys []domain.TaskGPUKey, adminTraining bool) (domain.TaskGPUSnapshots, error) {
	result := domain.TaskGPUSnapshots{}
	if (!adminTraining && scope.UserID <= 0) || len(keys) == 0 || len(keys) > domain.TaskGPUSnapshotLimit {
		return result, ErrTaskQuery
	}
	kinds, ids := make([]string, len(keys)), make([]string, len(keys))
	seen := make(map[domain.TaskGPUKey]bool, len(keys))
	for i, key := range keys {
		if key.ID == "" || !gpuTextValid(key.ID, 200) || seen[key] ||
			(key.Kind != "generation" && key.Kind != "lora_training" && key.Kind != "lora_caption") {
			return result, ErrTaskQuery
		}
		seen[key] = true
		kinds[i], ids[i] = key.Kind, key.ID
	}
	rows, err := s.db.QueryContext(ctx, `WITH requested AS (
		SELECT kind,source_id,ordinal FROM unnest($2::text[],$3::text[]) WITH ORDINALITY AS q(kind,source_id,ordinal)
	), source AS (
		SELECT q.ordinal,q.kind,j.public_id AS job_key,''::text AS work_id,j.user_id,
			j.state IN ('completed','failed','cancelled','expired') AS terminal,
			j.cancellation_requested_at IS NOT NULL AS cancelled
		FROM requested q JOIN generation_jobs j ON q.kind='generation' AND j.public_id=q.source_id
		WHERE $5 AND j.user_id=$1
		UNION ALL
		SELECT q.ordinal,q.kind,j.public_id,'',j.user_id,j.state IN ('completed','failed','cancelled'),j.cancellation_requested_at IS NOT NULL
		FROM requested q JOIN lora_training_jobs j ON q.kind='lora_training' AND j.public_id=q.source_id
		WHERE $6 AND ($7 OR j.user_id=$1)
		UNION ALL
		SELECT q.ordinal,q.kind,CASE WHEN j.run_token<>'' THEN j.id||':'||j.run_token ELSE '' END,
			COALESCE(j.inference_work_id,''),j.user_id,j.state NOT IN ('queued','running'),j.cancel_requested
		FROM requested q JOIN lora_caption_jobs j ON q.kind='lora_caption' AND j.id=q.source_id
		WHERE $6 AND j.user_id=$1
	), selected AS (
		SELECT w.*,src.ordinal,src.terminal,src.cancelled AS source_cancelled,
			NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>statement_timestamp()) AS owner_available
		FROM source src JOIN users u ON u.id=src.user_id
		LEFT JOIN gpu_work w ON w.user_id=src.user_id AND w.kind=src.kind
			AND ((src.work_id<>'' AND w.id=src.work_id) OR
				(src.work_id='' AND src.job_key<>'' AND w.job_key=src.job_key))
	), resources AS (
		SELECT DISTINCT resource_id FROM selected WHERE resource_id IS NOT NULL
	), ranked AS (
		SELECT w.id,row_number() OVER(PARTITION BY w.resource_id ORDER BY w.queued_at-CASE WHEN w.priority THEN ($4::bigint*interval '1 second') ELSE interval '0 seconds' END,w.id) AS position
		FROM gpu_work w JOIN users u ON u.id=w.user_id JOIN resources res ON res.resource_id=w.resource_id
		WHERE w.state='waiting'
			AND w.ready_until>statement_timestamp() AND NOT w.cancellation_requested AND NOT u.disabled
			AND (u.account_expires_at IS NULL OR u.account_expires_at>statement_timestamp())
	)
	SELECT req.kind,req.source_id,w.ordinal IS NOT NULL,
		COALESCE(w.id,''),COALESCE(w.state,''),COALESCE(w.phase,''),w.lease_until,
		COALESCE(w.ready_until,statement_timestamp()),COALESCE(w.cancellation_requested,false) OR COALESCE(w.source_cancelled,false),
		COALESCE(w.terminal,false),COALESCE(w.owner_available,false),COALESCE(q.position,0),COALESCE(r.observation,'unknown'),
		COALESCE(r.valid_until,statement_timestamp()),statement_timestamp(),
		EXISTS(SELECT 1 FROM gpu_work h WHERE h.resource_id=w.resource_id AND h.id<>w.id AND h.state IN ('held','uncertain')),
		EXISTS(SELECT 1 FROM gpu_work h WHERE h.resource_id=w.resource_id AND h.id<>w.id AND
			(h.state='uncertain' OR (h.state='held' AND (h.lease_until IS NULL OR h.lease_until<=statement_timestamp())))),
		EXISTS(SELECT 1 FROM gpu_executor_permits p WHERE p.resource_id=w.resource_id AND p.state IN ('reserved','running'))
	FROM requested req LEFT JOIN selected w ON w.ordinal=req.ordinal
	LEFT JOIN ranked q ON q.id=w.id LEFT JOIN gpu_resources r ON r.id=w.resource_id ORDER BY req.ordinal`,
		scope.UserID, pq.Array(kinds), pq.Array(ids), int64(domain.GPUPriorityHeadStart.Seconds()), scope.Generation, scope.Training, adminTraining)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Tasks = make([]domain.TaskGPUSnapshotEntry, 0, len(keys))
	for rows.Next() {
		var entry domain.TaskGPUSnapshotEntry
		var observation taskGPUObservation
		if err := rows.Scan(&entry.Kind, &entry.ID, &entry.Available,
			&observation.workID, &observation.state, &observation.phase, &observation.leaseUntil,
			&observation.readyUntil, &observation.cancelled, &observation.terminal, &observation.ownerAvailable, &observation.position,
			&observation.observation, &observation.validUntil, &observation.now, &observation.holder, &observation.uncertain, &observation.external); err != nil {
			return domain.TaskGPUSnapshots{}, err
		}
		entry.GPU = observation.status()
		result.SnapshotAt = observation.now
		result.Tasks = append(result.Tasks, entry)
	}
	if err := rows.Err(); err != nil {
		return domain.TaskGPUSnapshots{}, err
	}
	return result, nil
}

type taskGPUObservation struct {
	state, phase, observation, workID                                string
	leaseUntil                                                       sql.NullTime
	readyUntil, validUntil, now                                      time.Time
	cancelled, terminal, ownerAvailable, holder, uncertain, external bool
	position                                                         int
}

func (o taskGPUObservation) status() *domain.TaskGPUStatus {
	if o.workID == "" || o.state == string(domain.GPUWorkReleased) || o.state == string(domain.GPUWorkCancelled) {
		return nil
	}
	status := &domain.TaskGPUStatus{SnapshotAt: o.now}
	switch {
	case o.state == string(domain.GPUWorkUncertain) || (o.state == string(domain.GPUWorkHeld) && (!o.leaseUntil.Valid || !o.leaseUntil.Time.After(o.now))):
		status.Code = "executor_unconfirmed"
	case o.state == string(domain.GPUWorkHeld):
		status.Code = "gpu_reserved"
		if o.phase == "running" {
			status.Code = "gpu_running"
		}
		if o.cancelled {
			status.Code = "cancellation_pending"
		}
	case o.cancelled:
		status.Code = "cancellation_pending"
	case o.terminal:
		status.Code = "source_terminal"
	case !o.ownerAvailable:
		status.Code = "owner_unavailable"
	case !o.readyUntil.After(o.now):
		status.Code = "intent_expired"
	default:
		status.Position = o.position
		switch {
		case o.uncertain:
			status.Code = "executor_unconfirmed"
		case o.holder:
			status.Code = "gpu_in_use"
		case o.external:
			status.Code = "external_work"
		case !o.validUntil.After(o.now) || o.observation == "unknown":
			status.Code = "executor_unavailable"
		case o.observation == "busy":
			status.Code = "external_work"
		case o.position > 1:
			status.Code = "waiting_turn"
		default:
			status.Code = "admission_pending"
		}
	}
	return status
}
