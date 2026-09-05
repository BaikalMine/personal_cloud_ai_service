package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/store"
)

type loraCaptionSource struct {
	Image       domain.LoraDatasetImage `json:"image"`
	TriggerWord string                  `json:"trigger_word"`
	ConceptType string                  `json:"concept_type"`
}
type loraCaptionJobView struct {
	domain.LoraCaptionJob
	domain.LoraCaptionResult
	Source  loraCaptionSource `json:"source"`
	Expires int64             `json:"expires"`
}

func (a *App) decodeLoraCaptionInput(job domain.LoraCaptionJob) (domain.LoraCaptionInput, error) {
	var input domain.LoraCaptionInput
	plain, err := a.contentCipher.DecryptBytes(job.InputCipher)
	if err != nil {
		return input, err
	}
	defer clear(plain)
	if err = json.Unmarshal(plain, &input); err != nil {
		return input, err
	}
	hash := sha256.Sum256([]byte(input.Instruction))
	if hex.EncodeToString(hash[:]) != job.InstructionVersion || input.InstructionVersion != job.InstructionVersion {
		return input, errors.New("caption instruction hash mismatch")
	}
	return input, nil
}
func (a *App) loraCaptionJobView(job domain.LoraCaptionJob) (loraCaptionJobView, error) {
	view := loraCaptionJobView{LoraCaptionJob: job, Expires: job.ExpiresAt.Unix()}
	input, err := a.decodeLoraCaptionInput(job)
	if err != nil {
		return view, err
	}
	view.Source = loraCaptionSource{Image: input.Image, TriggerWord: input.TriggerWord, ConceptType: input.ConceptType}
	if len(job.ResultCipher) > 0 {
		plain, err := a.contentCipher.DecryptBytes(job.ResultCipher)
		if err != nil {
			return view, err
		}
		defer clear(plain)
		err = json.Unmarshal(plain, &view.LoraCaptionResult)
		if err != nil {
			return view, err
		}
	}
	return view, nil
}
func (a *App) makeLoraCaptionJob(datasetID string, image domain.LoraDatasetImage, asset domain.LoraDatasetAsset, trigger, concept string) (domain.LoraCaptionJob, error) {
	var job domain.LoraCaptionJob
	if err := validateLoraTriggerWord(trigger); err != nil {
		return job, datasetInputError(err.Error())
	}
	if !validLoraConceptType(concept) {
		return job, datasetInputError("Выберите тип LoRA.")
	}
	instruction, version := promptassistant.LoraCaptionInstructionSnapshot()
	input := domain.LoraCaptionInput{TriggerWord: strings.TrimSpace(trigger), ConceptType: concept, Image: image, AssetHash: asset.Hash, Instruction: instruction, InstructionVersion: version}
	plain, err := json.Marshal(input)
	if err != nil {
		return job, err
	}
	defer clear(plain)
	keyData, err := json.Marshal(struct {
		Dataset string
		Input   domain.LoraCaptionInput
	}{datasetID, input})
	if err != nil {
		return job, err
	}
	defer clear(keyData)
	key := sha256.Sum256(keyData)
	cipher, err := a.contentCipher.EncryptBytes(plain)
	if err != nil {
		return job, err
	}
	return domain.LoraCaptionJob{ID: newRequestID(), DatasetID: datasetID, ImageID: image.ID, AssetID: asset.ID, RequestKey: hex.EncodeToString(key[:]), InstructionVersion: version, InputCipher: cipher}, nil
}
func (a *App) writeLoraCaptionJobs(w http.ResponseWriter, jobs []domain.LoraCaptionJob, status int) {
	views := []loraCaptionJobView{}
	for _, job := range jobs {
		view, err := a.loraCaptionJobView(job)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		views = append(views, view)
	}
	writeJSON(w, status, map[string]any{"jobs": views})
}

func (a *App) handleLoraDatasetCaptions(w http.ResponseWriter, r *http.Request, row domain.LoraDatasetRow) {
	if r.Method == http.MethodGet {
		jobs, err := a.store.ListLoraCaptionJobs(r.Context(), row.UserID, row.ID)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		a.writeLoraCaptionJobs(w, jobs, 200)
		return
	}
	var request struct {
		Revision  int64    `json:"revision"`
		ImageIDs  []string `json:"image_ids"`
		OnlyEmpty bool     `json:"only_empty"`
		Cancel    bool     `json:"cancel"`
	}
	if !readLoraDatasetJSON(w, r, &request) {
		return
	}
	if request.Cancel {
		count, err := a.store.CancelLoraCaptions(r.Context(), row.UserID, row.ID, "")
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"cancelled": count})
		return
	}
	if a.promptAssistant == nil || !a.promptAssistant.VisionConfigured() {
		writeGenerationError(w, 503, "Локальная vision-модель не настроена.")
		return
	}
	if row.Revision != request.Revision {
		writeLoraDatasetError(w, store.ErrLoraDatasetConflict)
		return
	}
	manifest, err := a.decodeLoraDatasetManifest(row.ManifestCipher)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	wanted := map[string]bool{}
	for _, id := range request.ImageIDs {
		if wanted[id] {
			writeLoraDatasetError(w, datasetInputError("Кадр выбран дважды."))
			return
		}
		wanted[id] = true
	}
	if len(wanted) == 0 || len(wanted) > 100 {
		writeLoraDatasetError(w, datasetInputError("Выберите от 1 до 100 кадров."))
		return
	}
	jobs := []domain.LoraCaptionJob{}
	for _, item := range manifest.Images {
		if !wanted[item.ID] {
			continue
		}
		delete(wanted, item.ID)
		if item.Excluded || (request.OnlyEmpty && strings.TrimSpace(item.Caption) != "") {
			continue
		}
		asset, err := a.store.LoraDatasetAsset(r.Context(), row.UserID, item.AssetID)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		job, err := a.makeLoraCaptionJob(row.ID, item, asset, manifest.Settings.TriggerWord, manifest.Settings.ConceptType)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		jobs = append(jobs, job)
	}
	if len(wanted) > 0 {
		writeLoraDatasetError(w, datasetInputError("Выбранный кадр больше не находится в наборе."))
		return
	}
	jobs, err = a.store.EnqueueLoraCaptions(r.Context(), row.UserID, row.ID, request.Revision, jobs)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	a.writeLoraCaptionJobs(w, jobs, 202)
}

// Compatibility endpoint for already-open pages; it now uses the same durable worker.
func (a *App) handleLoraTrainingCaption(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeGenerationError(w, 405, "метод не поддерживается")
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, 401, "требуется вход")
		return
	}
	if a.promptAssistant == nil || !a.promptAssistant.VisionConfigured() {
		writeGenerationError(w, 503, "локальная vision-модель не настроена")
		return
	}
	submission, err := a.readLoraCaptionSubmission(w, r)
	defer clear(submission.Image)
	if err != nil {
		status := 400
		if errors.Is(err, errLoraCaptionCSRF) {
			status = 403
		} else if errors.Is(err, errLoraCaptionTooLarge) {
			status = 413
		}
		writeGenerationError(w, status, err.Error())
		return
	}
	asset, err := a.persistLoraDatasetImage(r.Context(), user.ID, submission.Filename, bytes.NewReader(submission.Image))
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	job, err := a.makeLoraCaptionJob("", domain.LoraDatasetImage{AssetID: asset.ID}, asset, submission.TriggerWord, submission.ConceptType)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	jobs, err := a.store.EnqueueLoraCaptions(r.Context(), user.ID, "", 0, []domain.LoraCaptionJob{job})
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	view, err := a.loraCaptionJobView(jobs[0])
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	writeJSON(w, 202, view)
}

func (a *App) handleLoraTrainingCaptionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	user := a.currentUser(r)
	if user == nil {
		writeGenerationError(w, 401, "требуется вход")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/lora-training/caption/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !loraDatasetIDPattern.MatchString(parts[0]) {
		writeGenerationError(w, 404, "описание не найдено")
		return
	}
	job, err := a.store.LoraCaptionJob(r.Context(), user.ID, parts[0])
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		if !a.validCSRFValue(r, r.Header.Get("X-CSRF-Token")) {
			writeGenerationError(w, 403, "проверка безопасности не пройдена")
			return
		}
		switch parts[1] {
		case "cancel":
			_, err = a.store.CancelLoraCaptions(r.Context(), user.ID, "", job.ID)
			if err == nil {
				job, err = a.store.LoraCaptionJob(r.Context(), user.ID, job.ID)
			}
		case "retry":
			job, err = a.store.RetryLoraCaption(r.Context(), user.ID, job.ID)
		default:
			writeGenerationError(w, 404, "действие не найдено")
			return
		}
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
	} else if len(parts) != 1 || r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeGenerationError(w, 405, "метод не поддерживается")
		return
	}
	view, err := a.loraCaptionJobView(job)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	writeJSON(w, 200, view)
}
