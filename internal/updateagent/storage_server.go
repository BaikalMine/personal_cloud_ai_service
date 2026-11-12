package updateagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"ai-access-gateway/internal/updates"
)

func (s *Server) storageReady(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		methodNotAllowed(w, method)
		return false
	}
	if r.URL.RawQuery != "" {
		writeComfyStorageError(w, http.StatusBadRequest, "invalid_storage_request")
		return false
	}
	if s.storage == nil {
		writeComfyStorageError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return false
	}
	return true
}

func (s *Server) handleComfyStorageStatus(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w, r, http.MethodGet) {
		return
	}
	status, err := s.storage.Status(r.Context())
	if err != nil {
		writeComfyStorageErrorFor(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleComfyStorageResult(w http.ResponseWriter, r *http.Request) {
	if !s.storageReady(w, r, http.MethodGet) {
		return
	}
	result, err := s.storage.Result(r.Context(), r.PathValue("id"))
	writeComfyStorageOperation(w, result, err)
}

func (s *Server) handleComfyStorageExecute(w http.ResponseWriter, r *http.Request) {
	s.handleComfyStorageCommand(w, r, false)
}

func (s *Server) handleComfyStorageFence(w http.ResponseWriter, r *http.Request) {
	s.handleComfyStorageCommand(w, r, true)
}

func (s *Server) handleComfyStorageCommand(w http.ResponseWriter, r *http.Request, fence bool) {
	if !s.storageReady(w, r, http.MethodPost) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request updates.ComfyStorageRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		writeComfyStorageError(w, http.StatusBadRequest, "invalid_storage_request")
		return
	}
	if _, _, err := updates.CanonicalComfyStorageRequest(request); err != nil {
		writeComfyStorageError(w, http.StatusBadRequest, "invalid_storage_request")
		return
	}
	var result updates.ComfyStorageResult
	var err error
	if fence {
		result, err = s.storage.Fence(r.Context(), request)
	} else {
		result, err = s.storage.Execute(r.Context(), request)
	}
	writeComfyStorageOperation(w, result, err)
}

func writeComfyStorageOperation(w http.ResponseWriter, result updates.ComfyStorageResult, err error) {
	if err != nil {
		if updates.ValidateComfyStorageResult(result) == nil {
			writeJSON(w, http.StatusServiceUnavailable, result)
		} else {
			writeComfyStorageErrorFor(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeComfyStorageErrorFor(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, updates.ErrComfyStorageRequest):
		writeComfyStorageError(w, http.StatusBadRequest, "invalid_storage_request")
	case errors.Is(err, updates.ErrComfyStorageNotFound):
		writeComfyStorageError(w, http.StatusNotFound, "storage_operation_not_found")
	case errors.Is(err, ErrComfyStorageBusy):
		writeComfyStorageError(w, http.StatusConflict, "storage_busy")
	case errors.Is(err, ErrComfyStorageConflict):
		writeComfyStorageError(w, http.StatusConflict, "storage_operation_conflict")
	default:
		writeComfyStorageError(w, http.StatusServiceUnavailable, "storage_unconfirmed")
	}
}

func writeComfyStorageError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, struct {
		Protocol int    `json:"protocol"`
		Error    string `json:"error"`
	}{Protocol: 1, Error: code})
}

func (s *Server) installWithStorageGuard(ctx context.Context, request updates.Request) (updates.Status, error) {
	if s.storage == nil {
		return s.controller.Install(ctx, request)
	}
	root, lock, err := s.storage.openJournal()
	if err != nil {
		return updates.Status{}, ErrComfyStorageBusy
	}
	defer root.Close()
	defer lock.Close()
	if _, err := readComfyStorageRecord(root, comfyStoragePendingFile, ""); !errors.Is(err, os.ErrNotExist) {
		return updates.Status{}, ErrComfyStorageBusy
	}
	guard, err := s.storage.guard.status(ctx)
	if err != nil || guard.Held {
		return updates.Status{}, ErrComfyStorageBusy
	}
	return s.controller.Install(ctx, request)
}

// MaintainStorage is owned by the command lifetime, not by HTTP requests.
func (s *Server) MaintainStorage(ctx context.Context) {
	if s.storage == nil {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.storage.Prune(ctx, time.Now()); err != nil && !errors.Is(err, ErrComfyStorageBusy) && ctx.Err() == nil {
			log.Print("ComfyUI storage receipt maintenance remains unconfirmed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
