package gateway

import (
	"context"
	"time"
)

const maintenanceInterval = 15 * time.Minute
const generationRefreshInterval = 30 * time.Second
const comfyMemoryMonitorInterval = 10 * time.Second

func (a *App) runMaintenance(ctx context.Context) {
	runMaintenanceWorkers(ctx, a.maintenanceWorkerRegistry(), a.maintenanceWorkerSpecs())
}

func (a *App) maintenanceWorkerSpecs() []maintenanceWorkerSpec {
	dependencyInterval := a.cfg.DependencyCheckInterval
	if dependencyInterval <= 0 {
		dependencyInterval = defaultDependencyCheckInterval
	}
	shortRetry := 15 * time.Second
	return []maintenanceWorkerSpec{
		{
			Key: "lora_datasets_cleanup", Name: "Очистка датасетов LoRA", Interval: maintenanceInterval, Timeout: time.Minute, InitialDelay: 18 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.store.CleanupLoraDatasets,
		},
		{
			Key: "generation_drafts_cleanup", Name: "Очистка черновиков", Interval: maintenanceInterval, Timeout: 15 * time.Second, InitialDelay: 16 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.store.DeleteExpiredGenerationDrafts,
		},
		{
			Key: "lora_training", Name: "Обучение LoRA", Interval: 3 * time.Second, Timeout: 20 * time.Minute,
			RetryDelay: 10 * time.Second, MaxBackoff: 2 * time.Minute, Run: a.refreshLoraTrainingJobs,
		},
		{
			Key: "lora_training_failed_cleanup", Name: "Очистка ошибок обучения LoRA", Interval: maintenanceInterval, Timeout: 2 * time.Minute, InitialDelay: 4 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.deleteExpiredFailedLoraTrainingJobs,
		},
		{
			Key: "generation_batches", Name: "Пакеты вариантов", Interval: 2 * time.Second, Timeout: 45 * time.Second,
			RetryDelay: 5 * time.Second, MaxBackoff: time.Minute, Run: a.dispatchGenerationBatchJobs,
		},
		{
			Key: "generation_jobs", Name: "Задания генераций", Interval: generationRefreshInterval, Timeout: 20 * time.Second,
			RetryDelay: shortRetry, MaxBackoff: 2 * time.Minute, Run: a.refreshTrackedGenerationStatuses,
		},
		{
			Key: "mining_leases", Name: "Аренды майнинга", Interval: generationRefreshInterval, Timeout: 15 * time.Second, InitialDelay: time.Second,
			RetryDelay: shortRetry, MaxBackoff: 2 * time.Minute, Run: a.refreshQuickGenerationMiningLeases,
		},
		{
			Key: "host_metrics", Name: "Метрики Windows", Interval: generationRefreshInterval, Timeout: 8 * time.Second, InitialDelay: 2 * time.Second,
			RetryDelay: shortRetry, MaxBackoff: 2 * time.Minute, Run: a.captureHostMetric,
		},
		{
			Key: "observability_snapshot", Name: "Снимок наблюдаемости", Interval: gatewayObservationInterval, Timeout: 8 * time.Second, InitialDelay: 2500 * time.Millisecond,
			RetryDelay: shortRetry, MaxBackoff: 2 * time.Minute, Run: a.captureGatewayObservation,
		},
		{
			Key: "comfy_memory", Name: "Освобождение памяти ComfyUI", Interval: comfyMemoryMonitorInterval, Timeout: 8 * time.Second, InitialDelay: 3 * time.Second,
			RetryDelay: shortRetry, MaxBackoff: time.Minute, Run: a.observeComfyQueueForMemoryRelease,
		},
		{
			Key: "dependency_health", Name: "Состояние зависимостей", Interval: dependencyInterval, Timeout: 4 * time.Second,
			RetryDelay: shortRetry, MaxBackoff: time.Minute, Run: func(ctx context.Context) (int64, error) {
				a.refreshDependencyStatuses(ctx)
				return int64(len(a.dependencySpecs())), nil
			},
		},
		{
			Key: "websocket_authorization", Name: "Авторизация WebSocket", Interval: websocketAuthorizationRefreshInterval, Timeout: 8 * time.Second, InitialDelay: 4 * time.Second,
			RetryDelay: shortRetry, MaxBackoff: 2 * time.Minute, Run: a.pruneUnauthorizedWebSockets,
		},
		{
			Key: "suggestion_scans", Name: "Проверка предложений", Interval: time.Minute, Timeout: 2 * time.Minute, InitialDelay: 5 * time.Second,
			RetryDelay: time.Minute, MaxBackoff: maintenanceInterval, Run: a.refreshFeatureSuggestionScans,
		},
		{
			Key: "media_archive", Name: "Архивация результатов", Interval: maintenanceInterval, Timeout: 2 * time.Minute, InitialDelay: 6 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.backfillComfyContentMedia,
		},
		{
			Key: "media_hashes", Name: "Хэши архивных медиа", Interval: maintenanceInterval, Timeout: 2 * time.Minute, InitialDelay: 7 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.backfillContentMediaHashes,
		},
		{
			Key: "comfy_input_cleanup", Name: "Очистка входных файлов", Interval: maintenanceInterval, Timeout: 3 * time.Minute, InitialDelay: 8 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.deleteExpiredComfyInputs,
		},
		{
			Key: "comfy_media_cleanup", Name: "Очистка результатов ComfyUI", Interval: maintenanceInterval, Timeout: 3 * time.Minute, InitialDelay: 9 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.deleteExpiredComfyMedia,
		},
		{
			Key: "other_media_cleanup", Name: "Очистка остальных медиа", Interval: maintenanceInterval, Timeout: 15 * time.Second, InitialDelay: 10 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.store.DeleteExpiredNonComfyMedia,
		},
		{
			Key: "database_retention", Name: "Сроки хранения БД", Interval: maintenanceInterval, Timeout: databaseRetentionTimeout, InitialDelay: 11 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: func(ctx context.Context) (int64, error) {
				report, err := a.runDatabaseRetentionCleanup(ctx)
				return report.TotalDeleted(), err
			},
		},
		{
			Key: "session_cleanup", Name: "Очистка сессий", Interval: maintenanceInterval, Timeout: 15 * time.Second, InitialDelay: 12 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: func(ctx context.Context) (int64, error) {
				return a.store.DeleteExpiredSessions(ctx, a.cfg.SessionIdleTimeout)
			},
		},
		{
			Key: "temporary_users", Name: "Удаление временных аккаунтов", Interval: maintenanceInterval, Timeout: 15 * time.Second, InitialDelay: 13 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.store.DeleteExpiredTemporaryUsers,
		},
		{
			Key: "content_cleanup", Name: "Очистка AI-контента", Interval: maintenanceInterval, Timeout: 15 * time.Second, InitialDelay: 14 * time.Second,
			RetryDelay: 30 * time.Second, MaxBackoff: 5 * time.Minute, Run: a.store.DeleteExpiredContent,
		},
	}
}
