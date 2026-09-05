package trainingagent

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/loratraining"
)

var (
	outputNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)
	ownerPattern      = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	progressPattern   = regexp.MustCompile(`(?i)(\d{1,6})\s*/\s*(\d{1,6})`)
	ErrJobNotTerminal = errors.New("LoRA training job is not terminal")
)

const failedJobRetention = 24 * time.Hour

type jobRecord struct {
	Spec         loratraining.JobSpec   `json:"spec"`
	Status       loratraining.JobStatus `json:"status"`
	DatasetPath  string                 `json:"dataset_path"`
	ArtifactPath string                 `json:"artifact_path,omitempty"`
	cancel       context.CancelFunc
}

type Controller struct {
	config           Config
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.RWMutex
	jobs             map[string]*jobRecord
	byGateway        map[string]string
	submissionFences map[string]bool
	queue            chan string
	wg               sync.WaitGroup
}

func NewController(config Config) (*Controller, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	for _, directory := range []string{config.RootDir, filepath.Join(config.RootDir, "jobs"), filepath.Join(config.RootDir, "models")} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	controller := &Controller{
		config: config, ctx: ctx, cancel: cancel, jobs: make(map[string]*jobRecord),
		byGateway: make(map[string]string), queue: make(chan string, 100),
	}
	if err := controller.loadSubmissionFences(); err != nil {
		cancel()
		return nil, err
	}
	if err := controller.loadJobs(); err != nil {
		cancel()
		return nil, err
	}
	controller.wg.Add(2)
	go controller.worker()
	go controller.failedJobCleanupWorker()
	return controller, nil
}

func (controller *Controller) Close() {
	controller.cancel()
	controller.wg.Wait()
}

func (controller *Controller) MaxDatasetBytes() int64 {
	return controller.config.MaxDatasetBytes
}

func (controller *Controller) Profiles() []loratraining.Profile {
	profiles := make([]loratraining.Profile, 0, len(controller.config.Profiles))
	for _, profile := range controller.config.Profiles {
		profiles = append(profiles, profile.Public(controller.config))
	}
	return profiles
}

func (controller *Controller) Submit(ctx context.Context, spec loratraining.JobSpec, dataset io.Reader) (loratraining.JobStatus, error) {
	// Older Gateway releases generated 63-bit seeds, while musubi uses NumPy's
	// legacy 32-bit seed range. Keep the local agent compatible during rollout.
	spec.Seed = normalizeTrainingSeed(spec.Seed)
	profile, err := controller.validateSpec(spec)
	if err != nil {
		return loratraining.JobStatus{}, err
	}
	if detail := profile.Readiness(controller.config); detail != "" {
		return loratraining.JobStatus{}, errors.New(detail)
	}
	controller.mu.RLock()
	if controller.submissionFences[spec.GatewayJobID] {
		controller.mu.RUnlock()
		return loratraining.JobStatus{}, errors.New("submission was fenced during recovery; create a new Gateway job")
	}
	if existingID := controller.byGateway[spec.GatewayJobID]; existingID != "" {
		status := cloneStatus(controller.jobs[existingID].Status)
		controller.mu.RUnlock()
		_, _ = io.Copy(io.Discard, dataset)
		return status, nil
	}
	controller.mu.RUnlock()

	id, err := randomID()
	if err != nil {
		return loratraining.JobStatus{}, err
	}
	jobDir := controller.jobDir(id)
	if err := os.MkdirAll(jobDir, 0o750); err != nil {
		return loratraining.JobStatus{}, err
	}
	datasetPath := filepath.Join(jobDir, "dataset.zip")
	file, err := os.OpenFile(datasetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return loratraining.JobStatus{}, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(dataset, controller.config.MaxDatasetBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written <= 0 || written > controller.config.MaxDatasetBytes {
		_ = os.RemoveAll(jobDir)
		if written > controller.config.MaxDatasetBytes {
			return loratraining.JobStatus{}, errors.New("датасет превышает допустимый размер")
		}
		return loratraining.JobStatus{}, errors.Join(copyErr, closeErr)
	}
	now := time.Now()
	record := &jobRecord{
		Spec: spec, DatasetPath: datasetPath,
		Status: loratraining.JobStatus{
			ID: id, GatewayJobID: spec.GatewayJobID, ProfileID: spec.ProfileID, State: "queued",
			Stage: "В очереди", Progress: 0, Message: "Датасет принят локальным агентом.", CreatedAt: now,
		},
	}
	controller.mu.Lock()
	if controller.submissionFences[spec.GatewayJobID] {
		controller.mu.Unlock()
		_ = os.RemoveAll(jobDir)
		return loratraining.JobStatus{}, errors.New("submission was fenced during recovery; create a new Gateway job")
	}
	if existingID := controller.byGateway[spec.GatewayJobID]; existingID != "" {
		status := cloneStatus(controller.jobs[existingID].Status)
		controller.mu.Unlock()
		_ = os.RemoveAll(jobDir)
		return status, nil
	}
	if err := controller.persist(record); err != nil {
		controller.mu.Unlock()
		_ = os.RemoveAll(jobDir)
		return loratraining.JobStatus{}, err
	}
	controller.jobs[id] = record
	controller.byGateway[spec.GatewayJobID] = id
	controller.mu.Unlock()
	select {
	case controller.queue <- id:
		return controller.Status(id)
	case <-ctx.Done():
		return loratraining.JobStatus{}, ctx.Err()
	case <-controller.ctx.Done():
		return loratraining.JobStatus{}, errors.New("агент завершает работу")
	}
}

func (controller *Controller) Status(id string) (loratraining.JobStatus, error) {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	record := controller.jobs[id]
	if record == nil {
		return loratraining.JobStatus{}, os.ErrNotExist
	}
	return cloneStatus(record.Status), nil
}

// Lookup also finds accepted jobs when the caller lost the Submit response.
// Absence in this index does not prove that an earlier executor stopped.
func (controller *Controller) StatusByGatewayID(id string) (loratraining.JobStatus, error) {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	record := controller.jobs[controller.byGateway[id]]
	if record == nil {
		return loratraining.JobStatus{}, os.ErrNotExist
	}
	return cloneStatus(record.Status), nil
}

func (controller *Controller) Cancel(id string) (loratraining.JobStatus, error) {
	controller.mu.Lock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.Unlock()
		return loratraining.JobStatus{}, os.ErrNotExist
	}
	if record.Status.Terminal() {
		status := cloneStatus(record.Status)
		controller.mu.Unlock()
		return status, nil
	}
	if record.Status.State == "queued" {
		record.Status.State = "cancelled"
		record.Status.Stage = "Отменено"
		record.Status.Progress = 100
		record.Status.Message = "Задание отменено до запуска."
		record.Status.FinishedAt = time.Now()
	} else {
		record.Status.Stage = "Отменяем обучение"
		record.Status.Message = "Останавливаем активный процесс и его дочерние процессы."
		if record.cancel != nil {
			record.cancel()
		}
	}
	copyRecord := cloneRecord(record)
	status := cloneStatus(record.Status)
	controller.mu.Unlock()
	_ = controller.persist(copyRecord)
	return status, nil
}

func (controller *Controller) Delete(id string) (loratraining.JobStatus, error) {
	controller.mu.RLock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.RUnlock()
		return loratraining.JobStatus{}, os.ErrNotExist
	}
	if !record.Status.Terminal() || record.Status.ExecutionUnconfirmed {
		controller.mu.RUnlock()
		return loratraining.JobStatus{}, ErrJobNotTerminal
	}
	copyRecord := cloneRecord(record)
	status := cloneStatus(record.Status)
	controller.mu.RUnlock()

	if err := controller.deleteRecordFiles(copyRecord); err != nil {
		return loratraining.JobStatus{}, err
	}

	controller.mu.Lock()
	if current := controller.jobs[id]; current != nil {
		if !current.Status.Terminal() || current.Status.ExecutionUnconfirmed {
			controller.mu.Unlock()
			return loratraining.JobStatus{}, ErrJobNotTerminal
		}
		delete(controller.byGateway, current.Spec.GatewayJobID)
		delete(controller.jobs, id)
	}
	controller.mu.Unlock()
	return status, nil
}

func (controller *Controller) Artifact(id string) (string, string, int64, error) {
	controller.mu.RLock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.RUnlock()
		return "", "", 0, os.ErrNotExist
	}
	artifactPath := record.ArtifactPath
	artifactName := record.Status.ArtifactName
	controller.mu.RUnlock()
	if artifactPath == "" {
		return "", "", 0, errors.New("артефакт ещё не готов")
	}
	info, err := os.Stat(artifactPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", 0, errors.New("файл LoRA не найден")
	}
	return artifactPath, artifactName, info.Size(), nil
}

func (controller *Controller) worker() {
	defer controller.wg.Done()
	for {
		select {
		case <-controller.ctx.Done():
			return
		case id := <-controller.queue:
			controller.execute(id)
		}
	}
}

func (controller *Controller) failedJobCleanupWorker() {
	defer controller.wg.Done()
	controller.deleteExpiredFailedJobs(time.Now())
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-controller.ctx.Done():
			return
		case now := <-ticker.C:
			controller.deleteExpiredFailedJobs(now)
		}
	}
}

func (controller *Controller) deleteExpiredFailedJobs(now time.Time) {
	cutoff := now.Add(-failedJobRetention)
	controller.mu.RLock()
	ids := make([]string, 0)
	for id, record := range controller.jobs {
		if record == nil || record.Status.State != "failed" || record.Status.ExecutionUnconfirmed {
			continue
		}
		finishedAt := record.Status.FinishedAt
		if finishedAt.IsZero() {
			finishedAt = record.Status.CreatedAt
		}
		if !finishedAt.IsZero() && finishedAt.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	controller.mu.RUnlock()
	for _, id := range ids {
		if _, err := controller.Delete(id); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("delete expired failed LoRA training job %s: %v", id, err)
		}
	}
}

func (controller *Controller) deleteRecordFiles(record *jobRecord) error {
	if record == nil {
		return nil
	}
	if record.ArtifactPath != "" {
		trainedRoot := filepath.Join(controller.config.ComfyLoraDir, "Trained")
		if !pathWithinDirectory(trainedRoot, record.ArtifactPath, false) {
			return fmt.Errorf("refusing to remove LoRA artifact outside trained directory: %s", record.ArtifactPath)
		}
		info, err := os.Lstat(record.ArtifactPath)
		if err == nil {
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("refusing to remove non-file LoRA artifact: %s", record.ArtifactPath)
			}
			if err := os.Remove(record.ArtifactPath); err != nil {
				return fmt.Errorf("remove LoRA artifact: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect LoRA artifact: %w", err)
		}
	}

	jobsRoot := filepath.Join(controller.config.RootDir, "jobs")
	jobDir := controller.jobDir(record.Status.ID)
	if !pathWithinDirectory(jobsRoot, jobDir, false) {
		return fmt.Errorf("refusing to remove LoRA job outside jobs directory: %s", jobDir)
	}
	if err := os.RemoveAll(jobDir); err != nil {
		return fmt.Errorf("remove LoRA job files: %w", err)
	}
	return nil
}

func pathWithinDirectory(root, target string, allowRoot bool) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	if relative == "." {
		return allowRoot
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (controller *Controller) execute(id string) {
	controller.mu.Lock()
	record := controller.jobs[id]
	if record == nil || record.Status.Terminal() {
		controller.mu.Unlock()
		return
	}
	jobCtx, cancel := context.WithCancel(controller.ctx)
	record.cancel = cancel
	record.Status.State = "preparing"
	record.Status.Stage = "Проверяем датасет"
	record.Status.Progress = 3
	record.Status.StartedAt = time.Now()
	copyRecord := cloneRecord(record)
	controller.mu.Unlock()
	_ = controller.persist(copyRecord)
	defer cancel()

	err := controller.run(jobCtx, id)
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		controller.finish(id, "cancelled", "Отменено", 100, "Обучение остановлено.", "")
		controller.cleanupWorkingFiles(id)
		return
	}
	log.Printf("LoRA training job %s failed: %v", id, err)
	controller.finish(id, "failed", "Ошибка", 100, "Обучение не завершено.", truncate(err.Error(), 2000))
	controller.cleanupWorkingFiles(id)
}

func (controller *Controller) run(ctx context.Context, id string) error {
	record := controller.record(id)
	profile, ok := controller.config.Profile(record.Spec.ProfileID)
	if !ok {
		return errors.New("профиль обучения больше не существует")
	}
	if detail := profile.Readiness(controller.config); detail != "" {
		return errors.New(detail)
	}
	jobDir := controller.jobDir(id)
	datasetDir := filepath.Join(jobDir, "dataset")
	cacheDir := filepath.Join(jobDir, "cache")
	outputDir := filepath.Join(jobDir, "output")
	for _, directory := range []string{datasetDir, cacheDir, outputDir} {
		if err := os.RemoveAll(directory); err != nil {
			return err
		}
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return err
		}
	}
	if err := extractDataset(record.DatasetPath, datasetDir, record.Spec.SampleCount, controller.config.MaxDatasetBytes); err != nil {
		return err
	}
	configPath := filepath.Join(jobDir, "dataset.toml")
	if err := writeDatasetConfig(configPath, filepath.Join(datasetDir, "images"), cacheDir, record.Spec.Resolution); err != nil {
		return err
	}

	modelPath := profile.DiT
	if profile.StripPrefix != "" {
		controller.setProgress(id, "preparing", "Готовим базовую модель", 6, "Нормализуем служебные имена тензоров без загрузки модели в ОЗУ.")
		modelPath = filepath.Join(controller.config.RootDir, "models", profile.ID+"-format-v2.safetensors")
		if err := normalizeSafeTensorForProfile(profile.DiT, modelPath, profile.StripPrefix, profile.Family); err != nil {
			return fmt.Errorf("подготовить базовую модель: %w", err)
		}
	}

	logPath := filepath.Join(jobDir, "training.log")
	if err := controller.runCommand(ctx, id, logPath, "caching", "Кешируем изображения", 10, 24, controller.latentCacheArgs(profile, configPath)...); err != nil {
		return fmt.Errorf("кеш латентов: %w", err)
	}
	if err := controller.runCommand(ctx, id, logPath, "caching", "Кешируем описания", 25, 39, controller.textCacheArgs(profile, configPath)...); err != nil {
		return fmt.Errorf("кеш текстового энкодера: %w", err)
	}
	if err := controller.runCommand(ctx, id, logPath, "running", "Обучаем LoRA", 40, 94, controller.trainingArgs(profile, record.Spec, modelPath, configPath, outputDir)...); err != nil {
		return fmt.Errorf("обучение: %w", err)
	}

	controller.setProgress(id, "installing", "Устанавливаем LoRA", 96, "Копируем итоговый адаптер в каталог ComfyUI.")
	trainedPath, err := findTrainingOutput(outputDir, record.Spec.OutputName)
	if err != nil {
		return err
	}
	ownerHash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(record.Spec.Owner))))
	owner := strings.Trim(ownerPattern.ReplaceAllString(record.Spec.Owner, "_"), "_-")
	if owner == "" {
		owner = "user"
	}
	owner = truncate(owner, 40) + "-" + hex.EncodeToString(ownerHash[:4])
	familyDir := "Krea2"
	if profile.Family == "flux2-klein" {
		familyDir = "Flux2-Klein"
	}
	destinationDir := filepath.Join(controller.config.ComfyLoraDir, "Trained", familyDir, owner)
	if err := os.MkdirAll(destinationDir, 0o750); err != nil {
		return err
	}
	artifactName := record.Spec.OutputName + ".safetensors"
	destination := filepath.Join(destinationDir, artifactName)
	if _, err := os.Stat(destination); err == nil {
		suffix := truncate(record.Spec.GatewayJobID, 8)
		artifactName = record.Spec.OutputName + "-" + suffix + ".safetensors"
		destination = filepath.Join(destinationDir, artifactName)
	}
	if err := copyFileAtomic(trainedPath, destination); err != nil {
		return fmt.Errorf("установить LoRA: %w", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return err
	}
	controller.mu.Lock()
	record = controller.jobs[id]
	record.ArtifactPath = destination
	record.Status.State = "completed"
	record.Status.Stage = "Готово"
	record.Status.Progress = 100
	record.Status.Message = "LoRA установлена в ComfyUI и готова к использованию."
	record.Status.ArtifactName = artifactName
	record.Status.ArtifactBytes = info.Size()
	record.Status.FinishedAt = time.Now()
	record.cancel = nil
	copyRecord := cloneRecord(record)
	controller.mu.Unlock()
	_ = controller.persist(copyRecord)
	_ = os.Remove(record.DatasetPath)
	_ = os.RemoveAll(datasetDir)
	_ = os.RemoveAll(cacheDir)
	_ = os.RemoveAll(outputDir)
	return nil
}

func (controller *Controller) cleanupWorkingFiles(id string) {
	jobDir := controller.jobDir(id)
	for _, target := range []string{"dataset.zip", "dataset", "cache", "output"} {
		_ = os.RemoveAll(filepath.Join(jobDir, target))
	}
}

func (controller *Controller) latentCacheArgs(profile ProfileConfig, datasetConfig string) []string {
	script := "krea2_cache_latents.py"
	args := []string{}
	if profile.Family == "flux2-klein" {
		script = "flux_2_cache_latents.py"
	}
	args = append(args, filepath.Join(controller.config.TunerDir, "src", "musubi_tuner", script), "--dataset_config", datasetConfig, "--vae", profile.VAE)
	if profile.Family == "flux2-klein" {
		args = append(args, "--model_version", profile.ModelVersion, "--vae_dtype", "bfloat16")
	}
	return args
}

func (controller *Controller) textCacheArgs(profile ProfileConfig, datasetConfig string) []string {
	script := "krea2_cache_text_encoder_outputs.py"
	if profile.Family == "flux2-klein" {
		script = "flux_2_cache_text_encoder_outputs.py"
	}
	args := []string{filepath.Join(controller.config.TunerDir, "src", "musubi_tuner", script), "--dataset_config", datasetConfig, "--text_encoder", profile.TextEncoder, "--batch_size", "1"}
	if profile.Family == "flux2-klein" {
		args = append(args, "--model_version", profile.ModelVersion)
		if profile.FP8TextEncoder {
			args = append(args, "--fp8_text_encoder")
		}
	}
	return args
}

func normalizeTrainingSeed(seed int64) int64 {
	if seed > loratraining.MaxNumpySeed {
		return seed % (loratraining.MaxNumpySeed + 1)
	}
	return seed
}

func (controller *Controller) trainingArgs(profile ProfileConfig, spec loratraining.JobSpec, modelPath, datasetConfig, outputDir string) []string {
	script := "krea2_train_network.py"
	networkModule := "networks.lora_krea2"
	timestepSampling := "krea2_shift"
	if profile.Family == "flux2-klein" {
		script = "flux_2_train_network.py"
		networkModule = "networks.lora_flux_2"
		timestepSampling = "flux2_shift"
	}
	args := []string{
		"-m", "accelerate.commands.launch", "--num_cpu_threads_per_process", "1", "--mixed_precision", "bf16",
		filepath.Join(controller.config.TunerDir, "src", "musubi_tuner", script),
		"--dit", modelPath, "--vae", profile.VAE, "--dataset_config", datasetConfig,
		"--sdpa", "--mixed_precision", "bf16", "--timestep_sampling", timestepSampling,
		"--weighting_scheme", "none", "--optimizer_type", "adamw8bit",
		"--learning_rate", strconv.FormatFloat(spec.LearningRate, 'g', -1, 64), "--gradient_checkpointing",
		"--max_data_loader_n_workers", "2", "--persistent_data_loader_workers",
		"--network_module", networkModule, "--network_dim", strconv.Itoa(spec.NetworkDim),
		"--network_alpha", strconv.Itoa(spec.NetworkAlpha), "--max_train_steps", strconv.Itoa(spec.MaxSteps),
		"--seed", strconv.FormatInt(spec.Seed, 10), "--output_dir", outputDir, "--output_name", spec.OutputName,
	}
	if profile.Family == "flux2-klein" {
		args = append(args, "--model_version", profile.ModelVersion, "--text_encoder", profile.TextEncoder, "--vae_dtype", "bfloat16")
		if profile.FP8TextEncoder {
			args = append(args, "--fp8_text_encoder")
		}
	}
	if profile.FP8Base {
		args = append(args, "--fp8_base", "--fp8_scaled")
	}
	if profile.BlocksToSwap > 0 {
		args = append(args, "--blocks_to_swap", strconv.Itoa(profile.BlocksToSwap))
	}
	return args
}

func (controller *Controller) runCommand(ctx context.Context, id, logPath, state, stage string, progressStart, progressEnd int, args ...string) error {
	controller.setProgress(id, state, stage, progressStart, "Этап запущен.")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.Command(controller.config.PythonExe, args...)
	command.Dir = controller.config.TunerDir
	command.Env = append(os.Environ(),
		"PYTHONUTF8=1",
		"PYTHONUNBUFFERED=1",
		"USE_TF=0",
		"USE_FLAX=0",
		"TF_CPP_MIN_LOG_LEVEL=3",
		"TOKENIZERS_PARALLELISM=false",
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	lines := make(chan string, 64)
	var readers sync.WaitGroup
	readers.Add(2)
	go scanCommandOutput(stdout, lines, &readers)
	go scanCommandOutput(stderr, lines, &readers)
	go func() {
		readers.Wait()
		close(lines)
	}()
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	done := false
	cancelSignal := ctx.Done()
	for !done {
		select {
		case line, open := <-lines:
			if !open {
				lines = nil
				continue
			}
			_, _ = fmt.Fprintln(logFile, line)
			controller.commandLine(id, state, stage, progressStart, progressEnd, line)
		case err = <-waited:
			done = true
		case <-cancelSignal:
			killProcessTree(command.Process)
			cancelSignal = nil
		}
	}
	if lines != nil {
		for line := range lines {
			_, _ = fmt.Fprintln(logFile, line)
			controller.appendLog(id, line)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	controller.setProgress(id, state, stage, progressEnd, "Этап завершён.")
	return nil
}

func scanCommandOutput(reader io.Reader, lines chan<- string, done *sync.WaitGroup) {
	defer done.Done()
	scanner := bufio.NewScanner(reader)
	scanner.Split(splitCommandLines)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines <- truncate(line, 1000)
		}
	}
}

func splitCommandLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for index, value := range data {
		if value == '\n' || value == '\r' {
			return index + 1, data[:index], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (controller *Controller) commandLine(id, state, stage string, start, end int, line string) {
	progress := start
	if match := progressPattern.FindStringSubmatch(line); len(match) == 3 {
		current, _ := strconv.Atoi(match[1])
		total, _ := strconv.Atoi(match[2])
		if total > 0 && current <= total {
			progress = start + int(float64(end-start)*float64(current)/float64(total))
		}
	}
	controller.mu.Lock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.Unlock()
		return
	}
	record.Status.LogTail = append(record.Status.LogTail, line)
	if len(record.Status.LogTail) > 80 {
		record.Status.LogTail = append([]string(nil), record.Status.LogTail[len(record.Status.LogTail)-80:]...)
	}
	shouldPersist := progress > record.Status.Progress
	if progress > record.Status.Progress {
		record.Status.Progress = progress
		record.Status.State = state
		record.Status.Stage = stage
	}
	copyRecord := cloneRecord(record)
	controller.mu.Unlock()
	if shouldPersist {
		_ = controller.persist(copyRecord)
	}
}

func (controller *Controller) appendLog(id, line string) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if record := controller.jobs[id]; record != nil {
		record.Status.LogTail = append(record.Status.LogTail, truncate(line, 1000))
		if len(record.Status.LogTail) > 80 {
			record.Status.LogTail = append([]string(nil), record.Status.LogTail[len(record.Status.LogTail)-80:]...)
		}
	}
}

func (controller *Controller) setProgress(id, state, stage string, progress int, message string) {
	controller.mu.Lock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.Unlock()
		return
	}
	record.Status.State = state
	record.Status.Stage = stage
	record.Status.Progress = progress
	record.Status.Message = message
	copyRecord := cloneRecord(record)
	controller.mu.Unlock()
	_ = controller.persist(copyRecord)
}

func (controller *Controller) finish(id, state, stage string, progress int, message, errorMessage string) {
	controller.mu.Lock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.Unlock()
		return
	}
	record.Status.State = state
	record.Status.Stage = stage
	record.Status.Progress = progress
	record.Status.Message = message
	record.Status.Error = errorMessage
	record.Status.FinishedAt = time.Now()
	record.cancel = nil
	copyRecord := cloneRecord(record)
	controller.mu.Unlock()
	_ = controller.persist(copyRecord)
}

func (controller *Controller) validateSpec(spec loratraining.JobSpec) (ProfileConfig, error) {
	profile, ok := controller.config.Profile(spec.ProfileID)
	if !ok {
		return ProfileConfig{}, errors.New("неизвестный профиль обучения")
	}
	if !outputNamePattern.MatchString(spec.OutputName) || len([]rune(strings.TrimSpace(spec.Name))) < 3 || len([]rune(spec.Name)) > 80 {
		return ProfileConfig{}, errors.New("некорректное имя LoRA")
	}
	if len([]rune(strings.TrimSpace(spec.TriggerWord))) < 2 || len([]rune(spec.TriggerWord)) > 80 {
		return ProfileConfig{}, errors.New("некорректное триггер-слово")
	}
	if spec.ConceptType != "character" && spec.ConceptType != "style" && spec.ConceptType != "object" && spec.ConceptType != "product" {
		return ProfileConfig{}, errors.New("неизвестный тип LoRA")
	}
	if spec.Resolution != 512 && spec.Resolution != 768 && spec.Resolution != 1024 {
		return ProfileConfig{}, errors.New("неподдерживаемое разрешение датасета")
	}
	if spec.MaxSteps < 100 || spec.MaxSteps > 5000 || (spec.NetworkDim != 8 && spec.NetworkDim != 16 && spec.NetworkDim != 32 && spec.NetworkDim != 64) || spec.NetworkAlpha < 1 || spec.NetworkAlpha > 64 {
		return ProfileConfig{}, errors.New("параметры обучения вне допустимого диапазона")
	}
	if spec.LearningRate < 0.000001 || spec.LearningRate > 0.001 || spec.Seed < 0 || spec.Seed > loratraining.MaxNumpySeed || spec.SampleCount < 5 || spec.SampleCount > 100 {
		return ProfileConfig{}, errors.New("параметры датасета вне допустимого диапазона")
	}
	if strings.TrimSpace(spec.GatewayJobID) == "" || len(spec.GatewayJobID) > 96 {
		return ProfileConfig{}, errors.New("некорректный идентификатор Gateway")
	}
	return profile, nil
}

func extractDataset(archivePath, destination string, expectedImages int, maxBytes int64) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return errors.New("датасет не является корректным ZIP-архивом")
	}
	defer archive.Close()
	images := make(map[string]struct{})
	captions := make(map[string]struct{})
	var extracted int64
	for _, entry := range archive.File {
		clean := path.Clean(strings.ReplaceAll(entry.Name, "\\", "/"))
		if entry.FileInfo().IsDir() {
			continue
		}
		if clean != entry.Name || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || path.Dir(clean) != "images" || !entry.Mode().IsRegular() {
			return fmt.Errorf("недопустимый файл в датасете: %s", entry.Name)
		}
		extension := strings.ToLower(path.Ext(clean))
		stem := strings.TrimSuffix(path.Base(clean), extension)
		switch extension {
		case ".png", ".jpg", ".jpeg", ".webp":
			images[stem] = struct{}{}
		case ".txt":
			captions[stem] = struct{}{}
		default:
			return fmt.Errorf("неподдерживаемый файл датасета: %s", entry.Name)
		}
		extracted += int64(entry.UncompressedSize64)
		if extracted > maxBytes {
			return errors.New("распакованный датасет превышает допустимый размер")
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeOutputErr, closeInputErr); err != nil {
			return err
		}
	}
	if len(images) != expectedImages {
		return fmt.Errorf("ожидалось %d изображений, найдено %d", expectedImages, len(images))
	}
	for stem := range images {
		if _, ok := captions[stem]; !ok {
			return fmt.Errorf("для изображения %s отсутствует описание", stem)
		}
	}
	return nil
}

func writeDatasetConfig(target, imageDir, cacheDir string, resolution int) error {
	body := fmt.Sprintf(`[general]
resolution = [%d, %d]
caption_extension = ".txt"
batch_size = 1
enable_bucket = true
bucket_no_upscale = false

[[datasets]]
image_directory = %s
cache_directory = %s
num_repeats = 1
`, resolution, resolution, strconv.Quote(filepath.ToSlash(imageDir)), strconv.Quote(filepath.ToSlash(cacheDir)))
	return os.WriteFile(target, []byte(body), 0o640)
}

func findTrainingOutput(outputDir, outputName string) (string, error) {
	direct := filepath.Join(outputDir, outputName+".safetensors")
	if info, err := os.Stat(direct); err == nil && info.Mode().IsRegular() {
		return direct, nil
	}
	matches, _ := filepath.Glob(filepath.Join(outputDir, outputName+"*.safetensors"))
	if len(matches) == 0 {
		return "", errors.New("Musubi завершился без итогового файла LoRA")
	}
	sort.Slice(matches, func(i, j int) bool {
		left, _ := os.Stat(matches[i])
		right, _ := os.Stat(matches[j])
		return left.ModTime().After(right.ModTime())
	})
	return matches[0], nil
}

func copyFileAtomic(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temporaryPath := destinationPath + ".partial"
	_ = os.Remove(temporaryPath)
	target, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(target, source, make([]byte, 8<<20))
	if syncErr := target.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := target.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(temporaryPath)
		return copyErr
	}
	if err := os.Remove(destinationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(temporaryPath)
		return err
	}
	return os.Rename(temporaryPath, destinationPath)
}

func killProcessTree(process *os.Process) {
	if process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		if err := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F").Run(); err == nil {
			return
		}
	}
	_ = process.Kill()
}

func (controller *Controller) record(id string) *jobRecord {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return cloneRecord(controller.jobs[id])
}

func (controller *Controller) jobDir(id string) string {
	return filepath.Join(controller.config.RootDir, "jobs", id)
}

func (controller *Controller) persist(record *jobRecord) error {
	if record == nil {
		return nil
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(controller.jobDir(record.Status.ID), "status.json")
	temporary := target + ".partial"
	if err := os.WriteFile(temporary, payload, 0o640); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporary, target)
}

func (controller *Controller) loadJobs() error {
	matches, err := filepath.Glob(filepath.Join(controller.config.RootDir, "jobs", "*", "status.json"))
	if err != nil {
		return err
	}
	for _, filename := range matches {
		payload, err := os.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read training execution record: %w", err)
		}
		var record jobRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return fmt.Errorf("decode training execution record: %w", err)
		}
		if record.Status.ID == "" || record.Spec.GatewayJobID == "" || filepath.Base(filepath.Dir(filename)) != record.Status.ID {
			return errors.New("incomplete training execution inventory")
		}
		if !record.Status.Terminal() {
			record.Status.ExecutionUnconfirmed = true
			record.Status.State = "failed"
			record.Status.Stage = "Агент перезапущен"
			record.Status.Progress = 100
			record.Status.Message = "Агент перезапущен. Завершение прежнего процесса ещё не подтверждено."
			record.Status.Error = "Перед новым запуском требуется проверить завершение прежнего процесса."
			record.Status.FinishedAt = time.Now()
			_ = controller.persist(&record)
		}
		if record.Status.State == "failed" && record.Status.Stage == "Агент перезапущен" && !record.Status.ExecutionUnconfirmed {
			record.Status.ExecutionUnconfirmed = true
			if err := controller.persist(&record); err != nil {
				return err
			}
		}
		controller.jobs[record.Status.ID] = &record
		controller.byGateway[record.Spec.GatewayJobID] = record.Status.ID
	}
	return nil
}

func cloneRecord(record *jobRecord) *jobRecord {
	if record == nil {
		return nil
	}
	copyRecord := *record
	copyRecord.Status = cloneStatus(record.Status)
	copyRecord.cancel = nil
	return &copyRecord
}

func cloneStatus(status loratraining.JobStatus) loratraining.JobStatus {
	status.LogTail = append([]string(nil), status.LogTail...)
	return status
}

func randomID() (string, error) {
	buffer := make([]byte, 18)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit])
}
