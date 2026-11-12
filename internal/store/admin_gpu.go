package store

import (
	"context"
	"time"

	"ai-access-gateway/internal/domain"
)

type AdminGPUWorkSummary struct {
	Kind                                            domain.GPUWorkKind
	Running, Reserved, Unconfirmed, Waiting, Paused int
	SnapshotAt                                      time.Time
}

// AdminGPUOverview only counts recorded Gateway work. Empty groups do not
// attest that the physical GPU or an external executor is idle.
func (s *Store) AdminGPUOverview(ctx context.Context) ([]AdminGPUWorkSummary, error) {
	rows, err := s.db.QueryContext(ctx, `WITH kinds(kind,ordinal) AS (
		VALUES ('generation',1),('lora_training',2),('lora_caption',3),('prompt_assistant',4)
	), work AS (
		SELECT w.kind,w.state,w.phase,w.lease_until,
			w.ready_until>statement_timestamp() AND NOT w.cancellation_requested
			AND u.id IS NOT NULL AND NOT u.disabled
			AND (u.account_expires_at IS NULL OR u.account_expires_at>statement_timestamp()) AS eligible
		FROM gpu_work w LEFT JOIN users u ON u.id=w.user_id
		WHERE w.resource_id=$1 AND w.state IN ('held','uncertain','waiting')
	)
	SELECT k.kind,
		count(*) FILTER(WHERE w.state='held' AND w.lease_until>statement_timestamp() AND w.phase='running'),
		count(*) FILTER(WHERE w.state='held' AND w.lease_until>statement_timestamp() AND w.phase<>'running'),
		count(*) FILTER(WHERE w.state='uncertain' OR (w.state='held' AND (w.lease_until IS NULL OR w.lease_until<=statement_timestamp()))),
		count(*) FILTER(WHERE w.state='waiting' AND w.eligible),
		count(*) FILTER(WHERE w.state='waiting' AND NOT w.eligible), statement_timestamp()
	FROM kinds k LEFT JOIN work w ON w.kind=k.kind GROUP BY k.kind,k.ordinal ORDER BY k.ordinal`, domain.GPUPrimaryResource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AdminGPUWorkSummary
	for rows.Next() {
		var item AdminGPUWorkSummary
		if err := rows.Scan(&item.Kind, &item.Running, &item.Reserved, &item.Unconfirmed, &item.Waiting, &item.Paused, &item.SnapshotAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
