package gateway

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type workflowCompatibilityIssue struct {
	Level     string `json:"level"`
	Code      string `json:"code"`
	NodeID    string `json:"node_id,omitempty"`
	ClassType string `json:"class_type,omitempty"`
	InputName string `json:"input_name,omitempty"`
	Field     string `json:"field,omitempty"`
	Label     string `json:"label"`
	Message   string `json:"message"`
}

type workflowCompatibilityError struct {
	Issues []workflowCompatibilityIssue
}

func (err *workflowCompatibilityError) Error() string {
	if err == nil || len(err.Issues) == 0 {
		return "workflow несовместим с установленным ComfyUI"
	}
	return err.Issues[0].Message
}

func validateComfyPrompt(catalog comfySchemaCatalog, prompt map[string]any) []workflowCompatibilityIssue {
	issues := make([]workflowCompatibilityIssue, 0)
	if len(catalog.Nodes) == 0 {
		return []workflowCompatibilityIssue{{Level: "error", Code: "schema_unavailable", Label: "Каталог ComfyUI", Message: "Не удалось получить схему установленных нод ComfyUI."}}
	}
	nodeIDs := make([]string, 0, len(prompt))
	for nodeID := range prompt {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		node, ok := promptNode(prompt[nodeID])
		if !ok {
			issues = append(issues, workflowIssue("invalid_node", nodeID, "", "", "", "Workflow", "Нода "+nodeID+" имеет некорректную структуру."))
			continue
		}
		classType, _ := node["class_type"].(string)
		classType = strings.TrimSpace(classType)
		if classType == "" {
			issues = append(issues, workflowIssue("missing_class", nodeID, "", "", "", "Workflow", "У ноды "+nodeID+" отсутствует class_type."))
			continue
		}
		schema, exists := catalog.Nodes[classType]
		if !exists {
			label := compatibilityNodeLabel(classType, classType)
			issues = append(issues, workflowIssue("unknown_node", nodeID, classType, "", compatibilityField(classType, ""), label, fmt.Sprintf("Нода %s (%s) не установлена в ComfyUI.", label, classType)))
			continue
		}
		inputs, _ := node["inputs"].(map[string]any)
		if inputs == nil {
			inputs = map[string]any{}
		}
		for _, inputName := range sortedInputNames(schema.Required) {
			inputSchema := schema.Required[inputName]
			value, found := inputs[inputName]
			if !found {
				issues = append(issues, missingInputIssue(nodeID, schema, inputName))
				continue
			}
			issues = append(issues, validateComfyInput(catalog, prompt, nodeID, schema, inputSchema, inputName, value)...)
			issues = append(issues, validateDynamicInputs(catalog, prompt, nodeID, schema, inputSchema, inputName, value, inputs)...)
		}
		for _, inputName := range sortedInputNames(schema.Optional) {
			value, found := inputs[inputName]
			if !found {
				continue
			}
			inputSchema := schema.Optional[inputName]
			issues = append(issues, validateComfyInput(catalog, prompt, nodeID, schema, inputSchema, inputName, value)...)
			issues = append(issues, validateDynamicInputs(catalog, prompt, nodeID, schema, inputSchema, inputName, value, inputs)...)
		}
		for inputName, value := range inputs {
			if containsWorkflowPlaceholder(value) {
				label := compatibilityNodeLabel(classType, schema.DisplayName)
				issues = append(issues, workflowIssue("unresolved_placeholder", nodeID, classType, inputName, compatibilityField(classType, inputName), label+" · "+inputName, "В итоговом workflow остался незаполненный шаблон "+inputName+"."))
			}
			if _, known := schema.Required[inputName]; known {
				continue
			}
			if _, known := schema.Optional[inputName]; known {
				continue
			}
			if reference, ok := comfyPromptReference(value); ok {
				issues = append(issues, validatePromptReference(catalog, prompt, nodeID, schema, inputName, comfyInputSchema{}, reference)...)
			}
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].NodeID != issues[right].NodeID {
			return issues[left].NodeID < issues[right].NodeID
		}
		if issues[left].InputName != issues[right].InputName {
			return issues[left].InputName < issues[right].InputName
		}
		return issues[left].Code < issues[right].Code
	})
	return issues
}

type promptReference struct {
	NodeID      string
	OutputIndex int
}

func comfyPromptReference(value any) (promptReference, bool) {
	parts, ok := value.([]any)
	if !ok || len(parts) != 2 {
		return promptReference{}, false
	}
	nodeID, ok := parts[0].(string)
	if !ok || strings.TrimSpace(nodeID) == "" {
		return promptReference{}, false
	}
	index, ok := integerValue(parts[1])
	if !ok || index < 0 {
		return promptReference{}, false
	}
	return promptReference{NodeID: nodeID, OutputIndex: index}, true
}

func validateComfyInput(catalog comfySchemaCatalog, prompt map[string]any, nodeID string, node comfyNodeSchema, schema comfyInputSchema, inputName string, value any) []workflowCompatibilityIssue {
	if reference, ok := comfyPromptReference(value); ok {
		return validatePromptReference(catalog, prompt, nodeID, node, inputName, schema, reference)
	}
	label := compatibilityNodeLabel(node.ClassType, node.DisplayName) + " · " + compatibilityInputLabel(inputName)
	field := compatibilityField(node.ClassType, inputName)
	if len(schema.Choices) > 0 && !isRuntimeAssetChoice(node.ClassType, inputName) && !choiceContains(schema.Choices, value) {
		return []workflowCompatibilityIssue{workflowIssue("invalid_choice", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Параметр %s содержит недоступное значение %q.", compatibilityInputLabel(inputName), fmt.Sprint(value)))}
	}
	comboValueMatched := schema.Type == "COMBO" && len(schema.Choices) > 0 && choiceContains(schema.Choices, value)
	if !comboValueMatched && !comfyScalarTypeMatches(schema.Type, value) {
		return []workflowCompatibilityIssue{workflowIssue("invalid_type", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Параметр %s имеет неверный тип; ComfyUI ожидает %s.", compatibilityInputLabel(inputName), schema.Type))}
	}
	if number, ok := numericValue(value); ok {
		if schema.Min != nil && number < *schema.Min {
			return []workflowCompatibilityIssue{workflowIssue("below_minimum", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Параметр %s меньше допустимого значения %s.", compatibilityInputLabel(inputName), formatSchemaNumber(*schema.Min)))}
		}
		if schema.Max != nil && number > *schema.Max {
			return []workflowCompatibilityIssue{workflowIssue("above_maximum", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Параметр %s больше допустимого значения %s.", compatibilityInputLabel(inputName), formatSchemaNumber(*schema.Max)))}
		}
	}
	return nil
}

func isRuntimeAssetChoice(classType, inputName string) bool {
	switch classType + "." + inputName {
	case "LoadImage.image", "LoadAudio.audio", "VHS_LoadVideo.video":
		return true
	default:
		return false
	}
}

func validateDynamicInputs(catalog comfySchemaCatalog, prompt map[string]any, nodeID string, node comfyNodeSchema, schema comfyInputSchema, inputName string, selector any, inputs map[string]any) []workflowCompatibilityIssue {
	if len(schema.DynamicOptions) == 0 {
		return nil
	}
	selected, ok := selector.(string)
	if !ok {
		return nil
	}
	option, found := schema.DynamicOptions[selected]
	if !found {
		return nil
	}
	issues := make([]workflowCompatibilityIssue, 0)
	for _, nestedName := range sortedInputNames(option.Required) {
		fullName := inputName + "." + nestedName
		value, exists := inputs[fullName]
		if !exists {
			label := compatibilityNodeLabel(node.ClassType, node.DisplayName) + " · " + compatibilityInputLabel(fullName)
			issues = append(issues, workflowIssue("missing_dynamic_input", nodeID, node.ClassType, fullName, compatibilityField(node.ClassType, fullName), label, fmt.Sprintf("Для режима %q не передан обязательный параметр %s.", selected, compatibilityInputLabel(fullName))))
			continue
		}
		nested := option.Required[nestedName]
		issues = append(issues, validateComfyInput(catalog, prompt, nodeID, node, nested, fullName, value)...)
	}
	for _, nestedName := range sortedInputNames(option.Optional) {
		fullName := inputName + "." + nestedName
		if value, exists := inputs[fullName]; exists {
			issues = append(issues, validateComfyInput(catalog, prompt, nodeID, node, option.Optional[nestedName], fullName, value)...)
		}
	}
	return issues
}

func validatePromptReference(catalog comfySchemaCatalog, prompt map[string]any, nodeID string, node comfyNodeSchema, inputName string, input comfyInputSchema, reference promptReference) []workflowCompatibilityIssue {
	label := compatibilityNodeLabel(node.ClassType, node.DisplayName) + " · " + compatibilityInputLabel(inputName)
	field := compatibilityField(node.ClassType, inputName)
	sourceValue, exists := prompt[reference.NodeID]
	if !exists {
		return []workflowCompatibilityIssue{workflowIssue("missing_source_node", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Вход %s ссылается на отсутствующую ноду %s.", compatibilityInputLabel(inputName), reference.NodeID))}
	}
	sourceNode, ok := promptNode(sourceValue)
	if !ok {
		return []workflowCompatibilityIssue{workflowIssue("invalid_source_node", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Источник %s имеет некорректную структуру.", reference.NodeID))}
	}
	sourceClass, _ := sourceNode["class_type"].(string)
	sourceSchema, known := catalog.Nodes[sourceClass]
	if !known || len(sourceSchema.Outputs) == 0 {
		return nil
	}
	if reference.OutputIndex >= len(sourceSchema.Outputs) {
		return []workflowCompatibilityIssue{workflowIssue("invalid_output_index", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Вход %s использует выход %d ноды %s, у которой доступно выходов: %d.", compatibilityInputLabel(inputName), reference.OutputIndex, reference.NodeID, len(sourceSchema.Outputs)))}
	}
	if input.Type != "" && !comfyConnectionTypesMatch(input.Type, sourceSchema.Outputs[reference.OutputIndex]) {
		return []workflowCompatibilityIssue{workflowIssue("incompatible_connection", nodeID, node.ClassType, inputName, field, label, fmt.Sprintf("Вход %s ожидает %s, но нода %s отдаёт %s.", compatibilityInputLabel(inputName), input.Type, reference.NodeID, sourceSchema.Outputs[reference.OutputIndex]))}
	}
	return nil
}

func promptNode(value any) (map[string]any, bool) {
	node, ok := value.(map[string]any)
	return node, ok && node != nil
}

func sortedInputNames(values map[string]comfyInputSchema) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func missingInputIssue(nodeID string, node comfyNodeSchema, inputName string) workflowCompatibilityIssue {
	label := compatibilityNodeLabel(node.ClassType, node.DisplayName) + " · " + compatibilityInputLabel(inputName)
	return workflowIssue("missing_required_input", nodeID, node.ClassType, inputName, compatibilityField(node.ClassType, inputName), label, fmt.Sprintf("Нода %s не получила обязательный параметр %s.", compatibilityNodeLabel(node.ClassType, node.DisplayName), compatibilityInputLabel(inputName)))
}

func workflowIssue(code, nodeID, classType, inputName, field, label, message string) workflowCompatibilityIssue {
	return workflowCompatibilityIssue{Level: "error", Code: code, NodeID: nodeID, ClassType: classType, InputName: inputName, Field: field, Label: label, Message: message}
}

func choiceContains(choices []any, value any) bool {
	for _, choice := range choices {
		if choiceEqual(choice, value) {
			return true
		}
	}
	return false
}

func choiceEqual(left, right any) bool {
	if leftNumber, ok := numericValue(left); ok {
		rightNumber, rightOK := numericValue(right)
		return rightOK && math.Abs(leftNumber-rightNumber) < 1e-9
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func integerValue(value any) (int, bool) {
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < float64(math.MinInt) || number > float64(math.MaxInt) {
		return 0, false
	}
	return int(number), true
}

func comfyScalarTypeMatches(expected string, value any) bool {
	switch strings.ToUpper(expected) {
	case "INT":
		_, ok := integerValue(value)
		return ok
	case "FLOAT", "NUMBER":
		_, ok := numericValue(value)
		return ok
	case "BOOLEAN":
		_, ok := value.(bool)
		return ok
	case "STRING", "COMBO", "COMFY_DYNAMICCOMBO_V3":
		_, ok := value.(string)
		return ok
	default:
		return true
	}
}

func comfyConnectionTypesMatch(expected, actual string) bool {
	expected = strings.ToUpper(strings.TrimSpace(expected))
	actual = strings.ToUpper(strings.TrimSpace(actual))
	if expected == "" || actual == "" || expected == actual || expected == "*" || actual == "*" {
		return true
	}
	for _, wildcard := range []string{"ANY", "IO.ANY", "COMFY_MATCHTYPE_V3", "COMFY_MATCHTYPE"} {
		if expected == wildcard || actual == wildcard {
			return true
		}
	}
	if expected == "FLOAT" && actual == "INT" || expected == "NUMBER" && (actual == "INT" || actual == "FLOAT") {
		return true
	}
	for _, expectedCandidate := range strings.Split(expected, ",") {
		for _, actualCandidate := range strings.Split(actual, ",") {
			if strings.TrimSpace(expectedCandidate) == strings.TrimSpace(actualCandidate) {
				return true
			}
		}
	}
	return false
}

func containsWorkflowPlaceholder(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, "{{") || strings.Contains(typed, "}}")
	case []any:
		for _, item := range typed {
			if containsWorkflowPlaceholder(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsWorkflowPlaceholder(item) {
				return true
			}
		}
	}
	return false
}

func compatibilityNodeLabel(classType, displayName string) string {
	switch classType {
	case "RTXVideoSuperResolution":
		return "Финальный апскейл · RTX"
	case "RIFE VFI":
		return "Плавность движения · RIFE"
	case "LCColorMatch":
		return "Единая палитра · ColorMatch"
	case "ImageSharpenKJ":
		return "Финальная резкость"
	case "VHS_VideoCombine":
		return "Сохранение видео"
	case "MiniMaxH3ImageToVideo":
		return "MiniMax H3 · первый и последний кадр"
	case "MiniMaxH3ReferenceToVideo":
		return "MiniMax H3 · свободные референсы"
	case "UNETLoader", "CheckpointLoaderSimple", "CLIPLoader", "ClipLoaderGGUF", "VAELoader":
		return "Модель и компоненты"
	}
	if strings.TrimSpace(displayName) != "" {
		return strings.TrimSpace(displayName)
	}
	return classType
}

func compatibilityInputLabel(name string) string {
	labels := map[string]string{
		"resize_type": "режим изменения размера", "resize_type.scale": "масштаб", "resize_type.width": "ширина", "resize_type.height": "высота",
		"quality": "качество", "ckpt_name": "модель", "multiplier": "множитель кадров", "batch_size": "размер пакета",
		"method": "метод", "method.strength": "сила", "method.radius": "радиус", "method.threshold": "порог", "method.iterations": "итерации",
		"strength": "сила", "frame_rate": "частота кадров", "format": "формат", "images": "кадры", "image": "изображение",
		"unet_name": "модель", "clip_name": "текстовый энкодер", "vae_name": "VAE", "lora_name": "LoRA",
		"first_frame": "первый кадр", "last_frame": "последний кадр", "ref_image_size": "размер референсов",
	}
	if label := labels[name]; label != "" {
		return label
	}
	return name
}

func compatibilityField(classType, inputName string) string {
	switch classType {
	case "RTXVideoSuperResolution":
		if strings.Contains(inputName, "quality") {
			return "video_rtx_quality"
		}
		if strings.Contains(inputName, "scale") || inputName == "resize_type" {
			return "video_rtx_scale"
		}
		return "video_rtx_enabled"
	case "RIFE VFI":
		switch inputName {
		case "ckpt_name":
			return "video_rife_checkpoint"
		case "multiplier":
			return "video_rife_multiplier"
		case "batch_size":
			return "video_rife_batch_size"
		case "dtype":
			return "video_rife_dtype"
		default:
			return "video_rife_enabled"
		}
	case "LCColorMatch":
		if inputName == "method" {
			return "video_color_method"
		}
		if inputName == "strength" {
			return "video_color_strength"
		}
		return "video_color_match"
	case "ImageSharpenKJ":
		switch {
		case inputName == "method":
			return "video_sharpen_method"
		case strings.HasSuffix(inputName, ".strength"):
			return "video_sharpen_strength"
		case strings.HasSuffix(inputName, ".radius"):
			return "video_sharpen_radius"
		case strings.HasSuffix(inputName, ".threshold"):
			return "video_sharpen_threshold"
		case strings.HasSuffix(inputName, ".iterations"):
			return "video_sharpen_iterations"
		default:
			return "video_sharpen_enabled"
		}
	case "UNETLoader", "CheckpointLoaderSimple", "CLIPLoader", "ClipLoaderGGUF", "VAELoader":
		return "model"
	}
	return ""
}

func formatSchemaNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
