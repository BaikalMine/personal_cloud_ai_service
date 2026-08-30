package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

const (
	maxComfyUploadBody        = 128 << 20
	maxComfyUploadField       = 16 << 10
	maxConcurrentComfyUploads = 2
	maxComfyImagePixels       = 64_000_000
	maxComfyImageSide         = 16_384
	maxComfyInputUserBytes    = int64(2 << 30)
	maxComfyInputGlobalBytes  = int64(20 << 30)
	maxComfyInputUserFiles    = 500
	maxComfyInputGlobalFiles  = 5000
	maxComfyUploadResponse    = 64 << 10
	maxComfyStoredInputBytes  = int64(320 << 20)
	comfyInputRetention       = 72 * time.Hour
)

var errForeignComfyAsset = errors.New("ComfyUI asset belongs to another Gateway user")

type comfyUploadAsset struct {
	Filename    string
	Subfolder   string
	SizeBytes   int64
	ContentHash string
}

type comfyInputReservation struct {
	ID        string
	Asset     comfyUploadAsset
	finalized atomic.Bool
	responded atomic.Bool
	accepted  atomic.Bool
	isMask    bool
}

type comfyInputReservationKey struct{}

func isComfyUploadRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/upload/image" || r.URL.Path == "/upload/mask")
}

func (a *App) rewriteComfyUpload(w http.ResponseWriter, r *http.Request, user *User) error {
	_, err := a.rewriteComfyUploadWithName(w, r, user, "")
	return err
}

func (a *App) rewriteComfyUploadWithName(w http.ResponseWriter, r *http.Request, user *User, targetFilename string) (comfyUploadAsset, error) {
	if user == nil || !isComfyUploadRequest(r) {
		return comfyUploadAsset{}, nil
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return comfyUploadAsset{}, errors.New("invalid ComfyUI multipart upload")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxComfyUploadBody)
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return comfyUploadAsset{}, fmt.Errorf("read ComfyUI upload: %w", err)
	}

	reader := multipart.NewReader(bytes.NewReader(body), parameters["boundary"])
	var rewritten bytes.Buffer
	writer := multipart.NewWriter(&rewritten)
	namespace := comfyUploadNamespace(a.comfyClientID(user.ID))
	asset := comfyUploadAsset{Subfolder: namespace}
	imageParts := 0
	foundSubfolder := false
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return comfyUploadAsset{}, fmt.Errorf("parse ComfyUI upload: %w", nextErr)
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
			return comfyUploadAsset{}, fmt.Errorf("read ComfyUI upload part: %w", readErr)
		}
		if part.FormName() != "image" && len(payload) > maxComfyUploadField {
			return comfyUploadAsset{}, errors.New("ComfyUI upload field is too large")
		}
		if part.FormName() == "image" {
			imageParts++
			if imageParts > 1 {
				return comfyUploadAsset{}, errors.New("multiple files in one ComfyUI upload are not supported")
			}
			if err := validateComfyUploadPayload(part.FileName(), payload); err != nil {
				return comfyUploadAsset{}, err
			}
			asset.Filename = part.FileName()
			if targetFilename != "" {
				asset.Filename = comfyStoredUploadFilename(targetFilename, part.FileName())
				disposition := mime.FormatMediaType("form-data", map[string]string{"name": part.FormName(), "filename": asset.Filename})
				part.Header.Set("Content-Disposition", disposition)
			}
			digest := sha256.Sum256(payload)
			asset.SizeBytes = int64(len(payload))
			asset.ContentHash = hex.EncodeToString(digest[:])
		}
		if part.FormName() == "subfolder" {
			original, pathErr := normalizeComfyDataPath(string(payload), true)
			if pathErr != nil {
				return comfyUploadAsset{}, pathErr
			}
			payload = []byte(namespace)
			if original != "" {
				payload = []byte(namespace + "/" + original)
			}
			asset.Subfolder = string(payload)
			foundSubfolder = true
		}
		if part.FormName() == "original_ref" && r.URL.Path == "/upload/mask" {
			if err := validateComfyUploadReference(payload, namespace); err != nil {
				return comfyUploadAsset{}, err
			}
		}
		target, createErr := writer.CreatePart(part.Header)
		if createErr != nil {
			return comfyUploadAsset{}, fmt.Errorf("create ComfyUI upload part: %w", createErr)
		}
		if _, err := target.Write(payload); err != nil {
			return comfyUploadAsset{}, fmt.Errorf("write ComfyUI upload part: %w", err)
		}
	}
	if !foundSubfolder {
		if err := writer.WriteField("subfolder", namespace); err != nil {
			return comfyUploadAsset{}, fmt.Errorf("add ComfyUI upload namespace: %w", err)
		}
	}
	if imageParts != 1 || asset.Filename == "" || asset.ContentHash == "" {
		return comfyUploadAsset{}, errors.New("ComfyUI upload requires exactly one file")
	}
	if err := writer.Close(); err != nil {
		return comfyUploadAsset{}, fmt.Errorf("finish ComfyUI upload: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten.Bytes()))
	r.ContentLength = int64(rewritten.Len())
	r.Header.Set("Content-Length", strconv.Itoa(rewritten.Len()))
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return asset, nil
}

func (a *App) prepareComfyInputUpload(w http.ResponseWriter, r *http.Request, user *User) (*http.Request, *comfyInputReservation, error) {
	id, err := security.RandomToken()
	if err != nil {
		return r, nil, err
	}
	asset, err := a.rewriteComfyUploadWithName(w, r, user, id)
	if err != nil {
		return r, nil, err
	}
	if a.store == nil {
		return r, nil, nil
	}
	quota := store.ComfyInputQuota{
		UserBytes: maxComfyInputUserBytes, GlobalBytes: maxComfyInputGlobalBytes,
		UserFiles: maxComfyInputUserFiles, GlobalFiles: maxComfyInputGlobalFiles,
	}
	reservedBytes := asset.SizeBytes
	isMask := r.URL.Path == "/upload/mask"
	if isMask {
		// ComfyUI composites the uploaded alpha channel with the original PNG,
		// so the stored bytes can be much larger than the mask request itself.
		reservedBytes = maxComfyStoredInputBytes
	}
	if err := a.store.ReserveComfyInputAsset(r.Context(), user.ID, id, asset.Filename, asset.Subfolder, reservedBytes, asset.ContentHash, quota); err != nil {
		return r, nil, err
	}
	reservation := &comfyInputReservation{ID: id, Asset: asset, isMask: isMask}
	r = r.WithContext(context.WithValue(r.Context(), comfyInputReservationKey{}, reservation))
	return r, reservation, nil
}

func comfyStoredUploadFilename(id, original string) string {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(original)))
	if len(extension) < 2 || len(extension) > 12 {
		extension = ".bin"
	}
	for _, character := range extension[1:] {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')) {
			extension = ".bin"
			break
		}
	}
	return "gateway-" + id + extension
}

func (a *App) finalizeComfyInputUpload(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	reservation, _ := resp.Request.Context().Value(comfyInputReservationKey{}).(*comfyInputReservation)
	if reservation == nil || a.store == nil {
		return nil
	}
	reservation.responded.Store(true)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil
	}
	reservation.accepted.Store(true)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxComfyUploadResponse+1))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	if len(body) > maxComfyUploadResponse {
		return errors.New("ComfyUI upload response is too large")
	}
	var result struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode ComfyUI upload response: %w", err)
	}
	if result.Type == "" {
		result.Type = "input"
	}
	normalizedSubfolder, err := normalizeComfyDataPath(result.Subfolder, false)
	if err != nil || result.Type != "input" || result.Name != reservation.Asset.Filename || normalizedSubfolder != reservation.Asset.Subfolder {
		return errors.New("ComfyUI stored upload at an unexpected path")
	}
	storedAsset := reservation.Asset
	if reservation.isMask {
		fingerprintCtx, fingerprintCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		sizeBytes, contentHash, fingerprintErr := a.comfyStoredInputFingerprint(fingerprintCtx, storedAsset)
		fingerprintCancel()
		if fingerprintErr != nil {
			// Keep the conservative reservation. Maintenance will either resolve
			// the exact stored bytes or confirm that the file is missing.
			log.Printf("fingerprint stored ComfyUI mask: %v", fingerprintErr)
			return nil
		}
		storedAsset.SizeBytes = sizeBytes
		storedAsset.ContentHash = contentHash
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finalized, err := a.store.FinalizeComfyInputAsset(persistCtx, reservation.ID, result.Name, normalizedSubfolder, storedAsset.SizeBytes, storedAsset.ContentHash, comfyInputRetention)
	if err != nil {
		return err
	}
	if !finalized {
		return errors.New("ComfyUI input reservation was not found")
	}
	reservation.finalized.Store(true)
	return nil
}

func (a *App) comfyStoredInputFingerprint(ctx context.Context, asset comfyUploadAsset) (int64, string, error) {
	if a == nil || a.cfg.ComfyUIUpstream == nil || asset.Filename == "" {
		return 0, "", errors.New("ComfyUI input fingerprint is not configured")
	}
	target := *a.cfg.ComfyUIUpstream
	target.Path = singleJoiningSlash(target.Path, "/view")
	query := target.Query()
	query.Set("filename", asset.Filename)
	query.Set("subfolder", asset.Subfolder)
	query.Set("type", "input")
	target.RawQuery = query.Encode()
	target.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0, "", err
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 2 * time.Minute, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("ComfyUI input returned %s", response.Status)
	}
	digest := sha256.New()
	sizeBytes, err := io.Copy(digest, io.LimitReader(response.Body, maxComfyStoredInputBytes+1))
	if err != nil {
		return 0, "", err
	}
	if sizeBytes <= 0 || sizeBytes > maxComfyStoredInputBytes {
		return 0, "", errors.New("stored ComfyUI input exceeds the tracked size limit")
	}
	return sizeBytes, hex.EncodeToString(digest.Sum(nil)), nil
}

func (a *App) releaseComfyInputReservation(reservation *comfyInputReservation) {
	if reservation == nil || reservation.finalized.Load() || a.store == nil || !reservation.responded.Load() || reservation.accepted.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.store.ReleaseComfyInputReservation(ctx, reservation.ID); err != nil {
		log.Printf("release ComfyUI input reservation: %v", err)
	}
}

func validateComfyUploadPayload(filename string, payload []byte) error {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	contentType := strings.TrimSpace(strings.Split(http.DetectContentType(payload), ";")[0])
	imageExpected := contentType == "image/jpeg" || contentType == "image/png" || contentType == "image/gif" || contentType == "image/webp" ||
		extension == ".jpg" || extension == ".jpeg" || extension == ".png" || extension == ".gif" || extension == ".webp"
	width, height, err := generationImageDimensions(payload)
	if err != nil {
		if imageExpected {
			return errors.New("не удалось прочитать загружаемое изображение")
		}
		if !validComfyAudioPayload(extension, payload) {
			return errors.New("поддерживаются только PNG, JPEG, GIF, WEBP и аудио WAV, MP3, FLAC, OGG/Opus, M4A или AAC")
		}
		return nil
	}
	if width > maxComfyImageSide || height > maxComfyImageSide || int64(width)*int64(height) > maxComfyImagePixels {
		return fmt.Errorf("изображение слишком большое: максимум %d Мп и %d пикселей по стороне", maxComfyImagePixels/1_000_000, maxComfyImageSide)
	}
	return nil
}

func validComfyAudioPayload(extension string, payload []byte) bool {
	if len(payload) < 4 {
		return false
	}
	switch extension {
	case ".wav":
		return len(payload) >= 12 && bytes.Equal(payload[:4], []byte("RIFF")) && bytes.Equal(payload[8:12], []byte("WAVE"))
	case ".mp3":
		return bytes.HasPrefix(payload, []byte("ID3")) || payload[0] == 0xff && payload[1]&0xe0 == 0xe0
	case ".flac":
		return bytes.HasPrefix(payload, []byte("fLaC"))
	case ".ogg", ".opus":
		return bytes.HasPrefix(payload, []byte("OggS"))
	case ".m4a":
		return len(payload) >= 12 && bytes.Equal(payload[4:8], []byte("ftyp"))
	case ".aac":
		return payload[0] == 0xff && payload[1]&0xf6 == 0xf0
	default:
		return false
	}
}

func validateComfyUploadReference(payload []byte, ownNamespace string) error {
	var reference struct {
		Filename  string `json:"filename"`
		Subfolder string `json:"subfolder"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(payload, &reference); err != nil {
		return errors.New("invalid ComfyUI mask reference")
	}
	if reference.Filename == "" || strings.ContainsAny(reference.Filename, `/\`) || reference.Type != "" && reference.Type != "input" {
		return errors.New("invalid ComfyUI mask reference")
	}
	return validateComfyInputReference(reference.Subfolder+"/"+reference.Filename, ownNamespace)
}

func comfyUploadNamespace(clientID string) string {
	return "gateway/" + clientID
}

func comfyNamespaceOwnership(subfolder, ownNamespace string) (namespaced, owned bool) {
	candidate := strings.TrimSpace(subfolder)
	precleaned := strings.Trim(strings.ReplaceAll(candidate, "\\", "/"), "/")
	namespaced = strings.HasPrefix(strings.ToLower(precleaned), "gateway/gateway-")
	normalized, err := normalizeComfyDataPath(candidate, false)
	if err != nil {
		return namespaced, false
	}
	normalized = strings.ToLower(normalized)
	ownNamespace = strings.ToLower(ownNamespace)
	if !strings.HasPrefix(normalized, "gateway/gateway-") {
		return false, false
	}
	return true, normalized == ownNamespace || strings.HasPrefix(normalized, ownNamespace+"/")
}

func validateComfyInputReference(value, ownNamespace string) error {
	normalized, err := normalizeComfyDataPath(strings.TrimSpace(value), false)
	if err != nil {
		return errors.New("invalid ComfyUI input path")
	}
	if normalized == ownNamespace {
		return errors.New("invalid ComfyUI input path")
	}
	namespaced, owned := comfyNamespaceOwnership(normalized, ownNamespace)
	if !namespaced || !owned {
		return errForeignComfyAsset
	}
	return nil
}
