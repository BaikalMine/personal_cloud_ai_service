package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const maxComfyControlBody = 1 << 20

// enforceComfyPromptSafety applies the same server-side policy to jobs queued
// through the full ComfyUI interface. The body is restored unchanged so later
// isolation and capture stages can keep processing it normally.
func (a *App) enforceComfyPromptSafety(r *http.Request) error {
	if r.Method != http.MethodPost || r.URL.Path != "/prompt" {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxComfyControlBody+1))
	if err != nil {
		return err
	}
	_ = r.Body.Close()
	if len(body) > maxComfyControlBody {
		return fmt.Errorf("ComfyUI prompt body exceeds %d bytes", maxComfyControlBody)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))

	var document struct {
		Prompt map[string]struct {
			ClassType string         `json:"class_type"`
			Inputs    map[string]any `json:"inputs"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return err
	}
	for _, node := range document.Prompt {
		for _, value := range comfyPromptTextInputs(node.ClassType, node.Inputs) {
			if err := validateGenerationPrompt(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func comfyPromptTextInputs(classType string, inputs map[string]any) []string {
	if len(inputs) == 0 {
		return nil
	}
	classType = strings.ToLower(classType)
	textNode := strings.Contains(classType, "text") || strings.Contains(classType, "prompt") || strings.Contains(classType, "string")
	values := make([]string, 0, len(inputs))
	for name, raw := range inputs {
		field := strings.ToLower(name)
		promptField := textNode || field == "text" || strings.Contains(field, "prompt") || strings.Contains(field, "positive") || strings.Contains(field, "negative")
		if !promptField {
			continue
		}
		collectComfyPromptStrings(raw, &values)
	}
	return values
}

func collectComfyPromptStrings(value any, target *[]string) {
	switch typed := value.(type) {
	case string:
		*target = append(*target, typed)
	case []any:
		for _, item := range typed {
			collectComfyPromptStrings(item, target)
		}
	case map[string]any:
		for _, item := range typed {
			collectComfyPromptStrings(item, target)
		}
	}
}

func (a *App) authorizeComfyMediaRequest(r *http.Request, user *User) (bool, error) {
	if user == nil || r.Method != http.MethodGet || r.URL.Path != "/view" && r.URL.Path != "/viewvideo" {
		return true, nil
	}
	name := strings.TrimSpace(r.URL.Query().Get("filename"))
	if name == "" {
		return true, nil
	}
	subfolder := strings.TrimSpace(r.URL.Query().Get("subfolder"))
	storageType := strings.TrimSpace(r.URL.Query().Get("type"))
	if storageType == "input" {
		if namespaced, owned := comfyNamespaceOwnership(subfolder, comfyUploadNamespace(a.comfyClientID(user.ID))); namespaced {
			return owned, nil
		}
		return user.Role == "admin", nil
	}
	ownerID, known, err := a.store.ComfyOutputOwner(r.Context(), name, subfolder, storageType)
	if err != nil {
		return false, err
	}
	if known {
		return ownerID == user.ID, nil
	}
	if user.Role == "admin" {
		return true, nil
	}
	if storageType != "" && storageType != "output" && storageType != "temp" {
		return false, nil
	}
	if err := a.refreshComfyOutputOwnerships(r.Context(), user.ID); err != nil {
		return false, err
	}
	ownerID, known, err = a.store.ComfyOutputOwner(r.Context(), name, subfolder, storageType)
	return known && ownerID == user.ID, err
}

func (a *App) refreshComfyOutputOwnerships(ctx context.Context, userID int64) error {
	allowed, err := a.store.ComfyPromptIDsForUser(ctx, userID)
	if err != nil || len(allowed) == 0 {
		return err
	}
	historyURL := *a.cfg.ComfyUIUpstream
	historyURL.Path = singleJoiningSlash(historyURL.Path, "/history")
	historyURL.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, historyURL.String(), http.NoBody)
	if err != nil {
		return err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ComfyUI history returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCapturedMedia+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxCapturedMedia {
		return errors.New("ComfyUI history exceeds the safe ownership refresh limit")
	}
	filtered, err := filterComfyHistoryJSON(body, allowed)
	if err != nil {
		return err
	}
	outputs, err := extractComfyOutputOwnerships(filtered)
	if err != nil {
		return err
	}
	return a.store.InsertComfyOutputOwnerships(ctx, userID, outputs)
}

func (a *App) filterComfyResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK || resp.Request.Method != http.MethodGet {
		return nil
	}
	user := a.currentUser(resp.Request)
	if user == nil {
		return nil
	}
	isHistory := resp.Request.URL.Path == "/history" || strings.HasPrefix(resp.Request.URL.Path, "/history/")
	isQueue := resp.Request.URL.Path == "/queue"
	if !isHistory && !isQueue {
		return nil
	}
	body, oversized, err := readResponseForFiltering(resp, maxCapturedMedia)
	if err != nil {
		return err
	}
	if oversized {
		return fmt.Errorf("ComfyUI %s response exceeds the safe isolation limit", resp.Request.URL.Path)
	}
	var filtered []byte
	if isHistory {
		allowed, err := a.store.ComfyPromptIDsForUser(resp.Request.Context(), user.ID)
		if err != nil {
			resetResponseBody(resp, body)
			return nil
		}
		filtered, err = filterComfyHistoryJSON(body, allowed)
		if err == nil {
			outputs, extractErr := extractComfyOutputOwnerships(filtered)
			if extractErr != nil {
				return extractErr
			}
			if err = a.store.InsertComfyOutputOwnerships(resp.Request.Context(), user.ID, outputs); err != nil {
				return err
			}
		}
	} else {
		filtered, err = filterComfyQueueJSON(body, a.comfyClientID(user.ID))
	}
	if err != nil {
		resetResponseBody(resp, body)
		return nil
	}
	resetResponseBody(resp, filtered)
	return nil
}

func extractComfyOutputOwnerships(body []byte) ([]domain.ComfyOutputOwnership, error) {
	var history map[string]json.RawMessage
	if err := json.Unmarshal(body, &history); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var outputs []domain.ComfyOutputOwnership
	for promptID, rawEntry := range history {
		var entry struct {
			Outputs map[string]json.RawMessage `json:"outputs"`
		}
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			return nil, err
		}
		for _, rawNode := range entry.Outputs {
			var node map[string]json.RawMessage
			if err := json.Unmarshal(rawNode, &node); err != nil {
				continue
			}
			for outputKind, rawItems := range node {
				if outputKind != "images" && outputKind != "gifs" && outputKind != "videos" {
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
					item.Filename = strings.TrimSpace(item.Filename)
					if item.Filename == "" {
						continue
					}
					mediaType := classifyComfyOutput(outputKind, item.Filename, item.Format)
					key := promptID + "\x00" + item.Filename + "\x00" + item.Subfolder + "\x00" + item.Type
					if _, exists := seen[key]; exists {
						continue
					}
					seen[key] = struct{}{}
					outputs = append(outputs, domain.ComfyOutputOwnership{
						PromptID: promptID, Filename: item.Filename, Subfolder: item.Subfolder,
						StorageType: item.Type, MediaType: mediaType,
					})
				}
			}
		}
	}
	return outputs, nil
}

func classifyComfyOutput(kind, filename, format string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	if kind == "videos" || strings.HasPrefix(strings.ToLower(format), "video/") {
		return "video"
	}
	switch extension {
	case ".mp4", ".webm", ".mov", ".mkv", ".avi":
		return "video"
	default:
		return "image"
	}
}

func readResponseForFiltering(resp *http.Response, limit int64) ([]byte, bool, error) {
	originalBody := resp.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		resp.Body = &captureReadCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		return nil, true, nil
	}
	_ = originalBody.Close()
	return body, false, nil
}

func resetResponseBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func filterComfyHistoryJSON(body []byte, allowed map[string]struct{}) ([]byte, error) {
	var history map[string]json.RawMessage
	if err := json.Unmarshal(body, &history); err != nil {
		return nil, err
	}
	for promptID := range history {
		if _, ok := allowed[promptID]; !ok {
			delete(history, promptID)
		}
	}
	return json.Marshal(history)
}

func filterComfyQueueJSON(body []byte, clientID string) ([]byte, error) {
	var queue map[string]json.RawMessage
	if err := json.Unmarshal(body, &queue); err != nil {
		return nil, err
	}
	for _, key := range []string{"queue_running", "queue_pending"} {
		var items []json.RawMessage
		if raw, ok := queue[key]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, err
			}
		}
		filtered := make([]json.RawMessage, 0, len(items))
		for _, item := range items {
			if comfyQueueItemClientID(item) == clientID {
				filtered = append(filtered, item)
			}
		}
		encoded, err := json.Marshal(filtered)
		if err != nil {
			return nil, err
		}
		queue[key] = encoded
	}
	return json.Marshal(queue)
}

func comfyQueueItemClientID(item json.RawMessage) string {
	var fields []json.RawMessage
	if json.Unmarshal(item, &fields) != nil || len(fields) < 4 {
		return ""
	}
	var extraData struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(fields[3], &extraData)
	return extraData.ClientID
}

func (a *App) enforceComfyControlIsolation(r *http.Request, user *User, service string) (bool, error) {
	if service != "comfyui" || user == nil || r.Method != http.MethodPost {
		return true, nil
	}
	switch r.URL.Path {
	case "/queue":
		return true, a.rewriteComfyQueueControl(r, user.ID)
	case "/interrupt":
		return a.comfyRunningJobOwnedBy(r.Context(), user.ID)
	default:
		return true, nil
	}
}

func (a *App) rewriteComfyQueueControl(r *http.Request, userID int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxComfyControlBody+1))
	if err != nil {
		return err
	}
	_ = r.Body.Close()
	if len(body) > maxComfyControlBody {
		return fmt.Errorf("ComfyUI queue control body exceeds %d bytes", maxComfyControlBody)
	}
	var document struct {
		Clear  bool     `json:"clear,omitempty"`
		Delete []string `json:"delete,omitempty"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return err
	}
	owned, err := a.store.ComfyPromptIDsForUser(r.Context(), userID)
	if err != nil {
		return err
	}
	if document.Clear {
		document.Delete = document.Delete[:0]
		for promptID := range owned {
			document.Delete = append(document.Delete, promptID)
		}
		sort.Strings(document.Delete)
		document.Clear = false
	} else {
		filtered := document.Delete[:0]
		for _, promptID := range document.Delete {
			if _, ok := owned[promptID]; ok {
				filtered = append(filtered, promptID)
			}
		}
		document.Delete = filtered
	}
	rewritten, err := json.Marshal(document)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	r.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

func (a *App) comfyRunningJobOwnedBy(ctx context.Context, userID int64) (bool, error) {
	queueURL := *a.cfg.ComfyUIUpstream
	queueURL.Path = singleJoiningSlash(queueURL.Path, "/queue")
	queueURL.RawQuery = ""
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, queueURL.String(), nil)
	if err != nil {
		return false, err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		req.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ComfyUI queue returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCapturedContent+1))
	if err != nil || len(body) > maxCapturedContent {
		return false, err
	}
	return comfyQueueContainsClient(body, "queue_running", a.comfyClientID(userID)), nil
}

func comfyQueueContainsClient(body []byte, key, clientID string) bool {
	var queue map[string]json.RawMessage
	var items []json.RawMessage
	if json.Unmarshal(body, &queue) != nil || json.Unmarshal(queue[key], &items) != nil {
		return false
	}
	for _, item := range items {
		if comfyQueueItemClientID(item) == clientID {
			return true
		}
	}
	return false
}

func rejectUpstreamRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}
