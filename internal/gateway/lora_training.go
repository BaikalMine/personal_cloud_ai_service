package gateway

import (
	"archive/zip"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"

	_ "golang.org/x/image/webp"
)

const (
	maxLoraTrainingRequestBytes = (512 << 20) + (2 << 20)
	maxLoraTrainingImageBytes   = 24 << 20
	maxLoraTrainingImages       = 100
	minLoraTrainingImages       = 5
)

var loraOutputNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)

type loraTrainingPreset struct {
	ID           string
	Name         string
	Description  string
	Steps        int
	NetworkDim   int
	NetworkAlpha int
	LearningRate float64
}

var loraTrainingPresets = []loraTrainingPreset{
	{ID: "quick", Name: "Пробный", Description: "800 шагов · rank 16. Быстрая проверка датасета и триггера.", Steps: 800, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001},
	{ID: "balanced", Name: "Основной", Description: "1600 шагов · rank 32. Базовый вариант для персонажа, объекта или стиля.", Steps: 1600, NetworkDim: 32, NetworkAlpha: 32, LearningRate: 0.0001},
	{ID: "detailed", Name: "Детальный", Description: "2800 шагов · rank 32. Для чистого датасета с разнообразными ракурсами.", Steps: 2800, NetworkDim: 32, NetworkAlpha: 32, LearningRate: 0.00008},
}

type loraTrainingForm struct {
	ProfileID   string
	Name        string
	OutputName  string
	TriggerWord string
	ConceptType string
	Preset      string
	Resolution  int
	Caption     string
}

type loraTrainingJobView struct {
	domain.LoraTrainingJob
	FamilyLabel  string
	ConceptLabel string
	PresetLabel  string
	StateLabel   string
	StateClass   string
	CanCancel    bool
	CanDownload  bool
}

type loraTrainingJobJSON struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	OutputName    string   `json:"output_name"`
	ProfileID     string   `json:"profile_id"`
	Family        string   `json:"family"`
	FamilyLabel   string   `json:"family_label"`
	BaseModel     string   `json:"base_model"`
	State         string   `json:"state"`
	StateLabel    string   `json:"state_label"`
	StateClass    string   `json:"state_class"`
	Stage         string   `json:"stage"`
	Progress      int      `json:"progress"`
	Message       string   `json:"message"`
	Error         string   `json:"error,omitempty"`
	LogTail       []string `json:"log_tail,omitempty"`
	SampleCount   int      `json:"sample_count"`
	ConceptLabel  string   `json:"concept_label"`
	PresetLabel   string   `json:"preset_label"`
	Resolution    int      `json:"resolution"`
	MaxTrainSteps int      `json:"max_train_steps"`
	CanCancel     bool     `json:"can_cancel"`
	CanDownload   bool     `json:"can_download"`
	DownloadURL   string   `json:"download_url,omitempty"`
	CancelURL     string   `json:"cancel_url,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
	ArtifactName  string   `json:"artifact_name,omitempty"`
	ArtifactBytes int64    `json:"artifact_bytes,omitempty"`
	Username      string   `json:"username,omitempty"`
}

type uploadedTrainingImage struct {
	Path         string
	Extension    string
	OriginalName string
	Width        int
	Height       int
}

func (a *App) registerLoraTrainingRoutes(mux *http.ServeMux) {
	access := func(next http.Handler) http.Handler {
		return a.requireAuth(a.requireLoraTrainingAccess(next))
	}
	mux.Handle("/train-lora", access(http.HandlerFunc(a.handleLoraTraining)))
	mux.Handle("/train-lora/", access(http.HandlerFunc(a.handleLoraTrainingAction)))
	mux.Handle("/api/lora-training/jobs", access(http.HandlerFunc(a.handleLoraTrainingJobsAPI)))
	mux.Handle("/api/lora-training/jobs/", access(http.HandlerFunc(a.handleLoraTrainingJobAPI)))
}

func (a *App) requireLoraTrainingAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if user == nil || (user.Role != "admin" && !user.CanTrainImageLora) {
			a.renderStatus(w, r, http.StatusForbidden, "service_forbidden", map[string]any{
				"Title": "Доступ закрыт", "Service": "обучению LoRA для изображений",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleLoraTraining(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/train-lora" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.renderLoraTrainingPage(w, r, http.StatusOK, defaultLoraTrainingForm(), "", loraTrainingPageMessage(r))
	case http.MethodPost:
		a.createLoraTrainingJob(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAdminLoraTraining(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	profiles, agentMessage := a.loraTrainingProfiles(r.Context())
	jobs, err := a.store.ListLoraTrainingJobs(r.Context(), 200)
	if err != nil {
		http.Error(w, "не удалось загрузить задания обучения", http.StatusInternalServerError)
		return
	}
	readyProfiles := 0
	activeJobs := 0
	completedJobs := 0
	failedJobs := 0
	for _, profile := range profiles {
		if profile.Ready {
			readyProfiles++
		}
	}
	views := make([]loraTrainingJobView, 0, len(jobs))
	for _, job := range jobs {
		views = append(views, newLoraTrainingJobView(job))
		switch {
		case !job.State.Terminal():
			activeJobs++
		case job.State == domain.LoraTrainingCompleted:
			completedJobs++
		case job.State == domain.LoraTrainingFailed:
			failedJobs++
		}
	}
	a.render(w, r, "admin_lora_training", map[string]any{
		"Title": "Обучение LoRA", "Profiles": profiles, "ReadyProfiles": readyProfiles,
		"AgentMessage": agentMessage, "Jobs": views, "ActiveJobs": activeJobs,
		"CompletedJobs": completedJobs, "FailedJobs": failedJobs, "Cancelled": r.URL.Query().Get("cancelled") == "1",
	})
}

func (a *App) handleAdminLoraTrainingAction(w http.ResponseWriter, r *http.Request, rawPath string) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	job, err := a.store.LoraTrainingJobByPublicID(r.Context(), parts[0], 0, true)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "download":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		a.downloadLoraTrainingArtifact(w, r, job)
	case "cancel":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		if !a.validCSRF(r) {
			http.Error(w, "неверный защитный токен", http.StatusForbidden)
			return
		}
		admin := a.currentUser(r)
		job, err = a.store.RequestLoraTrainingCancellation(r.Context(), job.PublicID, 0, true)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "не удалось отменить обучение", http.StatusInternalServerError)
			return
		}
		if err == nil && job.AgentJobID != "" && a.loraTraining != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			_, _ = a.loraTraining.Cancel(ctx, job.AgentJobID)
			cancel()
		}
		if err == nil && job.State.Terminal() {
			a.removeLoraTrainingDataset(job)
			a.releaseMiningPauseForLoraTraining(r.Context(), job.ID)
		}
		a.audit(r.Context(), &admin.ID, "lora_training_cancel_requested", "lora_training_job", &job.ID, a.clientIP(r), r.UserAgent(), map[string]any{"public_id": job.PublicID, "owner": job.UsernameSnapshot})
		http.Redirect(w, r, "/admin/lora-training?cancelled=1", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) handleLoraTrainingAction(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/train-lora/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	admin := user.Role == "admin"
	job, err := a.store.LoraTrainingJobByPublicID(r.Context(), parts[0], user.ID, admin)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "cancel":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		if !a.validCSRF(r) {
			http.Error(w, "неверный защитный токен", http.StatusForbidden)
			return
		}
		job, err = a.store.RequestLoraTrainingCancellation(r.Context(), job.PublicID, user.ID, admin)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "не удалось отменить обучение", http.StatusInternalServerError)
			return
		}
		if err == nil && job.AgentJobID != "" && a.loraTraining != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			_, _ = a.loraTraining.Cancel(ctx, job.AgentJobID)
			cancel()
		}
		if err == nil && job.State.Terminal() {
			a.removeLoraTrainingDataset(job)
			a.releaseMiningPauseForLoraTraining(r.Context(), job.ID)
		}
		a.audit(r.Context(), &user.ID, "lora_training_cancel_requested", "lora_training_job", &job.ID, a.clientIP(r), r.UserAgent(), map[string]any{"public_id": job.PublicID})
		http.Redirect(w, r, "/train-lora?cancelled=1#training-history", http.StatusFound)
	case "download":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		a.downloadLoraTrainingArtifact(w, r, job)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) renderLoraTrainingPage(w http.ResponseWriter, r *http.Request, status int, form loraTrainingForm, errorMessage, message string) {
	user := a.currentUser(r)
	profiles, agentMessage := a.loraTrainingProfiles(r.Context())
	jobs, err := a.store.ListLoraTrainingJobsByUser(r.Context(), user.ID, 30)
	if err != nil {
		http.Error(w, "не удалось загрузить историю обучения", http.StatusInternalServerError)
		return
	}
	readyProfiles := 0
	for _, profile := range profiles {
		if profile.Ready {
			readyProfiles++
		}
	}
	views := make([]loraTrainingJobView, 0, len(jobs))
	for _, job := range jobs {
		views = append(views, newLoraTrainingJobView(job))
	}
	a.renderStatus(w, r, status, "lora_training", map[string]any{
		"Title": "Обучение LoRA", "Profiles": profiles, "ReadyProfiles": readyProfiles,
		"AgentMessage": agentMessage, "Presets": loraTrainingPresets, "Form": form,
		"Jobs": views, "Error": errorMessage, "Message": message,
	})
}

func (a *App) createLoraTrainingJob(w http.ResponseWriter, r *http.Request) {
	workspace := filepath.Join(a.cfg.MediaSpoolDir, "lora-training", "staging-"+newRequestID())
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		http.Error(w, "не удалось подготовить хранилище датасета", http.StatusInternalServerError)
		return
	}
	keepWorkspace := false
	defer func() {
		if !keepWorkspace {
			_ = os.RemoveAll(workspace)
		}
	}()

	form, fields, images, err := a.readLoraTrainingSubmission(w, r, workspace)
	if err != nil {
		a.renderLoraTrainingPage(w, r, http.StatusBadRequest, form, err.Error(), "")
		return
	}
	profile, err := a.readyLoraTrainingProfile(r.Context(), form.ProfileID)
	if err != nil {
		a.renderLoraTrainingPage(w, r, http.StatusConflict, form, err.Error(), "")
		return
	}
	preset, ok := loraTrainingPresetByID(form.Preset)
	if !ok {
		a.renderLoraTrainingPage(w, r, http.StatusBadRequest, form, "Выберите пресет обучения.", "")
		return
	}
	if err := validateLoraTrainingForm(form); err != nil {
		a.renderLoraTrainingPage(w, r, http.StatusBadRequest, form, err.Error(), "")
		return
	}
	if len(images) < minLoraTrainingImages || len(images) > maxLoraTrainingImages {
		a.renderLoraTrainingPage(w, r, http.StatusBadRequest, form, fmt.Sprintf("Добавьте от %d до %d изображений.", minLoraTrainingImages, maxLoraTrainingImages), "")
		return
	}
	captions, err := trainingCaptions(fields["caption"], form.Caption, form.TriggerWord, len(images))
	if err != nil {
		a.renderLoraTrainingPage(w, r, http.StatusBadRequest, form, err.Error(), "")
		return
	}
	archivePath := filepath.Join(workspace, "dataset.zip")
	if err := writeLoraTrainingArchive(archivePath, images, captions); err != nil {
		http.Error(w, "не удалось собрать датасет", http.StatusInternalServerError)
		return
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil || archiveInfo.Size() <= 0 || archiveInfo.Size() > 512<<20 {
		a.renderLoraTrainingPage(w, r, http.StatusBadRequest, form, "Архив датасета превышает 512 МБ.", "")
		return
	}
	for _, image := range images {
		_ = os.Remove(image.Path)
	}

	user := a.currentUser(r)
	publicID := newRequestID()
	requestPublicID := requestID(r)
	if len(requestPublicID) < 16 {
		requestPublicID = publicID
	}
	finalWorkspace := filepath.Join(a.cfg.MediaSpoolDir, "lora-training", publicID)
	if err := os.Rename(workspace, finalWorkspace); err != nil {
		http.Error(w, "не удалось сохранить датасет", http.StatusInternalServerError)
		return
	}
	keepWorkspace = true
	archivePath = filepath.Join(finalWorkspace, "dataset.zip")
	job, err := a.store.CreateLoraTrainingJob(r.Context(), domain.CreateLoraTrainingJobParams{
		PublicID: publicID, UserID: user.ID, UsernameSnapshot: user.Username, RequestID: requestPublicID,
		ProfileID: profile.ID, Family: profile.Family, BaseModel: profile.BaseModel, Name: form.Name,
		OutputName: form.OutputName, TriggerWord: form.TriggerWord, ConceptType: form.ConceptType,
		Preset: preset.ID, Resolution: form.Resolution, MaxTrainSteps: preset.Steps,
		NetworkDim: preset.NetworkDim, NetworkAlpha: preset.NetworkAlpha, LearningRate: preset.LearningRate,
		Seed: randomTrainingSeed(), SampleCount: len(images), DatasetBytes: archiveInfo.Size(), DatasetPath: archivePath,
	})
	if err != nil {
		_ = os.RemoveAll(finalWorkspace)
		if errors.Is(err, store.ErrLoraTrainingAlreadyActive) {
			a.renderLoraTrainingPage(w, r, http.StatusConflict, form, "У вас уже есть активное обучение. Дождитесь его завершения или отмените задачу.", "")
			return
		}
		http.Error(w, "не удалось создать задание обучения", http.StatusInternalServerError)
		return
	}
	a.audit(r.Context(), &user.ID, "lora_training_created", "lora_training_job", &job.ID, a.clientIP(r), r.UserAgent(), map[string]any{
		"profile_id": profile.ID, "family": profile.Family, "samples": len(images), "preset": preset.ID, "resolution": form.Resolution,
	})
	http.Redirect(w, r, "/train-lora?created=1#training-history", http.StatusFound)
}

func (a *App) readLoraTrainingSubmission(w http.ResponseWriter, r *http.Request, workspace string) (loraTrainingForm, map[string][]string, []uploadedTrainingImage, error) {
	form := defaultLoraTrainingForm()
	r.Body = http.MaxBytesReader(w, r.Body, maxLoraTrainingRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return form, nil, nil, errors.New("не удалось прочитать форму обучения")
	}
	fields := make(map[string][]string)
	images := make([]uploadedTrainingImage, 0, 24)
	csrfVerified := false
	var totalBytes int64
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			return form, fields, images, errors.New("не удалось прочитать загруженные файлы")
		}
		name := part.FormName()
		if part.FileName() == "" {
			value, readErr := io.ReadAll(io.LimitReader(part, 16<<10))
			part.Close()
			if readErr != nil {
				return form, fields, images, errors.New("не удалось прочитать поле формы")
			}
			if name == "csrf" {
				csrfVerified = a.validCSRFValue(r, strings.TrimSpace(string(value)))
				if !csrfVerified {
					return form, fields, images, errors.New("проверка безопасности не пройдена")
				}
				continue
			}
			fields[name] = append(fields[name], string(value))
			continue
		}
		if name != "images" {
			part.Close()
			continue
		}
		if !csrfVerified {
			part.Close()
			return form, fields, images, errors.New("проверка безопасности не пройдена")
		}
		if len(images) >= maxLoraTrainingImages {
			part.Close()
			return form, fields, images, fmt.Errorf("можно загрузить не более %d изображений", maxLoraTrainingImages)
		}
		temporaryPath := filepath.Join(workspace, fmt.Sprintf("source-%03d.bin", len(images)+1))
		file, createErr := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if createErr != nil {
			part.Close()
			return form, fields, images, errors.New("не удалось сохранить изображение")
		}
		written, copyErr := io.Copy(file, io.LimitReader(part, maxLoraTrainingImageBytes+1))
		closeErr := file.Close()
		part.Close()
		if copyErr != nil || closeErr != nil || written <= 0 || written > maxLoraTrainingImageBytes {
			return form, fields, images, errors.New("каждое изображение должно быть не больше 24 МБ")
		}
		totalBytes += written
		if totalBytes > 512<<20 {
			return form, fields, images, errors.New("датасет должен быть не больше 512 МБ")
		}
		imageInfo, validationErr := inspectTrainingImage(temporaryPath, part.FileName())
		if validationErr != nil {
			return form, fields, images, validationErr
		}
		images = append(images, imageInfo)
	}
	form = loraTrainingForm{
		ProfileID: strings.TrimSpace(firstField(fields, "profile_id")), Name: strings.TrimSpace(firstField(fields, "name")),
		OutputName: strings.TrimSpace(firstField(fields, "output_name")), TriggerWord: strings.TrimSpace(firstField(fields, "trigger_word")),
		ConceptType: strings.TrimSpace(firstField(fields, "concept_type")), Preset: strings.TrimSpace(firstField(fields, "preset")),
		Resolution: parseIntDefault(firstField(fields, "resolution"), 768), Caption: strings.TrimSpace(firstField(fields, "global_caption")),
	}
	if !csrfVerified {
		return form, fields, images, errors.New("проверка безопасности не пройдена")
	}
	return form, fields, images, nil
}

func (a *App) validCSRFValue(r *http.Request, value string) bool {
	cookie, err := r.Cookie(sessionCookieName)
	return err == nil && a.csrfSigner.Verify(cookie.Value, value)
}

func inspectTrainingImage(filename, originalName string) (uploadedTrainingImage, error) {
	file, err := os.Open(filename)
	if err != nil {
		return uploadedTrainingImage{}, errors.New("не удалось проверить изображение")
	}
	defer file.Close()
	header := make([]byte, 512)
	read, _ := io.ReadFull(file, header)
	mimeType := http.DetectContentType(header[:read])
	extension := ""
	switch mimeType {
	case "image/png":
		extension = ".png"
	case "image/jpeg":
		extension = ".jpg"
	case "image/webp":
		extension = ".webp"
	default:
		return uploadedTrainingImage{}, fmt.Errorf("%s: поддерживаются только PNG, JPG и WebP", filepath.Base(originalName))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return uploadedTrainingImage{}, errors.New("не удалось проверить изображение")
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width < 256 || config.Height < 256 || config.Width > 16384 || config.Height > 16384 {
		return uploadedTrainingImage{}, fmt.Errorf("%s: сторона изображения должна быть от 256 до 16384 пикселей", filepath.Base(originalName))
	}
	return uploadedTrainingImage{Path: filename, Extension: extension, OriginalName: filepath.Base(originalName), Width: config.Width, Height: config.Height}, nil
}

func trainingCaptions(values []string, fallback, trigger string, count int) ([]string, error) {
	captions := make([]string, count)
	for index := 0; index < count; index++ {
		caption := ""
		if index < len(values) {
			caption = strings.TrimSpace(values[index])
		}
		if caption == "" {
			caption = strings.TrimSpace(fallback)
		}
		if caption == "" {
			return nil, fmt.Errorf("добавьте описание для изображения %d или общее описание датасета", index+1)
		}
		if len([]rune(caption)) > 1000 {
			return nil, fmt.Errorf("описание изображения %d длиннее 1000 символов", index+1)
		}
		if !strings.Contains(strings.ToLower(caption), strings.ToLower(trigger)) {
			caption = trigger + ", " + caption
		}
		captions[index] = caption
	}
	return captions, nil
}

func writeLoraTrainingArchive(target string, images []uploadedTrainingImage, captions []string) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	for index, item := range images {
		stem := fmt.Sprintf("%04d", index+1)
		header := &zip.FileHeader{Name: "images/" + stem + item.Extension, Method: zip.Store}
		header.SetMode(0o640)
		output, err := archive.CreateHeader(header)
		if err != nil {
			archive.Close()
			file.Close()
			return err
		}
		input, err := os.Open(item.Path)
		if err != nil {
			archive.Close()
			file.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		input.Close()
		if copyErr != nil {
			archive.Close()
			file.Close()
			return copyErr
		}
		captionHeader := &zip.FileHeader{Name: "images/" + stem + ".txt", Method: zip.Deflate}
		captionHeader.SetMode(0o640)
		captionOutput, err := archive.CreateHeader(captionHeader)
		if err != nil {
			archive.Close()
			file.Close()
			return err
		}
		if _, err := io.WriteString(captionOutput, captions[index]); err != nil {
			archive.Close()
			file.Close()
			return err
		}
	}
	return errors.Join(archive.Close(), file.Close())
}

func validateLoraTrainingForm(form loraTrainingForm) error {
	if len([]rune(form.Name)) < 3 || len([]rune(form.Name)) > 80 {
		return errors.New("Название LoRA должно содержать от 3 до 80 символов.")
	}
	if !loraOutputNamePattern.MatchString(form.OutputName) {
		return errors.New("Имя файла: 3–64 латинских символа, цифры, дефис или подчёркивание.")
	}
	if len([]rune(form.TriggerWord)) < 2 || len([]rune(form.TriggerWord)) > 80 || strings.ContainsAny(form.TriggerWord, "\r\n") {
		return errors.New("Триггер должен содержать от 2 до 80 символов в одной строке.")
	}
	if form.ConceptType != "character" && form.ConceptType != "style" && form.ConceptType != "object" && form.ConceptType != "product" {
		return errors.New("Выберите тип LoRA.")
	}
	if form.Resolution != 512 && form.Resolution != 768 && form.Resolution != 1024 {
		return errors.New("Выберите разрешение 512, 768 или 1024.")
	}
	return nil
}

func (a *App) loraTrainingProfiles(ctx context.Context) ([]loratraining.Profile, string) {
	if a.loraTraining == nil || !a.loraTraining.Configured() {
		return nil, "Локальный агент обучения ещё не настроен. Задания нельзя запускать."
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := a.loraTraining.Profiles(requestCtx)
	if err != nil {
		return nil, "Локальный агент обучения не отвечает. Повторяем проверку автоматически."
	}
	return response.Profiles, response.Message
}

func (a *App) readyLoraTrainingProfile(ctx context.Context, id string) (loratraining.Profile, error) {
	profiles, message := a.loraTrainingProfiles(ctx)
	for _, profile := range profiles {
		if profile.ID != id {
			continue
		}
		if !profile.Ready {
			return loratraining.Profile{}, errors.New(profile.Detail)
		}
		return profile, nil
	}
	if message != "" {
		return loratraining.Profile{}, errors.New(message)
	}
	return loratraining.Profile{}, errors.New("Выбранный профиль обучения недоступен.")
}

func (a *App) handleLoraTrainingJobsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	jobs, err := a.store.ListLoraTrainingJobsByUser(r.Context(), user.ID, 30)
	if err != nil {
		http.Error(w, "не удалось загрузить задания", http.StatusInternalServerError)
		return
	}
	items := make([]loraTrainingJobJSON, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, loraTrainingJSON(job, nil, false))
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"jobs": items, "server_time": time.Now().UnixMilli()})
}

func (a *App) handleLoraTrainingJobAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	publicID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/lora-training/jobs/"), "/")
	user := a.currentUser(r)
	job, err := a.store.LoraTrainingJobByPublicID(r.Context(), publicID, user.ID, user.Role == "admin")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var agentStatus *loratraining.JobStatus
	if job.AgentJobID != "" && !job.State.Terminal() && a.loraTraining != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		status, statusErr := a.loraTraining.Status(ctx, job.AgentJobID)
		cancel()
		if statusErr == nil {
			agentStatus = &status
		}
	}
	writeJSONResponse(w, http.StatusOK, loraTrainingJSON(job, agentStatus, user.Role == "admin"))
}

func (a *App) downloadLoraTrainingArtifact(w http.ResponseWriter, r *http.Request, job domain.LoraTrainingJob) {
	if job.State != domain.LoraTrainingCompleted || job.AgentJobID == "" || a.loraTraining == nil {
		http.Error(w, "файл LoRA ещё не готов", http.StatusConflict)
		return
	}
	body, name, size, err := a.loraTraining.Artifact(r.Context(), job.AgentJobID)
	if err != nil {
		http.Error(w, "не удалось получить файл LoRA", http.StatusBadGateway)
		return
	}
	defer body.Close()
	if name == "" {
		name = job.OutputName + ".safetensors"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(name, `"`, "")))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, body)
	}
}

func (a *App) refreshLoraTrainingJobs(ctx context.Context) (int64, error) {
	if a.loraTraining == nil || !a.loraTraining.Configured() {
		return 0, nil
	}
	a.loraTrainingMu.Lock()
	defer a.loraTrainingMu.Unlock()
	active, err := a.store.ActiveLoraTrainingJobs(ctx)
	if err != nil {
		return 0, err
	}
	var processed int64
	var refreshErrors []error
	for _, job := range active {
		if ctx.Err() != nil {
			return processed, errors.Join(append(refreshErrors, ctx.Err())...)
		}
		processed++
		if job.AgentJobID == "" {
			if job.CancellationRequestedAt != nil {
				_ = a.store.UpdateLoraTrainingJob(ctx, job.ID, store.UpdateLoraTrainingJobParams{
					State: domain.LoraTrainingCancelled, Stage: "Отменено", Progress: 100,
					Message: "Задание отменено до запуска локального процесса.",
				})
				a.removeLoraTrainingDataset(job)
				_ = a.store.ClearLoraTrainingDatasetPath(ctx, job.ID)
				a.releaseMiningPauseForLoraTraining(ctx, job.ID)
			}
			continue
		}
		if job.CancellationRequestedAt != nil {
			if _, cancelErr := a.loraTraining.Cancel(ctx, job.AgentJobID); cancelErr != nil {
				refreshErrors = append(refreshErrors, fmt.Errorf("cancel LoRA training %s: %w", job.PublicID, cancelErr))
				continue
			}
		}
		status, statusErr := a.loraTraining.Status(ctx, job.AgentJobID)
		if statusErr != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("LoRA training status %s: %w", job.PublicID, statusErr))
			continue
		}
		state := loraTrainingStateFromAgent(status.State)
		if err := a.store.UpdateLoraTrainingJob(ctx, job.ID, store.UpdateLoraTrainingJobParams{
			State: state, Stage: truncateLoraText(status.Stage, 120), Progress: clampLoraProgress(status.Progress),
			Message: truncateLoraText(status.Message, 1000), ErrorMessage: truncateLoraText(status.Error, 2000),
			ArtifactName: truncateLoraText(status.ArtifactName, 255), ArtifactBytes: status.ArtifactBytes,
		}); err != nil {
			refreshErrors = append(refreshErrors, err)
			continue
		}
		if state.Terminal() {
			a.removeLoraTrainingDataset(job)
			_ = a.store.ClearLoraTrainingDatasetPath(ctx, job.ID)
			a.releaseMiningPauseForLoraTraining(ctx, job.ID)
			a.audit(ctx, job.UserID, "lora_training_"+string(state), "lora_training_job", &job.ID, "", "", map[string]any{"public_id": job.PublicID, "artifact": status.ArtifactName})
		}
	}

	job, claimErr := a.store.ClaimNextLoraTrainingJob(ctx)
	if errors.Is(claimErr, sql.ErrNoRows) {
		return processed, errors.Join(refreshErrors...)
	}
	if claimErr != nil {
		return processed, errors.Join(append(refreshErrors, claimErr)...)
	}
	processed++
	file, openErr := os.Open(job.DatasetPath)
	if openErr != nil {
		_ = a.store.UpdateLoraTrainingJob(ctx, job.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingFailed, Stage: "Ошибка датасета", Progress: 100, Message: "Исходный датасет не найден.", ErrorMessage: openErr.Error()})
		a.removeLoraTrainingDataset(job)
		_ = a.store.ClearLoraTrainingDatasetPath(ctx, job.ID)
		a.releaseMiningPauseForLoraTraining(ctx, job.ID)
		return processed, errors.Join(refreshErrors...)
	}
	user, userErr := a.store.UserByID(ctx, dereferenceUserID(job.UserID))
	if userErr != nil {
		file.Close()
		_ = a.store.UpdateLoraTrainingJob(ctx, job.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingFailed, Stage: "Нет пользователя", Progress: 100, Message: "Владелец задания больше не существует.", ErrorMessage: userErr.Error()})
		a.removeLoraTrainingDataset(job)
		_ = a.store.ClearLoraTrainingDatasetPath(ctx, job.ID)
		a.releaseMiningPauseForLoraTraining(ctx, job.ID)
		return processed, errors.Join(refreshErrors...)
	}
	lease, miningWarning, pauseErr := a.pauseMiningForLoraTraining(ctx, &user, job.ID)
	if pauseErr != nil {
		file.Close()
		requeued, requeueErr := a.store.RequeueLoraTrainingJob(ctx, job.ID, "Ждём освобождения GPU: "+pauseErr.Error())
		if requeueErr == nil && requeued.State.Terminal() {
			a.removeLoraTrainingDataset(requeued)
			_ = a.store.ClearLoraTrainingDatasetPath(ctx, requeued.ID)
			a.releaseMiningPauseForLoraTraining(ctx, requeued.ID)
		}
		if requeueErr != nil {
			refreshErrors = append(refreshErrors, requeueErr)
		}
		return processed, errors.Join(append(refreshErrors, pauseErr)...)
	}
	status, submitErr := a.loraTraining.Submit(ctx, loratraining.JobSpec{
		GatewayJobID: job.PublicID, ProfileID: job.ProfileID, Owner: job.UsernameSnapshot, Name: job.Name,
		OutputName: job.OutputName, TriggerWord: job.TriggerWord, ConceptType: job.ConceptType,
		Resolution: job.Resolution, MaxSteps: job.MaxTrainSteps, NetworkDim: job.NetworkDim,
		NetworkAlpha: job.NetworkAlpha, LearningRate: job.LearningRate, Seed: job.Seed, SampleCount: job.SampleCount,
	}, file, job.DatasetBytes)
	file.Close()
	if submitErr != nil {
		if lease != nil {
			a.releaseMiningPause(ctx, lease.ID)
		}
		var httpErr *loratraining.HTTPError
		if errors.Is(submitErr, loratraining.ErrUnavailable) || !errors.As(submitErr, &httpErr) || httpErr.StatusCode >= 500 {
			requeued, requeueErr := a.store.RequeueLoraTrainingJob(ctx, job.ID, "Агент временно недоступен. Задание осталось в очереди.")
			if requeueErr == nil && requeued.State.Terminal() {
				a.removeLoraTrainingDataset(requeued)
				_ = a.store.ClearLoraTrainingDatasetPath(ctx, requeued.ID)
			}
			if requeueErr != nil {
				refreshErrors = append(refreshErrors, requeueErr)
			}
			return processed, errors.Join(append(refreshErrors, submitErr)...)
		}
		_ = a.store.UpdateLoraTrainingJob(ctx, job.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingFailed, Stage: "Профиль не готов", Progress: 100, Message: "Агент отклонил запуск.", ErrorMessage: truncateLoraText(submitErr.Error(), 2000)})
		a.removeLoraTrainingDataset(job)
		_ = a.store.ClearLoraTrainingDatasetPath(ctx, job.ID)
		return processed, errors.Join(refreshErrors...)
	}
	message := status.Message
	if miningWarning != "" {
		message = strings.TrimSpace(message + " " + miningWarning)
	}
	if err := a.store.AttachLoraTrainingAgentJob(ctx, job.ID, status.ID, truncateLoraText(status.Stage, 120), truncateLoraText(message, 1000), clampLoraProgress(status.Progress)); err != nil {
		requeued, requeueErr := a.store.RequeueLoraTrainingJob(ctx, job.ID, "Повторно связываем задание с локальным агентом.")
		if requeueErr == nil && requeued.State.Terminal() {
			_, _ = a.loraTraining.Cancel(ctx, status.ID)
			a.removeLoraTrainingDataset(requeued)
			_ = a.store.ClearLoraTrainingDatasetPath(ctx, requeued.ID)
			a.releaseMiningPauseForLoraTraining(ctx, requeued.ID)
		}
		return processed, errors.Join(append(refreshErrors, err, requeueErr)...)
	}
	a.removeLoraTrainingDataset(job)
	_ = a.store.ClearLoraTrainingDatasetPath(ctx, job.ID)
	return processed, errors.Join(refreshErrors...)
}

func loraTrainingStateFromAgent(state string) domain.LoraTrainingState {
	switch state {
	case "queued", "preparing":
		return domain.LoraTrainingPreparing
	case "caching":
		return domain.LoraTrainingCaching
	case "running":
		return domain.LoraTrainingRunning
	case "installing":
		return domain.LoraTrainingInstalling
	case "completed":
		return domain.LoraTrainingCompleted
	case "cancelled":
		return domain.LoraTrainingCancelled
	default:
		return domain.LoraTrainingFailed
	}
}

func loraTrainingJSON(job domain.LoraTrainingJob, agent *loratraining.JobStatus, includeUsername bool) loraTrainingJobJSON {
	view := newLoraTrainingJobView(job)
	result := loraTrainingJobJSON{
		ID: job.PublicID, Name: job.Name, OutputName: job.OutputName, ProfileID: job.ProfileID,
		Family: job.Family, FamilyLabel: view.FamilyLabel, BaseModel: job.BaseModel,
		State: string(job.State), StateLabel: view.StateLabel, StateClass: view.StateClass,
		Stage: job.Stage, Progress: job.Progress, Message: job.Message, Error: job.ErrorMessage,
		SampleCount: job.SampleCount, ConceptLabel: view.ConceptLabel, PresetLabel: view.PresetLabel,
		Resolution: job.Resolution, MaxTrainSteps: job.MaxTrainSteps, CanCancel: view.CanCancel,
		CanDownload: view.CanDownload, CreatedAt: job.CreatedAt.UnixMilli(), UpdatedAt: job.UpdatedAt.UnixMilli(),
		ArtifactName: job.ArtifactName, ArtifactBytes: job.ArtifactBytes,
	}
	if agent != nil {
		agentState := loraTrainingStateFromAgent(agent.State)
		result.State = string(agentState)
		result.StateLabel = loraTrainingStateLabel(agentState)
		result.StateClass = loraTrainingStateClass(agentState)
		result.CanCancel = agentState.Cancellable()
		result.Stage, result.Progress, result.Message, result.Error, result.LogTail = agent.Stage, clampLoraProgress(agent.Progress), agent.Message, agent.Error, agent.LogTail
	}
	if result.CanDownload {
		result.DownloadURL = "/train-lora/" + job.PublicID + "/download"
	}
	if result.CanCancel {
		result.CancelURL = "/train-lora/" + job.PublicID + "/cancel"
	}
	if includeUsername {
		result.Username = job.UsernameSnapshot
	}
	return result
}

func newLoraTrainingJobView(job domain.LoraTrainingJob) loraTrainingJobView {
	return loraTrainingJobView{
		LoraTrainingJob: job, FamilyLabel: loraTrainingFamilyLabel(job.Family), ConceptLabel: loraConceptLabel(job.ConceptType),
		PresetLabel: loraPresetLabel(job.Preset), StateLabel: loraTrainingStateLabel(job.State), StateClass: loraTrainingStateClass(job.State),
		CanCancel: job.State.Cancellable(), CanDownload: job.State == domain.LoraTrainingCompleted && job.ArtifactName != "",
	}
}

func loraTrainingStateLabel(state domain.LoraTrainingState) string {
	switch state {
	case domain.LoraTrainingQueued:
		return "В очереди"
	case domain.LoraTrainingUploading:
		return "Передача"
	case domain.LoraTrainingPreparing:
		return "Подготовка"
	case domain.LoraTrainingCaching:
		return "Кеширование"
	case domain.LoraTrainingRunning:
		return "Обучение"
	case domain.LoraTrainingInstalling:
		return "Установка"
	case domain.LoraTrainingCompleted:
		return "Готово"
	case domain.LoraTrainingCancelled:
		return "Отменено"
	default:
		return "Ошибка"
	}
}

func loraTrainingStateClass(state domain.LoraTrainingState) string {
	if state == domain.LoraTrainingCompleted {
		return "is-complete"
	}
	if state == domain.LoraTrainingFailed {
		return "is-error"
	}
	if state == domain.LoraTrainingCancelled {
		return "is-muted"
	}
	return "is-active"
}

func loraTrainingFamilyLabel(family string) string {
	if family == "flux2-klein" {
		return "Flux.2 Klein"
	}
	return "Krea2"
}

func loraConceptLabel(concept string) string {
	switch concept {
	case "style":
		return "Стиль"
	case "object":
		return "Объект"
	case "product":
		return "Продукт"
	default:
		return "Персонаж"
	}
}

func loraPresetLabel(id string) string {
	if preset, ok := loraTrainingPresetByID(id); ok {
		return preset.Name
	}
	return id
}

func loraTrainingPresetByID(id string) (loraTrainingPreset, bool) {
	for _, preset := range loraTrainingPresets {
		if preset.ID == id {
			return preset, true
		}
	}
	return loraTrainingPreset{}, false
}

func defaultLoraTrainingForm() loraTrainingForm {
	return loraTrainingForm{ConceptType: "character", Preset: "balanced", Resolution: 768}
}

func loraTrainingPageMessage(r *http.Request) string {
	if r.URL.Query().Get("created") == "1" {
		return "Задание создано. Датасет поставлен в очередь обучения."
	}
	if r.URL.Query().Get("cancelled") == "1" {
		return "Отмена запрошена. Активный процесс будет остановлен безопасно."
	}
	return ""
}

func firstField(fields map[string][]string, name string) string {
	if values := fields[name]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func parseIntDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func randomTrainingSeed() int64 {
	var data [8]byte
	if _, err := cryptorand.Read(data[:]); err != nil {
		return time.Now().UnixNano() & int64(^uint64(0)>>1)
	}
	return int64(binary.LittleEndian.Uint64(data[:]) & uint64(^uint64(0)>>1))
}

func clampLoraProgress(progress int) int {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return progress
}

func truncateLoraText(value string, limit int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit])
}

func dereferenceUserID(userID *int64) int64 {
	if userID == nil {
		return 0
	}
	return *userID
}

func (a *App) removeLoraTrainingDataset(job domain.LoraTrainingJob) {
	if job.DatasetPath == "" {
		return
	}
	root := filepath.Clean(filepath.Join(a.cfg.MediaSpoolDir, "lora-training"))
	workspace := filepath.Clean(filepath.Dir(job.DatasetPath))
	relative, err := filepath.Rel(root, workspace)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		log.Printf("refusing to remove LoRA training workspace outside spool: %s", workspace)
		return
	}
	if err := os.RemoveAll(workspace); err != nil {
		log.Printf("remove LoRA training workspace %s: %v", workspace, err)
	}
}

func writeJSONResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
