package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mediaprobe"
)

type postprocessSourceView struct {
	JobID        string                        `json:"job_id"`
	MediaID      string                        `json:"media_id"`
	Measurement  mediaprobe.DecodedMeasurement `json:"measurement"`
	Availability string                        `json:"availability"`
}

type postprocessView struct {
	Version int                     `json:"version"`
	Plan    postprocessPlan         `json:"plan"`
	Sources []postprocessSourceView `json:"sources"`
}

type postprocessVariantPayload struct {
	Purpose  string          `json:"purpose"`
	Version  int             `json:"version"`
	UserID   int64           `json:"user_id,string"`
	JobID    string          `json:"job_id"`
	PromptID string          `json:"prompt_id"`
	View     postprocessView `json:"view"`
}

const postprocessVariantPurpose = "gateway.postprocess.variant"

func postprocessViewFromQuote(q postprocessQuote) postprocessView {
	view := postprocessView{Version: 1, Plan: q.Plan, Sources: make([]postprocessSourceView, 0, len(q.Sources))}
	for _, source := range q.Sources {
		view.Sources = append(view.Sources, postprocessSourceView{JobID: source.Identity.JobPublicID,
			MediaID: strconv.FormatInt(source.Identity.MediaID, 10), Measurement: source.Measurement, Availability: "not_checked"})
	}
	return view
}

func postprocessVariantAAD(userID int64, promptID string) []byte {
	return []byte(postprocessVariantPurpose + "/1/" + strconv.FormatInt(userID, 10) + "/" + promptID)
}

func postprocessLabel(templateID string) string {
	if templateID == domain.PostprocessVideoTemplate {
		return "Постобработка видео"
	}
	return "Постобработка изображения"
}

func validPostprocessView(view postprocessView, templateID string) bool {
	if view.Version != 1 || len(view.Sources) < 1 || len(view.Sources) > 2 || (view.Plan.Options.ColorMethod != "") != (len(view.Sources) == 2) {
		return false
	}
	for i, source := range view.Sources {
		if !validGenerationRequestID(source.JobID) || source.Availability != "not_checked" {
			return false
		}
		if _, err := postprocessExactID(source.MediaID); err != nil {
			return false
		}
		m := source.Measurement
		if m.Version != 1 || m.MeasuredAt.IsZero() || m.SizeBytes <= 0 || m.SizeBytes > mediaprobe.MaxInputBytes || len(m.SHA256) != 64 {
			return false
		}
		if i == 1 && (m.MediaType != "image" || m.Frames != 1 || source.MediaID == view.Sources[0].MediaID) {
			return false
		}
	}
	plan, err := planPostprocess(postprocessMeasuredFacts(view.Sources[0].Measurement), view.Plan.Options)
	if err != nil || plan != view.Plan {
		return false
	}
	return templateID == domain.PostprocessImageTemplate && plan.MediaType == "image" || templateID == domain.PostprocessVideoTemplate && plan.MediaType == "video"
}

func (a *App) encodePostprocessVariant(job domain.GenerationJob, q postprocessQuote) ([]byte, error) {
	if job.UserID == nil || job.PromptID == "" || q.UserID != *job.UserID || q.RequestID != job.RequestID || q.TemplateID != job.TemplateID || !validGenerationRequestID(job.PublicID) || job.WorkflowID != domain.PostprocessWorkflow {
		return nil, errPostprocessQuoteInvalid
	}
	payload := postprocessVariantPayload{Purpose: postprocessVariantPurpose, Version: 1, UserID: *job.UserID, JobID: job.PublicID, PromptID: job.PromptID, View: postprocessViewFromQuote(q)}
	if !validPostprocessView(payload.View, job.TemplateID) {
		return nil, errPostprocessQuoteInvalid
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(plain) > maxGenerationRecipePayload {
		return nil, errPostprocessQuoteInvalid
	}
	return a.contentCipher.EncryptBytesWithAAD(plain, postprocessVariantAAD(*job.UserID, job.PromptID))
}

func (a *App) decodePostprocessVariant(row domain.GenerationVariantRow) (*postprocessView, error) {
	if a.contentCipher == nil || row.UserID <= 0 || row.PromptID == "" || row.WorkflowID != domain.PostprocessWorkflow || len(row.PayloadCipher) > maxGenerationRecipePayload+128 {
		return nil, errPostprocessQuoteInvalid
	}
	plain, err := a.contentCipher.DecryptBytesWithAAD(row.PayloadCipher, postprocessVariantAAD(row.UserID, row.PromptID))
	if err != nil || len(plain) > maxGenerationRecipePayload {
		return nil, errPostprocessQuoteInvalid
	}
	var payload postprocessVariantPayload
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF || payload.Version != 1 || payload.Purpose != postprocessVariantPurpose ||
		payload.UserID != row.UserID || payload.PromptID != row.PromptID || !validGenerationRequestID(payload.JobID) || row.JobPublicID != "" && payload.JobID != row.JobPublicID || !validPostprocessView(payload.View, row.TemplateID) {
		return nil, errPostprocessQuoteInvalid
	}
	return &payload.View, nil
}

func (a *App) postprocessJobView(ctx context.Context, job domain.GenerationJob) (*postprocessView, error) {
	if job.UserID == nil {
		return nil, sql.ErrNoRows
	}
	receipt, err := a.store.GenerationPostprocessByRequest(ctx, *job.UserID, job.RequestID)
	if err != nil {
		return nil, err
	}
	if receipt.State != "accepted" || receipt.JobID == nil || *receipt.JobID != job.ID || receipt.JobPublicID != job.PublicID || !bytes.Equal(receipt.PayloadCipher, job.PayloadCipher) {
		return nil, errPostprocessQuoteInvalid
	}
	q, err := a.decodePostprocessQuote(receipt)
	if err != nil {
		return nil, err
	}
	if q.TemplateID != job.TemplateID || job.WorkflowID != domain.PostprocessWorkflow {
		return nil, errPostprocessQuoteInvalid
	}
	view := postprocessViewFromQuote(q)
	return &view, nil
}

func (a *App) resolvePostprocessViewSources(ctx context.Context, userID int64, view *postprocessView) error {
	for i := range view.Sources {
		source := &view.Sources[i]
		id, err := postprocessExactID(source.MediaID)
		if err != nil {
			return err
		}
		_, current, err := a.loadPostprocessOutputSource(ctx, userID, source.JobID, id)
		if errors.Is(err, sql.ErrNoRows) {
			source.Availability = "unavailable"
			continue
		}
		if err != nil {
			return err
		}
		if current.SizeBytes != source.Measurement.SizeBytes || current.MediaType != source.Measurement.MediaType || current.ContentHash != "" && current.ContentHash != source.Measurement.SHA256 {
			source.Availability = "unavailable"
		} else {
			source.Availability = "available"
		}
	}
	return nil
}

// Preserve the existing numeric seed contract (including zero) for ordinary
// generation. Postprocessing has no sampler seed, not a seed with value zero.
func (view generationVariantView) MarshalJSON() ([]byte, error) {
	type plain generationVariantView
	var seed *int64
	if view.Operation != "postprocess" {
		seed = &view.Seed
	}
	return json.Marshal(struct {
		plain
		Seed *int64 `json:"seed,omitempty"`
	}{plain(view), seed})
}

func (view generationJobView) MarshalJSON() ([]byte, error) {
	type plain generationJobView
	var seed *int64
	if view.Operation != "postprocess" {
		seed = &view.Seed
	}
	return json.Marshal(struct {
		plain
		Seed *int64 `json:"seed,omitempty"`
	}{plain(view), seed})
}
