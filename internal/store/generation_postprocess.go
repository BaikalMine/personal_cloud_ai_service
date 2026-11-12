package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

var (
	ErrPostprocessConflict   = errors.New("postprocess confirmation changed or cancelled")
	ErrPostprocessSource     = errors.New("postprocess source changed or unavailable")
	ErrPostprocessExpired    = errors.New("postprocess confirmation expired")
	ErrPostprocessQuoteLimit = errors.New("too many pending postprocess confirmations")
)

const postprocessReceiptColumns = `user_id,request_id,state,version,confirmation_sha256,payload_cipher,sources,sensitive,job_id,job_public_id,created_at,expires_at`

func scanPostprocessReceipt(row generationJobScanner) (domain.GenerationPostprocessReceipt, error) {
	var r domain.GenerationPostprocessReceipt
	var sources []byte
	err := row.Scan(&r.UserID, &r.RequestID, &r.State, &r.Version, &r.ConfirmationSHA256, &r.PayloadCipher, &sources, &r.Sensitive, &r.JobID, &r.JobPublicID, &r.CreatedAt, &r.ExpiresAt)
	if err == nil {
		err = json.Unmarshal(sources, &r.Sources)
	}
	return r, err
}

func (s *Store) GenerationPostprocessByRequest(ctx context.Context, userID int64, requestID string) (domain.GenerationPostprocessReceipt, error) {
	return scanPostprocessReceipt(s.db.QueryRowContext(ctx, `SELECT `+postprocessReceiptColumns+` FROM generation_postprocess_receipts WHERE user_id=$1 AND request_id=$2`, userID, requestID))
}

func postprocessIDValid(id string) bool {
	return len(id) >= 16 && gpuTextValid(id, 96)
}

func postprocessDigestValid(digest string) bool {
	value, err := hex.DecodeString(digest)
	return err == nil && len(value) == 32 && strings.ToLower(digest) == digest
}

func postprocessSourcesValid(sources []domain.PostprocessSourceIdentity) bool {
	if len(sources) < 1 || len(sources) > 2 {
		return false
	}
	for i, source := range sources {
		if source.JobID <= 0 || source.MediaID <= 0 || !postprocessIDValid(source.JobPublicID) || source.SizeBytes <= 0 || source.SizeBytes > 512<<20 || !postprocessDigestValid(source.SHA256) || source.ArchiveSHA256 != "" && source.ArchiveSHA256 != source.SHA256 {
			return false
		}
		if source.MediaType == "image" {
			if source.MIMEType != "image/png" && source.MIMEType != "image/jpeg" && source.MIMEType != "image/webp" {
				return false
			}
		} else if source.MediaType != "video" || source.MIMEType != "video/mp4" && source.MIMEType != "video/webm" {
			return false
		}
		if i == 1 && (source.MediaType != "image" || source.MediaID == sources[0].MediaID) {
			return false
		}
	}
	return true
}

// One owner lock serializes quote, confirmation and abandonment. Source locks
// then fence concurrent hide/delete/permission changes until acceptance commits.
func lockPostprocessUser(ctx context.Context, tx *sql.Tx, userID int64) (domain.User, error) {
	var u domain.User
	var expired bool
	err := tx.QueryRowContext(ctx, `SELECT id,username,role,disabled,COALESCE(account_expires_at<=clock_timestamp(),false),can_use_quick_generation,can_generate_text_to_image,can_generate_image_to_image,can_generate_video FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &expired, &u.CanUseQuickGeneration, &u.CanGenerateTextToImage, &u.CanGenerateImageToImage, &u.CanGenerateVideo)
	if err == nil && (u.Disabled || expired || !u.CanUseService("quick_generation")) {
		err = ErrQuickGenerationForbidden
	}
	return u, err
}

func checkPostprocessSources(ctx context.Context, tx *sql.Tx, u domain.User, sources []domain.PostprocessSourceIdentity) (bool, error) {
	if !postprocessSourcesValid(sources) {
		return false, ErrPostprocessSource
	}
	sensitive := false
	var expiresAt time.Time
	for _, source := range sources {
		var jobID, size int64
		var templateID, mediaType, mimeType, archiveHash string
		var flagged bool
		var sourceExpiresAt time.Time
		err := tx.QueryRowContext(ctx, `SELECT j.id,j.template_id,m.media_type,m.mime_type,m.size_bytes,m.content_hash,e.is_sensitive,LEAST(e.expires_at,m.expires_at) `+generationOutputSourceFrom+`
		 AND m.id=$3 AND j.state='completed' AND e.expires_at>clock_timestamp() AND m.expires_at>clock_timestamp() FOR SHARE OF j,e,m`, u.ID, source.JobPublicID, source.MediaID).Scan(&jobID, &templateID, &mediaType, &mimeType, &size, &archiveHash, &flagged, &sourceExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrPostprocessSource
		}
		if err != nil {
			return false, err
		}
		family := domain.PostprocessImageTemplate
		if mediaType == "video" {
			family = domain.PostprocessVideoTemplate
		}
		if jobID != source.JobID || mediaType != source.MediaType || mimeType != source.MIMEType || size != source.SizeBytes || archiveHash != source.ArchiveSHA256 || !u.CanUseQuickGenerationType(templateID) || !u.CanUseQuickGenerationType(family) {
			return false, ErrPostprocessSource
		}
		sensitive = sensitive || flagged
		if expiresAt.IsZero() || sourceExpiresAt.Before(expiresAt) {
			expiresAt = sourceExpiresAt
		}
	}
	// Time can pass while acquiring row locks even when no row was changed.
	var live bool
	if err := tx.QueryRowContext(ctx, `SELECT $2::timestamptz>clock_timestamp() AND (account_expires_at IS NULL OR account_expires_at>clock_timestamp()) FROM users WHERE id=$1`, u.ID, expiresAt).Scan(&live); err != nil {
		return false, err
	}
	if !live {
		return false, ErrPostprocessSource
	}
	return sensitive, nil
}

func (s *Store) CreateGenerationPostprocessQuote(ctx context.Context, p domain.CreateGenerationPostprocessQuoteParams) (domain.GenerationPostprocessReceipt, bool, error) {
	var empty domain.GenerationPostprocessReceipt
	if p.UserID <= 0 || !postprocessIDValid(p.RequestID) || !postprocessDigestValid(p.ConfirmationSHA256) || len(p.PayloadCipher) == 0 || len(p.PayloadCipher) > 4<<20 || !postprocessSourcesValid(p.Sources) || p.ExpiresAt.IsZero() || p.ExpiresAt.Nanosecond()%1000 != 0 {
		return empty, false, ErrPostprocessConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	u, err := lockPostprocessUser(ctx, tx, p.UserID)
	if err != nil {
		return empty, false, err
	}
	existing, err := scanPostprocessReceipt(tx.QueryRowContext(ctx, `SELECT `+postprocessReceiptColumns+` FROM generation_postprocess_receipts WHERE user_id=$1 AND request_id=$2`, p.UserID, p.RequestID))
	if err == nil {
		if existing.State == "cancelled" || existing.ConfirmationSHA256 != p.ConfirmationSHA256 || !slices.Equal(existing.Sources, p.Sources) || !existing.ExpiresAt.Equal(p.ExpiresAt) {
			return empty, false, ErrPostprocessConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	var claimed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generation_requests WHERE user_id=$1 AND request_id=$2) OR EXISTS(SELECT 1 FROM generation_jobs WHERE user_id=$1 AND request_id=$2)`, p.UserID, p.RequestID).Scan(&claimed); err != nil {
		return empty, false, err
	}
	if claimed {
		return empty, false, ErrPostprocessConflict
	}
	// Preserve an ID fence, but release encrypted plans that can no longer be
	// confirmed. Accepted receipts retain their execution/recovery evidence.
	if _, err = tx.ExecContext(ctx, `UPDATE generation_postprocess_receipts SET state='cancelled',confirmation_sha256='',payload_cipher=''::bytea,sources='[]'::jsonb
	WHERE user_id=$1 AND state='quoted' AND expires_at<=clock_timestamp()`, p.UserID); err != nil {
		return empty, false, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM generation_postprocess_receipts WHERE user_id=$1 AND state='quoted'`, p.UserID).Scan(&pending); err != nil {
		return empty, false, err
	}
	if pending >= 4 {
		return empty, false, ErrPostprocessQuoteLimit
	}
	sensitive, err := checkPostprocessSources(ctx, tx, u, p.Sources)
	if err != nil {
		return empty, false, err
	}
	sources, err := json.Marshal(p.Sources)
	if err != nil {
		return empty, false, err
	}
	var validExpiry bool
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz>clock_timestamp() AND $1::timestamptz<=clock_timestamp()+interval '10 minutes'`, p.ExpiresAt).Scan(&validExpiry); err != nil {
		return empty, false, err
	}
	if !validExpiry {
		return empty, false, ErrPostprocessExpired
	}
	r, err := scanPostprocessReceipt(tx.QueryRowContext(ctx, `INSERT INTO generation_postprocess_receipts(user_id,request_id,state,confirmation_sha256,payload_cipher,sources,sensitive,expires_at) VALUES($1,$2,'quoted',$3,$4,$5::jsonb,$6,$7) RETURNING `+postprocessReceiptColumns, p.UserID, p.RequestID, p.ConfirmationSHA256, p.PayloadCipher, sources, sensitive, p.ExpiresAt))
	if err != nil {
		return empty, false, err
	}
	return r, true, tx.Commit()
}

// Confirmation consumes the stored encrypted plan, never a replacement supplied
// by the caller. Accepted receipts survive deletion and expiry without recreation.
func (s *Store) ConfirmGenerationPostprocess(ctx context.Context, userID int64, requestID, digest, publicID string) (domain.GenerationPostprocessReceipt, bool, error) {
	var empty domain.GenerationPostprocessReceipt
	if userID <= 0 || !postprocessIDValid(requestID) || !postprocessIDValid(publicID) || !postprocessDigestValid(digest) {
		return empty, false, ErrPostprocessConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	u, err := lockPostprocessUser(ctx, tx, userID)
	if err != nil {
		return empty, false, err
	}
	r, err := scanPostprocessReceipt(tx.QueryRowContext(ctx, `SELECT `+postprocessReceiptColumns+` FROM generation_postprocess_receipts WHERE user_id=$1 AND request_id=$2`, userID, requestID))
	if err != nil {
		return empty, false, err
	}
	if r.State == "cancelled" || r.ConfirmationSHA256 != digest {
		return empty, false, ErrPostprocessConflict
	}
	if r.State == "accepted" {
		return r, false, tx.Commit()
	}
	var expired bool
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, r.ExpiresAt).Scan(&expired); err != nil {
		return empty, false, err
	}
	if expired {
		return empty, false, ErrPostprocessExpired
	}
	sensitive, err := checkPostprocessSources(ctx, tx, u, r.Sources)
	if err != nil {
		return empty, false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, r.ExpiresAt).Scan(&expired); err != nil {
		return empty, false, err
	}
	if expired {
		return empty, false, ErrPostprocessExpired
	}
	for _, source := range r.Sources {
		if publicID == source.JobPublicID {
			return empty, false, ErrPostprocessConflict
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO generation_requests(user_id,request_id,correlation_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, userID, requestID, publicID)
	if err != nil {
		return empty, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return empty, false, err
	}
	if n != 1 {
		return empty, false, ErrPostprocessConflict
	}
	templateID := domain.PostprocessImageTemplate
	if r.Sources[0].MediaType == "video" {
		templateID = domain.PostprocessVideoTemplate
	}
	job, err := scanGenerationJob(tx.QueryRowContext(ctx, `INSERT INTO generation_jobs
	(public_id,correlation_id,user_id,username_snapshot,request_id,parent_job_id,state,status_message,template_id,workflow_id,seed,payload_cipher,dependencies,input_count)
	VALUES($1,$1,$2,$3,$4,$5,'waiting_for_resources','Postprocessing confirmed',$6,$7,0,$8,'["comfyui"]'::jsonb,$9) RETURNING `+generationJobColumns,
		publicID, userID, u.Username, requestID, r.Sources[0].JobID, templateID, domain.PostprocessWorkflow, r.PayloadCipher, len(r.Sources)))
	if err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE generation_requests SET job_id=$3 WHERE user_id=$1 AND request_id=$2`, userID, requestID, job.ID); err != nil {
		return empty, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO generation_job_transitions(job_id,correlation_id,from_state,to_state,message,attempt) VALUES($1,$2,'','waiting_for_resources',$3,1)`, job.ID, job.CorrelationID, job.StatusMessage); err != nil {
		return empty, false, err
	}
	r, err = scanPostprocessReceipt(tx.QueryRowContext(ctx, `UPDATE generation_postprocess_receipts SET state='accepted',job_id=$3,job_public_id=$4,sensitive=sensitive OR $5 WHERE user_id=$1 AND request_id=$2 RETURNING `+postprocessReceiptColumns, userID, requestID, job.ID, job.PublicID, sensitive))
	if err != nil {
		return empty, false, err
	}
	if err = incrementUserNotificationRevision(ctx, tx, userID); err != nil {
		return empty, false, err
	}
	if err = incrementGenerationJobRevision(ctx, tx); err != nil {
		return empty, false, err
	}
	return r, true, tx.Commit()
}

// An absent quote is not proof that its creation stopped. Persist a tombstone;
// if confirmation already won, return that child instead of claiming cancellation.
func (s *Store) DiscardGenerationPostprocess(ctx context.Context, userID int64, requestID string) (domain.GenerationPostprocessReceipt, error) {
	var empty domain.GenerationPostprocessReceipt
	if userID <= 0 || !postprocessIDValid(requestID) {
		return empty, ErrPostprocessConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&id); err != nil {
		return empty, err
	}
	r, err := scanPostprocessReceipt(tx.QueryRowContext(ctx, `INSERT INTO generation_postprocess_receipts(user_id,request_id,state) VALUES($1,$2,'cancelled')
	ON CONFLICT(user_id,request_id) DO UPDATE SET state='cancelled',confirmation_sha256='',payload_cipher=''::bytea,sources='[]'::jsonb
	WHERE generation_postprocess_receipts.state='quoted' RETURNING `+postprocessReceiptColumns, userID, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		r, err = scanPostprocessReceipt(tx.QueryRowContext(ctx, `SELECT `+postprocessReceiptColumns+` FROM generation_postprocess_receipts WHERE user_id=$1 AND request_id=$2`, userID, requestID))
	}
	if err != nil {
		return empty, err
	}
	return r, tx.Commit()
}
