package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const (
	minGenerationBatchSize       = 2
	maxGenerationBatchSize       = 20
	maxActiveGenerationBatchJobs = 2
)

type generationBatchSpec struct {
	Mode          domain.GenerationBatchMode
	Count         int
	ParameterName string
	From          float64
	To            float64
}

type generationBatchParameterSpec struct {
	Name    string
	Label   string
	Minimum float64
	Maximum float64
	Step    float64
	Integer bool
}

type generationBatchCandidate struct {
	Input           generationForm
	Values          url.Values
	PayloadCipher   []byte
	ExperimentValue string
}

type generationBatchDifferenceValue struct {
	JobID    string `json:"job_id"`
	Position int    `json:"position"`
	Value    string `json:"value"`
}

type generationBatchDifferenceView struct {
	Name   string                           `json:"name"`
	Label  string                           `json:"label"`
	Values []generationBatchDifferenceValue `json:"values"`
}

type generationBatchView struct {
	BatchID                string                          `json:"batch_id"`
	ParentBatchID          string                          `json:"parent_batch_id,omitempty"`
	SourceJobID            string                          `json:"source_job_id,omitempty"`
	WinnerJobID            string                          `json:"winner_job_id,omitempty"`
	TemplateID             string                          `json:"template_id"`
	WorkflowID             string                          `json:"workflow_id"`
	ModelName              string                          `json:"model_name"`
	Mode                   string                          `json:"mode"`
	ParameterName          string                          `json:"parameter_name,omitempty"`
	ParameterLabel         string                          `json:"parameter_label,omitempty"`
	SeedLocked             bool                            `json:"seed_locked"`
	State                  string                          `json:"state"`
	TotalCount             int                             `json:"total_count"`
	FinishedCount          int                             `json:"finished_count"`
	CompletedCount         int                             `json:"completed_count"`
	FailedCount            int                             `json:"failed_count"`
	CancelledCount         int                             `json:"cancelled_count"`
	MissingCount           int                             `json:"missing_count,omitempty"`
	ProgressPercent        int                             `json:"progress_percent"`
	Cancellable            bool                            `json:"cancellable"`
	EstimatedStartSeconds  int                             `json:"estimated_start_seconds,omitempty"`
	EstimatedFinishSeconds int                             `json:"estimated_finish_seconds,omitempty"`
	CancellationRequested  bool                            `json:"cancellation_requested"`
	CreatedAt              time.Time                       `json:"created_at"`
	UpdatedAt              time.Time                       `json:"updated_at"`
	Jobs                   []generationJobView             `json:"jobs"`
	Differences            []generationBatchDifferenceView `json:"differences"`
}

func parseGenerationBatchSpec(values url.Values) (generationBatchSpec, error) {
	count, err := strconv.Atoi(strings.TrimSpace(values.Get("batch_count")))
	if err != nil || count < minGenerationBatchSize || count > maxGenerationBatchSize {
		return generationBatchSpec{}, fmt.Errorf("выберите от %d до %d вариантов", minGenerationBatchSize, maxGenerationBatchSize)
	}
	spec := generationBatchSpec{Mode: domain.GenerationBatchMode(strings.TrimSpace(values.Get("batch_mode"))), Count: count}
	if !spec.Mode.Valid() || spec.Mode == domain.GenerationBatchLoraVerification {
		return generationBatchSpec{}, errors.New("выберите способ изменения вариантов")
	}
	if spec.Mode == domain.GenerationBatchSeeds {
		return spec, nil
	}
	spec.ParameterName = strings.TrimSpace(values.Get("batch_parameter"))
	if spec.ParameterName == "" {
		return generationBatchSpec{}, errors.New("выберите один параметр для эксперимента")
	}
	spec.From, err = parseGenerationFloat(values.Get("batch_from"))
	if err != nil {
		return generationBatchSpec{}, errors.New("некорректное начальное значение эксперимента")
	}
	spec.To, err = parseGenerationFloat(values.Get("batch_to"))
	if err != nil {
		return generationBatchSpec{}, errors.New("некорректное конечное значение эксперимента")
	}
	if math.IsNaN(spec.From) || math.IsInf(spec.From, 0) || math.IsNaN(spec.To) || math.IsInf(spec.To, 0) {
		return generationBatchSpec{}, errors.New("значения эксперимента должны быть конечными числами")
	}
	return spec, nil
}

func generationBatchParameter(input generationForm, name string) (generationBatchParameterSpec, bool) {
	imageFamily := input.ModelFamily == modelFamilyKrea2 || input.ModelFamily == modelFamilyFlux2 || input.ModelFamily == modelFamilyQwen21 || input.ModelFamily == modelFamilyMoss
	miniMax := input.ModelFamily == modelFamilyMiniMaxH3
	byName := func(spec generationBatchParameterSpec, available bool) (generationBatchParameterSpec, bool) {
		return spec, available
	}
	switch name {
	case "steps":
		return byName(generationBatchParameterSpec{Name: name, Label: "Шаги", Minimum: 1, Maximum: 100, Step: 1, Integer: true}, imageFamily)
	case "cfg":
		return byName(generationBatchParameterSpec{Name: name, Label: "CFG", Minimum: 0, Maximum: 30, Step: 0.1}, input.ModelFamily == modelFamilyKrea2)
	case "denoise":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сила изменения", Minimum: 0.01, Maximum: 1, Step: 0.01}, input.TemplateID == "image-to-image")
	case "output_megapixels":
		return byName(generationBatchParameterSpec{Name: name, Label: "Итоговые мегапиксели", Minimum: 0.5, Maximum: 4.7, Step: 0.1}, input.ModelFamily == modelFamilyKrea2 && input.TemplateID == "text-to-image")
	case "source_megapixels":
		return byName(generationBatchParameterSpec{Name: name, Label: "Мегапиксели исходного прохода", Minimum: 0.25, Maximum: 16, Step: 0.25}, input.ModelFamily == modelFamilyFlux2 && input.TemplateID == "image-to-image")
	case "reference_boost":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сохранение референса", Minimum: 0, Maximum: 8, Step: 0.1}, input.ModelFamily == modelFamilyKrea2 && input.TemplateID == "image-to-image")
	case "flux_guidance":
		return byName(generationBatchParameterSpec{Name: name, Label: "Flux Guidance", Minimum: 0, Maximum: 10, Step: 0.1}, input.ModelFamily == modelFamilyFlux2)
	case "flux_active_scale":
		return byName(generationBatchParameterSpec{Name: name, Label: "Flux Active Scale", Minimum: 0, Maximum: 10, Step: 0.05}, input.ModelFamily == modelFamilyFlux2)
	case "upscale_factor":
		return byName(generationBatchParameterSpec{Name: name, Label: "Коэффициент апскейла", Minimum: 1, Maximum: 2, Step: 0.05}, input.ModelFamily == modelFamilyKrea2 && input.TemplateID == "image-to-image")
	case "upscale_denoise":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сила перерисовки апскейла", Minimum: 0.01, Maximum: 0.5, Step: 0.01}, input.ModelFamily == modelFamilyKrea2)
	case "detail_denoise":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сила финальной детализации", Minimum: 0.005, Maximum: 0.2, Step: 0.005}, input.ModelFamily == modelFamilyKrea2 && input.TemplateID == "text-to-image")
	case "video_steps":
		return byName(generationBatchParameterSpec{Name: name, Label: "Шаги видео", Minimum: 1, Maximum: 100, Step: 1, Integer: true}, miniMax)
	case "video_shift_video":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сдвиг Video Sigma", Minimum: 1, Maximum: 30, Step: 1, Integer: true}, miniMax)
	case "video_shift_audio":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сдвиг Audio Sigma", Minimum: 1, Maximum: 30, Step: 1, Integer: true}, miniMax)
	case "video_duration_seconds":
		return byName(generationBatchParameterSpec{Name: name, Label: "Длительность видео", Minimum: 5, Maximum: 15, Step: 5, Integer: true}, miniMax)
	case "video_sparse_budget":
		return byName(generationBatchParameterSpec{Name: name, Label: "Бюджет Sparse Attention", Minimum: 0.05, Maximum: 1, Step: 0.05}, miniMax && input.VideoSparseAttention)
	case "video_rife_multiplier":
		return byName(generationBatchParameterSpec{Name: name, Label: "Множитель кадров RIFE", Minimum: 2, Maximum: 4, Step: 1, Integer: true}, miniMax && input.VideoRIFEEnabled)
	case "video_rtx_scale":
		return byName(generationBatchParameterSpec{Name: name, Label: "Масштаб RTX", Minimum: 1, Maximum: 2, Step: 0.25}, miniMax && input.VideoRTXEnabled)
	case "video_color_strength":
		return byName(generationBatchParameterSpec{Name: name, Label: "Сила ColorMatch", Minimum: 0, Maximum: 1, Step: 0.05}, miniMax && input.VideoColorMatch)
	case "video_sharpen_strength":
		maximum := 1.0
		if input.VideoSharpenMethod == "adaptive_usm" || input.VideoSharpenMethod == "high_pass" {
			maximum = 3
		}
		return byName(generationBatchParameterSpec{Name: name, Label: "Сила резкости видео", Minimum: 0, Maximum: maximum, Step: 0.05}, miniMax && input.VideoSharpenEnabled)
	case "video_output_crf":
		return byName(generationBatchParameterSpec{Name: name, Label: "Качество H.264 (CRF)", Minimum: 1, Maximum: 51, Step: 1, Integer: true}, miniMax)
	}
	if strings.HasPrefix(name, "lora_model_strength_") {
		position, err := strconv.Atoi(strings.TrimPrefix(name, "lora_model_strength_"))
		if err == nil && position >= 1 && position <= len(input.LoraNames) && strings.TrimSpace(input.LoraNames[position-1]) != "" {
			return generationBatchParameterSpec{Name: name, Label: fmt.Sprintf("Сила LoRA %d", position), Minimum: -4, Maximum: 4, Step: 0.05}, true
		}
	}
	return generationBatchParameterSpec{}, false
}

func generationBatchParameterLabel(name string) string {
	probe := generationForm{
		TemplateID: "image-to-image", ModelFamily: modelFamilyMiniMaxH3,
		VideoSparseAttention: true, VideoRIFEEnabled: true, VideoRTXEnabled: true, VideoColorMatch: true, VideoSharpenEnabled: true,
	}
	if spec, ok := generationBatchParameter(probe, name); ok {
		return spec.Label
	}
	labels := map[string]string{
		"steps": "Шаги", "cfg": "CFG", "denoise": "Сила изменения", "output_megapixels": "Итоговые мегапиксели",
		"source_megapixels": "Мегапиксели исходного прохода", "reference_boost": "Сохранение референса",
		"flux_guidance": "Flux Guidance", "flux_active_scale": "Flux Active Scale", "upscale_factor": "Коэффициент апскейла",
		"upscale_denoise": "Сила перерисовки апскейла", "detail_denoise": "Сила финальной детализации",
	}
	if label := labels[name]; label != "" {
		return label
	}
	if strings.HasPrefix(name, "lora_model_strength_") {
		return "Сила LoRA " + strings.TrimPrefix(name, "lora_model_strength_")
	}
	return name
}

func cloneGenerationValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for name, items := range values {
		cloned[name] = append([]string(nil), items...)
	}
	return cloned
}

func formatGenerationBatchValue(value float64, spec generationBatchParameterSpec) string {
	if spec.Integer {
		return strconv.FormatInt(int64(math.Round(value)), 10)
	}
	precision := 0
	for step := spec.Step; step > 0 && step < 1 && precision < 6; step *= 10 {
		precision++
	}
	return strconv.FormatFloat(value, 'f', precision, 64)
}

func generationBatchParameterValues(spec generationBatchSpec, parameter generationBatchParameterSpec) ([]string, error) {
	if spec.From < parameter.Minimum || spec.From > parameter.Maximum || spec.To < parameter.Minimum || spec.To > parameter.Maximum {
		return nil, fmt.Errorf("%s: допустимый диапазон от %s до %s", parameter.Label,
			formatGenerationBatchValue(parameter.Minimum, parameter), formatGenerationBatchValue(parameter.Maximum, parameter))
	}
	values := make([]string, 0, spec.Count)
	seen := make(map[string]struct{}, spec.Count)
	for index := 0; index < spec.Count; index++ {
		value := spec.From + (spec.To-spec.From)*float64(index)/float64(spec.Count-1)
		value = math.Round(value/parameter.Step) * parameter.Step
		formatted := formatGenerationBatchValue(value, parameter)
		if _, exists := seen[formatted]; exists {
			return nil, errors.New("диапазон слишком узкий для выбранного количества разных вариантов")
		}
		seen[formatted] = struct{}{}
		values = append(values, formatted)
	}
	return values, nil
}

func generationBatchSeeds(base int64, count int, randomBase bool) ([]int64, error) {
	const maximumSeed = int64(1 << 50)
	seeds := make([]int64, 0, count)
	seen := make(map[int64]struct{}, count)
	for len(seeds) < count {
		seed := base + int64(len(seeds))
		if randomBase || seed < 0 || seed > maximumSeed {
			var err error
			seed, err = randomSeed()
			if err != nil {
				return nil, err
			}
		}
		if _, exists := seen[seed]; exists {
			randomBase = true
			continue
		}
		seen[seed] = struct{}{}
		seeds = append(seeds, seed)
	}
	return seeds, nil
}

func parseGenerationValues(ctx context.Context, values url.Values) (generationForm, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/generate/batches", strings.NewReader(values.Encode()))
	if err != nil {
		return generationForm{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return parseGenerationForm(request)
}

func (a *App) buildGenerationBatchCandidates(ctx context.Context, user *User, base generationPreparation, values url.Values, spec generationBatchSpec) ([]generationBatchCandidate, []string, error) {
	if spec.Mode != domain.GenerationBatchSeeds && spec.Mode != domain.GenerationBatchParameter {
		return nil, nil, errors.New("этот вид пакета требует отдельного подтверждения")
	}
	seedWasRandom := base.Input.Seed < 0 || strings.TrimSpace(values.Get("seed")) == "-1"
	baseSeed := base.Input.Seed
	if baseSeed < 0 {
		var err error
		baseSeed, err = randomSeed()
		if err != nil {
			return nil, nil, errors.New("не удалось подготовить seed пакета")
		}
	}
	var parameter generationBatchParameterSpec
	parameterValues := make([]string, 0)
	if spec.Mode == domain.GenerationBatchParameter {
		var ok bool
		parameter, ok = generationBatchParameter(base.Input, spec.ParameterName)
		if !ok {
			return nil, nil, errors.New("этот параметр нельзя изменять в выбранном workflow")
		}
		var err error
		parameterValues, err = generationBatchParameterValues(spec, parameter)
		if err != nil {
			return nil, nil, err
		}
	}
	seeds, err := generationBatchSeeds(baseSeed, spec.Count, seedWasRandom && spec.Mode == domain.GenerationBatchSeeds)
	if err != nil {
		return nil, nil, errors.New("не удалось подготовить разные seed")
	}
	candidates := make([]generationBatchCandidate, 0, spec.Count)
	for index := 0; index < spec.Count; index++ {
		candidateValues := cloneGenerationValues(values)
		candidateValues.Del("batch_enabled")
		candidateValues.Del("batch_mode")
		candidateValues.Del("batch_count")
		candidateValues.Del("batch_parameter")
		candidateValues.Del("batch_from")
		candidateValues.Del("batch_to")
		candidateValues.Del("client_request_id")
		candidateValues.Del("csrf")
		seed := seeds[index]
		if spec.Mode == domain.GenerationBatchParameter {
			seed = baseSeed
			candidateValues.Set(spec.ParameterName, parameterValues[index])
		}
		candidateValues.Set("seed", strconv.FormatInt(seed, 10))
		input, parseErr := parseGenerationValues(ctx, candidateValues)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("вариант %d: %w", index+1, parseErr)
		}
		preparation, prepareErr := a.prepareGeneration(ctx, user, input, false)
		if prepareErr != nil {
			return nil, nil, fmt.Errorf("вариант %d: %w", index+1, prepareErr)
		}
		cipher, cipherErr := a.generationJobPayloadCipher(preparation.Input, candidateValues)
		if cipherErr != nil {
			return nil, nil, cipherErr
		}
		experimentValue := strconv.FormatInt(seed, 10)
		if spec.Mode == domain.GenerationBatchParameter {
			experimentValue = parameterValues[index]
		}
		candidates = append(candidates, generationBatchCandidate{
			Input: preparation.Input, Values: candidateValues, PayloadCipher: cipher, ExperimentValue: experimentValue,
		})
	}
	return candidates, parameterValues, nil
}

func generationBatchChildRequestID(batchRequestID string, position int) string {
	suffix := fmt.Sprintf("_v%02d", position)
	if maximum := 96 - len(suffix); len(batchRequestID) > maximum {
		batchRequestID = batchRequestID[:maximum]
	}
	return batchRequestID + suffix
}

func generationBatchState(batch domain.GenerationBatch) string {
	finished := batch.CompletedCount + batch.FailedCount + batch.CancelledCount + batch.ExpiredCount + batch.MissingCount
	if finished >= batch.TotalCount {
		switch {
		case batch.CompletedCount == batch.TotalCount:
			return "completed"
		case batch.CompletedCount > 0:
			return "partial"
		case batch.CancelledCount+batch.ExpiredCount == batch.TotalCount:
			return "cancelled"
		default:
			return "failed"
		}
	}
	if batch.CancellationRequestedAt != nil {
		return "cancelling"
	}
	if batch.ActiveCount > 0 {
		return "running"
	}
	return "queued"
}

func generationBatchDifferences(batch domain.GenerationBatch, jobs []generationJobView) []generationBatchDifferenceView {
	if len(jobs) < 2 {
		return nil
	}
	name := "seed"
	label := "Seed"
	if batch.Mode == domain.GenerationBatchParameter {
		name = batch.ParameterName
		label = generationBatchParameterLabel(name)
	} else if batch.Mode == domain.GenerationBatchLoraVerification {
		name, label = "lora_weight", "Вес проверяемой LoRA"
	}
	values := make([]generationBatchDifferenceValue, 0, len(jobs))
	unique := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		value := strconv.FormatInt(job.Seed, 10)
		if batch.Mode == domain.GenerationBatchParameter || batch.Mode == domain.GenerationBatchLoraVerification {
			value = job.ExperimentValue
		}
		unique[value] = struct{}{}
		values = append(values, generationBatchDifferenceValue{JobID: job.JobID, Position: job.BatchPosition, Value: value})
	}
	if len(unique) < 2 {
		return nil
	}
	return []generationBatchDifferenceView{{Name: name, Label: label, Values: values}}
}

func makeGenerationBatchView(batch domain.GenerationBatch, children []generationJobView) generationBatchView {
	finished := batch.CompletedCount + batch.FailedCount + batch.CancelledCount + batch.ExpiredCount + batch.MissingCount
	cancellable := false
	for _, child := range children {
		cancellable = cancellable || child.Cancellable
	}
	view := generationBatchView{
		BatchID: batch.PublicID, ParentBatchID: batch.ParentBatchPublicID, SourceJobID: batch.SourceJobPublicID,
		WinnerJobID: batch.WinnerJobPublicID, TemplateID: batch.TemplateID, WorkflowID: batch.WorkflowID,
		ModelName: batch.ModelName, Mode: string(batch.Mode), ParameterName: batch.ParameterName,
		ParameterLabel: generationBatchParameterLabel(batch.ParameterName), SeedLocked: batch.SeedLocked,
		State: generationBatchState(batch), TotalCount: batch.TotalCount, FinishedCount: finished,
		CompletedCount: batch.CompletedCount, FailedCount: batch.FailedCount,
		CancelledCount:        batch.CancelledCount + batch.ExpiredCount,
		MissingCount:          batch.MissingCount,
		Cancellable:           cancellable && batch.CancellationRequestedAt == nil,
		CancellationRequested: batch.CancellationRequestedAt != nil,
		CreatedAt:             batch.CreatedAt, UpdatedAt: batch.UpdatedAt, Jobs: children,
		Differences: generationBatchDifferences(batch, children),
	}
	if batch.TotalCount > 0 {
		view.ProgressPercent = int(math.Round(float64(finished) * 100 / float64(batch.TotalCount)))
	}
	if batch.Mode == domain.GenerationBatchLoraVerification {
		view.ParameterLabel = "Вес проверяемой LoRA"
	}
	return view
}

func (a *App) generationBatchViews(ctx context.Context, userID int64) ([]generationBatchView, error) {
	batches, err := a.store.ListGenerationBatches(ctx, userID, 24, time.Now().Add(-a.retentionPolicy().GenerationHistory))
	if err != nil {
		return nil, err
	}
	jobsByBatch := make(map[int64][]domain.GenerationJob, len(batches))
	allJobs := make([]domain.GenerationJob, 0)
	for _, batch := range batches {
		jobs, loadErr := a.store.GenerationBatchJobs(ctx, userID, batch.ID)
		if loadErr != nil {
			return nil, loadErr
		}
		jobsByBatch[batch.ID] = jobs
		allJobs = append(allJobs, jobs...)
	}
	jobViews, err := a.generationJobViews(ctx, allJobs, userID)
	if err != nil {
		return nil, err
	}
	viewByJobID := make(map[int64]generationJobView, len(allJobs))
	for index, job := range allJobs {
		viewByJobID[job.ID] = jobViews[index]
	}
	averageSeconds := 0
	queueWait := 0
	if overview, overviewErr := a.generationQueueOverview(ctx); overviewErr == nil {
		averageSeconds = overview.AverageTaskSeconds
		queueWait = overview.EstimatedWaitSeconds
	}
	if averageSeconds <= 0 {
		if average, averageErr := a.store.AverageGenerationDuration(ctx); averageErr == nil && average > 0 {
			averageSeconds = int(average.Round(time.Second).Seconds())
		}
	}
	views := make([]generationBatchView, 0, len(batches))
	queuedAhead := 0
	for index := len(batches) - 1; index >= 0; index-- {
		batch := batches[index]
		jobs := jobsByBatch[batch.ID]
		children := make([]generationJobView, 0, len(jobs))
		for _, job := range jobs {
			view := viewByJobID[job.ID]
			view.BatchID = batch.PublicID
			children = append(children, view)
		}
		finished := batch.CompletedCount + batch.FailedCount + batch.CancelledCount + batch.ExpiredCount + batch.MissingCount
		view := makeGenerationBatchView(batch, children)
		if averageSeconds > 0 && finished < batch.TotalCount {
			view.EstimatedStartSeconds = queueWait + queuedAhead*averageSeconds
			view.EstimatedFinishSeconds = view.EstimatedStartSeconds + (batch.TotalCount-finished)*averageSeconds
		}
		queuedAhead += batch.DraftCount
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt.After(views[j].CreatedAt) })
	return views, nil
}

func (a *App) generationBatchViewByID(ctx context.Context, userID int64, publicID string) (generationBatchView, error) {
	views, err := a.generationBatchViews(ctx, userID)
	if err != nil {
		return generationBatchView{}, err
	}
	for _, view := range views {
		if view.BatchID == publicID {
			return view, nil
		}
	}
	return generationBatchView{}, sql.ErrNoRows
}

func (a *App) handleGenerationBatches(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		views, err := a.generationBatchViews(r.Context(), a.currentUser(r).ID)
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить пакеты вариантов")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"batches": views})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	user := a.currentUser(r)
	input, err := parseGenerationForm(r)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec, err := parseGenerationBatchSpec(r.Form)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	requestID := strings.TrimSpace(r.Form.Get("client_request_id"))
	if !validGenerationRequestID(requestID) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор пакета")
		return
	}
	correlation := strings.TrimSpace(r.Form.Get("correlation_id"))
	if correlation == "" {
		correlation = correlationID(r)
	}
	if !validCorrelationID(correlation) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор трассировки")
		return
	}
	var sourceJob *domain.GenerationJob
	if parentPublicID := strings.TrimSpace(r.Form.Get("parent_job_id")); parentPublicID != "" {
		parent, parentErr := a.store.GenerationJobByPublicID(r.Context(), user.ID, parentPublicID)
		if errors.Is(parentErr, sql.ErrNoRows) {
			writeGenerationError(w, http.StatusBadRequest, "исходный вариант ветки больше недоступен")
			return
		}
		if parentErr != nil || parent.State != domain.GenerationJobCompleted {
			writeGenerationError(w, http.StatusConflict, "ветку можно создать только от готового варианта")
			return
		}
		sourceJob = &parent
	}
	base, err := a.prepareGeneration(r.Context(), user, input, true)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errMinorSexualContent) {
			status = http.StatusUnprocessableEntity
		}
		writeGenerationError(w, status, err.Error())
		return
	}
	candidates, parameterValues, err := a.buildGenerationBatchCandidates(r.Context(), user, base, r.Form, spec)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	collectionID, err := generationCollectionID(r.PostForm)
	if err != nil {
		writeGenerationCollectionError(w, err)
		return
	}
	batchPublicID := newRequestID()
	params := domain.CreateGenerationBatchParams{
		CollectionID: collectionID,
		PublicID:     batchPublicID, UserID: user.ID, UsernameSnapshot: user.Username, RequestID: requestID,
		TemplateID: base.Input.TemplateID, WorkflowID: base.Input.PresetID, ModelName: base.Input.ModelName,
		Mode: spec.Mode, ParameterName: spec.ParameterName, ParameterValues: parameterValues,
		SeedLocked: spec.Mode == domain.GenerationBatchParameter, MaxParallel: 1,
		Jobs: make([]domain.CreateGenerationBatchJobParams, 0, len(candidates)),
	}
	if sourceJob != nil {
		params.SourceJobID = &sourceJob.ID
		params.ParentBatchID = sourceJob.BatchID
	}
	for index, candidate := range candidates {
		position := index + 1
		var parentJobID *int64
		if sourceJob != nil {
			parentJobID = &sourceJob.ID
		}
		params.Jobs = append(params.Jobs, domain.CreateGenerationBatchJobParams{
			PublicID: newRequestID(), CorrelationID: correlation, RequestID: generationBatchChildRequestID(requestID, position),
			ParentJobID: parentJobID, Position: position, ExperimentValue: candidate.ExperimentValue,
			Prepared: domain.PreparedGenerationJob{
				InputNames: generationJobInputNames(candidate.Input),
				TemplateID: candidate.Input.TemplateID, WorkflowID: candidate.Input.PresetID, ModelName: candidate.Input.ModelName,
				Seed: candidate.Input.Seed, PayloadCipher: candidate.PayloadCipher,
				Dependencies: generationJobDependencies(candidate.Input, user), InputCount: generationJobInputCount(candidate.Input),
			},
		})
	}
	batch, created, err := a.store.CreateGenerationBatch(r.Context(), params)
	if err != nil {
		if errors.Is(err, store.ErrGenerationCollectionMissing) {
			writeGenerationCollectionError(w, err)
			return
		}
		if writeGenerationSubmissionError(w, err, "batch", requestID) {
			return
		}
		if errors.Is(err, store.ErrGenerationInputUnavailable) {
			writeGenerationError(w, http.StatusConflict, generationInputUnavailableMessage)
			return
		}
		status, message := quickGenerationLimitError(err)
		if !errors.Is(err, store.ErrQuickGenerationDailyLimit) && !errors.Is(err, store.ErrQuickGenerationTotalLimit) && !errors.Is(err, store.ErrQuickGenerationForbidden) {
			status, message = http.StatusInternalServerError, "не удалось создать пакет вариантов"
		}
		writeGenerationError(w, status, message)
		return
	}
	view, err := a.generationBatchViewByID(r.Context(), user.ID, batch.PublicID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "пакет создан, но его состояние пока недоступно")
		return
	}
	quota, _ := a.generationQuotaView(r.Context(), user.ID)
	a.audit(r.Context(), &user.ID, "quick_generation_batch_created", "generation_batch", &batch.ID, a.clientIP(r), r.UserAgent(), map[string]any{
		"batch_id": batch.PublicID, "count": batch.TotalCount, "mode": batch.Mode, "parameter": batch.ParameterName, "created": created,
	})
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"batch": view, "quota": quota, "created": created, "request_id": requestID, "kind": "batch"})
}

func (a *App) handleGenerationBatchCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	user := a.currentUser(r)
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	defer cancel()
	publicID := strings.TrimSpace(r.Form.Get("batch_id"))
	batch, jobs, changed, err := a.store.RequestGenerationBatchCancellation(cancelCtx, user.ID, publicID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось отменить пакет")
		return
	}
	var cancelErrors []error
	for _, job := range jobs {
		if job.CancellationRequestedAt == nil || job.State.Terminal() {
			continue
		}
		if _, _, cancelErr := a.continueGenerationJobCancellation(cancelCtx, job); cancelErr != nil {
			cancelErrors = append(cancelErrors, cancelErr)
		}
	}
	view, viewErr := a.generationBatchViewByID(cancelCtx, user.ID, batch.PublicID)
	if viewErr != nil {
		writeGenerationError(w, http.StatusInternalServerError, "отмена принята, но состояние пакета пока недоступно")
		return
	}
	a.audit(cancelCtx, &user.ID, "quick_generation_batch_cancelled", "generation_batch", &batch.ID, a.clientIP(r), r.UserAgent(), map[string]any{"batch_id": batch.PublicID, "changed": changed})
	response := map[string]any{"batch": view, "cancelled": view.State == "cancelled"}
	if len(cancelErrors) > 0 || view.FinishedCount < view.TotalCount {
		response["message"] = "Отмена принята. Активный вариант будет остановлен после подтверждения ComfyUI."
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (a *App) handleGenerationBatchWinner(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	user := a.currentUser(r)
	batch, err := a.store.SetGenerationBatchWinner(r.Context(), user.ID, strings.TrimSpace(r.Form.Get("batch_id")), strings.TrimSpace(r.Form.Get("job_id")))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrGenerationBatchWinnerConflict) {
		writeGenerationError(w, http.StatusConflict, "победителем можно выбрать только готовый вариант этого пакета")
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось сохранить победителя")
		return
	}
	view, err := a.generationBatchViewByID(r.Context(), user.ID, batch.PublicID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "победитель сохранён, но пакет пока недоступен")
		return
	}
	a.audit(r.Context(), &user.ID, "quick_generation_batch_winner_selected", "generation_batch", &batch.ID, a.clientIP(r), r.UserAgent(), map[string]any{"batch_id": batch.PublicID, "job_id": batch.WinnerJobPublicID})
	writeJSON(w, http.StatusOK, map[string]any{"batch": view})
}

func (a *App) dispatchGenerationJobs(ctx context.Context) (int64, error) {
	if a.store == nil || a.contentCipher == nil {
		return 0, nil
	}
	job, err := a.store.ClaimNextGenerationDispatch(ctx, newRequestID(), maxActiveGenerationBatchJobs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	jobCtx := generationJobTraceContext(ctx, job)
	if job.UserID == nil {
		a.failGenerationJob(jobCtx, job, "generation_owner_deleted", "Владелец задания удалён", errors.New("generation owner was deleted"))
		return 1, nil
	}
	user, err := a.store.UserByID(jobCtx, *job.UserID)
	if err != nil || user.Disabled || (user.AccountExpiresAt.Valid && user.AccountExpiresAt.Time.Before(time.Now())) {
		if err == nil {
			err = errors.New("generation owner is unavailable")
		}
		a.failGenerationJob(jobCtx, job, "generation_owner_unavailable", "Учётная запись больше не может запускать генерации", err)
		return 1, nil
	}
	if _, linkErr := a.store.LinkGenerationJobAssistantEvents(jobCtx, job.ID, user.ID, job.CorrelationID); linkErr != nil {
		log.Printf("link generation job %s assistant audit: %v", job.PublicID, linkErr)
	}
	var preparation generationPreparation
	if isPostprocessJob(job) {
		postprocess, prepareErr := a.preparePostprocessDispatch(jobCtx, job)
		if prepareErr != nil {
			settleCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			handled, settleErr := a.settlePostprocessDispatchChange(settleCtx, job)
			cancel()
			if handled || settleErr != nil {
				return 1, settleErr
			}
		}
		if errors.Is(prepareErr, errPostprocessDispatchWaiting) {
			return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, "Проверяем исходники постобработки")
		}
		if prepareErr != nil {
			a.failGenerationJob(jobCtx, job, "postprocess_preparation_failed", "Не удалось проверить постобработку", prepareErr)
			return 1, nil
		}
		preparation = generationPreparation{Postprocess: &postprocess, Prompt: postprocess.Prompt}
	} else {
		payload, err := a.decodeGenerationSavedPayload(job.PayloadCipher)
		if err != nil {
			a.failGenerationJob(jobCtx, job, "generation_batch_payload_failed", "Не удалось восстановить параметры генерации", err)
			return 1, nil
		}
		form := generationJobFormValues(payload.Values)
		var input generationForm
		if payload.Repeat != nil {
			if payload.ExecutionInput == nil {
				err = errGenerationRepeatDrift
			} else {
				input = *payload.ExecutionInput
				current, currentErr := a.comfyObjectInfo(jobCtx, true)
				if currentErr != nil || current.Source != comfyObjectInfoLive {
					return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, "Проверяем текущую схему перед повтором")
				}
			}
		} else {
			input, err = parseGenerationValues(jobCtx, form)
		}
		if err != nil {
			a.failGenerationJob(jobCtx, job, "generation_batch_payload_invalid", "Параметры генерации больше не поддерживаются", err)
			return 1, nil
		}
		preparation, err = a.prepareGeneration(jobCtx, &user, input, false)
		if err != nil {
			a.failGenerationJob(jobCtx, job, "generation_batch_preflight_failed", "Workflow не прошёл проверку", err)
			return 1, nil
		}
		input = preparation.Input
		if job.State == domain.GenerationJobPreparing && generationJobInputCount(input) > 0 {
			job, _, err = a.store.TransitionGenerationJob(jobCtx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobUploading, Message: "Проверяем референсы"})
			if err != nil {
				a.failGenerationJob(jobCtx, job, "generation_batch_input_state_failed", "Не удалось закрепить референсы", err)
				return 1, nil
			}
		}
		if job.State != domain.GenerationJobWaitingForResources {
			preparedJob, err := a.store.PrepareGenerationJob(jobCtx, job.ID, domain.PreparedGenerationJob{
				InputNames: generationJobInputNames(input),
				TemplateID: input.TemplateID, WorkflowID: input.PresetID, ModelName: input.ModelName, Seed: input.Seed,
				PayloadCipher: job.PayloadCipher, Dependencies: generationJobDependencies(input, &user), InputCount: generationJobInputCount(input),
			})
			if err != nil {
				a.failGenerationJob(jobCtx, job, "generation_batch_prepare_failed", "Не удалось подготовить генерацию", err)
				return 1, nil
			}
			job = preparedJob
			job, _, err = a.store.TransitionGenerationJob(jobCtx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobWaitingForResources, Message: "Ожидаем ресурсы для генерации"})
			if err != nil {
				a.failGenerationJob(jobCtx, job, "generation_batch_resource_state_failed", "Не удалось подготовить ресурсы генерации", err)
				return 1, nil
			}
		}
		current, err := a.store.GenerationJobByPublicID(jobCtx, user.ID, job.PublicID)
		if err != nil {
			return 0, err
		}
		if current.DispatchToken != job.DispatchToken || current.State.Terminal() {
			return 0, nil
		}
		job = current
		if current.CancellationRequestedAt != nil {
			_, _, _ = a.continueGenerationJobCancellation(jobCtx, current)
			return 1, nil
		}
		waitingForFiles, inspectionErr := a.inspectGenerationPreparationRequired(jobCtx, job, &preparation, payload.Repeat != nil && payload.Repeat.FilesObserved)
		if inspectionErr != nil {
			a.failGenerationJob(jobCtx, job, "generation_component_inspection_failed", inspectionErr.Error(), inspectionErr)
			return 1, nil
		}
		if waitingForFiles {
			return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, "Проверяем файлы моделей перед запуском")
		}
		if err := a.validateGenerationRepeatDispatch(jobCtx, &user, job, payload, preparation); err != nil {
			if errors.Is(err, errGenerationRepeatWaiting) {
				return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, "Проверяем материалы перед повтором")
			}
			a.failGenerationJob(jobCtx, job, "generation_repeat_changed", "Условия повтора изменились. Проверьте их и подтвердите новый запуск", err)
			return 1, nil
		}
		preparation.Repeat = payload.Repeat
	}
	var miningLease *domain.QuickGenerationMiningLease
	promptID, err := a.submitComfyPrompt(jobCtx, user.ID, job.PublicID, user.QueuePriority, preparation.Prompt, func(sendCtx context.Context) error {
		if preparation.Postprocess != nil {
			if err := a.refreshPostprocessSubmission(sendCtx, job, preparation.Postprocess); err != nil {
				return err
			}
		}
		gpuWork, gpuErr := a.gpuCoordinator.admit(sendCtx, domain.GPUWorkGeneration, job.PublicID, &user, job.DispatchToken, job.CreatedAt)
		if gpuErr != nil {
			return gpuErr
		}
		var warning string
		var pauseErr error
		miningLease, warning, pauseErr = a.pauseMiningForQuickGeneration(sendCtx, &user, job.ID)
		if pauseErr != nil {
			return fmt.Errorf("%w: %w", errGenerationMiningPause, pauseErr)
		}
		if warning != "" {
			log.Printf("generation %s mining policy: %s", job.PublicID, warning)
		}
		if gpuErr := a.gpuCoordinator.dispatch(sendCtx, gpuWork); gpuErr != nil {
			return gpuErr
		}
		execution, err := a.captureGenerationExecution(sendCtx, job, preparation)
		if err != nil {
			return err
		}
		return a.store.BeginGenerationJobSubmissionWithExecution(sendCtx, job.ID, job.DispatchToken, execution)
	})
	// The worker timeout must not erase a receipt or turn an uncertain send into
	// a failed job. Persist the handoff independently of the expired request.
	receiptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobCtx = generationJobTraceContext(receiptCtx, job)
	if err != nil {
		if preparation.Postprocess != nil {
			if handled, settleErr := a.settlePostprocessDispatchChange(jobCtx, job); handled || settleErr != nil {
				return 1, settleErr
			}
		}
		if errors.Is(err, errPostprocessDispatchWaiting) {
			return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, "Проверяем исходники постобработки")
		}
		var waiting *gpuAdmissionWait
		if errors.As(err, &waiting) {
			return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, waiting.Error())
		}
		if errors.Is(err, errComfyPromptAdmission) {
			return 0, a.store.PostponeGenerationDispatch(jobCtx, job.ID, job.DispatchToken, "Ожидаем свободное место в очереди ComfyUI")
		}
		if errors.Is(err, errComfyPromptRejected) {
			if rejectErr := a.store.RejectGenerationJobSubmission(jobCtx, job.ID, job.DispatchToken); rejectErr != nil {
				return 1, errors.Join(err, rejectErr)
			}
		}
		code, message := generationSubmissionFailure(err)
		a.failGenerationJob(jobCtx, job, code, message, err)
		return 1, err
	}
	jobCtx = traceContext(jobCtx, job.CorrelationID, job.ID, promptID)
	if err := a.attachMiningPauseToGeneration(jobCtx, miningLease, promptID); err != nil {
		log.Printf("attach mining-pause lease to generation %s: %v", promptID, err)
	}
	a.rememberGeneration(promptID, user.ID)
	bound, bindErr := a.store.BindGenerationJobPrompt(jobCtx, job.ID, promptID)
	if bindErr != nil {
		log.Printf("bind generation job %s to prompt %s: %v", job.PublicID, promptID, bindErr)
		return 1, nil
	}
	job = bound
	if _, commitErr := a.store.CommitQuickGenerationForJob(jobCtx, job.ID); commitErr != nil {
		log.Printf("commit quota for generation job %s: %v", job.PublicID, commitErr)
	}
	if projectionErr := a.ensureGenerationJobProjections(jobCtx, job); projectionErr != nil {
		log.Printf("project generation job %s: %v", job.PublicID, projectionErr)
	}
	if _, _, transitionErr := a.store.TransitionGenerationJob(jobCtx, job.ID, domain.GenerationJobTransitionParams{State: domain.GenerationJobQueued, Message: "Генерация поставлена в очередь ComfyUI"}); transitionErr != nil {
		return 1, transitionErr
	}
	return 1, nil
}
