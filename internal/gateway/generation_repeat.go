package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
)

var errGenerationRepeatWaiting = errors.New("generation repeat inspection is pending")
var errGenerationRepeatDrift = errors.New("generation repeat no longer matches its confirmation")

type generationRepeatConstraint struct {
	Version               int    `json:"version"`
	SourceID              string `json:"source_id"`
	SourcePayloadSHA256   string `json:"source_payload_sha256"`
	SourceExecutionSHA256 string `json:"source_execution_sha256"`
	PreparedSHA256        string `json:"prepared_sha256"`
	FilesObserved         bool   `json:"files_observed"`
	FilesSHA256           string `json:"files_sha256"`
	AssetsSHA256          string `json:"assets_sha256"`
}

func (value generationRepeatConstraint) digest() string {
	data, _ := json.Marshal(value)
	return generationEvidenceDigest(data)
}

type generationRepeatDifference struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Field     string `json:"field,omitempty"`
	Technical bool   `json:"technical,omitempty"`
}

type generationRepeatAsset struct {
	Kind   string `json:"kind"`
	Number int    `json:"number"`
	Name   string `json:"name"`
	ID     string `json:"id"`
	Bytes  int64  `json:"bytes"`
}

type generationRepeatView struct {
	Version       int                          `json:"version"`
	SourceID      string                       `json:"source_id"`
	State         string                       `json:"state"`
	Message       string                       `json:"message"`
	Seed          string                       `json:"seed"`
	Model         string                       `json:"model"`
	Workflow      string                       `json:"workflow"`
	Prompt        string                       `json:"prompt"`
	TemplateID    string                       `json:"template_id"`
	Recorded      bool                         `json:"recorded"`
	FilesObserved bool                         `json:"files_observed"`
	Differences   []generationRepeatDifference `json:"differences"`
	Warnings      []string                     `json:"warnings"`
	Assets        []generationRepeatAsset      `json:"assets"`
	CheckedAt     time.Time                    `json:"checked_at"`
	Confirmation  string                       `json:"confirmation,omitempty"`
	ExpiresAt     int64                        `json:"expires_at,omitempty"`
	Quota         generationQuotaView          `json:"quota"`
	QuotaKnown    bool                         `json:"quota_known"`
	Source        *generationJobView           `json:"source,omitempty"`
}

type generationRepeatPreparation struct {
	Source     domain.GenerationJob
	Payload    generationSavedPayload
	Prepared   generationPreparation
	Constraint generationRepeatConstraint
	View       generationRepeatView
}

func repeatExecutionInput(input generationForm) generationForm {
	input.AssistantRequested = false
	input.AssistantApplied = false
	input.AssistantAction = ""
	input.AssistantTemplate = ""
	input.AssistantThink = false
	input.AssistantOriginal = ""
	input.AssistantSuggestion = ""
	return input
}

// Only the service-added job suffix is normalized. All other graph changes,
// including output configuration, remain part of the comparison.
func generationRepeatGraph(prompt json.RawMessage, jobID string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(prompt))
	decoder.UseNumber()
	var graph map[string]any
	if err := decoder.Decode(&graph); err != nil || len(graph) == 0 {
		return nil, errors.New("invalid saved generation graph")
	}
	for _, raw := range graph {
		node, _ := raw.(map[string]any)
		inputs, _ := node["inputs"].(map[string]any)
		if prefix, ok := inputs["filename_prefix"].(string); ok {
			prefix = strings.TrimSpace(prefix)
			if jobID != "" {
				prefix = strings.TrimSuffix(prefix, "-"+jobID)
			}
			prefix = strings.TrimRight(prefix, "-_")
			if prefix == "" {
				prefix = "AI-Gateway"
			}
			inputs["filename_prefix"] = prefix
		}
	}
	return json.Marshal(graph)
}

func generationRepeatPreparedDigest(prepared generationPreparation, jobID string) (string, error) {
	graph, err := json.Marshal(prepared.Prompt)
	if err != nil {
		return "", err
	}
	graph, err = generationRepeatGraph(graph, jobID)
	if err != nil {
		return "", err
	}
	var manifest any
	if value, ok := workflowManifestForInput(prepared.Input); ok && value.Family == prepared.Input.ModelFamily {
		manifest = value
	}
	data, err := json.Marshal(struct {
		Input      generationForm
		Definition workflowDefinition
		Manifest   any
		Graph      json.RawMessage
	}{repeatExecutionInput(prepared.Input), prepared.Definition, manifest, graph})
	if err != nil {
		return "", err
	}
	return generationEvidenceDigest(data), nil
}

func generationRepeatFilesDigest(response *componentinspection.Response) (string, error) {
	if response == nil {
		return "", nil
	}
	type file struct {
		Kind, Name, SHA256 string
		Bytes              int64
	}
	files := []file{}
	for _, item := range response.Items {
		if item.State != "verified" {
			return "", errGenerationRepeatWaiting
		}
		files = append(files, file{item.Kind, item.Name, item.SHA256, item.Bytes})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Kind+"\x00"+files[i].Name < files[j].Kind+"\x00"+files[j].Name })
	data, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	return generationEvidenceDigest(data), nil
}

func (a *App) generationRepeatAssets(ctx context.Context, user *User, input generationForm) ([]generationRepeatAsset, error) {
	assets := []generationRepeatAsset{}
	for _, ref := range generationExecutionAssets(input) {
		if err := a.validateGenerationAsset(ref.InputName, user.ID, "исходный материал"); err != nil {
			return nil, err
		}
		name := path.Base(ref.InputName)
		folder := path.Dir(ref.InputName)
		asset, err := a.store.ComfyInputAssetByPathForUser(ctx, user.ID, name, folder)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("Исходный материал %s больше не хранится. Выберите его заново в редакторе", name)
		}
		if err != nil {
			return nil, errGenerationRepeatWaiting
		}
		if a.cfg.ComfyUIUpstream == nil {
			return nil, errGenerationRepeatWaiting
		}
		target := *a.cfg.ComfyUIUpstream
		target.Path = singleJoiningSlash(target.Path, "/view")
		target.RawQuery = url.Values{"filename": {name}, "subfolder": {folder}, "type": {"input"}}.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Range", "bytes=0-0")
		if a.cfg.ComfyUIUpstreamAuthHeader != "" {
			request.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
		}
		response, err := (&http.Client{Timeout: 4 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(request)
		if err != nil {
			return nil, errGenerationRepeatWaiting
		}
		response.Body.Close()
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
			return nil, fmt.Errorf("Файл %s не найден в хранилище исходных материалов", name)
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
			return nil, errGenerationRepeatWaiting
		}
		assets = append(assets, generationRepeatAsset{Kind: ref.Kind, Number: ref.Number, Name: name, ID: asset.ID, Bytes: asset.SizeBytes})
	}
	return assets, nil
}

func generationRepeatAssetsDigest(assets []generationRepeatAsset) string {
	data, _ := json.Marshal(assets)
	return generationEvidenceDigest(data)
}

func (a *App) prepareGenerationRepeat(ctx context.Context, user *User, sourceID string) (generationRepeatPreparation, error) {
	result := generationRepeatPreparation{View: generationRepeatView{Version: 1, SourceID: sourceID, State: "blocked", Differences: []generationRepeatDifference{}, Warnings: []string{}, Assets: []generationRepeatAsset{}, CheckedAt: time.Now().UTC()}}
	source, err := a.store.GenerationJobByPublicID(ctx, user.ID, sourceID)
	if err != nil {
		return result, err
	}
	result.Source = source
	if !user.CanUseQuickGenerationType(source.TemplateID) {
		return result, errGenerationRepeatAccess
	}
	if isPostprocessJob(source) {
		result.View.Message = "Это постобработка готового файла, а не генерация по промпту. Откройте результат в галерее."
		return result, nil
	}
	if !source.State.Terminal() {
		result.View.Message = "Дождитесь завершения исходного задания или отмените его"
		return result, nil
	}
	payload, err := a.decodeGenerationSavedPayload(source.PayloadCipher)
	if err != nil {
		result.View.Message = "Параметры исходного задания больше недоступны"
		return result, nil
	}
	result.Payload = payload
	var input generationForm
	var original *generationExecutionSnapshot
	record, err := a.store.GenerationExecutionForJob(ctx, user.ID, sourceID)
	if err == nil {
		snapshot, decodeErr := a.decodeGenerationExecution(source, record)
		if decodeErr != nil {
			return result, decodeErr
		}
		if err = json.Unmarshal(snapshot.ResolvedInput, &input); err != nil {
			return result, err
		}
		original = &snapshot
		result.View.Recorded = true
		result.Constraint.SourceExecutionSHA256 = record.PayloadSHA256
		if source.SubmissionRejectedAt != nil {
			result.View.Warnings = append(result.View.Warnings, "Исходная отправка была отклонена ComfyUI. Сравниваем параметры попытки, а не выполненную генерацию")
		} else if source.PromptID == "" {
			result.View.Warnings = append(result.View.Warnings, "Выполнение исходной отправки не подтверждено ComfyUI")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		input, err = parseGenerationValues(ctx, generationJobFormValues(payload.Values))
		if err != nil {
			result.View.Message = "Сохранённая форма больше не поддерживается. Откройте её в редакторе"
			return result, nil
		}
		if payload.Repeat != nil && payload.ExecutionInput != nil {
			input = *payload.ExecutionInput
		}
		input.Seed = source.Seed
		result.View.Warnings = append(result.View.Warnings, "У исходного задания нет записанного паспорта отправки. Проверяем сохранённые параметры формы")
	} else {
		return result, err
	}
	input = repeatExecutionInput(input)
	result.View.Seed = strconv.FormatInt(input.Seed, 10)
	result.View.Prompt = input.Positive
	result.View.TemplateID = input.TemplateID
	result.View.Model = input.ModelName
	result.View.Workflow = input.PresetID
	if input.Seed < 0 {
		result.View.Message = "Исходный seed не был сохранён"
		return result, nil
	}
	current, err := a.comfyObjectInfo(ctx, true)
	if err != nil || current.Source != comfyObjectInfoLive {
		result.View.State = "pending"
		result.View.Message = "Не удалось получить текущий каталог ComfyUI. Проверку можно повторить"
		return result, nil
	}
	prepared, err := a.prepareGeneration(ctx, user, input, false)
	if err != nil {
		result.View.Message = err.Error()
		return result, nil
	}
	result.Prepared = prepared
	result.View.Model = prepared.Input.ModelName
	assets, err := a.generationRepeatAssets(ctx, user, prepared.Input)
	if errors.Is(err, errGenerationRepeatWaiting) {
		result.View.State = "pending"
		result.View.Message = "Проверяем доступность исходных материалов"
		return result, nil
	}
	if err != nil {
		result.View.Message = err.Error()
		return result, nil
	}
	result.View.Assets = assets
	wait, err := a.inspectGenerationPreparation(ctx, source, &result.Prepared)
	if wait {
		result.View.State = "pending"
		result.View.Message = "Проверяем файлы моделей и LoRA"
		return result, nil
	}
	if err != nil {
		result.View.Message = err.Error()
		return result, nil
	}
	result.View.FilesObserved = result.Prepared.FileObservations != nil
	if !result.View.FilesObserved {
		result.View.Warnings = append(result.View.Warnings, "Проверка файлов на хосте выключена. Совпадение имён не подтверждает одинаковые веса моделей и LoRA")
	}
	if original != nil && original.FileObservations == nil {
		result.View.Warnings = append(result.View.Warnings, "Контрольные суммы файлов исходного запуска не записаны. Сравнить их содержимое с текущими файлами невозможно")
	}
	result.View.Warnings = append(result.View.Warnings, "Повтор создаёт новое задание. Состояние моделей в памяти ComfyUI и совпадение изображения не гарантируются")
	result.View.Differences = generationRepeatDifferences(original, input, result.Prepared, sourceID)
	result.View.State = "ready"
	result.View.Message = "Параметры проверены. Можно создать новое задание"
	if len(result.View.Differences) > 0 {
		result.View.State = "changed"
		result.View.Message = "Условия запуска изменились. Проверьте различия перед повтором"
	}
	if original == nil || !result.View.FilesObserved || original.FileObservations == nil {
		if result.View.State == "ready" {
			result.View.State = "unverified"
			result.View.Message = "Запуск доступен, но полного подтверждения совпадения нет"
		}
	}
	result.Constraint.Version = 1
	result.Constraint.SourceID = sourceID
	result.Constraint.SourcePayloadSHA256 = generationEvidenceDigest(source.PayloadCipher)
	result.Constraint.PreparedSHA256, err = generationRepeatPreparedDigest(result.Prepared, "")
	if err != nil {
		return result, err
	}
	result.Constraint.FilesObserved = result.View.FilesObserved
	result.Constraint.FilesSHA256, err = generationRepeatFilesDigest(result.Prepared.FileObservations)
	if err != nil {
		return result, err
	}
	result.Constraint.AssetsSHA256 = generationRepeatAssetsDigest(assets)
	result.View.Quota, err = a.generationQuotaView(ctx, user.ID)
	result.View.QuotaKnown = err == nil
	return result, err
}

func generationRepeatDifferences(original *generationExecutionSnapshot, old generationForm, current generationPreparation, sourceID string) []generationRepeatDifference {
	differences := []generationRepeatDifference{}
	// Today's parser defaults are not evidence of a legacy job's resolved input.
	if original == nil {
		return differences
	}
	add := func(kind, label, before, after string) {
		if before != after {
			differences = append(differences, generationRepeatDifference{Kind: kind, Label: label, Before: before, After: after})
		}
	}
	technical := func(kind, label, before, after string) {
		if before != after {
			differences = append(differences, generationRepeatDifference{Kind: kind, Label: label, Before: before, After: after, Technical: true})
		}
	}
	if original != nil {
		definition, _ := json.Marshal(current.Definition)
		if generationEvidenceDigest(definition) != original.Workflow.DefinitionSHA256 {
			add("workflow", "Определение схемы", "Сохранённая схема", "Обновлённая схема")
			technical("workflow", "Контрольная сумма определения", original.Workflow.DefinitionSHA256, generationEvidenceDigest(definition))
		}
		if manifest, ok := workflowManifestForInput(current.Input); ok && manifest.Family == current.Input.ModelFamily {
			encoded, _ := json.Marshal(manifest)
			if generationEvidenceDigest(encoded) != original.Workflow.ManifestSHA256 {
				add("workflow", "Версия рецепта", original.Workflow.Version+" (сохранена)", manifest.Version+" (содержимое изменилось)")
				technical("workflow", "Контрольная сумма рецепта", original.Workflow.ManifestSHA256, generationEvidenceDigest(encoded))
			}
		} else if len(original.Workflow.Manifest) > 0 {
			add("workflow", "Версия рецепта", original.Workflow.Version, "Недоступна")
		}
		oldGraph, oldErr := generationRepeatGraph(original.Graph, sourceID)
		newGraph, _ := json.Marshal(current.Prompt)
		newGraph, newErr := generationRepeatGraph(newGraph, "")
		if oldErr == nil && newErr == nil && !bytes.Equal(oldGraph, newGraph) {
			var oldNodes, newNodes map[string]json.RawMessage
			_ = json.Unmarshal(oldGraph, &oldNodes)
			_ = json.Unmarshal(newGraph, &newNodes)
			keys := map[string]bool{}
			for key := range oldNodes {
				keys[key] = true
			}
			for key := range newNodes {
				keys[key] = true
			}
			ordered := []string{}
			for key := range keys {
				ordered = append(ordered, key)
			}
			sort.Strings(ordered)
			for _, key := range ordered {
				if !bytes.Equal(oldNodes[key], newNodes[key]) {
					differences = append(differences, generationRepeatDifference{Kind: "graph", Label: "Узел схемы " + key, Before: string(oldNodes[key]), After: string(newNodes[key]), Technical: true})
				}
			}
		}
	}
	labels := map[string]string{"ModelName": "Модель", "TextEncoder": "Текстовая модель", "VAE": "Декодер изображения", "AudioVAE": "Декодер аудио", "Lora": "Обязательная LoRA", "IdentityLora": "LoRA внешности", "LoraNames": "Стек LoRA", "LoraModel": "Вес LoRA модели", "LoraClip": "Вес LoRA текста", "Width": "Ширина базы", "Height": "Высота базы", "Seed": "Seed", "Steps": "Шаги", "CFG": "Следование описанию", "Denoise": "Сила изменения", "Sampler": "Метод генерации", "Scheduler": "Расписание шагов", "OutputMegapixels": "Размер результата, Мп", "VideoResolution": "База видео", "VideoDurationSeconds": "Длительность, с", "VideoMode": "Способ создания видео", "VideoSteps": "Шаги видео", "VideoRIFEMultiplier": "Множитель FPS", "VideoRTXScale": "Масштаб видео"}
	before, _ := json.Marshal(repeatExecutionInput(old))
	after, _ := json.Marshal(repeatExecutionInput(current.Input))
	var first, second map[string]json.RawMessage
	_ = json.Unmarshal(before, &first)
	_ = json.Unmarshal(after, &second)
	keys := []string{}
	for key := range second {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if bytes.Equal(first[key], second[key]) {
			continue
		}
		label, known := labels[key]
		if !known {
			label = "Дополнительный параметр: " + key
		}
		differences = append(differences, generationRepeatDifference{Kind: "parameter", Label: label, Field: key, Before: generationRepeatDisplayValue(first[key]), After: generationRepeatDisplayValue(second[key]), Technical: !known})
	}
	if original != nil && current.FileObservations != nil {
		saved := map[string]componentinspection.Observation{}
		if original.FileObservations != nil {
			for _, item := range original.FileObservations.Items {
				saved[item.Reference.Key()] = item
			}
		}
		for _, item := range current.FileObservations.Items {
			previous, ok := saved[item.Reference.Key()]
			if ok && previous.State == "verified" && previous.SHA256 != item.SHA256 {
				add("component", item.Name, "Файл исходного запуска", "Другой файл с тем же именем")
				technical("component", item.Name, previous.SHA256, item.SHA256)
			}
			delete(saved, item.Reference.Key())
		}
		keys := []string{}
		for key := range saved {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			add("component", saved[key].Name, "Использовался в исходной схеме", "Больше не используется")
			technical("component", saved[key].Name, saved[key].SHA256, "Больше не используется")
		}
	}
	return differences
}

func generationRepeatDisplayValue(raw json.RawMessage) string {
	var value string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}

func (a *App) validateGenerationRepeatDispatch(ctx context.Context, user *User, job domain.GenerationJob, payload generationSavedPayload, prepared generationPreparation) error {
	constraint := payload.Repeat
	if constraint == nil {
		return nil
	}
	if constraint.Version != 1 || payload.ExecutionInput == nil || !validGenerationRequestID(constraint.SourceID) || payload.ExecutionInput.Seed != job.Seed || payload.ExecutionInput.TemplateID != job.TemplateID || payload.ExecutionInput.PresetID != job.WorkflowID {
		return errGenerationRepeatDrift
	}
	current, err := generationRepeatPreparedDigest(prepared, "")
	if err != nil || current != constraint.PreparedSHA256 {
		return errGenerationRepeatDrift
	}
	if constraint.FilesObserved {
		files, err := generationRepeatFilesDigest(prepared.FileObservations)
		if err != nil {
			return err
		}
		if files == "" || files != constraint.FilesSHA256 {
			return errGenerationRepeatDrift
		}
	}
	assets, err := a.generationRepeatAssets(ctx, user, prepared.Input)
	if err != nil {
		return err
	}
	if generationRepeatAssetsDigest(assets) != constraint.AssetsSHA256 {
		return errGenerationRepeatDrift
	}
	return nil
}
