package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"ai-access-gateway/internal/domain"
)

const generationOutputSourceBase = `
	FROM generation_jobs j
	JOIN content_events e ON e.user_id=j.user_id AND
	 (e.generation_job_id=j.id OR (e.generation_job_id IS NULL AND j.prompt_id<>'' AND e.external_id=j.prompt_id))
	JOIN content_media m ON m.event_id=e.id
	WHERE j.user_id=$1 AND e.service='comfyui' AND e.kind='comfyui_prompt'
	 AND e.expires_at>now() AND m.expires_at>now() AND m.profile_hidden_at IS NULL
	 AND m.media_type IN ('image','video')`

const generationOutputSourceFrom = generationOutputSourceBase + ` AND j.public_id=$2`

// Listing reads metadata only; it never materializes or probes media files.
func (s *Store) GenerationOutputSources(ctx context.Context, userID int64, jobID string, mediaID int64) ([]domain.GenerationOutputSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.media_type,m.mime_type,m.size_bytes,m.content_hash,
	 LEAST(e.expires_at,m.expires_at),
	 COALESCE((SELECT p.payload_cipher FROM content_media_measurements p WHERE p.media_id=m.id AND p.size_bytes=m.size_bytes AND (m.content_hash='' OR m.content_hash=p.content_sha256)),''::bytea),
	 COALESCE((SELECT p.content_sha256 FROM content_media_measurements p WHERE p.media_id=m.id AND p.size_bytes=m.size_bytes AND (m.content_hash='' OR m.content_hash=p.content_sha256)),'')
	`+generationOutputSourceFrom+` AND ($3::bigint=0 OR m.id=$3) ORDER BY m.id LIMIT 101`, userID, jobID, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.GenerationOutputSource{}
	for rows.Next() {
		var item domain.GenerationOutputSource
		if err := rows.Scan(&item.MediaID, &item.MediaType, &item.MIMEType, &item.SizeBytes, &item.ContentHash, &item.ExpiresAt, &item.MeasurementCipher, &item.MeasurementSHA256); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveMediaMeasurement(ctx context.Context, userID int64, jobID string, record domain.MediaMeasurementRecord) error {
	digest, err := hex.DecodeString(record.SHA256)
	if err != nil || len(digest) != 32 || record.SHA256 != strings.ToLower(record.SHA256) || record.MediaID <= 0 || record.SizeBytes <= 0 || len(record.PayloadCipher) == 0 || len(record.PayloadCipher) > 16384 {
		return errors.New("invalid media measurement")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO content_media_measurements(media_id,content_sha256,size_bytes,payload_cipher)
	 SELECT m.id,$4,$5,$6 `+generationOutputSourceFrom+`
	 AND m.id=$3 AND m.size_bytes=$5 AND (m.content_hash='' OR m.content_hash=$4)
	 ON CONFLICT(media_id) DO UPDATE SET content_sha256=EXCLUDED.content_sha256,
	 size_bytes=EXCLUDED.size_bytes,payload_cipher=EXCLUDED.payload_cipher,recorded_at=clock_timestamp()
	 WHERE content_media_measurements.content_sha256=EXCLUDED.content_sha256`, userID, jobID, record.MediaID, record.SHA256, record.SizeBytes, record.PayloadCipher)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
