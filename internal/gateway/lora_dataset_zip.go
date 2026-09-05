package gateway

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type loraDatasetZIPImage struct {
	File     string `json:"file"`
	Caption  string `json:"caption"`
	Excluded bool   `json:"excluded"`
}
type loraDatasetZIPManifest struct {
	Version  int                        `json:"version"`
	Settings domain.LoraDatasetSettings `json:"settings"`
	Images   []loraDatasetZIPImage      `json:"images"`
}

func datasetImageExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func (a *App) exportLoraDataset(ctx context.Context, row domain.LoraDatasetRow) (*os.File, error) {
	manifest, err := a.decodeLoraDatasetManifest(row.ManifestCipher)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(a.mediaSpoolDir(), "gateway-dataset-export-*.zip")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			file.Close()
			os.Remove(file.Name())
		}
	}()
	writer := zip.NewWriter(file)
	defer writer.Close()
	portable := loraDatasetZIPManifest{Version: 1, Settings: manifest.Settings, Images: []loraDatasetZIPImage{}}
	for index, item := range manifest.Images {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		asset, err := a.store.LoraDatasetAsset(ctx, row.UserID, item.AssetID)
		if err != nil {
			return nil, err
		}
		ext := datasetImageExtension(asset.MIMEType)
		if ext == "" {
			return nil, errors.New("unsupported dataset image")
		}
		stem := fmt.Sprintf("images/%04d", index+1)
		err = func() error {
			image, err := a.materializeLoraDatasetAsset(ctx, asset)
			if err != nil {
				return err
			}
			defer image.Close()
			entry, err := writer.CreateHeader(&zip.FileHeader{Name: stem + ext, Method: zip.Store})
			if err != nil {
				return err
			}
			_, err = io.Copy(entry, image)
			return err
		}()
		if err != nil {
			return nil, err
		}
		caption, err := writer.Create(stem + ".txt")
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(caption, item.Caption); err != nil {
			return nil, err
		}
		portable.Images = append(portable.Images, loraDatasetZIPImage{File: stem + ext, Caption: stem + ".txt", Excluded: item.Excluded})
	}
	entry, err := writer.Create("dataset.json")
	if err != nil {
		return nil, err
	}
	if err = json.NewEncoder(entry).Encode(portable); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	keep = true
	return file, nil
}

func (a *App) handleLoraDatasetExport(w http.ResponseWriter, r *http.Request, row domain.LoraDatasetRow) {
	file, err := a.exportLoraDataset(r.Context(), row)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "dataset.zip"}))
	http.ServeContent(w, r, "dataset.zip", row.UpdatedAt, file)
}

func loraDatasetZIPEntries(archive *zip.Reader) (map[string]*zip.File, error) {
	if len(archive.File) > 401 {
		return nil, datasetInputError("В ZIP слишком много файлов.")
	}
	entries := map[string]*zip.File{}
	folded := map[string]bool{}
	var total uint64
	for _, entry := range archive.File {
		name := entry.Name
		if strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || name == "." || name == ".." || strings.HasPrefix(name, "../") || len(name) > 1024 || entry.Mode()&os.ModeSymlink != 0 {
			return nil, datasetInputError("ZIP содержит недопустимые пути или ссылки.")
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		key := strings.ToLower(name)
		if folded[key] {
			return nil, datasetInputError("В ZIP повторяются имена файлов.")
		}
		folded[key] = true
		if entry.UncompressedSize64 > uint64(maxLoraTrainingImageBytes) {
			return nil, datasetInputError("Файл внутри ZIP превышает 24 МБ.")
		}
		total += entry.UncompressedSize64
		if total > uint64(domain.LoraDatasetMaxBytes+2<<20) {
			return nil, store.ErrLoraDatasetQuota
		}
		entries[name] = entry
	}
	return entries, nil
}

func readLoraDatasetZIPEntry(entry *zip.File, limit int64) ([]byte, error) {
	if entry == nil || entry.UncompressedSize64 > uint64(limit) {
		return nil, datasetInputError("Файл отсутствует в ZIP или превышает допустимый размер.")
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, datasetInputError("Не удалось открыть файл в ZIP.")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, datasetInputError("ZIP повреждён или содержит слишком большой файл.")
	}
	return data, nil
}

func (a *App) importLoraDataset(ctx context.Context, row domain.LoraDatasetRow, revision int64, archive *zip.Reader) (domain.LoraDatasetRow, error) {
	if row.Revision != revision {
		return row, store.ErrLoraDatasetConflict
	}
	entries, err := loraDatasetZIPEntries(archive)
	if err != nil {
		return row, err
	}
	current, err := a.decodeLoraDatasetManifest(row.ManifestCipher)
	if err != nil {
		return row, err
	}
	portable := loraDatasetZIPManifest{Version: 1, Settings: current.Settings}
	if entry := entries["dataset.json"]; entry != nil {
		data, err := readLoraDatasetZIPEntry(entry, loraDatasetManifestBytes)
		if err != nil {
			return row, err
		}
		if err = json.Unmarshal(data, &portable); err != nil || portable.Version != 1 {
			return row, datasetInputError("Неизвестный формат dataset.json.")
		}
	} else {
		names := []string{}
		for name := range entries {
			switch strings.ToLower(path.Ext(name)) {
			case ".png", ".jpg", ".jpeg", ".webp":
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			caption := strings.TrimSuffix(name, path.Ext(name)) + ".txt"
			if entries[caption] == nil {
				caption = ""
			}
			portable.Images = append(portable.Images, loraDatasetZIPImage{File: name, Caption: caption})
		}
	}
	if len(portable.Images) > domain.LoraDatasetMaxImages {
		return row, store.ErrLoraDatasetQuota
	}
	if len(portable.Images) == 0 {
		return row, datasetInputError("В ZIP нет изображений.")
	}
	manifest := domain.LoraDatasetManifest{Version: 1, Settings: portable.Settings, Images: []domain.LoraDatasetImage{}}
	if err := validateLoraDatasetManifest(manifest); err != nil {
		return row, err
	}
	// Validate the complete caption map before persisting any new image.
	captions := make([]string, len(portable.Images))
	var declared uint64
	for index, item := range portable.Images {
		entry := entries[item.File]
		if entry == nil {
			return row, datasetInputError("В ZIP отсутствует изображение из dataset.json.")
		}
		declared += entry.UncompressedSize64
		if declared > uint64(domain.LoraDatasetMaxBytes) {
			return row, store.ErrLoraDatasetQuota
		}
		if item.Caption != "" {
			data, err := readLoraDatasetZIPEntry(entries[item.Caption], 4000)
			if err != nil {
				return row, err
			}
			if !utf8.Valid(data) || utf8.RuneCount(data) > 1000 || strings.ContainsRune(string(data), 0) {
				return row, datasetInputError("Подпись должна быть UTF-8, не больше 1000 символов.")
			}
			captions[index] = string(data)
		}
	}
	ids := make([]string, 0, len(portable.Images))
	for index, item := range portable.Images {
		if err := ctx.Err(); err != nil {
			return row, err
		}
		reader, err := entries[item.File].Open()
		if err != nil {
			return row, datasetInputError("Не удалось прочитать изображение в ZIP.")
		}
		asset, err := a.persistLoraDatasetImage(ctx, row.UserID, path.Base(item.File), reader)
		reader.Close()
		if err != nil {
			return row, err
		}
		ids = append(ids, asset.ID)
		manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: newRequestID(), AssetID: asset.ID, Caption: captions[index], Excluded: item.Excluded})
	}
	cipher, _, err := a.encryptLoraDatasetManifest(manifest)
	if err != nil {
		return row, err
	}
	return a.store.SaveLoraDataset(ctx, row.UserID, row.ID, revision, manifest.Settings.Name, cipher, ids)
}

func (a *App) handleLoraDatasetImport(w http.ResponseWriter, r *http.Request, row domain.LoraDatasetRow) {
	revision, err := strconv.ParseInt(r.Header.Get("X-Dataset-Revision"), 10, 64)
	if err != nil || revision != row.Revision {
		writeLoraDatasetError(w, store.ErrLoraDatasetConflict)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, domain.LoraDatasetMaxBytes+4<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		writeLoraDatasetError(w, datasetInputError("Не удалось прочитать ZIP."))
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "archive" {
		writeLoraDatasetError(w, datasetInputError("Выберите один ZIP-архив."))
		return
	}
	defer part.Close()
	file, err := os.CreateTemp(a.mediaSpoolDir(), "gateway-dataset-import-*.zip")
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	const maxArchiveBytes = domain.LoraDatasetMaxBytes + 2<<20
	size, err := io.Copy(file, io.LimitReader(part, maxArchiveBytes+1))
	if err != nil {
		writeLoraDatasetError(w, datasetInputError("Загрузка ZIP прервана."))
		return
	}
	if size > maxArchiveBytes {
		writeLoraDatasetError(w, datasetInputError("ZIP превышает 514 МБ."))
		return
	}
	if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
		writeLoraDatasetError(w, datasetInputError("Выберите один ZIP-архив размером до 514 МБ."))
		return
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		writeLoraDatasetError(w, datasetInputError("Не удалось открыть ZIP. Проверьте архив."))
		return
	}
	row, err = a.importLoraDataset(r.Context(), row, revision, archive)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	a.writeLoraDatasetView(w, r, row, http.StatusOK)
}
