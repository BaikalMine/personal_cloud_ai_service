package store

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/lib/pq"
)

var ErrGenerationInputUnavailable = errors.New("generation input is unavailable or expired")

const generationInputReferenceLockID int64 = 743629835

// Terminal UI state is not proof that an accepted executor has stopped reading.
const generationInputLiveJobSQL = `(j.state NOT IN ('completed','failed','cancelled','expired')
 OR ((j.submission_started_at IS NOT NULL OR j.prompt_id IS NOT NULL) AND j.submission_rejected_at IS NULL AND j.resources_released_at IS NULL))`

// The caller names the tracked asset "a". Old encrypted jobs have no reliable
// manifest; absence of a reference row must not permit their inputs to disappear.
const generationInputProtectedSQL = `(EXISTS(SELECT 1 FROM generation_jobs j
 WHERE NOT j.input_references_verified AND ` + generationInputLiveJobSQL + `)
 OR EXISTS(SELECT 1 FROM generation_input_references r JOIN generation_jobs j ON j.id=r.job_id
 WHERE r.input_id=a.id AND ` + generationInputLiveJobSQL + `))`

func canonicalGenerationInputNames(names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}
	if len(names) > 64 {
		return nil, ErrGenerationInputUnavailable
	}
	result := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || len(name) > 1024 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
			return nil, ErrGenerationInputUnavailable
		}
		for _, c := range name {
			if c < 0x20 || c == 0x7f {
				return nil, ErrGenerationInputUnavailable
			}
		}
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result, nil
}

// Called in the transaction that stores the prepared payload, while its job row
// is locked. Asset row locks serialize against finalization/receipt settlement.
func bindGenerationInputReferences(ctx context.Context, tx *sql.Tx, jobID, userID int64, inputNames []string) error {
	names, err := canonicalGenerationInputNames(inputNames)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationInputReferenceLockID); err != nil {
		return err
	}
	if names == nil {
		// Do not leave a previous verified manifest attached to an unknown payload.
		var cleanup bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM comfy_storage_operations WHERE state IN ('dispatching','unconfirmed'))`).Scan(&cleanup); err != nil {
			return err
		}
		if cleanup {
			return ErrGenerationInputUnavailable
		}
		_, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET input_references_verified=false WHERE id=$1`, jobID)
		return err
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		subfolder := path.Dir(name)
		if subfolder == "." {
			subfolder = ""
		}
		rows, err := tx.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_id=$1,false) AND a.state='stored'
		 AND (a.expires_at>clock_timestamp() OR EXISTS(SELECT 1 FROM generation_input_references r
		 WHERE r.job_id=$4 AND r.input_id=a.id AND r.input_name=$5)) eligible FROM comfy_input_assets a
		 WHERE a.filename=$2 AND a.subfolder=$3 AND a.storage_type='input'
		 ORDER BY a.id FOR SHARE OF a`, userID, path.Base(name), subfolder, jobID, name)
		if err != nil {
			return err
		}
		matches := []string{}
		eligible := false
		for rows.Next() {
			var id string
			if err := rows.Scan(&id, &eligible); err != nil {
				rows.Close()
				return err
			}
			matches = append(matches, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(matches) != 1 || !eligible {
			return ErrGenerationInputUnavailable
		}
		if err := rejectComfyStorageClaim(ctx, tx, "input:"+matches[0]); err != nil {
			if errors.Is(err, ErrComfyStorageBusy) {
				return ErrGenerationInputUnavailable
			}
			return err
		}
		ids = append(ids, matches[0])
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM generation_input_references WHERE job_id=$1 AND NOT(input_id=ANY($2::text[]))`, jobID, pq.Array(ids)); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_input_references(job_id,input_id,input_name)
		 VALUES($1,$2,$3) ON CONFLICT(job_id,input_id) DO UPDATE SET input_name=EXCLUDED.input_name`, jobID, id, names[i]); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE generation_jobs SET input_references_verified=true WHERE id=$1`, jobID)
	return err
}
