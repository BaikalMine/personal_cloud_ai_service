package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const (
	maxGenerationRequest = 32 << 10
	maxComfyObjectInfo   = 32 << 20
	maxGenerationHistory = 32 << 20
)

type generationOutput struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	URL       string `json:"url"`
}

type generationStatus struct {
	PromptID string             `json:"prompt_id"`
	State    string             `json:"state"`
	Message  string             `json:"message"`
	Outputs  []generationOutput `json:"outputs,omitempty"`
}

func (a *App) registerGenerationRoutes(mux *http.ServeMux) {
	page := a.requireAuth(a.requireServiceAccess("comfyui", http.HandlerFunc(a.handleGeneratePage)))
	run := a.requireAuth(a.requireServiceAccess("comfyui", http.HandlerFunc(a.handleGenerateRun)))
	status := a.requireAuth(a.requireServiceAccess("comfyui", http.HandlerFunc(a.handleGenerateStatus)))
	mux.Handle("/generate", page)
	mux.Handle("/generate/", page)
	mux.Handle("/generate/run", run)
	mux.Handle("/generate/status", status)
}

func (a *App) handleGeneratePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/generate" && r.URL.Path != "/generate/" {
		http.NotFound(w, r)
		return
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		http.Error(w, "не удалось загрузить шаблоны генерации", http.StatusInternalServerError)
		return
	}
	models := a.comfyCheckpointNames(r.Context())
	views := make([]workflowView, 0, len(definitions))
	for _, definition := range definitions {
		views = append(views, workflowView{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RequiresImage: definition.RequiresImage,
		})
	}
	a.render(w, r, "generate", map[string]any{
		"Title":            "Быстрая генерация",
		"Workflows":        views,
		"Models":           models,
		"ComfyOnline":      len(models) > 0,
		"SelectedWorkflow": r.URL.Query().Get("workflow"),
	})
}

func (a *App) handleGenerateRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationRequest)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	input, err := parseGenerationForm(r)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить шаблоны генерации")
		return
	}
	definition, ok := findWorkflow(definitions, input.TemplateID)
	if !ok {
		writeGenerationError(w, http.StatusBadRequest, "неизвестный шаблон workflow")
		return
	}
	if input.Seed < 0 {
		input.Seed, err = randomSeed()
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить случайный seed")
			return
		}
	}
	user := a.currentUser(r)
	if definition.RequiresImage {
		if err := a.validateGenerationImage(input.InputImage, user.ID); err != nil {
			writeGenerationError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	prompt, err := definition.buildPrompt(input)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	promptID, err := a.submitComfyPrompt(r.Context(), user.ID, prompt)
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "ComfyUI не принял workflow: "+err.Error())
		return
	}
	a.rememberGeneration(promptID, user.ID)
	a.recordGenerationEvent(r.Context(), user.ID, promptID, definition, input)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"prompt_id": promptID,
		"message":   "Генерация поставлена в очередь",
	})
}

func (a *App) handleGenerateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	promptID := strings.TrimSpace(r.URL.Query().Get("prompt_id"))
	if !validComfyPromptID(promptID) {
		writeGenerationError(w, http.StatusBadRequest, "некорректный идентификатор генерации")
		return
	}
	user := a.currentUser(r)
	owned, err := a.generationOwned(r.Context(), promptID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось проверить владельца генерации")
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	status, err := a.fetchGenerationStatus(r.Context(), promptID, user.ID)
	if err != nil {
		writeGenerationError(w, http.StatusBadGateway, "не удалось получить состояние генерации: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) submitComfyPrompt(ctx context.Context, userID int64, prompt map[string]any) (string, error) {
	document := map[string]any{"prompt": prompt, "client_id": a.comfyClientID(userID)}
	body, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/prompt")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", response.StatusCode, truncate(string(responseBody), 300))
	}
	var result struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors map[string]any `json:"node_errors"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("некорректный ответ ComfyUI: %w", err)
	}
	if result.PromptID == "" {
		if len(result.NodeErrors) > 0 {
			return "", errors.New("ComfyUI отклонил узлы workflow")
		}
		return "", errors.New("ComfyUI не вернул prompt_id")
	}
	if !validComfyPromptID(result.PromptID) {
		return "", errors.New("ComfyUI вернул некорректный prompt_id")
	}
	return result.PromptID, nil
}

func (a *App) fetchGenerationStatus(ctx context.Context, promptID string, userID int64) (generationStatus, error) {
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/history/"+url.PathEscape(promptID))
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return generationStatus{}, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return generationStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return generationStatus{}, fmt.Errorf("history returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGenerationHistory+1))
	if err != nil || len(body) > maxGenerationHistory {
		if err != nil {
			return generationStatus{}, err
		}
		return generationStatus{}, errors.New("ответ history слишком большой")
	}
	var history map[string]json.RawMessage
	if err := json.Unmarshal(body, &history); err != nil {
		return generationStatus{}, err
	}
	rawEntry, exists := history[promptID]
	status := generationStatus{PromptID: promptID, State: "queued", Message: "Генерация ожидает запуска"}
	if !exists {
		return status, nil
	}
	var entry struct {
		Outputs map[string]json.RawMessage `json:"outputs"`
		Status  struct {
			StatusStr string `json:"status_str"`
			Completed bool   `json:"completed"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rawEntry, &entry); err != nil {
		return generationStatus{}, err
	}
	outputs, err := parseGenerationOutputs(entry.Outputs)
	if err != nil {
		return generationStatus{}, err
	}
	status.Outputs = outputs
	for _, output := range outputs {
		if a.store == nil {
			break
		}
		// Ownership is recorded only after ComfyUI confirms the output exists.
		_ = a.store.InsertComfyOutputOwnerships(ctx, userID, []domain.ComfyOutputOwnership{{
			PromptID: promptID, Filename: output.Filename, Subfolder: output.Subfolder,
			StorageType: output.Type, MediaType: output.MediaType,
		}})
	}
	a.rememberGenerationOutputs(promptID, outputs)
	statusStr := strings.ToLower(strings.TrimSpace(entry.Status.StatusStr))
	if statusStr == "error" || statusStr == "failed" {
		status.State, status.Message = "error", "ComfyUI завершил генерацию с ошибкой"
		return status, nil
	}
	if entry.Status.Completed || statusStr == "success" || len(outputs) > 0 {
		status.State, status.Message = "completed", "Готово"
		return status, nil
	}
	status.State, status.Message = "running", "ComfyUI выполняет workflow"
	return status, nil
}

func parseGenerationOutputs(nodes map[string]json.RawMessage) ([]generationOutput, error) {
	seen := map[string]struct{}{}
	var outputs []generationOutput
	for _, rawNode := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(rawNode, &node); err != nil {
			continue
		}
		for kind, rawItems := range node {
			if kind != "images" && kind != "gifs" && kind != "videos" {
				continue
			}
			var items []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
				Format    string `json:"format"`
			}
			if json.Unmarshal(rawItems, &items) != nil {
				continue
			}
			for _, item := range items {
				if strings.TrimSpace(item.Filename) == "" {
					continue
				}
				if item.Type == "" {
					item.Type = "output"
				}
				mediaType := classifyComfyOutput(kind, item.Filename, item.Format)
				key := item.Filename + "\x00" + item.Subfolder + "\x00" + item.Type
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				query := url.Values{}
				query.Set("filename", item.Filename)
				query.Set("subfolder", item.Subfolder)
				query.Set("type", item.Type)
				path := "/comfyui/view"
				if mediaType == "video" {
					path = "/comfyui/viewvideo"
				}
				outputs = append(outputs, generationOutput{Filename: item.Filename, Subfolder: item.Subfolder, Type: item.Type, MediaType: mediaType, URL: path + "?" + query.Encode()})
			}
		}
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Filename < outputs[j].Filename })
	return outputs, nil
}

func (a *App) rememberGeneration(promptID string, userID int64) {
	a.generationMu.Lock()
	defer a.generationMu.Unlock()
	if a.generationJobs == nil {
		a.generationJobs = make(map[string]*generationJob)
	}
	for id, job := range a.generationJobs {
		if time.Since(job.CreatedAt) > 2*time.Hour {
			delete(a.generationJobs, id)
		}
	}
	a.generationJobs[promptID] = &generationJob{UserID: userID, CreatedAt: time.Now(), Outputs: make(map[string]struct{})}
}

func (a *App) rememberGenerationOutputs(promptID string, outputs []generationOutput) {
	a.generationMu.Lock()
	defer a.generationMu.Unlock()
	job := a.generationJobs[promptID]
	if job == nil {
		return
	}
	if job.Outputs == nil {
		job.Outputs = make(map[string]struct{})
	}
	for _, output := range outputs {
		job.Outputs[output.Filename+"\x00"+output.Subfolder+"\x00"+output.Type] = struct{}{}
	}
}

func (a *App) generationOwned(ctx context.Context, promptID string, userID int64) (bool, error) {
	a.generationMu.Lock()
	job := a.generationJobs[promptID]
	a.generationMu.Unlock()
	if job != nil {
		return job.UserID == userID, nil
	}
	if a.store == nil {
		return false, nil
	}
	return a.store.ContentEventOwnedByUser(ctx, "comfyui", promptID, userID)
}

func (a *App) validateGenerationImage(value string, userID int64) error {
	normalized, err := normalizeComfyDataPath(value, false)
	if err != nil {
		return errors.New("некорректное загруженное фото")
	}
	ownNamespace := comfyUploadNamespace(a.comfyClientID(userID))
	if !strings.HasPrefix(normalized, ownNamespace+"/") {
		return errors.New("фото принадлежит другой сессии")
	}
	return nil
}

func (a *App) recordGenerationEvent(ctx context.Context, userID int64, promptID string, definition workflowDefinition, input generationForm) {
	if a.contentCipher == nil || a.store == nil {
		return
	}
	metadata, _ := json.Marshal(map[string]any{"workflow": definition.ID, "checkpoint": input.Checkpoint, "width": input.Width, "height": input.Height, "steps": input.Steps, "cfg": input.CFG, "denoise": input.Denoise, "seed": input.Seed})
	promptCipher, err := a.contentCipher.Encrypt(input.Positive)
	if err != nil {
		log.Printf("generation prompt encryption: %v", err)
		return
	}
	negativeCipher, err := a.contentCipher.Encrypt(input.Negative)
	if err != nil {
		log.Printf("generation negative prompt encryption: %v", err)
		return
	}
	metadataCipher, err := a.contentCipher.Encrypt(string(metadata))
	if err != nil {
		log.Printf("generation metadata encryption: %v", err)
		return
	}
	if _, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: promptID, Model: input.Checkpoint, PromptCipher: promptCipher, ResponseCipher: negativeCipher, MetadataCipher: metadataCipher}); err != nil {
		log.Printf("store generation event: %v", err)
	}
}

func (a *App) comfyCheckpointNames(ctx context.Context) []string {
	if a.cfg.ComfyUIUpstream == nil {
		return nil
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/object_info")
	endpoint.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return nil
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxComfyObjectInfo+1))
	if err != nil || len(body) > maxComfyObjectInfo {
		return nil
	}
	var info map[string]struct {
		Input struct {
			Required map[string]json.RawMessage `json:"required"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &info) != nil {
		return nil
	}
	loader, ok := info["CheckpointLoaderSimple"]
	if !ok {
		return nil
	}
	raw, ok := loader.Input.Required["ckpt_name"]
	if !ok {
		return nil
	}
	var choices []any
	if json.Unmarshal(raw, &choices) != nil || len(choices) == 0 {
		return nil
	}
	values, ok := choices[0].([]any)
	if !ok {
		return nil
	}
	models := make([]string, 0, len(values))
	for _, value := range values {
		if name, ok := value.(string); ok && name != "" {
			models = append(models, name)
		}
	}
	sort.Strings(models)
	return models
}

func validComfyPromptID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'f') && (char < 'A' || char > 'F') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func writeGenerationError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
