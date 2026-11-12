package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
	"ai-access-gateway/internal/store"
)

var errPostprocessDispatchWaiting = errors.New("postprocess dispatch evidence is temporarily unavailable")
var errPostprocessInputMissing = errors.New("postprocess input file is missing")
var errPostprocessUploadRejected = errors.New("ComfyUI rejected the postprocess upload")

type postprocessUploadedInput struct {
	Source    domain.PostprocessSourceIdentity `json:"source"`
	InputID   string                           `json:"input_id"`
	InputName string                           `json:"input_name"`
	SizeBytes int64                            `json:"size_bytes"`
	SHA256    string                           `json:"sha256"`
	ReadAt    time.Time                        `json:"read_at"`
}

type postprocessDispatchPreparation struct {
	Quote            postprocessQuote
	Prompt           map[string]any
	ObjectInfo       comfyObjectInfoSnapshot
	FileObservations *componentinspection.Response
	Inputs           []postprocessUploadedInput
	Sensitive        bool
}

func postprocessInputAttempt(q postprocessQuote, job domain.GenerationJob, position int) domain.PostprocessInput {
	s := q.Sources[position].Identity
	id := generationEvidenceDigest([]byte(fmt.Sprintf("%d:%s:%s:%d", q.UserID, job.PublicID, job.DispatchToken, position)))
	return domain.PostprocessInput{ID: "postprocess-input-" + id, JobID: job.ID, SourcePosition: position, DispatchToken: job.DispatchToken,
		Filename: "postprocess-" + id + path.Ext(q.Sources[position].InputName), Subfolder: q.Namespace, SizeBytes: s.SizeBytes, SHA256: s.SHA256}
}

// The readback has no preview, resize or channel query. It proves a complete
// byte snapshot served by ComfyUI, not what a later GPU node will load.
func (a *App) readPostprocessInput(ctx context.Context, input domain.PostprocessInput) (time.Time, error) {
	if a.cfg.ComfyUIUpstream == nil || input.SizeBytes <= 0 || input.SizeBytes > mediaprobe.MaxInputBytes {
		return time.Time{}, errPostprocessQuoteInvalid
	}
	target := *a.cfg.ComfyUIUpstream
	target.Path = singleJoiningSlash(target.Path, "/view")
	target.RawQuery = url.Values{"filename": {input.Filename}, "subfolder": {input.Subfolder}, "type": {"input"}}.Encode()
	target.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return time.Time{}, err
	}
	request.Header.Set("Cache-Control", "no-cache, no-store")
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: time.Minute, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", errPostprocessDispatchWaiting, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return time.Time{}, errPostprocessInputMissing
	}
	if response.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("%w: input HTTP %d", errPostprocessDispatchWaiting, response.StatusCode)
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(response.Body, input.SizeBytes+1))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", errPostprocessDispatchWaiting, err)
	}
	if n != input.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != input.SHA256 {
		return time.Time{}, errPostprocessSourceChanged
	}
	return time.Now().UTC(), nil
}

func postprocessMultipartRequest(ctx context.Context, target string, input domain.PostprocessInput, media *materializedContentMedia) (*http.Request, error) {
	var header bytes.Buffer
	writer := multipart.NewWriter(&header)
	for _, field := range [][2]string{{"type", "input"}, {"subfolder", input.Subfolder}, {"overwrite", "false"}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return nil, err
		}
	}
	if _, err := writer.CreateFormFile("image", input.Filename); err != nil {
		return nil, err
	}
	prefix := bytes.Clone(header.Bytes())
	header.Reset()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	suffix := bytes.Clone(header.Bytes())
	if _, err := media.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, io.MultiReader(bytes.NewReader(prefix), io.LimitReader(media, input.SizeBytes), bytes.NewReader(suffix)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.ContentLength = int64(len(prefix)) + input.SizeBytes + int64(len(suffix))
	return request, nil
}

func (a *App) uploadPostprocessInput(ctx context.Context, job domain.GenerationJob, q postprocessQuote, position int, source *preparedPostprocessSource) (postprocessUploadedInput, error) {
	var empty postprocessUploadedInput
	expected := q.Sources[position]
	if postprocessQuoteIdentity(source) != expected.Identity || postprocessMeasuredFacts(source.Measurement) != postprocessMeasuredFacts(expected.Measurement) {
		return empty, errPostprocessSourceChanged
	}
	if _, err := a.store.CheckGenerationPostprocessDispatch(ctx, q.UserID, job.ID, job.DispatchToken); err != nil {
		return empty, err
	}
	finish := func(input domain.PostprocessInput, readAt time.Time) (postprocessUploadedInput, error) {
		if err := a.store.VerifyGenerationPostprocessInput(ctx, q.UserID, job.ID, job.DispatchToken, input, a.retentionPolicy().ComfyInputs); err != nil {
			return empty, err
		}
		return postprocessUploadedInput{Source: expected.Identity, InputID: input.ID, InputName: input.Subfolder + "/" + input.Filename, SizeBytes: input.SizeBytes, SHA256: input.SHA256, ReadAt: readAt}, nil
	}
	existing, err := a.store.GenerationPostprocessInputForJob(ctx, q.UserID, job.ID, position)
	if err == nil {
		if existing.Subfolder != q.Namespace || existing.SizeBytes != expected.Identity.SizeBytes || existing.SHA256 != expected.Identity.SHA256 ||
			validateComfyInputReference(existing.Subfolder+"/"+existing.Filename, q.Namespace) != nil {
			return empty, errPostprocessSourceChanged
		}
		readAt, readErr := a.readPostprocessInput(ctx, existing)
		if readErr == nil {
			return finish(existing, readAt)
		}
		if !errors.Is(readErr, errPostprocessInputMissing) {
			return empty, readErr
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	input := postprocessInputAttempt(q, job, position)
	// Check the private spool immediately before streaming. No retained source
	// or earlier input file is modified, even after an uncertain upload response.
	if _, err := source.Media.Seek(0, io.SeekStart); err != nil {
		return empty, err
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(source.Media, input.SizeBytes+1))
	if err != nil {
		return empty, err
	}
	if n != input.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != input.SHA256 {
		return empty, errPostprocessSourceChanged
	}
	target := *a.cfg.ComfyUIUpstream
	target.Path = singleJoiningSlash(target.Path, "/upload/image")
	target.RawQuery, target.Fragment = "", ""
	request, err := postprocessMultipartRequest(ctx, target.String(), input, source.Media)
	if err != nil {
		return empty, err
	}
	defer request.Body.Close()
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	input, created, err := a.store.ReserveGenerationPostprocessInput(ctx, q.UserID, input, store.ComfyInputQuota{UserBytes: maxComfyInputUserBytes, GlobalBytes: maxComfyInputGlobalBytes, UserFiles: maxComfyInputUserFiles, GlobalFiles: maxComfyInputGlobalFiles})
	if err != nil {
		return empty, err
	}
	if !created {
		readAt, err := a.readPostprocessInput(ctx, input)
		if errors.Is(err, errPostprocessInputMissing) {
			return empty, errPostprocessDispatchWaiting
		}
		if err != nil {
			return empty, err
		}
		return finish(input, readAt)
	}
	response, err := (&http.Client{Timeout: time.Minute, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return empty, fmt.Errorf("%w: upload: %v", errPostprocessDispatchWaiting, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests {
			return empty, fmt.Errorf("%w: HTTP %d", errPostprocessUploadRejected, response.StatusCode)
		}
		return empty, fmt.Errorf("%w: upload HTTP %d", errPostprocessDispatchWaiting, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxComfyUploadResponse+1))
	if err != nil {
		return empty, fmt.Errorf("%w: %v", errPostprocessDispatchWaiting, err)
	}
	if len(body) > maxComfyUploadResponse {
		return empty, errPostprocessSourceChanged
	}
	var result struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return empty, errPostprocessSourceChanged
	}
	if result.Name != input.Filename || result.Subfolder != input.Subfolder || result.Type != "input" {
		return empty, errPostprocessSourceChanged
	}
	readAt, err := a.readPostprocessInput(ctx, input)
	if errors.Is(err, errPostprocessInputMissing) {
		return empty, errPostprocessDispatchWaiting
	}
	if err != nil {
		return empty, err
	}
	return finish(input, readAt)
}

// Preparation consumes only an accepted, reserved, currently claimed child. It
// does not send /prompt; the common dispatcher must own that handoff and recovery.
func (a *App) preparePostprocessDispatch(ctx context.Context, job domain.GenerationJob) (postprocessDispatchPreparation, error) {
	var result postprocessDispatchPreparation
	if job.UserID == nil || a.cfg.ComfyUIUpstream == nil {
		return result, errPostprocessQuoteInvalid
	}
	receipt, err := a.store.CheckGenerationPostprocessDispatch(ctx, *job.UserID, job.ID, job.DispatchToken)
	if err != nil {
		return result, err
	}
	q, err := a.decodePostprocessQuote(receipt)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(job.PayloadCipher, receipt.PayloadCipher) || job.TemplateID != q.TemplateID || job.WorkflowID != domain.PostprocessWorkflow || receipt.JobPublicID != job.PublicID {
		return result, errPostprocessQuoteInvalid
	}
	quotedGraph, err := validatePostprocessQuote(q)
	if err != nil {
		return result, err
	}
	snapshot, observations, err := a.inspectPostprocessRuntime(ctx, q, quotedGraph, job.PublicID)
	if err != nil {
		return result, err
	}
	result.Quote, result.ObjectInfo, result.Sensitive = q, snapshot, receipt.Sensitive
	result.FileObservations = observations
	for position, expected := range q.Sources {
		source, err := a.preparePostprocessSource(ctx, q.UserID, expected.Identity.JobPublicID, expected.Identity.MediaID)
		if errors.Is(err, errPostprocessSourceBusy) {
			return result, errPostprocessDispatchWaiting
		}
		if err != nil {
			return result, err
		}
		input, err := a.uploadPostprocessInput(ctx, job, q, position, source)
		closeErr := source.Close()
		if err != nil {
			return result, err
		}
		if closeErr != nil {
			return result, closeErr
		}
		result.Inputs = append(result.Inputs, input)
	}
	reference := ""
	if len(result.Inputs) == 2 {
		reference = result.Inputs[1].InputName
	}
	graph, plan, err := buildPostprocessPrompt(postprocessMeasuredFacts(q.Sources[0].Measurement), q.Plan.Options, result.Inputs[0].InputName, reference, q.Namespace, snapshot.Schema)
	if err != nil || plan != q.Plan {
		return result, errPostprocessSourceChanged
	}
	result.Prompt = graph
	current, err := a.store.CheckGenerationPostprocessDispatch(ctx, q.UserID, job.ID, job.DispatchToken)
	if err != nil {
		return result, err
	}
	result.Sensitive = result.Sensitive || current.Sensitive
	return result, nil
}
