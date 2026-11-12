package gateway

import "strconv"

func qwen21TextWorkflowManifest() workflowManifest {
	parameters := qwen21WorkflowParameters()
	return workflowManifest{
		ID: "qwen-image21", Version: "1", DefinitionID: "text-to-image-qwen21", TemplateID: "text-to-image", Family: modelFamilyQwen21,
		Name: "Qwen Image 2.1", Description: "Изображение по тексту на Qwen Image 2.1 INT8.", ModelFamilies: []string{modelFamilyQwen21},
		Modes:           []workflowModeManifest{{ID: "text", Name: "Текст в изображение", Description: "Создание изображения по описанию.", Default: true, InputLimits: map[string]workflowLimit{"image": {Minimum: 0, Maximum: 0}}}},
		Parameters:      parameters,
		Branches:        qwen21LoraBranches(),
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "TextEncodeQwenImage21", "EmptyLatentImage", "KSampler", "VAEDecode", "SaveImage"},
		PromptAssistant: workflowAssistantManifest{Profile: "workflow-default", Allowed: true, Rules: []string{"Вернуть один цельный промт для генерации изображения."}},
		Output:          workflowOutputManifest{Kinds: []string{"image"}},
	}
}

func qwen21EditWorkflowManifest() workflowManifest {
	parameters := append(qwen21WorkflowParameters(),
		booleanWorkflowParameter("edit_use_custom_size", "EditUseCustomSize", "Задать размер вручную", "source", false),
	)
	fields := []string{"input_image", "input_image_2", "input_image_3", "input_image_4", "input_image_5",
		"input_image_6", "input_image_7", "input_image_8", "input_image_9", "input_image_10"}
	identifiers := make([]string, len(fields))
	for index := range identifiers {
		identifiers[index] = "<image" + strconv.Itoa(index+1) + ">"
	}
	return workflowManifest{
		ID: "qwen-image21-edit", Version: "1", DefinitionID: "image-to-image-qwen21", TemplateID: "image-to-image", Family: modelFamilyQwen21,
		Name: "Qwen 2.1", Description: "Редактирует первое фото; до девяти других фото служат референсами.", ModelFamilies: []string{modelFamilyQwen21}, ModelCapabilities: []string{"supports_image"},
		Modes: []workflowModeManifest{{ID: "edit", Name: "Фото и промт", Description: "Первое фото обязательно, остальные задают детали для изменения.", Default: true,
			InputLimits: map[string]workflowLimit{"image": {Minimum: 1, Maximum: 10}}}},
		Inputs: []workflowInputManifest{{ID: "image", Kind: "image", Name: "Фото", Description: "Первое фото изменяется; остальные задают образы и детали.",
			FormFields: fields, MimeTypes: []string{"image/png", "image/jpeg", "image/webp"}, MaxBytes: 32 << 20, Roles: standardImageReferenceRoles()}},
		Parameters:      parameters,
		Branches:        qwen21LoraBranches(),
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "LoadImage", "TextEncodeQwenImage21", "EmptyLatentImage", "KSampler", "VAEDecode", "SaveImage"},
		PromptAssistant: workflowAssistantManifest{Profile: "qwen21-edit", Allowed: true, VisionReferences: true,
			ReferenceIdentifiers: map[string][]string{"edit": identifiers},
			Rules:                []string{"Разобрать каждое загруженное фото и его роль.", "При нескольких фото обозначать источники только как <image1>, <image2> и далее; при одном фото не использовать нумерованный тег.", "Явно описать, что изменить в основном фото и что сохранить."}},
		Output: workflowOutputManifest{Kinds: []string{"image"}},
	}
}

func qwen21LoraBranches() []workflowBranchManifest {
	return []workflowBranchManifest{{ID: "loras", Name: "LoRA", Description: "Опциональные LoRA Qwen Image 2.1.", Conditions: []workflowCondition{{Field: "lora_count", Operator: "greater_than", Value: 0}}, RequiredClasses: []string{"LoraLoaderModelOnly"}, Order: 10}}
}

func qwen21WorkflowParameters() []workflowParameterManifest {
	parameters := commonImageWorkflowParameters(25, 1, 1)
	for index := range parameters {
		switch parameters[index].Name {
		case "aspect_ratio":
			parameters[index].Default = "1:1"
		case "output_megapixels":
			parameters[index].Default = 1.0
		case "dimension_multiple":
			parameters[index].Default = 32
		case "scheduler":
			parameters[index].Default = "simple"
		}
	}
	return parameters
}
