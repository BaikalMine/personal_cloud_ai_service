package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ai-access-gateway/internal/domain"
)

const ContentMediaChunkSize = 1 << 20

type ContentMediaChunkWriter struct {
	tx           *sql.Tx
	mediaID      int64
	eventID      int64
	expiresAt    time.Time
	expectedSize int64
	writtenSize  int64
	nextIndex    int
	skipped      bool
	closed       bool
}

func (s *Store) BeginContentMediaChunks(ctx context.Context, media domain.ContentMediaRecord) (*ContentMediaChunkWriter, error) {
	if media.ExpiresAt.IsZero() {
		return nil, errors.New("content media expiration is required")
	}
	if media.SizeBytes <= 0 {
		return nil, errors.New("chunked content media size must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	writer := &ContentMediaChunkWriter{
		tx:           tx,
		eventID:      media.EventID,
		expiresAt:    media.ExpiresAt,
		expectedSize: media.SizeBytes,
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes,content_hash,expires_at,storage_format)
		VALUES ($1,$2,$3,$4,$5,$6,'\x'::bytea,$7,$8,$9,'chunked_v1')
		ON CONFLICT (event_id,original_name,subfolder,storage_type) DO NOTHING
		RETURNING id
	`, media.EventID, media.MediaType, media.MIMEType, media.OriginalName, media.Subfolder,
		media.StorageType, media.SizeBytes, media.ContentHash, media.ExpiresAt).Scan(&writer.mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		writer.skipped = true
		writer.closed = true
		_ = tx.Rollback()
		return writer, nil
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return writer, nil
}

func (writer *ContentMediaChunkWriter) MediaID() int64 {
	if writer == nil {
		return 0
	}
	return writer.mediaID
}

func (writer *ContentMediaChunkWriter) Skipped() bool {
	return writer == nil || writer.skipped
}

func (writer *ContentMediaChunkWriter) WriteChunk(ctx context.Context, index int, payloadCipher []byte, plainSize int) error {
	if writer == nil || writer.closed || writer.tx == nil {
		return errors.New("content media chunk writer is closed")
	}
	if index != writer.nextIndex || plainSize <= 0 || plainSize > ContentMediaChunkSize || len(payloadCipher) == 0 {
		return errors.New("invalid content media chunk")
	}
	if _, err := writer.tx.ExecContext(ctx, `
		INSERT INTO content_media_chunks(media_id,chunk_index,payload_cipher,plain_size)
		VALUES ($1,$2,$3,$4)
	`, writer.mediaID, index, payloadCipher, plainSize); err != nil {
		return err
	}
	writer.nextIndex++
	writer.writtenSize += int64(plainSize)
	return nil
}

func (writer *ContentMediaChunkWriter) Commit(ctx context.Context) error {
	if writer == nil {
		return errors.New("content media chunk writer is nil")
	}
	if writer.skipped {
		return nil
	}
	if writer.closed || writer.tx == nil {
		return errors.New("content media chunk writer is closed")
	}
	writer.closed = true
	if writer.nextIndex == 0 || writer.writtenSize != writer.expectedSize {
		_ = writer.tx.Rollback()
		return fmt.Errorf("content media chunks total %d bytes, want %d", writer.writtenSize, writer.expectedSize)
	}
	if _, err := writer.tx.ExecContext(ctx, `
		UPDATE content_events
		SET generated_media_count=generated_media_count+1,
		    media_expires_at=GREATEST(COALESCE(media_expires_at,'epoch'::timestamptz),$2)
		WHERE id=$1
	`, writer.eventID, writer.expiresAt); err != nil {
		_ = writer.tx.Rollback()
		return err
	}
	return writer.tx.Commit()
}

func (writer *ContentMediaChunkWriter) Rollback() {
	if writer == nil || writer.closed || writer.tx == nil {
		return
	}
	writer.closed = true
	_ = writer.tx.Rollback()
}

func (s *Store) ForEachContentMediaChunk(ctx context.Context, mediaID int64, visit func(index int, payloadCipher []byte, plainSize int) error) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT chunk_index,payload_cipher,plain_size
		FROM content_media_chunks
		WHERE media_id=$1
		ORDER BY chunk_index
	`, mediaID)
	if err != nil {
		return err
	}
	defer rows.Close()
	expectedIndex := 0
	for rows.Next() {
		var index, plainSize int
		var payloadCipher []byte
		if err := rows.Scan(&index, &payloadCipher, &plainSize); err != nil {
			return err
		}
		if index != expectedIndex {
			return fmt.Errorf("content media chunk sequence is incomplete at %d", expectedIndex)
		}
		if err := visit(index, payloadCipher, plainSize); err != nil {
			return err
		}
		expectedIndex++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if expectedIndex == 0 {
		return sql.ErrNoRows
	}
	return nil
}
