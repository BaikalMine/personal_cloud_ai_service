package gateway

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func (a *App) inspectPostprocessRuntime(ctx context.Context, q postprocessQuote, graph map[string]any, requestID string) (comfyObjectInfoSnapshot, *componentinspection.Response, error) {
	snapshot, err := a.comfyObjectInfo(ctx, true)
	if err != nil || snapshot.Source != comfyObjectInfoLive {
		return snapshot, nil, errPostprocessDispatchWaiting
	}
	schemas, err := postprocessBoundSchemas(snapshot.Schema, graph)
	if err != nil {
		return snapshot, nil, err
	}
	digest, err := postprocessJSONDigest(schemas)
	if err != nil || digest != q.SchemaSHA256 {
		return snapshot, nil, errPostprocessSourceChanged
	}
	if len(generationExecutionComponents(graph)) == 0 {
		return snapshot, nil, nil
	}
	if !a.loraTraining.Configured() {
		return snapshot, nil, errPostprocessComponentsUnavailable
	}
	request, err := generationComponentRequest(requestID, graph)
	if err != nil {
		return snapshot, nil, err
	}
	response, err := a.loraTraining.InspectGenerationComponents(ctx, request)
	if err != nil || response.State == "pending" {
		return snapshot, nil, errPostprocessDispatchWaiting
	}
	if err := validatePostprocessComponents(requestID, graph, &response, time.Now()); err != nil {
		return snapshot, nil, err
	}
	digest, err = generationRepeatFilesDigest(&response)
	if err != nil || digest != q.FilesSHA256 {
		return snapshot, nil, errPostprocessSourceChanged
	}
	return snapshot, &response, nil
}

func postprocessConfirmationValid(digest string) bool {
	value, err := hex.DecodeString(digest)
	return err == nil && len(value) == 32 && strings.ToLower(digest) == digest
}

// Only the first acceptance needs expensive observations. Durable accepted
// receipts are retried without decoding sources or minting another child.
func (a *App) confirmPostprocessQuote(ctx context.Context, userID int64, requestID, digest string) (domain.GenerationPostprocessReceipt, error) {
	var empty domain.GenerationPostprocessReceipt
	if userID <= 0 || len(requestID) < 16 || !validGenerationRequestID(requestID) || !postprocessConfirmationValid(digest) {
		return empty, errPostprocessQuoteInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r, err := a.store.GenerationPostprocessByRequest(ctx, userID, requestID)
	if err != nil {
		return empty, err
	}
	if r.State == "cancelled" || r.ConfirmationSHA256 != digest {
		return empty, store.ErrPostprocessConflict
	}
	q, err := a.decodePostprocessQuote(r)
	if err != nil {
		return empty, err
	}
	if r.State == "accepted" {
		return r, nil
	}
	if r.State != "quoted" {
		return empty, store.ErrPostprocessConflict
	}
	if !q.ExpiresAt.After(time.Now()) {
		return empty, store.ErrPostprocessExpired
	}
	release, ok := a.acquirePostprocessQuote()
	if !ok {
		return empty, errPostprocessSourceBusy
	}
	defer release()
	graph, err := validatePostprocessQuote(q)
	if err != nil {
		return empty, err
	}
	for _, expected := range q.Sources {
		source, err := a.preparePostprocessSource(ctx, userID, expected.Identity.JobPublicID, expected.Identity.MediaID)
		if err != nil {
			return empty, err
		}
		matches := postprocessQuoteIdentity(source) == expected.Identity && postprocessMeasuredFacts(source.Measurement) == postprocessMeasuredFacts(expected.Measurement)
		closeErr := source.Close()
		if !matches {
			return empty, errPostprocessSourceChanged
		}
		if closeErr != nil {
			return empty, closeErr
		}
	}
	if _, _, err := a.inspectPostprocessRuntime(ctx, q, graph, requestID); err != nil {
		return empty, err
	}
	if !q.ExpiresAt.After(time.Now()) {
		return empty, store.ErrPostprocessExpired
	}
	r, _, err = a.store.ConfirmGenerationPostprocess(ctx, userID, requestID, digest, newRequestID())
	return r, err
}

func (a *App) handlePostprocessConfirm(w http.ResponseWriter, r *http.Request) {
	v, ok := postprocessHTTPValues(w, r, http.MethodPost, "request_id", "confirmation")
	if !ok {
		return
	}
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "Проверка безопасности не пройдена")
		return
	}
	user, err := a.generationRepeatUser(r.Context(), r)
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	receipt, err := a.confirmPostprocessQuote(r.Context(), user.ID, v.Get("request_id"), v.Get("confirmation"))
	if err != nil {
		writePostprocessHTTPError(w, err)
		return
	}
	if receipt.JobID != nil {
		if _, err := a.resumePostprocessPublication(r.Context(), user.ID, receipt.RequestID); err != nil {
			// Acceptance is already durable. Status describes the same child;
			// the server loop retries publication even after the browser leaves.
			logGateway(r.Context(), slog.LevelWarn, "postprocess_publication_pending", "Accepted postprocessing awaits publication recovery", "job_public_id", receipt.JobPublicID, "error", err)
		}
	}
	a.writePostprocessQuoteReceipt(w, r.Context(), user, receipt)
}
