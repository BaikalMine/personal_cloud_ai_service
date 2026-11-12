package gateway

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"net/http"

	"ai-access-gateway/internal/domain"
	xdraw "golang.org/x/image/draw"
)

// These measurements are advisory visual cues, not semantic image assessments.
// They are calculated on demand and never alter a dataset or training snapshot.
type loraDatasetQuality = domain.LoraDatasetQuality

func loraDatasetQualityAAD(asset domain.LoraDatasetAsset) []byte {
	return []byte("ai-access-gateway:lora-dataset:quality:v1:" + asset.ID + ":" + asset.Hash)
}

func (a *App) restoreLoraDatasetQuality(asset *domain.LoraDatasetAsset) {
	asset.Quality = nil
	if len(asset.QualityCipher) == 0 {
		return
	}
	data, err := a.contentCipher.DecryptBytesWithAAD(asset.QualityCipher, loraDatasetQualityAAD(*asset))
	if err != nil {
		return
	}
	defer clear(data)
	var quality domain.LoraDatasetQuality
	// Derived state can be recalculated; a corrupt cache must not hide the photo.
	if json.Unmarshal(data, &quality) == nil && quality.Valid() {
		asset.Quality = &quality
	}
}

// MeasureLoraDatasetQuality measures a decoded image without retaining its pixels.
// Callers must enforce upload/decode limits before calling it; the preview uses
// the same measurements as the production dataset handler.
func MeasureLoraDatasetQuality(source image.Image) loraDatasetQuality {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	scale := math.Min(1, 256/float64(max(width, height)))
	width, height = max(3, int(float64(width)*scale)), max(3, int(float64(height)*scale))
	preview := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(preview, preview.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	xdraw.ApproxBiLinear.Scale(preview, preview.Bounds(), source, bounds, draw.Over, nil)
	luma := make([]float64, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := preview.RGBAAt(x, y)
			luma[y*width+x] = .299*float64(c.R) + .587*float64(c.G) + .114*float64(c.B)
		}
	}
	var sum, squares float64
	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			i := y*width + x
			value := luma[i-1] + luma[i+1] + luma[i-width] + luma[i+width] - 4*luma[i]
			sum += value
			squares += value * value
		}
	}
	count := float64((width - 2) * (height - 2))
	variance := math.Max(0, squares/count-math.Pow(sum/count, 2))
	result := loraDatasetQuality{Version: 1, DetailScore: math.Round(variance*100) / 100, LowDetail: variance < 12, Colors: make([]uint16, 192)}
	// Average whole cells, not selected pixels: avoid treating shifted texture
	// samples as a reliable sign of different composition or background.
	for cy := 0; cy < 8; cy++ {
		for cx := 0; cx < 8; cx++ {
			var channels [3]int
			n := 0
			for y := cy * height / 8; y < max(cy*height/8+1, (cy+1)*height/8); y++ {
				for x := cx * width / 8; x < max(cx*width/8+1, (cx+1)*width/8); x++ {
					c := preview.RGBAAt(x, y)
					channels[0] += int(c.R)
					channels[1] += int(c.G)
					channels[2] += int(c.B)
					n++
				}
			}
			for channel, total := range channels {
				result.Colors[(cy*8+cx)*3+channel] = uint16(total / n)
			}
		}
	}
	return result
}

func (a *App) analyzeLoraDatasetAsset(ctx context.Context, asset domain.LoraDatasetAsset) (loraDatasetQuality, error) {
	var empty loraDatasetQuality
	file, err := a.materializeLoraDatasetAsset(ctx, asset)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil || datasetImageMIME(format) != asset.MIMEType || config.Width != asset.Width || config.Height != asset.Height {
		return empty, datasetInputError("Не удалось прочитать изображение для проверки качества.")
	}
	pixels := int64(config.Width) * int64(config.Height)
	if config.Width < 256 || config.Height < 256 || config.Width > 16384 || config.Height > 16384 || pixels > loraDatasetMaxPixels {
		return empty, datasetInputError("Изображение превышает допустимые размеры для проверки.")
	}
	release, acquired := a.mediaByteLimiter().tryAcquire(pixels*8 + 1<<20)
	if !acquired {
		return empty, errMediaMemoryBudget
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return empty, err
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return empty, datasetInputError("Изображение повреждено. Загрузите исходный файл заново.")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return MeasureLoraDatasetQuality(decoded), nil
}

func (a *App) handleLoraDatasetQuality(w http.ResponseWriter, r *http.Request, assetID string) {
	asset, err := a.store.LoraDatasetAsset(r.Context(), a.currentUser(r).ID, assetID)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	a.restoreLoraDatasetQuality(&asset)
	if asset.Quality.Valid() {
		writeJSON(w, http.StatusOK, map[string]any{"asset_id": asset.ID, "sha256": asset.Hash, "quality": asset.Quality})
		return
	}
	quality, err := a.analyzeLoraDatasetAsset(r.Context(), asset)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	data, err := json.Marshal(quality)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	defer clear(data)
	cipher, err := a.contentCipher.EncryptBytesWithAAD(data, loraDatasetQualityAAD(asset))
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	if err := a.store.SaveLoraDatasetQuality(r.Context(), asset, cipher); err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"asset_id": asset.ID, "sha256": asset.Hash, "quality": quality})
}
