package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const chunkedMediaMemoryReservation = int64(8 << 20)

var errMediaMemoryBudget = errors.New("media memory budget is exhausted")

type materializedContentMedia struct {
	file    *os.File
	path    string
	release func()
	once    sync.Once
	err     error
}

func (media *materializedContentMedia) Read(payload []byte) (int, error) {
	return media.file.Read(payload)
}

func (media *materializedContentMedia) Seek(offset int64, whence int) (int64, error) {
	return media.file.Seek(offset, whence)
}

func (media *materializedContentMedia) Close() error {
	if media == nil {
		return nil
	}
	media.once.Do(func() {
		if media.file != nil {
			media.err = media.file.Close()
		}
		if removeErr := os.Remove(media.path); media.err == nil && removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			media.err = removeErr
		}
		if media.release != nil {
			media.release()
		}
	})
	return media.err
}

func contentMediaChunkAAD(mediaID int64, chunkIndex int) []byte {
	return []byte(fmt.Sprintf("ai-access-gateway:content-media:chunk:v1:%d:%d", mediaID, chunkIndex))
}

func mediaMemoryReservation(storageFormat string, sizeBytes int64) int64 {
	if storageFormat == "chunked_v1" && sizeBytes > chunkedMediaMemoryReservation {
		return chunkedMediaMemoryReservation
	}
	if sizeBytes <= 0 {
		return 1
	}
	return sizeBytes
}

func normalizedMediaStorageFormat(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "inline_v1"
	}
	return value
}

func (a *App) materializeContentMedia(ctx context.Context, media domain.ContentMediaRow) (result *materializedContentMedia, operationErr error) {
	if a == nil || a.contentCipher == nil || a.store == nil {
		return nil, errors.New("content media storage is unavailable")
	}
	storageFormat := normalizedMediaStorageFormat(media.StorageFormat)
	release, acquired := a.mediaByteLimiter().tryAcquire(mediaMemoryReservation(storageFormat, media.SizeBytes))
	if !acquired {
		return nil, errMediaMemoryBudget
	}
	started := time.Now()
	defer func() { a.observeMediaOperation("media_materialize", media.SizeBytes, started, operationErr) }()

	file, err := os.CreateTemp(a.mediaSpoolDir(), "gateway-media-read-*")
	if err != nil {
		release()
		return nil, fmt.Errorf("create media read spool: %w", err)
	}
	result = &materializedContentMedia{file: file, path: file.Name(), release: release}
	cleanup := true
	defer func() {
		if cleanup {
			_ = result.Close()
			result = nil
		}
	}()

	var written int64
	switch storageFormat {
	case "inline_v1":
		plain, err := a.contentCipher.DecryptBytes(media.PayloadCipher)
		if err != nil {
			return nil, err
		}
		written, err = io.Copy(file, bytes.NewReader(plain))
		clear(plain)
		if err != nil {
			return nil, fmt.Errorf("write inline media spool: %w", err)
		}
	case "chunked_v1":
		var plain []byte
		err := a.store.ForEachContentMediaChunk(ctx, media.ID, func(index int, encrypted []byte, plainSize int) error {
			var err error
			plain, err = a.contentCipher.DecryptBytesWithAADInto(plain[:0], encrypted, contentMediaChunkAAD(media.ID, index))
			if err != nil {
				return fmt.Errorf("decrypt media chunk %d: %w", index, err)
			}
			if len(plain) != plainSize {
				clear(plain)
				return fmt.Errorf("media chunk %d contains %d bytes, want %d", index, len(plain), plainSize)
			}
			chunkWritten, err := file.Write(plain)
			written += int64(chunkWritten)
			clear(plain)
			if err != nil {
				return fmt.Errorf("write media chunk %d: %w", index, err)
			}
			if chunkWritten != len(plain) {
				return io.ErrShortWrite
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported content media storage format %q", storageFormat)
	}
	if media.SizeBytes > 0 && written != media.SizeBytes {
		return nil, fmt.Errorf("materialized media contains %d bytes, want %d", written, media.SizeBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind media read spool: %w", err)
	}
	cleanup = false
	return result, nil
}

func (a *App) readContentMediaBytes(ctx context.Context, media domain.ContentMediaRow, limit int64) ([]byte, error) {
	if limit <= 0 || media.SizeBytes <= 0 || media.SizeBytes > limit {
		return nil, errors.New("content media exceeds the read limit")
	}
	materializeReservation := mediaMemoryReservation(normalizedMediaStorageFormat(media.StorageFormat), media.SizeBytes)
	additionalReservation := media.SizeBytes - materializeReservation
	if additionalReservation > 0 {
		release, acquired := a.mediaByteLimiter().tryAcquire(additionalReservation)
		if !acquired {
			return nil, errMediaMemoryBudget
		}
		defer release()
	}
	materialized, err := a.materializeContentMedia(ctx, media)
	if err != nil {
		return nil, err
	}
	defer materialized.Close()
	payload, err := io.ReadAll(io.LimitReader(materialized, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != media.SizeBytes || int64(len(payload)) > limit {
		clear(payload)
		return nil, errors.New("materialized content media has an unexpected size")
	}
	return payload, nil
}

func (a *App) persistComfyMediaReader(ctx context.Context, capture *proxyContentCapture, reader io.Reader, sizeBytes int64, contentHash string) (operationErr error) {
	if capture == nil || reader == nil || sizeBytes <= 0 || capture.status < 200 || capture.status >= 300 {
		return nil
	}
	if a == nil || a.store == nil || a.contentCipher == nil {
		return errors.New("content media storage is unavailable")
	}
	if sizeBytes > maxArchivedGenerationMedia {
		return nil
	}
	used, err := a.store.ContentMediaBytesForUser(ctx, capture.userID)
	if err != nil || used+sizeBytes > maxMediaBytesPerUser {
		return err
	}
	eventID, err := a.store.ComfyOutputEventID(ctx, capture.userID, capture.mediaName,
		capture.mediaSubfolder, capture.mediaStorageType)
	if errors.Is(err, sql.ErrNoRows) {
		eventID, err = a.store.LatestComfyContentEventID(ctx, capture.userID)
	}
	if err != nil {
		return nil
	}

	release, acquired := a.mediaByteLimiter().tryAcquire(chunkedMediaMemoryReservation)
	if !acquired {
		return errMediaMemoryBudget
	}
	defer release()
	started := time.Now()
	defer func() { a.observeMediaOperation("media_encrypt_chunks", sizeBytes, started, operationErr) }()

	mimeType := strings.TrimSpace(capture.mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	writer, err := a.store.BeginContentMediaChunks(ctx, domain.ContentMediaRecord{
		EventID: eventID, MediaType: capture.mediaType, MIMEType: mimeType,
		OriginalName: capture.mediaName, Subfolder: capture.mediaSubfolder, StorageType: capture.mediaStorageType,
		SizeBytes: sizeBytes, ContentHash: contentHash,
		ExpiresAt: time.Now().Add(a.retentionPolicy().GenerationMedia),
	})
	if err != nil {
		return err
	}
	if writer.Skipped() {
		return nil
	}
	defer writer.Rollback()

	digest := sha256.New()
	buffer := make([]byte, store.ContentMediaChunkSize)
	var encrypted []byte
	defer func() {
		clear(buffer)
		clear(encrypted)
	}()
	remaining := sizeBytes
	for index := 0; remaining > 0; index++ {
		chunkSize := int64(len(buffer))
		if remaining < chunkSize {
			chunkSize = remaining
		}
		chunk := buffer[:int(chunkSize)]
		if _, err := io.ReadFull(reader, chunk); err != nil {
			return fmt.Errorf("read media chunk %d: %w", index, err)
		}
		_, _ = digest.Write(chunk)
		encrypted, err = a.contentCipher.EncryptBytesWithAADInto(encrypted[:0], chunk, contentMediaChunkAAD(writer.MediaID(), index))
		if err != nil {
			return err
		}
		if err := writer.WriteChunk(ctx, index, encrypted, len(chunk)); err != nil {
			clear(encrypted)
			return err
		}
		clear(encrypted)
		clear(chunk)
		remaining -= chunkSize
	}
	var extra [1]byte
	if count, err := reader.Read(extra[:]); count != 0 || err != nil && !errors.Is(err, io.EOF) {
		return errors.New("media reader contains more bytes than declared")
	}
	computedHash := hex.EncodeToString(digest.Sum(nil))
	if contentHash != "" && !strings.EqualFold(contentHash, computedHash) {
		return errors.New("media content hash changed while archiving")
	}
	if err := writer.Commit(ctx); err != nil {
		return err
	}
	if capture.mediaType == "image" {
		a.queueSensitiveMediaClassification()
	}
	return nil
}
