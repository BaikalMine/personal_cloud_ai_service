package miningagent

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-access-gateway/internal/mining"
)

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	controller, ok := s.controller.(fileController)
	if !ok {
		http.Error(w, "file management unavailable", http.StatusNotImplemented)
		return
	}
	result, err := controller.Files(r.Context())
	status := http.StatusOK
	if err != nil {
		result.Files = nil
		result.Message = err.Error()
		status = http.StatusConflict
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handleSaveScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	controller, ok := s.controller.(fileController)
	if !ok {
		http.Error(w, "file management unavailable", http.StatusNotImplemented)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input mining.SaveScriptRequest
	if err := decoder.Decode(&input); err != nil {
		writeScript(w, http.StatusBadRequest, mining.Script{Message: "Некорректное тело запроса."})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeScript(w, http.StatusBadRequest, mining.Script{Message: "Лишние данные в запросе."})
		return
	}
	input.ScriptPath = strings.TrimSpace(input.ScriptPath)
	if input.ScriptPath == "" || len(input.ScriptPath) > 1024 || !validProcessName(input.ProcessName) || !validSHA256(input.ExpectedSHA256) || len(input.Content) > maxEditableScriptBytes {
		writeScript(w, http.StatusBadRequest, mining.Script{Message: "Проверьте файл, имя процесса и размер текста BAT."})
		return
	}
	// Serialize file edits with fenced start/stop/update commands.
	select {
	case s.commandGate <- struct{}{}:
		defer func() { <-s.commandGate }()
	case <-r.Context().Done():
		return
	}
	if s.commandUnconfirmed {
		writeScript(w, http.StatusConflict, mining.Script{Message: "Состояние команды майнера ещё не подтверждено."})
		return
	}
	result, err := controller.SaveScript(r.Context(), input)
	if err != nil {
		result.Content = ""
		result.Message = err.Error()
		writeScript(w, http.StatusConflict, result)
		return
	}
	writeScript(w, http.StatusOK, result)
}
