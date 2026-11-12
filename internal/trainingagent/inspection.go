package trainingagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

var errInspectionBusy = errors.New("artifact inspection is busy")

func (controller *Controller) InspectArtifact(ctx context.Context, id string, request loratraining.ArtifactInspectionRequest) (loratraining.ArtifactInspection, error) {
	result := loratraining.ArtifactInspection{Version: 1, AgentJobID: id, Models: []loratraining.ModelInspection{}, CheckedAt: time.Now().UTC()}
	if !request.Valid() {
		return result, errors.New("invalid inspection request")
	}
	if !controller.inspectionMu.TryLock() {
		return result, errInspectionBusy
	}
	defer controller.inspectionMu.Unlock()
	controller.mu.RLock()
	record := controller.jobs[id]
	if record == nil {
		controller.mu.RUnlock()
		return result, os.ErrNotExist
	}
	spec, status := record.Spec, cloneStatus(record.Status)
	controller.mu.RUnlock()
	result.GatewayJobID, result.Owner = spec.GatewayJobID, spec.Owner
	result.State = "not_completed"
	if status.State != "completed" || status.ExecutionUnconfirmed {
		return result, nil
	}
	p := status.Provenance
	result.State = "provenance_missing"
	if p == nil || p.Artifact == nil || p.Validate() != nil || p.GatewayJobID != spec.GatewayJobID || p.Profile.ID != spec.ProfileID {
		return result, nil
	}
	result.Family, result.ProfileSHA256 = p.Profile.Family, p.ProfileSHA256
	architecture := loratraining.TrainingArchitecture(p.Profile.Family, p.Profile.ModelVersion)
	if architecture == "" {
		return result, nil
	}
	relative, err := loratraining.ArtifactRelativeName(spec.Owner, p.Profile.Family, status.ArtifactName)
	if err != nil || p.Artifact.Name != status.ArtifactName || p.Artifact.Bytes != status.ArtifactBytes {
		result.State = "artifact_changed"
		return result, nil
	}
	result.RelativeName = relative
	loraRoot, err := os.OpenRoot(controller.config.ComfyLoraDir)
	if err != nil {
		result.State = "artifact_missing"
		return result, nil
	}
	defer loraRoot.Close()
	file, err := loraRoot.Open(filepath.FromSlash(relative))
	if err != nil {
		result.State = "artifact_missing"
		return result, nil
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != p.Artifact.Bytes {
		result.State = "artifact_changed"
		return result, nil
	}
	n, digest, err := backupDigest(ctx, io.Discard, file)
	if err != nil {
		return result, err
	}
	after, err := file.Stat()
	current, currentErr := loraRoot.Stat(filepath.FromSlash(relative))
	if err != nil || currentErr != nil || !os.SameFile(before, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) || n != p.Artifact.Bytes || digest != p.Artifact.SHA256 {
		result.State = "artifact_changed"
		return result, nil
	}
	identity := domain.LoraFileIdentity{Name: status.ArtifactName, Bytes: n, SHA256: digest}
	result.Artifact = &identity
	directory := controller.config.ComfyDiffusionDir
	if directory == "" {
		directory = filepath.Join(filepath.Dir(controller.config.ComfyLoraDir), "diffusion_models")
	}
	modelRoot, err := os.OpenRoot(directory)
	if err == nil {
		defer modelRoot.Close()
	}
	for _, candidate := range request.Models {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name, _ := loratraining.ModelRelativeName(candidate)
		model := loratraining.ModelInspection{Name: name, State: "unavailable"}
		if modelRoot != nil {
			model.Architecture, model.State = inspectGenerationModel(modelRoot, name)
			if model.State == "inspected" {
				model.State = "incompatible"
				if model.Architecture == architecture {
					model.State = "compatible"
				}
			}
		}
		result.Models = append(result.Models, model)
	}
	// Removal/replacement during model inspection invalidates the whole observation.
	current, currentErr = loraRoot.Stat(filepath.FromSlash(relative))
	controller.mu.RLock()
	stillPresent := controller.jobs[id] == record && record.Status.State == "completed"
	controller.mu.RUnlock()
	if currentErr != nil || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) || !stillPresent {
		result.State = "artifact_changed"
		result.Models = nil
		return result, nil
	}
	result.State = "verified"
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func inspectGenerationModel(root *os.Root, name string) (string, string) {
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return "", "unavailable"
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return "", "unavailable"
	}
	architecture, err := loratraining.SafetensorsArchitecture(file, before.Size())
	if err != nil {
		return "", "unsupported"
	}
	after, err := file.Stat()
	current, currentErr := root.Stat(filepath.FromSlash(name))
	if err != nil || currentErr != nil || !os.SameFile(before, current) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) ||
		current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
		return "", "unavailable"
	}
	return architecture, "inspected"
}

func (server *Server) handleArtifactInspection(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "Некорректный запрос проверки.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request loratraining.ArtifactInspectionRequest
	var trailing any
	if decoder.Decode(&request) != nil || decoder.Decode(&trailing) != io.EOF || !request.Valid() {
		writeError(w, http.StatusBadRequest, "Некорректный запрос проверки.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	result, err := server.controller.InspectArtifact(ctx, id, request)
	if errors.Is(err, os.ErrNotExist) {
		writeCodedError(w, http.StatusNotFound, "job_not_found", "Задание не найдено.")
		return
	}
	if errors.Is(err, errInspectionBusy) {
		writeCodedError(w, http.StatusServiceUnavailable, "inspection_busy", "Другая проверка ещё выполняется. Повторите запрос.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Не удалось проверить файлы LoRA и моделей.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
