package gateway

import "errors"

var errGenerationMiningPause = errors.New("generation mining pause failed")

func generationSubmissionFailure(err error) (code, message string) {
	if errors.Is(err, errGenerationMiningPause) {
		return "mining_pause_failed", "Не удалось освободить GPU: остановка майнинга не подтверждена. Генерация не запускалась."
	}
	return "comfy_submission_failed", "ComfyUI не подтвердил запуск"
}
