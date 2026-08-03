package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxComfyUploadBody        = 128 << 20
	maxComfyUploadField       = 16 << 10
	maxConcurrentComfyUploads = 2
)

var errForeignComfyAsset = errors.New("ComfyUI asset belongs to another Gateway user")

func isComfyUploadRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/upload/image" || r.URL.Path == "/upload/mask")
}

func (a *App) rewriteComfyUpload(w http.ResponseWriter, r *http.Request, user *User) error {
	if user == nil || !isComfyUploadRequest(r) {
		return nil
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return errors.New("invalid ComfyUI multipart upload")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxComfyUploadBody)
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return fmt.Errorf("read ComfyUI upload: %w", err)
	}

	reader := multipart.NewReader(bytes.NewReader(body), parameters["boundary"])
	var rewritten bytes.Buffer
	writer := multipart.NewWriter(&rewritten)
	namespace := comfyUploadNamespace(a.comfyClientID(user.ID))
	foundSubfolder := false
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("parse ComfyUI upload: %w", nextErr)
		}
		var payload []byte
		var readErr error
		if part.FormName() == "image" {
			payload, readErr = io.ReadAll(part)
		} else {
			payload, readErr = io.ReadAll(io.LimitReader(part, maxComfyUploadField+1))
		}
		_ = part.Close()
		if readErr != nil {
			return fmt.Errorf("read ComfyUI upload part: %w", readErr)
		}
		if part.FormName() != "image" && len(payload) > maxComfyUploadField {
			return errors.New("ComfyUI upload field is too large")
		}
		if part.FormName() == "subfolder" {
			original, pathErr := normalizeComfyDataPath(string(payload), true)
			if pathErr != nil {
				return pathErr
			}
			payload = []byte(namespace)
			if original != "" {
				payload = []byte(namespace + "/" + original)
			}
			foundSubfolder = true
		}
		if part.FormName() == "original_ref" && r.URL.Path == "/upload/mask" {
			if err := validateComfyUploadReference(payload, namespace); err != nil {
				return err
			}
		}
		target, createErr := writer.CreatePart(part.Header)
		if createErr != nil {
			return fmt.Errorf("create ComfyUI upload part: %w", createErr)
		}
		if _, err := target.Write(payload); err != nil {
			return fmt.Errorf("write ComfyUI upload part: %w", err)
		}
	}
	if !foundSubfolder {
		if err := writer.WriteField("subfolder", namespace); err != nil {
			return fmt.Errorf("add ComfyUI upload namespace: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish ComfyUI upload: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten.Bytes()))
	r.ContentLength = int64(rewritten.Len())
	r.Header.Set("Content-Length", strconv.Itoa(rewritten.Len()))
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return nil
}

func validateComfyUploadReference(payload []byte, ownNamespace string) error {
	var reference struct {
		Subfolder string `json:"subfolder"`
	}
	if err := json.Unmarshal(payload, &reference); err != nil {
		return errors.New("invalid ComfyUI mask reference")
	}
	if namespaced, owned := comfyNamespaceOwnership(reference.Subfolder, ownNamespace); namespaced && !owned {
		return errForeignComfyAsset
	}
	return nil
}

func comfyUploadNamespace(clientID string) string {
	return "gateway/" + clientID
}

func comfyNamespaceOwnership(subfolder, ownNamespace string) (namespaced, owned bool) {
	normalized := strings.Trim(strings.ReplaceAll(subfolder, "\\", "/"), "/")
	if !strings.HasPrefix(normalized, "gateway/gateway-") {
		return false, false
	}
	return true, normalized == ownNamespace || strings.HasPrefix(normalized, ownNamespace+"/")
}
