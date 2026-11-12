package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
)

type postprocessExecutionEvidence struct {
	Quote       postprocessQuote           `json:"confirmed_plan"`
	QuoteSHA256 string                     `json:"confirmed_plan_sha256"`
	Inputs      []postprocessUploadedInput `json:"inputs"`
	Sensitive   bool                       `json:"sensitive"`
	OutputNode  string                     `json:"output_node"`
}

func isPostprocessJob(job domain.GenerationJob) bool {
	return job.TemplateID == domain.PostprocessImageTemplate || job.TemplateID == domain.PostprocessVideoTemplate
}

func postprocessFinalOutputs(outputs map[string]json.RawMessage) map[string]json.RawMessage {
	if final, ok := outputs["6"]; ok {
		return map[string]json.RawMessage{"6": final}
	}
	return nil
}

// Preparation can finish after cancellation or a replacement claim. Do not let
// the old attempt fail or release the resources of the current attempt.
func (a *App) settlePostprocessDispatchChange(ctx context.Context, job domain.GenerationJob) (bool, error) {
	current, err := a.store.GenerationJobByID(ctx, job.ID)
	if err != nil {
		return true, err
	}
	if current.DispatchToken != job.DispatchToken || current.State.Terminal() || current.PromptID != "" {
		return true, nil
	}
	if current.CancellationRequestedAt != nil {
		_, _, err := a.continueGenerationJobCancellation(ctx, current)
		return true, err
	}
	if current.SubmissionStartedAt == nil && current.PromptID == "" && current.DispatchUntil != nil && !current.DispatchUntil.After(time.Now()) {
		return true, a.store.PostponeGenerationDispatch(ctx, job.ID, job.DispatchToken, "Повторяем проверку исходников постобработки")
	}
	return false, nil
}

func postprocessExecutionComponents(graph map[string]any, observations *componentinspection.Response) []generationExecutionComponent {
	components := generationExecutionComponents(graph)
	if observations == nil {
		return components
	}
	for i, component := range components {
		name, _ := componentinspection.NormalizeName(component.Name)
		for _, item := range observations.Items {
			if item.Reference.Kind == component.Kind && item.Reference.Name == name && item.State == "verified" {
				components[i].SHA256, components[i].HashState = item.SHA256, "verified"
			}
		}
	}
	return components
}

// Recheck external observations before the common durable submission marker.
// The marker transaction separately locks the current account, receipt and sources.
func (a *App) refreshPostprocessSubmission(ctx context.Context, job domain.GenerationJob, prepared *postprocessDispatchPreparation) error {
	q := prepared.Quote
	current, err := a.store.CheckGenerationPostprocessDispatch(ctx, q.UserID, job.ID, job.DispatchToken)
	if err != nil {
		return err
	}
	quote, err := a.decodePostprocessQuote(current)
	if err != nil || !reflect.DeepEqual(quote, q) {
		return errPostprocessQuoteInvalid
	}
	snapshot, observations, err := a.inspectPostprocessRuntime(ctx, q, prepared.Prompt, job.PublicID)
	if err != nil {
		return err
	}
	prepared.FileObservations = observations
	if len(prepared.Inputs) != len(q.Sources) {
		return errPostprocessQuoteInvalid
	}
	for position, input := range prepared.Inputs {
		stored, err := a.store.GenerationPostprocessInputForJob(ctx, q.UserID, job.ID, position)
		if err != nil {
			return err
		}
		if stored.ID != input.InputID || stored.State != "stored" || stored.CleanupBlocked ||
			stored.Subfolder+"/"+stored.Filename != input.InputName || stored.SizeBytes != input.SizeBytes || stored.SHA256 != input.SHA256 {
			return errPostprocessSourceChanged
		}
		readAt, err := a.readPostprocessInput(ctx, stored)
		if err != nil {
			return err
		}
		prepared.Inputs[position].ReadAt = readAt
	}
	prepared.ObjectInfo = snapshot
	prepared.Sensitive = prepared.Sensitive || current.Sensitive
	return nil
}

func postprocessExecutionGraph(job domain.GenerationJob, evidence postprocessExecutionEvidence) (map[string]any, error) {
	q := evidence.Quote
	if job.UserID == nil || q.UserID != *job.UserID || q.RequestID != job.RequestID || q.TemplateID != job.TemplateID ||
		job.WorkflowID != domain.PostprocessWorkflow || len(evidence.Inputs) != len(q.Sources) || evidence.OutputNode != "6" {
		return nil, errPostprocessQuoteInvalid
	}
	if _, err := validatePostprocessQuote(q); err != nil {
		return nil, err
	}
	digest, err := postprocessJSONDigest(q)
	if err != nil || digest != evidence.QuoteSHA256 {
		return nil, errPostprocessQuoteInvalid
	}
	for i, input := range evidence.Inputs {
		if input.Source != q.Sources[i].Identity || input.SizeBytes != input.Source.SizeBytes || input.SHA256 != input.Source.SHA256 ||
			input.InputID == "" || input.ReadAt.IsZero() || path.Dir(input.InputName) != q.Namespace ||
			validateComfyInputReference(input.InputName, q.Namespace) != nil {
			return nil, errPostprocessQuoteInvalid
		}
	}
	reference := ""
	if len(evidence.Inputs) == 2 {
		reference = evidence.Inputs[1].InputName
	}
	graph, plan, err := buildPostprocessPrompt(postprocessMeasuredFacts(q.Sources[0].Measurement), q.Plan.Options,
		evidence.Inputs[0].InputName, reference, q.Namespace, comfySchemaCatalog{Nodes: q.Schemas})
	if err != nil || plan != q.Plan {
		return nil, errPostprocessQuoteInvalid
	}
	attachGenerationOutputPrefixes(graph, job.PublicID)
	return graph, nil
}

func (a *App) capturePostprocessExecution(job domain.GenerationJob, prepared postprocessDispatchPreparation) (domain.GenerationExecutionRecord, error) {
	var empty domain.GenerationExecutionRecord
	if a.contentCipher == nil || job.ID <= 0 || job.PublicID == "" {
		return empty, errPostprocessQuoteInvalid
	}
	digest, err := postprocessJSONDigest(prepared.Quote)
	if err != nil {
		return empty, err
	}
	evidence := postprocessExecutionEvidence{Quote: prepared.Quote, QuoteSHA256: digest,
		Inputs: prepared.Inputs, Sensitive: prepared.Sensitive, OutputNode: "6"}
	graph, err := json.Marshal(prepared.Prompt)
	if err != nil {
		return empty, err
	}
	definition, err := json.Marshal(prepared.Quote.Plan)
	if err != nil {
		return empty, err
	}
	snapshot := generationExecutionSnapshot{Version: 2, JobID: job.PublicID, TemplateID: job.TemplateID,
		ParentJobID: prepared.Quote.Sources[0].Identity.JobPublicID, CapturedAt: time.Now().UTC(),
		Workflow: generationExecutionWorkflow{ID: domain.PostprocessWorkflow, DefinitionID: domain.PostprocessWorkflow,
			Definition: definition, DefinitionSHA256: generationEvidenceDigest(definition)},
		Graph: graph, GraphSHA256: generationEvidenceDigest(graph), CatalogSHA256: prepared.ObjectInfo.Fingerprint,
		CatalogFetchedAt: prepared.ObjectInfo.FetchedAt, CatalogSource: prepared.ObjectInfo.Source,
		Components: postprocessExecutionComponents(prepared.Prompt, prepared.FileObservations), FileObservations: prepared.FileObservations,
		Postprocess: &evidence, EvidenceGaps: []string{"runtime_loaded_bytes_not_attested", "output_measurements_not_recorded", "audio_output_not_verified"}}
	if err := validatePostprocessExecution(job, snapshot); err != nil {
		return empty, err
	}
	plain, err := json.Marshal(snapshot)
	if err != nil {
		return empty, err
	}
	if len(plain) > maxGenerationExecutionBytes {
		return empty, errors.New("postprocess execution evidence is too large")
	}
	cipher, err := a.contentCipher.Encrypt(string(plain))
	return domain.GenerationExecutionRecord{JobID: job.ID, PayloadCipher: cipher, PayloadSHA256: generationEvidenceDigest(plain)}, err
}

func validatePostprocessExecution(job domain.GenerationJob, snapshot generationExecutionSnapshot) error {
	if snapshot.Version != 2 || snapshot.Postprocess == nil || snapshot.Seed != "" || snapshot.ModelFamily != "" ||
		len(snapshot.ResolvedInput) != 0 || snapshot.TemplateID != job.TemplateID || snapshot.CapturedAt.IsZero() ||
		snapshot.Workflow.ID != domain.PostprocessWorkflow || snapshot.Workflow.DefinitionID != domain.PostprocessWorkflow {
		return errPostprocessQuoteInvalid
	}
	expected, err := postprocessExecutionGraph(job, *snapshot.Postprocess)
	if err != nil {
		return err
	}
	digest, err := postprocessJSONDigest(expected)
	if err != nil || digest != snapshot.GraphSHA256 || generationEvidenceDigest(snapshot.Graph) != digest ||
		snapshot.ParentJobID != snapshot.Postprocess.Quote.Sources[0].Identity.JobPublicID {
		return errPostprocessQuoteInvalid
	}
	definition, err := postprocessJSONDigest(snapshot.Postprocess.Quote.Plan)
	if err != nil || definition != snapshot.Workflow.DefinitionSHA256 || generationEvidenceDigest(snapshot.Workflow.Definition) != definition {
		return errPostprocessQuoteInvalid
	}
	for _, input := range snapshot.Postprocess.Inputs {
		if input.ReadAt.After(snapshot.CapturedAt) || snapshot.CapturedAt.Sub(input.ReadAt) > time.Minute {
			return errPostprocessQuoteInvalid
		}
	}
	if len(generationExecutionComponents(expected)) > 0 {
		if err := validatePostprocessComponents(job.PublicID, expected, snapshot.FileObservations, snapshot.CapturedAt); err != nil {
			return err
		}
		digest, err := generationRepeatFilesDigest(snapshot.FileObservations)
		if err != nil || digest != snapshot.Postprocess.Quote.FilesSHA256 {
			return errPostprocessSourceChanged
		}
	} else if snapshot.FileObservations != nil {
		return errPostprocessQuoteInvalid
	}
	if !reflect.DeepEqual(snapshot.Components, postprocessExecutionComponents(expected, snapshot.FileObservations)) {
		return errPostprocessQuoteInvalid
	}
	return nil
}

func (a *App) ensurePostprocessJobProjections(ctx context.Context, job domain.GenerationJob) error {
	record, err := a.store.GenerationExecutionForJob(ctx, *job.UserID, job.PublicID)
	if err != nil {
		return err
	}
	snapshot, err := a.decodeGenerationExecution(job, record)
	if err != nil {
		return err
	}
	receipt, err := a.store.GenerationPostprocessByRequest(ctx, *job.UserID, job.RequestID)
	if err != nil {
		return err
	}
	if receipt.JobPublicID != job.PublicID || receipt.ConfirmationSHA256 != snapshot.Postprocess.QuoteSHA256 {
		return errPostprocessQuoteInvalid
	}
	sensitive := receipt.Sensitive || snapshot.Postprocess.Sensitive
	metadata, err := json.Marshal(map[string]any{"operation": "postprocess", "workflow": domain.PostprocessWorkflow,
		"postprocess": snapshot.Postprocess, "execution_sha256": record.PayloadSHA256, "sensitive_content": sensitive})
	if err != nil {
		return err
	}
	metadataCipher, err := a.contentCipher.Encrypt(string(metadata))
	if err != nil {
		return err
	}
	variantCipher, err := a.encodePostprocessVariant(job, snapshot.Postprocess.Quote)
	if err != nil {
		return err
	}
	if err := a.store.InsertGenerationVariantForJob(ctx, job.ID, *job.UserID, job.PromptID, job.TemplateID, job.WorkflowID, "", 0, variantCipher); err != nil {
		return err
	}
	if _, err := a.store.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: *job.UserID, GenerationJobID: &job.ID,
		CorrelationID: job.CorrelationID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: job.PromptID,
		GenerationState: "queued", MetadataCipher: metadataCipher, Sensitive: sensitive,
		ExpiresAt: time.Now().Add(a.retentionPolicy().AIContent)}); err != nil {
		return err
	}
	return a.store.LinkGenerationJobContentEvent(ctx, job.ID, *job.UserID, job.PromptID)
}
