package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
	"ai-access-gateway/internal/store"
)

const (
	postprocessQuotePurpose  = "gateway.postprocess.quote"
	postprocessQuoteVersion  = 1
	postprocessQuoteMaxBytes = 2 << 20
	postprocessQuoteTTL      = 5 * time.Minute
)

var errPostprocessQuoteInvalid = errors.New("postprocess quote is invalid")
var errPostprocessComponentsUnavailable = errors.New("postprocess component verification is unavailable")

type postprocessQuoteSource struct {
	Identity    domain.PostprocessSourceIdentity `json:"identity"`
	Measurement mediaprobe.DecodedMeasurement    `json:"measurement"`
	InputName   string                           `json:"input_name"`
}

// This policy describes workload admission, not promised VRAM usage or base
// sampling quality. Any change requires a new policy/envelope version.
type postprocessWorkloadPolicy struct {
	Version               int   `json:"version"`
	MaxSide               int   `json:"max_side"`
	MaxFramePixels        int64 `json:"max_frame_pixels"`
	MaxInputBytes         int64 `json:"max_input_bytes"`
	MaxSourceFrames       int   `json:"max_source_frames"`
	MaxPixelFrames        int64 `json:"max_pixel_frames"`
	MaxWorkingTensorBytes int64 `json:"max_working_tensor_bytes"`
	MaxScale              int   `json:"max_scale"`
	MaxSourceFPS          int   `json:"max_source_fps"`
	MaxOutputFPS          int   `json:"max_output_fps"`
	MaxSeconds            int   `json:"max_seconds"`
}

func currentPostprocessWorkloadPolicy() postprocessWorkloadPolicy {
	return postprocessWorkloadPolicy{Version: postprocessPlanVersion, MaxSide: postprocessMaxSide,
		MaxFramePixels: postprocessMaxFramePixels, MaxInputBytes: mediaprobe.MaxInputBytes,
		MaxSourceFrames: postprocessMaxSourceFrames, MaxPixelFrames: postprocessMaxPixelFrames,
		MaxWorkingTensorBytes: postprocessMaxWorkingTensorBytes, MaxScale: postprocessMaxScale,
		MaxSourceFPS: postprocessMaxSourceFPS, MaxOutputFPS: postprocessMaxOutputFPS, MaxSeconds: postprocessMaxSeconds}
}

type postprocessQuote struct {
	Purpose      string                        `json:"purpose"`
	Version      int                           `json:"version"`
	UserID       int64                         `json:"user_id,string"`
	RequestID    string                        `json:"request_id"`
	CreatedAt    time.Time                     `json:"created_at"`
	ExpiresAt    time.Time                     `json:"expires_at"`
	Namespace    string                        `json:"namespace"`
	Sources      []postprocessQuoteSource      `json:"sources"`
	Plan         postprocessPlan               `json:"plan"`
	Policy       postprocessWorkloadPolicy     `json:"policy"`
	TemplateID   string                        `json:"template_id"`
	Cost         int                           `json:"cost"`
	Schemas      map[string]comfyNodeSchema    `json:"schemas"`
	SchemaSHA256 string                        `json:"schema_sha256"`
	GraphSHA256  string                        `json:"graph_sha256"`
	Components   *componentinspection.Response `json:"components,omitempty"`
	FilesSHA256  string                        `json:"files_sha256,omitempty"`
}

type postprocessQuoteRequest struct {
	RequestID        string
	SourceJobID      string
	SourceMediaID    int64
	ReferenceJobID   string
	ReferenceMediaID int64
	Options          postprocessOptions
}

func postprocessQuoteAAD(userID int64, requestID string) []byte {
	return []byte(postprocessQuotePurpose + "/1/" + strconv.FormatInt(userID, 10) + "/" + requestID)
}

func postprocessJSONDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return generationEvidenceDigest(data), nil
}

// Only operational schemas of used nodes are bound. Runtime upload choices and
// labels are deliberately excluded; clone first because the live cache is shared.
func postprocessBoundSchemas(catalog comfySchemaCatalog, graph map[string]any) (map[string]comfyNodeSchema, error) {
	used := map[string]comfyNodeSchema{}
	for _, raw := range graph {
		node, ok := raw.(map[string]any)
		if !ok {
			return nil, errPostprocessQuoteInvalid
		}
		class, ok := node["class_type"].(string)
		schema, exists := catalog.Nodes[class]
		if !ok || !exists {
			return nil, errPostprocessQuoteInvalid
		}
		used[class] = schema
	}
	data, err := json.Marshal(used)
	if err != nil {
		return nil, err
	}
	if len(data) > postprocessQuoteMaxBytes/2 {
		return nil, errPostprocessQuoteInvalid
	}
	var result map[string]comfyNodeSchema
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	for class, schema := range result {
		schema.DisplayName, schema.Category, schema.SchemaFingerprint = "", "", ""
		for _, inputs := range []map[string]comfyInputSchema{schema.Required, schema.Optional} {
			for field, input := range inputs {
				if isRuntimeAssetChoice(class, field) {
					input.Choices, input.Default = nil, nil
					inputs[field] = input
				}
			}
		}
		result[class] = schema
	}
	return result, nil
}

func postprocessMeasuredFacts(m mediaprobe.DecodedMeasurement) postprocessSourceFacts {
	return postprocessSourceFacts{MediaType: m.MediaType, Width: m.Width, Height: m.Height, Frames: m.Frames,
		FPSNumerator: m.FPSNumerator, FPSDenominator: m.FPSDenominator, ConstantFrameRate: m.UniformCadence,
		SquarePixels: m.SquarePixels, OrientationNormal: m.NormalOrientation, Opaque: m.Opaque,
		DecodeVerified: m.DecodeVersion == mediaprobe.FullDecodeVersion && m.Source == mediaprobe.FullDecodeSource}
}

func postprocessQuoteInputName(namespace, requestID string, index int, source domain.PostprocessSourceIdentity) string {
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "video/mp4": "mp4", "video/webm": "webm"}[source.MIMEType]
	return fmt.Sprintf("%s/postprocess-%s-%d-%s.%s", namespace, requestID, index, source.SHA256, ext)
}

func postprocessQuoteIdentity(source *preparedPostprocessSource) domain.PostprocessSourceIdentity {
	return domain.PostprocessSourceIdentity{JobID: source.ParentJob.ID, JobPublicID: source.ParentJob.PublicID,
		MediaID: source.Source.MediaID, MediaType: source.Source.MediaType, MIMEType: source.Source.MIMEType,
		SizeBytes: source.Source.SizeBytes, SHA256: source.Measurement.SHA256, ArchiveSHA256: source.Source.ContentHash}
}

func postprocessQuoteSources(q postprocessQuote) []domain.PostprocessSourceIdentity {
	sources := make([]domain.PostprocessSourceIdentity, len(q.Sources))
	for i := range q.Sources {
		sources[i] = q.Sources[i].Identity
	}
	return sources
}

func validatePostprocessComponents(requestID string, graph map[string]any, response *componentinspection.Response, at time.Time) error {
	if len(generationExecutionComponents(graph)) == 0 {
		if response != nil {
			return errPostprocessQuoteInvalid
		}
		return nil
	}
	request, err := generationComponentRequest(requestID, graph)
	if err != nil || response == nil || !response.ValidFor(request) || response.State != "complete" {
		return errPostprocessComponentsUnavailable
	}
	for _, item := range response.Items {
		if item.State != "verified" || item.Bytes <= 0 || item.CheckedAt.Before(at.Add(-time.Minute)) || item.CheckedAt.After(at.Add(time.Minute)) {
			return errPostprocessComponentsUnavailable
		}
	}
	return nil
}

// Rebuild the allowlisted graph rather than trusting persisted graph JSON. Quote
// expiry is checked by confirmation, not here: an accepted job can wait in queue.
func validatePostprocessQuote(q postprocessQuote) (map[string]any, error) {
	invalid := func() (map[string]any, error) { return nil, errPostprocessQuoteInvalid }
	if q.Purpose != postprocessQuotePurpose || q.Version != postprocessQuoteVersion || q.UserID <= 0 || !validGenerationRequestID(q.RequestID) || len(q.RequestID) < 16 ||
		q.CreatedAt.IsZero() || q.CreatedAt.Nanosecond()%1000 != 0 || !q.ExpiresAt.Equal(q.CreatedAt.Add(postprocessQuoteTTL)) ||
		len(q.Sources) < 1 || len(q.Sources) > 2 || q.Policy != currentPostprocessWorkloadPolicy() || q.Cost != 1 {
		return invalid()
	}
	for i, source := range q.Sources {
		s, m := source.Identity, source.Measurement
		hash, err := hex.DecodeString(s.SHA256)
		if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != s.SHA256 || s.JobID <= 0 || s.MediaID <= 0 || !validGenerationRequestID(s.JobPublicID) ||
			s.SizeBytes <= 0 || s.SizeBytes > mediaprobe.MaxInputBytes || s.ArchiveSHA256 != "" && s.ArchiveSHA256 != s.SHA256 ||
			m.SHA256 != s.SHA256 || m.SizeBytes != s.SizeBytes || m.MediaType != s.MediaType || m.Version != 1 ||
			m.MeasuredAt.IsZero() || m.MeasuredAt.Before(q.CreatedAt.Add(-postprocessQuoteTTL)) || m.MeasuredAt.After(q.CreatedAt.Add(time.Minute)) ||
			m.DecodeVersion != mediaprobe.FullDecodeVersion || m.Source != mediaprobe.FullDecodeSource || !m.Opaque || !m.NormalOrientation || !m.SquarePixels ||
			m.Width < postprocessMinSide || m.Height < postprocessMinSide || m.Width > postprocessMaxSide || m.Height > postprocessMaxSide || m.Frames < 1 || m.Frames > mediaprobe.MaxDecodeFrames ||
			m.DecodedPixels != int64(m.Width)*int64(m.Height)*int64(m.Frames) || m.DecodedPixels > mediaprobe.MaxDecodePixels ||
			source.InputName != postprocessQuoteInputName(q.Namespace, q.RequestID, i, s) {
			return invalid()
		}
		if s.MediaType == "image" {
			if s.MIMEType != "image/png" && s.MIMEType != "image/jpeg" && s.MIMEType != "image/webp" || m.Frames != 1 || m.FPSNumerator != 0 || m.FPSDenominator != 0 || m.DecodedPixels > postprocessMaxFramePixels {
				return invalid()
			}
		} else if s.MediaType != "video" || s.MIMEType != "video/mp4" && s.MIMEType != "video/webm" {
			return invalid()
		}
		if i == 1 && (s.MediaType != "image" || s.MediaID == q.Sources[0].Identity.MediaID) {
			return invalid()
		}
	}
	if (q.Plan.Options.ColorMethod != "") != (len(q.Sources) == 2) {
		return invalid()
	}
	family := domain.PostprocessImageTemplate
	if q.Sources[0].Identity.MediaType == "video" {
		family = domain.PostprocessVideoTemplate
	}
	if q.TemplateID != family {
		return invalid()
	}
	ref := ""
	if len(q.Sources) == 2 {
		ref = q.Sources[1].InputName
	}
	catalog := comfySchemaCatalog{Nodes: q.Schemas}
	graph, plan, err := buildPostprocessPrompt(postprocessMeasuredFacts(q.Sources[0].Measurement), q.Plan.Options, q.Sources[0].InputName, ref, q.Namespace, catalog)
	if err != nil || plan != q.Plan {
		return invalid()
	}
	schemas, err := postprocessBoundSchemas(catalog, graph)
	if err != nil {
		return invalid()
	}
	digest, err := postprocessJSONDigest(schemas)
	storedDigest, storedErr := postprocessJSONDigest(q.Schemas)
	if err != nil || storedErr != nil || digest != q.SchemaSHA256 || storedDigest != digest {
		return invalid()
	}
	digest, err = postprocessJSONDigest(graph)
	if err != nil || digest != q.GraphSHA256 {
		return invalid()
	}
	if err := validatePostprocessComponents(q.RequestID, graph, q.Components, q.CreatedAt); err != nil {
		return nil, err
	}
	digest, err = generationRepeatFilesDigest(q.Components)
	if err != nil || digest != q.FilesSHA256 {
		return invalid()
	}
	return graph, nil
}

func (a *App) encodePostprocessQuote(q postprocessQuote) ([]byte, string, error) {
	if _, err := validatePostprocessQuote(q); err != nil {
		return nil, "", err
	}
	if q.Namespace != comfyUploadNamespace(a.comfyClientID(q.UserID)) {
		return nil, "", errPostprocessQuoteInvalid
	}
	data, err := json.Marshal(q)
	if err != nil {
		return nil, "", err
	}
	if len(data) > postprocessQuoteMaxBytes {
		return nil, "", errPostprocessQuoteInvalid
	}
	cipher, err := a.contentCipher.EncryptBytesWithAAD(data, postprocessQuoteAAD(q.UserID, q.RequestID))
	return cipher, generationEvidenceDigest(data), err
}

func (a *App) decodePostprocessQuote(receipt domain.GenerationPostprocessReceipt) (postprocessQuote, error) {
	var q postprocessQuote
	if receipt.Version != 1 || receipt.UserID <= 0 || !validGenerationRequestID(receipt.RequestID) || len(receipt.PayloadCipher) == 0 || len(receipt.PayloadCipher) > postprocessQuoteMaxBytes+128 {
		return q, errPostprocessQuoteInvalid
	}
	data, err := a.contentCipher.DecryptBytesWithAAD(receipt.PayloadCipher, postprocessQuoteAAD(receipt.UserID, receipt.RequestID))
	if err != nil {
		return q, errPostprocessQuoteInvalid
	}
	if len(data) > postprocessQuoteMaxBytes || generationEvidenceDigest(data) != receipt.ConfirmationSHA256 {
		return q, errPostprocessQuoteInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		return postprocessQuote{}, errPostprocessQuoteInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return postprocessQuote{}, errPostprocessQuoteInvalid
	}
	canonical, err := json.Marshal(q)
	if err != nil || !bytes.Equal(canonical, data) || q.UserID != receipt.UserID || q.RequestID != receipt.RequestID || !q.ExpiresAt.Equal(receipt.ExpiresAt) ||
		q.Namespace != comfyUploadNamespace(a.comfyClientID(q.UserID)) || !reflect.DeepEqual(postprocessQuoteSources(q), receipt.Sources) {
		return postprocessQuote{}, errPostprocessQuoteInvalid
	}
	if _, err := validatePostprocessQuote(q); err != nil {
		return postprocessQuote{}, err
	}
	return q, nil
}

func postprocessQuoteMatches(q postprocessQuote, request postprocessQuoteRequest) bool {
	if q.RequestID != request.RequestID || q.Plan.Options != request.Options || len(q.Sources) == 0 || q.Sources[0].Identity.JobPublicID != request.SourceJobID || q.Sources[0].Identity.MediaID != request.SourceMediaID {
		return false
	}
	if len(q.Sources) == 1 {
		return request.ReferenceJobID == "" && request.ReferenceMediaID == 0
	}
	return q.Sources[1].Identity.JobPublicID == request.ReferenceJobID && q.Sources[1].Identity.MediaID == request.ReferenceMediaID
}

func (a *App) existingPostprocessQuote(receipt domain.GenerationPostprocessReceipt, request postprocessQuoteRequest) (postprocessQuote, error) {
	if receipt.State != "quoted" && receipt.State != "accepted" {
		return postprocessQuote{}, store.ErrPostprocessConflict
	}
	q, err := a.decodePostprocessQuote(receipt)
	if err != nil {
		return q, err
	}
	if !postprocessQuoteMatches(q, request) {
		return postprocessQuote{}, store.ErrPostprocessConflict
	}
	if receipt.State == "quoted" && !q.ExpiresAt.After(time.Now()) {
		return postprocessQuote{}, store.ErrPostprocessExpired
	}
	return q, nil
}

// It prepares a confirmation without uploading assets,
// reserving quota, enqueuing work or sending a ComfyUI prompt.
func (a *App) createPostprocessQuote(ctx context.Context, userID int64, request postprocessQuoteRequest) (domain.GenerationPostprocessReceipt, postprocessQuote, error) {
	var empty domain.GenerationPostprocessReceipt
	var q postprocessQuote
	if userID <= 0 || len(request.RequestID) < 16 || !validGenerationRequestID(request.RequestID) ||
		(request.Options.ColorMethod != "") != (request.ReferenceMediaID > 0 && request.ReferenceJobID != "") ||
		request.Options.ColorMethod == "" && (request.ReferenceJobID != "" || request.ReferenceMediaID != 0) {
		return empty, q, errPostprocessQuoteInvalid
	}
	fresh, err := a.store.UserByID(ctx, userID)
	if err != nil {
		return empty, q, err
	}
	if fresh.Disabled || fresh.AccountExpiresAt.Valid && !fresh.AccountExpiresAt.Time.After(time.Now()) || !fresh.CanUseService("quick_generation") {
		return empty, q, store.ErrQuickGenerationForbidden
	}
	receipt, err := a.store.GenerationPostprocessByRequest(ctx, userID, request.RequestID)
	if err == nil {
		q, err = a.existingPostprocessQuote(receipt, request)
		if err == nil && !fresh.CanUseQuickGenerationType(q.TemplateID) {
			return empty, postprocessQuote{}, store.ErrQuickGenerationForbidden
		}
		return receipt, q, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, q, err
	}
	release, ok := a.acquirePostprocessQuote()
	if !ok {
		return empty, q, errPostprocessSourceBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	source, err := a.preparePostprocessSource(ctx, userID, request.SourceJobID, request.SourceMediaID)
	if err != nil {
		return empty, q, err
	}
	defer source.Close()
	prepared := []*preparedPostprocessSource{source}
	if request.Options.ColorMethod != "" {
		reference, err := a.preparePostprocessSource(ctx, userID, request.ReferenceJobID, request.ReferenceMediaID)
		if err != nil {
			return empty, q, err
		}
		defer reference.Close()
		if reference.Source.MediaType != "image" || reference.Source.MediaID == source.Source.MediaID {
			return empty, q, errPostprocessQuoteInvalid
		}
		prepared = append(prepared, reference)
	}
	if _, err := planPostprocess(source.Facts, request.Options); err != nil {
		return empty, q, fmt.Errorf("%w: %v", errPostprocessQuoteInvalid, err)
	}
	snapshot, err := a.comfyObjectInfo(ctx, true)
	if err != nil || snapshot.Source != comfyObjectInfoLive {
		return empty, q, errPostprocessComponentsUnavailable
	}
	q = postprocessQuote{Purpose: postprocessQuotePurpose, Version: postprocessQuoteVersion, UserID: userID, RequestID: request.RequestID,
		Namespace: comfyUploadNamespace(a.comfyClientID(userID)), Policy: currentPostprocessWorkloadPolicy(), Cost: 1, TemplateID: domain.PostprocessImageTemplate}
	if source.Source.MediaType == "video" {
		q.TemplateID = domain.PostprocessVideoTemplate
	}
	for i, item := range prepared {
		identity := postprocessQuoteIdentity(item)
		q.Sources = append(q.Sources, postprocessQuoteSource{identity, item.Measurement, postprocessQuoteInputName(q.Namespace, q.RequestID, i, identity)})
	}
	ref := ""
	if len(q.Sources) == 2 {
		ref = q.Sources[1].InputName
	}
	graph, plan, err := buildPostprocessPrompt(source.Facts, request.Options, q.Sources[0].InputName, ref, q.Namespace, snapshot.Schema)
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	q.Plan = plan
	q.Schemas, err = postprocessBoundSchemas(snapshot.Schema, graph)
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	q.SchemaSHA256, err = postprocessJSONDigest(q.Schemas)
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	q.GraphSHA256, err = postprocessJSONDigest(graph)
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	if len(generationExecutionComponents(graph)) > 0 {
		if !a.loraTraining.Configured() {
			return empty, postprocessQuote{}, errPostprocessComponentsUnavailable
		}
		inspection, err := generationComponentRequest(request.RequestID, graph)
		if err != nil {
			return empty, postprocessQuote{}, err
		}
		observed, err := a.loraTraining.InspectGenerationComponents(ctx, inspection)
		if err != nil {
			return empty, postprocessQuote{}, errPostprocessComponentsUnavailable
		}
		q.Components = &observed
	}
	q.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	q.ExpiresAt = q.CreatedAt.Add(postprocessQuoteTTL)
	if err := validatePostprocessComponents(q.RequestID, graph, q.Components, q.CreatedAt); err != nil {
		return empty, postprocessQuote{}, err
	}
	q.FilesSHA256, err = generationRepeatFilesDigest(q.Components)
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	cipher, digest, err := a.encodePostprocessQuote(q)
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	receipt, _, err = a.store.CreateGenerationPostprocessQuote(ctx, domain.CreateGenerationPostprocessQuoteParams{UserID: userID, RequestID: request.RequestID,
		ConfirmationSHA256: digest, PayloadCipher: cipher, Sources: postprocessQuoteSources(q), ExpiresAt: q.ExpiresAt})
	if errors.Is(err, store.ErrPostprocessConflict) {
		// A concurrent identical request may have won with a different timestamp.
		// Return its original plan only if the user's exact intent still matches.
		if existing, readErr := a.store.GenerationPostprocessByRequest(ctx, userID, request.RequestID); readErr == nil {
			old, oldErr := a.existingPostprocessQuote(existing, request)
			return existing, old, oldErr
		}
	}
	if err != nil {
		return empty, postprocessQuote{}, err
	}
	return receipt, q, nil
}
