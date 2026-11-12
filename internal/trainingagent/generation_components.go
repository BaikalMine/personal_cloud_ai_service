package trainingagent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-access-gateway/internal/componentinspection"
)

func (server *Server) handleGenerationComponents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "Invalid inspection query.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request componentinspection.Request
	var trailing any
	if decoder.Decode(&request) != nil || decoder.Decode(&trailing) != io.EOF || !request.Valid() {
		writeError(w, http.StatusBadRequest, "Invalid component inspection.")
		return
	}
	if server.controller.components == nil {
		writeCodedError(w, http.StatusServiceUnavailable, "inspection_unavailable", "Component inspection is unavailable.")
		return
	}
	response, err := server.controller.components.Inspect(request)
	if errors.Is(err, componentinspection.ErrBusy) {
		writeCodedError(w, http.StatusServiceUnavailable, "inspection_busy", "Component inspection is busy.")
		return
	}
	if err != nil {
		writeCodedError(w, http.StatusServiceUnavailable, "inspection_unavailable", "Component inspection is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
