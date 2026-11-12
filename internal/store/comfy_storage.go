package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"ai-access-gateway/internal/domain"
	"github.com/lib/pq"
)

var ErrComfyStorageQuery = errors.New("invalid ComfyUI storage query")

type ComfyStorageQuery struct {
	Kind   string
	State  string
	Search string
	Keys   []string
	Limit  int
	Offset int
}

func ValidComfyStorageKey(key string) bool {
	kind, id, ok := strings.Cut(key, ":")
	if !ok {
		return false
	}
	if kind == "output" || kind == "cleanup" {
		n, err := strconv.ParseInt(id, 10, 64)
		return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
	}
	if kind != "input" || len(id) < 32 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (q ComfyStorageQuery) Validate() error {
	if q.Kind != "" && q.Kind != "input" && q.Kind != "output" {
		return ErrComfyStorageQuery
	}
	switch q.State {
	case "", "pending", "retained", "retry", "mismatched", "unconfirmed", "referenced":
	default:
		return ErrComfyStorageQuery
	}
	if len(q.Search) > 200 || strings.ContainsRune(q.Search, 0) || q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 100000 || len(q.Keys) > 50 {
		return ErrComfyStorageQuery
	}
	seen := map[string]bool{}
	for _, key := range q.Keys {
		if !ValidComfyStorageKey(key) || seen[key] {
			return ErrComfyStorageQuery
		}
		seen[key] = true
	}
	return nil
}

// This is a registry view, not a filesystem scan. Reused paths can legitimately
// have a retained result and an older cleanup identity, so totals count records.
const comfyStorageCTE = `WITH owners AS (
 SELECT filename,subfolder,storage_type,max(id) id,min(user_id) owner_id,
 count(DISTINCT user_id)::integer owner_count,min(created_at) created_at,max(expires_at) expires_at
 FROM comfy_output_ownership WHERE storage_type='output' GROUP BY filename,subfolder,storage_type
), records AS (
 SELECT 'input:'||a.id key,'input' kind,a.filename,a.subfolder,a.storage_type,a.size_bytes,true size_known,a.content_hash,
 a.user_id owner_id,CASE WHEN a.user_id IS NULL THEN 0 ELSE 1 END owner_count,a.state,
 a.expires_at<=now() pending,CASE WHEN a.state='reserved' THEN 'upload_reservation' ELSE 'retention' END reason,
 a.created_at,a.expires_at,a.expires_at eligible_at,a.cleanup_retry_at next_attempt_at,a.cleanup_last_attempt_at last_attempt_at,
 a.cleanup_attempts attempts,a.cleanup_last_outcome last_outcome,false profile_hidden,p.job_id postprocess_job_id,
 COALESCE(p.cleanup_blocked OR j.state NOT IN ('completed','failed','cancelled','expired')
 OR (j.prompt_id IS NULL AND j.submission_started_at IS NOT NULL AND j.submission_rejected_at IS NULL),false)
 OR ` + generationInputProtectedSQL + ` reference_protected
 FROM comfy_input_assets a LEFT JOIN generation_postprocess_inputs p ON p.input_id=a.id LEFT JOIN generation_jobs j ON j.id=p.job_id
 UNION ALL
 SELECT 'cleanup:'||t.id,'output',t.filename,t.subfolder,t.storage_type,t.size_bytes,true,t.content_hash,
 CASE WHEN o.owner_count=1 THEN o.owner_id END,COALESCE(o.owner_count,0),'cleanup_pending',true,t.cleanup_reason,
 t.created_at,NULL::timestamptz,t.eligible_at,t.next_attempt_at,t.last_attempt_at,t.attempt_count,t.last_outcome,
 t.cleanup_reason='profile_hidden',NULL::bigint,false
 FROM comfy_output_cleanup_tombstones t LEFT JOIN owners o USING(filename,subfolder,storage_type)
 UNION ALL
 SELECT 'output:'||o.id,'output',o.filename,o.subfolder,o.storage_type,COALESCE(m.size_bytes,0),m.id IS NOT NULL,COALESCE(m.content_hash,''),
 CASE WHEN o.owner_count=1 THEN o.owner_id END,o.owner_count,'tracked',false,'retained',
 o.created_at,COALESCE(m.expires_at,o.expires_at),NULL::timestamptz,NULL::timestamptz,NULL::timestamptz,0,'not_queued',
 m.profile_hidden_at IS NOT NULL,NULL::bigint,false
 FROM owners o LEFT JOIN LATERAL (
 SELECT m.id,m.size_bytes,m.content_hash,m.expires_at,m.profile_hidden_at FROM content_media m JOIN content_events e ON e.id=m.event_id
 WHERE e.service='comfyui' AND m.original_name=o.filename AND m.subfolder=o.subfolder AND m.storage_type='output' ORDER BY m.id DESC LIMIT 1
 ) m ON true
 WHERE NOT EXISTS(SELECT 1 FROM comfy_output_cleanup_tombstones t WHERE t.filename=o.filename AND t.subfolder=o.subfolder AND t.storage_type=o.storage_type
 AND (m.id IS NULL OR (t.size_bytes=m.size_bytes AND t.content_hash=m.content_hash)))
), inventory AS (
 SELECT r.*,COALESCE(u.username,'') owner_name FROM records r LEFT JOIN users u ON u.id=r.owner_id
) `

const comfyStorageFilter = `WHERE ($1='' OR kind=$1)
 AND ($2='' OR ($2='pending' AND pending) OR ($2='retained' AND NOT pending)
 OR ($2='retry' AND pending AND attempts>0) OR ($2='mismatched' AND last_outcome='mismatched')
 OR ($2='unconfirmed' AND (state='reserved' OR last_outcome IN ('unconfirmed','legacy_retry')))
 OR ($2='referenced' AND reference_protected))
 AND ($3='' OR strpos(lower(filename||' '||subfolder||' '||owner_name),lower($3))>0)
 AND (cardinality($4::text[])=0 OR key=ANY($4::text[]))`

const comfyStorageItemColumns = `key,kind,filename,subfolder,storage_type,size_bytes,size_known,content_hash,owner_id,owner_count,owner_name,state,pending,reason,created_at,expires_at,eligible_at,next_attempt_at,last_attempt_at,attempts,last_outcome,profile_hidden,postprocess_job_id,reference_protected`

func scanComfyStorageItem(row datasetScanner) (item domain.ComfyStorageItem, err error) {
	err = row.Scan(&item.Key, &item.Kind, &item.Filename, &item.Subfolder, &item.StorageType, &item.SizeBytes, &item.SizeKnown, &item.ContentHash, &item.OwnerID, &item.OwnerCount, &item.OwnerName, &item.State, &item.Pending, &item.Reason, &item.CreatedAt, &item.ExpiresAt, &item.EligibleAt, &item.NextAttemptAt, &item.LastAttemptAt, &item.Attempts, &item.LastOutcome, &item.ProfileHidden, &item.PostprocessJobID, &item.ReferenceProtected)
	return
}

func readComfyStorageActivity(ctx context.Context, tx *sql.Tx, view *domain.ComfyStorageInventory) error {
	return tx.QueryRowContext(ctx, `SELECT now(),now()-interval '90 days',
 EXISTS(SELECT 1 FROM generation_jobs WHERE state NOT IN ('completed','failed','cancelled','expired')),
 EXISTS(SELECT 1 FROM generation_jobs WHERE prompt_id IS NULL AND submission_started_at IS NOT NULL AND submission_rejected_at IS NULL AND dispatch_closed_at IS NULL)
	 OR EXISTS(SELECT 1 FROM gpu_work WHERE state='uncertain'),
	 EXISTS(SELECT 1 FROM gpu_executor_permits WHERE executor='comfyui' AND state IN ('reserved','running')),
	 EXISTS(SELECT 1 FROM generation_jobs j WHERE NOT j.input_references_verified AND `+generationInputLiveJobSQL+`)`).Scan(
		&view.CheckedAt, &view.Summary.ReportingSince, &view.Summary.GenerationActive, &view.Summary.ExecutionUnconfirmed, &view.Summary.ExecutorOccupied, &view.Summary.UnknownInputManifest)
}

func (s *Store) ComfyStorageInventory(ctx context.Context, query ComfyStorageQuery) (domain.ComfyStorageInventory, error) {
	view := domain.ComfyStorageInventory{Items: []domain.ComfyStorageItem{}, Limit: query.Limit, Offset: query.Offset}
	if err := query.Validate(); err != nil {
		return view, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	if err := readComfyStorageActivity(ctx, tx, &view); err != nil {
		return view, err
	}
	if err := tx.QueryRowContext(ctx, comfyStorageCTE+`SELECT
 count(*) FILTER(WHERE kind='input'),COALESCE(sum(size_bytes) FILTER(WHERE kind='input' AND size_known),0),
 count(*) FILTER(WHERE kind='output'),COALESCE(sum(size_bytes) FILTER(WHERE kind='output' AND size_known),0),
 count(*) FILTER(WHERE NOT size_known),count(*) FILTER(WHERE pending),COALESCE(sum(size_bytes) FILTER(WHERE pending AND size_known),0),
 count(*) FILTER(WHERE pending AND attempts>0),count(*) FILTER(WHERE last_outcome='mismatched'),count(*) FILTER(WHERE last_outcome='host_unavailable'),
 count(*) FILTER(WHERE last_outcome IN ('unconfirmed','legacy_retry')),count(*) FILTER(WHERE state='reserved'),count(*) FILTER(WHERE reference_protected),
 min(eligible_at) FILTER(WHERE pending AND eligible_at<=now()) FROM inventory`).Scan(
		&view.Summary.InputRecords, &view.Summary.InputExpectedBytes, &view.Summary.OutputRecords, &view.Summary.OutputExpectedBytes,
		&view.Summary.UnknownSizeRecords, &view.Summary.PendingRecords, &view.Summary.PendingExpectedBytes, &view.Summary.RetryRecords,
		&view.Summary.MismatchedRecords, &view.Summary.UnavailableRecords, &view.Summary.UnconfirmedRecords, &view.Summary.UnconfirmedUploads,
		&view.Summary.ProtectedReferences, &view.Summary.OldestEligibleAt); err != nil {
		return view, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(freed_bytes),0),count(*) FILTER(WHERE outcome='missing'),count(*) FILTER(WHERE outcome='legacy_confirmed'),max(confirmed_at)
 FROM (SELECT freed_bytes,outcome,confirmed_at FROM comfy_input_cleanup_receipts WHERE confirmed_at>=now()-interval '90 days'
 UNION ALL SELECT freed_bytes,outcome,confirmed_at FROM comfy_output_cleanup_receipts WHERE confirmed_at>=now()-interval '90 days') r`).Scan(
		&view.Summary.ReceiptCount, &view.Summary.ConfirmedFreedBytes, &view.Summary.MissingReceipts, &view.Summary.LegacyReceipts, &view.Summary.LatestConfirmationAt); err != nil {
		return view, err
	}
	keys := query.Keys
	if keys == nil {
		keys = []string{}
	}
	args := []any{query.Kind, query.State, strings.TrimSpace(query.Search), pq.Array(keys)}
	if err := tx.QueryRowContext(ctx, comfyStorageCTE+`SELECT count(*) FROM inventory `+comfyStorageFilter, args...).Scan(&view.Total); err != nil {
		return view, err
	}
	args = append(args, query.Limit, query.Offset)
	rows, err := tx.QueryContext(ctx, comfyStorageCTE+`SELECT `+comfyStorageItemColumns+`
 FROM inventory `+comfyStorageFilter+` ORDER BY pending DESC,eligible_at NULLS LAST,created_at,key LIMIT $5 OFFSET $6`, args...)
	if err != nil {
		return view, err
	}
	for rows.Next() {
		item, err := scanComfyStorageItem(rows)
		if err != nil {
			rows.Close()
			return view, err
		}
		decorateComfyStorageItem(&item, view.Summary)
		view.Items = append(view.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return view, err
	}
	return view, tx.Commit()
}

func decorateComfyStorageItem(item *domain.ComfyStorageItem, summary domain.ComfyStorageSummary) {
	item.Protection = []string{}
	if item.State == "reserved" {
		item.Protection = append(item.Protection, "upload_unconfirmed")
	}
	if item.ReferenceProtected {
		reason := "generation_input_reference"
		if item.PostprocessJobID != nil {
			reason = "postprocess_reference"
		}
		item.Protection = append(item.Protection, reason)
	}
	if item.Kind == "input" && summary.UnknownInputManifest {
		item.Protection = append(item.Protection, "generation_inputs_unknown")
	}
	if summary.GenerationActive {
		item.Protection = append(item.Protection, "generation_active")
	}
	if summary.ExecutionUnconfirmed {
		item.Protection = append(item.Protection, "execution_unconfirmed")
	}
	if summary.ExecutorOccupied {
		item.Protection = append(item.Protection, "executor_occupied")
	}
	if !item.Pending {
		item.Protection = append(item.Protection, "retained")
	}
	decoded, err := hex.DecodeString(item.ContentHash)
	item.CanInspect = item.SizeKnown && item.State != "reserved" && item.SizeBytes >= 0 && item.SizeBytes <= 2<<30 && err == nil && len(decoded) == 32
	if !item.CanInspect && item.State != "reserved" {
		item.Protection = append(item.Protection, "fingerprint_unavailable")
	}
	// The registry cannot prove exclusion of direct ComfyUI use. No observed
	// idle queue or successful hash inspection is a manual deletion permit.
	item.Protection = append(item.Protection, "executor_not_fenced")
	item.Version = ""
	payload, _ := json.Marshal(struct {
		Record domain.ComfyStorageItem
		Hash   string
	}{*item, item.ContentHash})
	hash := sha256.Sum256(payload)
	item.Version = hex.EncodeToString(hash[:])
}
