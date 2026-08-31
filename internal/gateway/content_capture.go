package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
)

const maxCapturedContent = 8 << 20
const maxCapturedMedia = 64 << 20
const maxConcurrentMediaCaptures = 2
const maxMediaBytesPerUser = 512 << 20
const contentPersistTimeout = 15 * time.Second

type contentCaptureKey struct{}

type proxyContentCapture struct {
	userID           int64
	service          string
	path             string
	requestBody      []byte
	response         limitedBuffer
	mediaName        string
	mediaSubfolder   string
	mediaStorageType string
	mediaType        string
	mimeType         string
	isMedia          bool
	status           int
	releaseOnce      sync.Once
	release          func()
}

type limitedBuffer struct {
	data      []byte
	remaining int
	truncated bool
}

func newLimitedBuffer(limit int) limitedBuffer {
	return limitedBuffer{remaining: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	accepted := 0
	if b.remaining > 0 {
		if len(p) > b.remaining {
			p = p[:b.remaining]
		}
		b.data = append(b.data, p...)
		b.remaining -= len(p)
		accepted = len(p)
	}
	if accepted < original {
		b.truncated = true
	}
	return original, nil
}

func (c *proxyContentCapture) Release() {
	if c != nil && c.release != nil {
		c.releaseOnce.Do(c.release)
	}
}

type captureReadCloser struct {
	io.Reader
	io.Closer
}

func contentPersistenceContext(requestContext context.Context) (context.Context, context.CancelFunc) {
	// The upstream response can finish after a browser has already closed its
	// request context. Audit storage must not be discarded in that case.
	return context.WithTimeout(context.WithoutCancel(requestContext), contentPersistTimeout)
}

func (a *App) beginContentCapture(r *http.Request, user *User, service string) (*proxyContentCapture, error) {
	if user == nil {
		return nil, nil
	}
	if service == "comfyui" && r.Method == http.MethodGet && (r.URL.Path == "/view" || r.URL.Path == "/viewvideo") {
		select {
		case a.mediaCaptureSlots <- struct{}{}:
		default:
			return nil, nil
		}
		name := strings.TrimSpace(r.URL.Query().Get("filename"))
		subfolder := strings.TrimSpace(r.URL.Query().Get("subfolder"))
		storageType := strings.TrimSpace(r.URL.Query().Get("type"))
		if name == "" {
			name = strings.TrimPrefix(r.URL.Path, "/")
		}
		mediaType := "image"
		if r.URL.Path == "/viewvideo" {
			mediaType = "video"
		}
		capture := &proxyContentCapture{
			userID: user.ID, service: service, path: r.URL.Path, mediaName: name,
			mediaSubfolder: subfolder, mediaStorageType: storageType,
			mediaType: mediaType, isMedia: true, response: newLimitedBuffer(maxCapturedMedia),
		}
		capture.release = func() { <-a.mediaCaptureSlots }
		return capture, nil
	}
	if r.Method != http.MethodPost || r.Body == nil {
		return nil, nil
	}
	wanted := service == "comfyui" && r.URL.Path == "/prompt" ||
		service == "openwebui" && isOpenWebContentPath(r.URL.Path)
	if !wanted {
		return nil, nil
	}
	originalBody := r.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, maxCapturedContent+1))
	if err != nil {
		return nil, fmt.Errorf("read content request: %w", err)
	}
	if len(body) > maxCapturedContent {
		r.Body = &captureReadCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		return nil, nil
	}
	_ = originalBody.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return &proxyContentCapture{
		userID: user.ID, service: service, path: r.URL.Path, requestBody: body,
		response: newLimitedBuffer(maxCapturedContent),
	}, nil
}

func attachResponseCapture(resp *http.Response) {
	capture, _ := resp.Request.Context().Value(contentCaptureKey{}).(*proxyContentCapture)
	if capture == nil || resp.Body == nil {
		return
	}
	capture.mimeType = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	capture.status = resp.StatusCode
	resp.Body = &captureReadCloser{Reader: io.TeeReader(resp.Body, &capture.response), Closer: resp.Body}
}

func (a *App) persistContentCapture(ctx context.Context, capture *proxyContentCapture) error {
	if capture == nil || a.contentCipher == nil {
		return nil
	}
	if capture.isMedia {
		return a.persistComfyMedia(ctx, capture)
	}
	var kind, externalID, model, prompt, response, metadata string
	var err error
	switch capture.service {
	case "openwebui":
		kind = "openwebui_chat"
		if isOpenWebChatSavePath(capture.path) {
			externalID, model, prompt, response, metadata, err = parseOpenWebChatSave(capture.requestBody, capture.path)
		} else {
			externalID, model, prompt, response, metadata, err = parseOpenWebCapture(capture.requestBody, capture.response.data)
		}
	case "comfyui":
		kind = "comfyui_prompt"
		externalID, model, prompt, response, metadata, err = parseComfyCapture(capture.requestBody, capture.response.data)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		// Do not write request contents to logs. The endpoint and byte counts are
		// enough to diagnose an unsupported upstream payload shape.
		log.Printf("skipped empty content capture: service=%s path=%s request_bytes=%d response_bytes=%d", capture.service, capture.path, len(capture.requestBody), len(capture.response.data))
		return nil
	}
	promptCipher, err := a.contentCipher.Encrypt(prompt)
	if err != nil {
		return err
	}
	responseCipher, err := a.contentCipher.Encrypt(response)
	if err != nil {
		return err
	}
	metadataCipher, err := a.contentCipher.Encrypt(metadata)
	if err != nil {
		return err
	}
	_, err = a.store.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: capture.userID, Service: capture.service, Kind: kind, ExternalID: externalID,
		Model: model, PromptCipher: promptCipher, ResponseCipher: responseCipher, MetadataCipher: metadataCipher,
		ExpiresAt: time.Now().Add(a.retentionPolicy().AIContent),
	})
	return err
}

func (a *App) persistComfyMedia(ctx context.Context, capture *proxyContentCapture) error {
	if capture.status < 200 || capture.status >= 300 || len(capture.response.data) == 0 || capture.response.truncated {
		return nil
	}
	used, err := a.store.ContentMediaBytesForUser(ctx, capture.userID)
	if err != nil || used+int64(len(capture.response.data)) > maxMediaBytesPerUser {
		return err
	}
	eventID, err := a.store.ComfyOutputEventID(ctx, capture.userID, capture.mediaName,
		capture.mediaSubfolder, capture.mediaStorageType)
	if errors.Is(err, sql.ErrNoRows) {
		eventID, err = a.store.LatestComfyContentEventID(ctx, capture.userID)
	}
	if err != nil {
		return nil
	}
	payload, err := a.contentCipher.EncryptBytes(capture.response.data)
	if err != nil {
		return err
	}
	mimeType := capture.mimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	digest := sha256.Sum256(capture.response.data)
	err = a.store.InsertContentMedia(ctx, domain.ContentMediaRecord{
		EventID: eventID, MediaType: capture.mediaType, MIMEType: mimeType,
		OriginalName: capture.mediaName, Subfolder: capture.mediaSubfolder, StorageType: capture.mediaStorageType,
		PayloadCipher: payload, SizeBytes: int64(len(capture.response.data)), ContentHash: hex.EncodeToString(digest[:]),
		ExpiresAt: time.Now().Add(a.retentionPolicy().GenerationMedia),
	})
	if err == nil && capture.mediaType == "image" {
		a.queueSensitiveMediaClassification()
	}
	return err
}

func parseOpenWebCapture(requestBody, responseBody []byte) (externalID, model, prompt, response, metadata string, err error) {
	var request map[string]any
	if err = json.Unmarshal(requestBody, &request); err != nil {
		return
	}
	externalID, _ = request["chat_id"].(string)
	model, _ = request["model"].(string)
	if messages, ok := request["messages"].([]any); ok {
		for i := len(messages) - 1; i >= 0; i-- {
			message, _ := messages[i].(map[string]any)
			if message["role"] == "user" {
				prompt = contentText(message["content"])
				break
			}
		}
	}
	response = openWebResponseText(responseBody)
	meta, _ := json.Marshal(map[string]any{"chat_id": externalID, "model": model})
	metadata = string(meta)
	return
}

func isOpenWebContentPath(requestPath string) bool {
	return requestPath == "/api/chat/completions" ||
		requestPath == "/ollama/api/chat" ||
		isOpenWebChatSavePath(requestPath)
}

func isOpenWebChatSavePath(requestPath string) bool {
	const prefix = "/api/v1/chats/"
	if !strings.HasPrefix(requestPath, prefix) {
		return false
	}
	resource := strings.Trim(strings.TrimPrefix(requestPath, prefix), "/")
	if resource == "" || strings.Contains(resource, "/") {
		return false
	}
	switch resource {
	case "all", "import", "new", "pinned", "tags":
		return false
	default:
		return true
	}
}

func parseOpenWebChatSave(requestBody []byte, requestPath string) (externalID, model, prompt, response, metadata string, err error) {
	var form struct {
		Chat map[string]any `json:"chat"`
	}
	if err = json.Unmarshal(requestBody, &form); err != nil {
		return
	}
	chat := form.Chat
	if len(chat) == 0 {
		return
	}
	chatID, _ := chat["id"].(string)
	if chatID == "" {
		chatID = strings.TrimPrefix(requestPath, "/api/v1/chats/")
	}
	history, _ := chat["history"].(map[string]any)
	messages, _ := history["messages"].(map[string]any)
	if len(messages) == 0 {
		return
	}

	currentID, _ := history["currentId"].(string)
	assistantID, assistant := openWebMessageByID(messages, currentID)
	if assistant == nil || assistant["role"] != "assistant" || contentText(assistant["content"]) == "" {
		assistantID, assistant = latestOpenWebAssistantMessage(messages)
	}
	if assistant == nil {
		return
	}
	userID, user := openWebParentUserMessage(messages, assistant)
	if user == nil {
		userID, user = latestOpenWebUserMessage(messages)
	}
	if user == nil {
		return
	}
	prompt = contentText(user["content"])
	response = contentText(assistant["content"])
	if response == "" {
		response = contentText(assistant["output"])
	}
	model = contentText(assistant["model"])
	if model == "" {
		model = contentText(chat["model"])
	}
	externalID = strings.Trim(chatID+":"+assistantID, ":")
	meta, _ := json.Marshal(map[string]any{
		"chat_id":         chatID,
		"user_message_id": userID,
		"assistant_id":    assistantID,
		"source":          "openwebui_chat_save",
	})
	metadata = string(meta)
	return
}

func openWebMessageByID(messages map[string]any, id string) (string, map[string]any) {
	if id == "" {
		return "", nil
	}
	message, _ := messages[id].(map[string]any)
	return id, message
}

func openWebParentUserMessage(messages map[string]any, start map[string]any) (string, map[string]any) {
	message := start
	for range len(messages) {
		parentID, _ := message["parentId"].(string)
		id, parent := openWebMessageByID(messages, parentID)
		if parent == nil {
			return "", nil
		}
		if parent["role"] == "user" {
			return id, parent
		}
		message = parent
	}
	return "", nil
}

func latestOpenWebAssistantMessage(messages map[string]any) (string, map[string]any) {
	return latestOpenWebMessage(messages, "assistant")
}

func latestOpenWebUserMessage(messages map[string]any) (string, map[string]any) {
	return latestOpenWebMessage(messages, "user")
}

func latestOpenWebMessage(messages map[string]any, role string) (string, map[string]any) {
	var latestID string
	var latest map[string]any
	var latestTimestamp float64
	for id, raw := range messages {
		message, _ := raw.(map[string]any)
		if message == nil || message["role"] != role || contentText(message["content"]) == "" {
			continue
		}
		timestamp, _ := message["timestamp"].(float64)
		if latest == nil || timestamp >= latestTimestamp {
			latestID, latest, latestTimestamp = id, message, timestamp
		}
	}
	return latestID, latest
}

func openWebResponseText(body []byte) string {
	var result strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), maxCapturedContent)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" || line == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		wrote := false
		if choices, ok := event["choices"].([]any); ok {
			for _, raw := range choices {
				choice, _ := raw.(map[string]any)
				if delta, ok := choice["delta"].(map[string]any); ok {
					text := contentText(delta["content"])
					result.WriteString(text)
					wrote = wrote || text != ""
				}
				if message, ok := choice["message"].(map[string]any); ok {
					text := contentText(message["content"])
					result.WriteString(text)
					wrote = wrote || text != ""
				}
			}
		}
		if message, ok := event["message"].(map[string]any); ok && !wrote {
			text := contentText(message["content"])
			result.WriteString(text)
			wrote = text != ""
		}
		if !wrote {
			result.WriteString(contentText(event["response"]))
		}
	}
	return strings.TrimSpace(result.String())
}

func contentText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		var parts []string
		for _, item := range value {
			if object, ok := item.(map[string]any); ok {
				if text := contentText(object["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "value", "input_text", "output"} {
			if text := contentText(value[key]); text != "" {
				return text
			}
		}
		return ""
	}
	return ""
}

func parseComfyCapture(requestBody, responseBody []byte) (externalID, model, prompt, response, metadata string, err error) {
	var request map[string]any
	if err = json.Unmarshal(requestBody, &request); err != nil {
		return
	}
	var result map[string]any
	_ = json.Unmarshal(responseBody, &result)
	externalID, _ = result["prompt_id"].(string)

	texts := map[string]struct{}{}
	models := map[string]struct{}{}
	nodeTypes := map[string]struct{}{}
	seeds := map[string]any{}
	if graph, ok := request["prompt"].(map[string]any); ok {
		for nodeID, rawNode := range graph {
			node, _ := rawNode.(map[string]any)
			if classType, ok := node["class_type"].(string); ok && strings.TrimSpace(classType) != "" {
				nodeTypes[classType] = struct{}{}
			}
			inputs, _ := node["inputs"].(map[string]any)
			for key, value := range inputs {
				normalizedKey := strings.ToLower(key)
				switch {
				case strings.Contains(normalizedKey, "text") || strings.Contains(normalizedKey, "prompt") || normalizedKey == "positive" || normalizedKey == "negative":
					if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
						texts[text] = struct{}{}
					}
				case normalizedKey == "seed" || normalizedKey == "noise_seed":
					seeds[nodeID+"."+key] = value
				case normalizedKey == "ckpt_name" || normalizedKey == "model_name":
					if name, ok := value.(string); ok {
						models[name] = struct{}{}
					}
				}
			}
		}
	}
	prompt = strings.Join(sortedKeys(texts), "\n\n")
	if prompt == "" && len(nodeTypes) > 0 {
		prompt = "[workflow] " + strings.Join(sortedKeys(nodeTypes), ", ")
	}
	model = strings.Join(sortedKeys(models), ", ")
	meta, _ := json.Marshal(map[string]any{
		"prompt_id": externalID,
		"seeds":     seeds,
		"models":    sortedKeys(models),
		"nodes":     sortedKeys(nodeTypes),
	})
	metadata = string(meta)
	return
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
