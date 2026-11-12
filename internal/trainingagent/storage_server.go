package trainingagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/loratraining"
)

func decodeStorageRequest(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return loratraining.ErrStorageRequest
	}
	return nil
}

func (server *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	query := r.URL.Query()
	if len(query["after"]) > 1 || len(query["limit"]) > 1 || len(query) > 2 {
		writeCodedError(w, 400, "invalid_storage_request", "Invalid storage query.")
		return
	}
	for key := range query {
		if key != "after" && key != "limit" {
			writeCodedError(w, 400, "invalid_storage_request", "Invalid storage query.")
			return
		}
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeCodedError(w, 400, "invalid_storage_request", "Invalid page size.")
			return
		}
		limit = value
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	result, err := server.controller.StorageInventory(ctx, query.Get("after"), limit)
	if err != nil {
		server.storageError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (server *Server) handleStoragePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input loratraining.StoragePreviewRequest
	if err := decodeStorageRequest(w, r, &input); err != nil {
		server.storageError(w, loratraining.ErrStorageRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	result, err := server.controller.PreviewStorageCleanup(ctx, input.IDs)
	if err != nil {
		server.storageError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (server *Server) handleStorageCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input loratraining.StorageCleanupRequest
	if err := decodeStorageRequest(w, r, &input); err != nil {
		server.storageError(w, loratraining.ErrStorageRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := server.controller.CleanupStorage(ctx, input)
	if err != nil {
		server.storageError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (server *Server) handleStorageOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/storage/operations/")
	result, err := server.controller.StorageCleanupResult(r.Context(), id)
	if err != nil {
		server.storageError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (server *Server) handleStorageFence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input loratraining.StorageCleanupRequest
	if err := decodeStorageRequest(w, r, &input); err != nil {
		server.storageError(w, loratraining.ErrStorageRequest)
		return
	}
	result, err := server.controller.FenceStorageCleanup(r.Context(), input)
	if err != nil {
		server.storageError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (server *Server) storageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, loratraining.ErrStorageRequest):
		writeCodedError(w, 400, "invalid_storage_request", "Invalid training storage request.")
	case errors.Is(err, ErrStorageBusy):
		writeCodedError(w, 409, "storage_busy", "Training storage is in use.")
	case errors.Is(err, ErrStorageOperationConflict):
		writeCodedError(w, 409, "storage_operation_conflict", "This operation ID belongs to another selection.")
	case errors.Is(err, ErrStorageResultExpired):
		writeCodedError(w, 410, "storage_result_expired", "The detailed result expired; this operation ID cannot execute again.")
	case errors.Is(err, os.ErrNotExist):
		writeCodedError(w, 404, "storage_operation_not_found", "The operation has no retained result.")
	default:
		log.Printf("training storage operation: %v", err)
		writeCodedError(w, 503, "storage_unavailable", "Training storage could not be inspected or the operation is unconfirmed.")
	}
}
