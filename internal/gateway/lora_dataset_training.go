package gateway

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func loraDatasetTrainingForm(settings domain.LoraDatasetSettings) loraTrainingForm {
	return loraTrainingForm{ProfileID: settings.ProfileID, Name: settings.Name, OutputName: settings.OutputName,
		TriggerWord: settings.TriggerWord, ConceptType: settings.ConceptType, Preset: settings.Preset,
		Resolution: settings.Resolution, Caption: settings.GlobalCaption}
}

func loraDatasetTrainingCaptions(manifest domain.LoraDatasetManifest) ([]domain.LoraDatasetImage, []string, error) {
	images := make([]domain.LoraDatasetImage, 0, len(manifest.Images))
	values := make([]string, 0, len(manifest.Images))
	for _, item := range manifest.Images {
		if !item.Excluded {
			images = append(images, item)
			values = append(values, item.Caption)
		}
	}
	if len(images) < minLoraTrainingImages || len(images) > maxLoraTrainingImages {
		return nil, nil, datasetInputError("Включите от 5 до 100 изображений для обучения.")
	}
	captions, err := trainingCaptions(values, manifest.Settings.GlobalCaption, manifest.Settings.TriggerWord, len(images))
	if err != nil {
		return nil, nil, datasetInputError(err.Error())
	}
	return images, captions, nil
}

func (a *App) writeLoraDatasetTrainingArchive(ctx context.Context, snapshot domain.LoraDatasetSnapshot, target string) (int, error) {
	manifest, err := a.decodeLoraDatasetManifest(snapshot.ManifestCipher)
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256(encoded)
	clear(encoded)
	if hex.EncodeToString(hash[:]) != snapshot.Hash {
		return 0, errors.New("dataset version integrity check failed")
	}
	images, captions, err := loraDatasetTrainingCaptions(manifest)
	if err != nil {
		return 0, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	success := false
	defer func() {
		file.Close()
		if !success {
			os.Remove(target)
		}
	}()
	archive := zip.NewWriter(file)
	defer archive.Close()
	var total int64
	for index, item := range images {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		asset, err := a.store.LoraDatasetAsset(ctx, snapshot.UserID, item.AssetID)
		if err != nil {
			return 0, err
		}
		total += asset.SizeBytes
		if total > domain.LoraDatasetMaxBytes {
			return 0, store.ErrLoraDatasetQuota
		}
		extension := ""
		switch asset.MIMEType {
		case "image/png":
			extension = ".png"
		case "image/jpeg":
			extension = ".jpg"
		case "image/webp":
			extension = ".webp"
		default:
			return 0, errors.New("unsupported dataset image format")
		}
		stem := fmt.Sprintf("images/%04d", index+1)
		err = func() error {
			image, err := a.materializeLoraDatasetAsset(ctx, asset)
			if err != nil {
				return err
			}
			defer image.Close()
			header := &zip.FileHeader{Name: stem + extension, Method: zip.Store}
			header.SetMode(0o600)
			entry, err := archive.CreateHeader(header)
			if err != nil {
				return err
			}
			_, err = io.Copy(entry, image)
			return err
		}()
		if err != nil {
			return 0, err
		}
		header := &zip.FileHeader{Name: stem + ".txt", Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return 0, err
		}
		if _, err := io.WriteString(entry, captions[index]); err != nil {
			return 0, err
		}
	}
	if err := archive.Close(); err != nil {
		return 0, err
	}
	if err := file.Close(); err != nil {
		return 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return 0, err
	}
	if info.Size() > domain.LoraDatasetMaxBytes {
		return 0, store.ErrLoraDatasetQuota
	}
	success = true
	return len(images), nil
}

func (a *App) handleLoraDatasetTraining(w http.ResponseWriter, r *http.Request, row domain.LoraDatasetRow, revision int64) {
	if row.Revision != revision {
		writeLoraDatasetError(w, store.ErrLoraDatasetConflict)
		return
	}
	manifest, err := a.decodeLoraDatasetManifest(row.ManifestCipher)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	form := loraDatasetTrainingForm(manifest.Settings)
	if err := validateLoraTrainingForm(form); err != nil {
		writeLoraDatasetError(w, datasetInputError(err.Error()))
		return
	}
	if _, _, err := loraDatasetTrainingCaptions(manifest); err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	preset, ok := loraTrainingPresetByID(form.Preset)
	if !ok {
		writeLoraDatasetError(w, datasetInputError("Выберите пресет обучения."))
		return
	}
	profile, err := a.readyLoraTrainingProfile(r.Context(), form.ProfileID)
	if err != nil {
		writeGenerationError(w, http.StatusConflict, err.Error())
		return
	}
	_, hash, err := a.encryptLoraDatasetManifest(manifest)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	user := a.currentUser(r)
	snapshot, err := a.store.CreateLoraDatasetSnapshot(r.Context(), user.ID, row.ID, revision, newRequestID(), hash)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	publicID := newRequestID()
	workspace := filepath.Join(a.mediaSpoolDir(), "lora-training", publicID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(workspace)
		}
	}()
	archivePath := filepath.Join(workspace, "dataset.zip")
	count, err := a.writeLoraDatasetTrainingArchive(r.Context(), snapshot, archivePath)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	job, err := a.store.CreateLoraTrainingJob(r.Context(), domain.CreateLoraTrainingJobParams{
		PublicID: publicID, UserID: user.ID, UsernameSnapshot: user.Username, RequestID: publicID,
		ProfileID: profile.ID, Family: profile.Family, BaseModel: profile.BaseModel, Name: form.Name, OutputName: form.OutputName,
		TriggerWord: form.TriggerWord, ConceptType: form.ConceptType, Preset: preset.ID, Resolution: form.Resolution,
		MaxTrainSteps: preset.Steps, NetworkDim: preset.NetworkDim, NetworkAlpha: preset.NetworkAlpha, LearningRate: preset.LearningRate,
		Seed: randomTrainingSeed(), SampleCount: count, DatasetBytes: info.Size(), DatasetPath: archivePath,
		DatasetSnapshotID: snapshot.ID, DatasetSnapshotHash: snapshot.Hash,
	})
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	keep = true
	a.audit(r.Context(), &user.ID, "lora_training_created", "lora_training_job", &job.ID, a.clientIP(r), r.UserAgent(), map[string]any{
		"profile_id": profile.ID, "family": profile.Family, "samples": count, "preset": preset.ID, "resolution": form.Resolution, "dataset_snapshot_id": snapshot.ID, "dataset_snapshot_hash": snapshot.Hash,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"job": loraTrainingJSON(job, nil, false), "version": snapshot})
}
