package gateway

import (
	"database/sql"
	"errors"
	"net/http"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func writeGenerationSubmissionError(w http.ResponseWriter, err error, kind, requestID string) bool {
	state, message := "", ""
	switch {
	case errors.Is(err, store.ErrGenerationSubmissionClosed):
		state, message = "cancelled", "Этот запрос закрыт до создания задания. Параметры сохранены."
	case errors.Is(err, store.ErrGenerationSubmissionExpired):
		state, message = "expired", "История этого запроса уже удалена. Повторная отправка того же запроса запрещена."
	default:
		return false
	}
	writeJSON(w, http.StatusGone, map[string]any{"kind": kind, "request_id": requestID, "state": state, "request_closed": true, "error": message, "message": message})
	return true
}

// A GET never turns absence into evidence of non-dispatch. Only POST close
// serializes with the creation transaction and can fence an absent request.
func (a *App) handleGenerationSubmission(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	closeRequest := r.URL.Path == "/generate/submission/close"
	method := http.MethodGet
	if closeRequest {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	values := r.URL.Query()
	if closeRequest {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if !a.validCSRF(r) {
			writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
			return
		}
		values = r.PostForm
	}
	kind, requestID := values.Get("kind"), values.Get("request_id")
	if kind != "single" && kind != "batch" || !validGenerationRequestID(requestID) || len(values["kind"]) != 1 || len(values["request_id"]) != 1 {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор запроса")
		return
	}
	for name := range values {
		if name != "kind" && name != "request_id" && !(closeRequest && name == "csrf") {
			writeGenerationError(w, http.StatusBadRequest, "неизвестный параметр запроса")
			return
		}
	}
	user := a.currentUser(r)
	var receipt domain.GenerationSubmission
	var err error
	if closeRequest {
		receipt, err = a.store.CloseGenerationSubmission(r.Context(), user.ID, kind, requestID)
	} else {
		receipt, err = a.store.GenerationSubmissionByRequest(r.Context(), user.ID, kind, requestID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]any{"kind": kind, "request_id": requestID, "state": "unknown", "request_closed": false, "message": "Ответ о запуске ещё не подтверждён. Повторная отправка не требуется."})
		return
	}
	if err != nil || !receipt.Closed && receipt.PublicID == "" {
		writeGenerationError(w, http.StatusServiceUnavailable, "состояние запроса пока не подтверждено")
		return
	}
	if receipt.Closed {
		if closeRequest {
			a.audit(r.Context(), &user.ID, "generation_submission_closed", "generation_submission", nil, a.clientIP(r), r.UserAgent(), map[string]any{"kind": kind, "request_id": requestID})
		}
		writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "request_id": requestID, "state": "cancelled", "request_closed": true, "message": "Запрос закрыт до создания задания."})
		return
	}
	var response map[string]any
	if kind == "single" {
		var job domain.GenerationJob
		job, err = a.store.GenerationJobByPublicID(r.Context(), user.ID, receipt.PublicID)
		if err == nil {
			if job.RequestID != requestID {
				err = store.ErrGenerationSubmissionUncertain
			} else {
				job = a.reconcileGenerationClientJob(r.Context(), job, user.ID)
				response = a.generationJobResponse(r.Context(), job)
				if job.State == domain.GenerationJobCompleted {
					views, viewErr := a.generationJobViews(r.Context(), []domain.GenerationJob{job}, user.ID)
					if viewErr != nil {
						err = viewErr
					} else {
						response["job"] = views[0]
					}
				}
			}
		}
	} else {
		view, viewErr := a.generationBatchViewByID(r.Context(), user.ID, receipt.PublicID)
		err = viewErr
		if err == nil {
			response = map[string]any{"batch": view, "state": view.State}
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeGenerationSubmissionError(w, store.ErrGenerationSubmissionExpired, kind, requestID)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "не удалось получить состояние задания")
		return
	}
	response["kind"], response["request_id"], response["request_closed"] = kind, requestID, false
	writeJSON(w, http.StatusOK, response)
}
