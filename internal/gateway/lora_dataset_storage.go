package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path"
	"strings"

	"ai-access-gateway/internal/domain"
)

const loraDatasetChunkBytes = 1 << 20
const loraDatasetMaxPixels = 32_000_000

type loraDatasetInputError struct{ message string }

func (err *loraDatasetInputError) Error() string { return err.message }
func datasetInputError(message string) error     { return &loraDatasetInputError{message} }

func loraDatasetChunkAAD(id string, index int) []byte {
	return []byte(fmt.Sprintf("ai-access-gateway:lora-dataset:chunk:v1:%s:%d", id, index))
}

func datasetImageMIME(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func (a *App) persistLoraDatasetImage(ctx context.Context, userID int64, name string, reader io.Reader) (domain.LoraDatasetAsset, error) {
	var asset domain.LoraDatasetAsset
	release, acquired := a.mediaByteLimiter().tryAcquire(chunkedMediaMemoryReservation)
	if !acquired {
		return asset, errMediaMemoryBudget
	}
	defer release()
	file, err := os.CreateTemp(a.mediaSpoolDir(), "gateway-dataset-upload-*")
	if err != nil {
		return asset, err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	hash := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(file, hash), io.LimitReader(reader, maxLoraTrainingImageBytes+1), make([]byte, 64<<10))
	if err != nil {
		return asset, err
	}
	if size == 0 || size > maxLoraTrainingImageBytes {
		return asset, datasetInputError("Изображение должно быть не больше 24 МБ.")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return asset, err
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil || datasetImageMIME(format) == "" {
		return asset, datasetInputError("Не удалось прочитать изображение. Поддерживаются PNG, JPG и WebP.")
	}
	pixels := int64(config.Width) * int64(config.Height)
	if config.Width < 256 || config.Height < 256 || config.Width > 16384 || config.Height > 16384 || pixels > loraDatasetMaxPixels {
		return asset, datasetInputError("Сторона изображения: от 256 до 16384 пикселей, всего не более 32 мегапикселей.")
	}
	// Decode one image at a time before committing it. A valid header alone
	// does not prove the full image is readable by the trainer.
	decodeRelease, acquired := a.mediaByteLimiter().tryAcquire(pixels * 8)
	if !acquired {
		return asset, errMediaMemoryBudget
	}
	decodeErr := func() error {
		defer decodeRelease()
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		decoded, decodedFormat, err := image.Decode(file)
		if err != nil || decodedFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
			return datasetInputError("Изображение повреждено или загружено не полностью.")
		}
		return nil
	}()
	if decodeErr != nil {
		return asset, decodeErr
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return asset, err
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" || len(name) > 1024 {
		name = "image." + format
	}
	asset = domain.LoraDatasetAsset{ID: newRequestID(), UserID: userID, Name: name, Hash: hex.EncodeToString(hash.Sum(nil)),
		MIMEType: datasetImageMIME(format), SizeBytes: size, Width: config.Width, Height: config.Height}
	plain := make([]byte, loraDatasetChunkBytes)
	defer clear(plain)
	index := 0
	return a.store.InsertLoraDatasetAsset(ctx, asset, func() ([]byte, int, error) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		n, err := io.ReadFull(file, plain)
		if errors.Is(err, io.EOF) {
			return nil, 0, io.EOF
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, 0, err
		}
		cipher, err := a.contentCipher.EncryptBytesWithAAD(plain[:n], loraDatasetChunkAAD(asset.ID, index))
		index++
		return cipher, n, err
	})
}

func (a *App) materializeLoraDatasetAsset(ctx context.Context, asset domain.LoraDatasetAsset) (*materializedContentMedia, error) {
	release, acquired := a.mediaByteLimiter().tryAcquire(chunkedMediaMemoryReservation)
	if !acquired {
		return nil, errMediaMemoryBudget
	}
	file, err := os.CreateTemp(a.mediaSpoolDir(), "gateway-dataset-read-*")
	if err != nil {
		release()
		return nil, err
	}
	result := &materializedContentMedia{file: file, path: file.Name(), release: release}
	ok := false
	defer func() {
		if !ok {
			result.Close()
		}
	}()
	hash := sha256.New()
	var total int64
	var plain []byte
	defer func() { clear(plain) }()
	err = a.store.ForEachLoraDatasetAssetChunk(ctx, asset.UserID, asset.ID, func(index int, cipher []byte, size int) error {
		var err error
		plain, err = a.contentCipher.DecryptBytesWithAADInto(plain[:0], cipher, loraDatasetChunkAAD(asset.ID, index))
		if err != nil {
			return err
		}
		if len(plain) != size || total+int64(size) > asset.SizeBytes {
			return io.ErrUnexpectedEOF
		}
		n, err := io.MultiWriter(file, hash).Write(plain)
		total += int64(n)
		clear(plain)
		return err
	})
	if err != nil {
		return nil, err
	}
	if total != asset.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != asset.Hash {
		return nil, errors.New("dataset image integrity check failed")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return result, nil
}
