package gateway

func aiHubWorkflowManifest() workflowManifest {
	m := workflowManifest{
		ID: aiHubWorkflowID, DefinitionID: aiHubWorkflowID, TemplateID: "minimax-h3-video", Version: "13",
		Family: modelFamilyMiniMaxH3, ModelFamilies: []string{modelFamilyMiniMaxH3},
		Name: "AIHUB H3 + Photo Carousel", Description: "Видео по референсам и фотокарусель MiniMax H3 с озвучкой LTX.",
		ModeParameter: "aihub_mode",
		Modes: []workflowModeManifest{
			{ID: "video", Name: "По видео", Default: true, InputLimits: map[string]workflowLimit{"image": {0, 9}, "video": {1, 1}, "audio": {0, 1}}, RequiredClasses: []string{"VHS_LoadVideo", "GatewayAIHubReferenceFrames"}},
			{ID: "photo", Name: "По фото", InputLimits: map[string]workflowLimit{"image": {1, 9}, "video": {0, 0}, "audio": {0, 1}}},
			{ID: "carousel", Name: "Фотокарусель", InputLimits: map[string]workflowLimit{"image": {1, 5}, "video": {0, 1}, "audio": {0, 0}}, RequiredClasses: []string{"SaveImage"}},
		},
		Inputs:          miniMaxH3WorkflowManifest().Inputs,
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "ModelAttentionBackend", "MiniMaxH3ReferenceToVideo", "RandomNoise", "KSamplerSelect", "BasicScheduler", "BasicGuider", "SamplerCustomAdvanced", "VAEDecode"},
		PromptAssistant: workflowAssistantManifest{Profile: "minimax-h3-ref2va", Allowed: true, VisionReferences: true},
		Branches: []workflowBranchManifest{
			{ID: "reference_duration", Name: "Длина видеореференса", ToggleParameter: "aihub_match_duration", RequiredClasses: []string{"ImageFromBatch", "TrimAudioDuration"}, Order: 5},
			{ID: "ltx_audio", Name: "Озвучка LTX 2.5", Conditions: []workflowCondition{{Field: "aihub_audio_source", Operator: "equals", Value: "ltx"}}, RequiredClasses: []string{"LTXVAudioOnlyModel", "LTXVAudioOnlyEmptyVideoLatent", "LTXVEmptyLatentAudio", "LTXVAudioVAEDecode", "LTXVConditioning", "LTXVConcatAVLatent", "LTXVSeparateAVLatent", "ManualSigmas", "CFGGuider", "TrimAudioDuration"}, Order: 10},
			{ID: "gemma", Name: "Уточнение звука Gemma", ToggleParameter: "aihub_audio_enhance", RequiredClasses: []string{"TextGenerateLTX2Prompt"}, Order: 20},
			{ID: "t8", Name: "Синхронизация T8", ToggleParameter: "aihub_t8", RequiredClasses: []string{"MiniMaxH3AudioLatentControlT8"}, Order: 30},
			{ID: "voice", Name: "Замена голоса", ToggleParameter: "aihub_voice_conversion", RequiredClasses: []string{"LoadAudio", "SeedVCRun"}, Order: 40},
			{ID: "sam3", Name: "Маска SAM3", Conditions: []workflowCondition{{Field: "aihub_mask", Operator: "equals", Value: "sam3"}}, RequiredClasses: []string{"SAM3_Detect", "SAM3_VideoTrack", "SAM3_TrackToMask"}, Order: 50},
			{ID: "sapiens", Name: "Маска Sapiens2", Conditions: []workflowCondition{{Field: "aihub_mask", Operator: "equals", Value: "sapiens"}}, RequiredClasses: []string{"Sapiens2Loader", "Sapiens2Seg", "Sapiens2SegExtract"}, Order: 60},
			{ID: "rembg", Name: "Удаление фона", ToggleParameter: "aihub_remove_background", RequiredClasses: []string{"Image Rembg (Remove Background)"}, Order: 70},
			{ID: "rtx", Name: "RTX Super Resolution", ToggleParameter: "video_rtx_enabled", RequiredClasses: []string{"RTXVideoSuperResolution"}, Order: 80},
			{ID: "dlss5", Name: "DLSS 5", ToggleParameter: "aihub_dlss_enabled", RequiredClasses: []string{"DLSS5Settings", "DLSS5EnhanceImages", "GatewayAIHubEnhanceImages", "GatewayAIHubVideoStream"}, Order: 95},
			{ID: "dlss_fg", Name: "Плавность движения DLSS FG", ToggleParameter: "aihub_fg_enabled", RequiredClasses: []string{"RH_DLSS5FrameInterpolation", "GatewayAIHubVideoStream"}, Order: 90},
		},
		Output: workflowOutputManifest{Kinds: []string{"video", "image"}, Container: "mp4", VideoCodec: "h264", PixelFormat: "yuv420p", FrameRate: 24, GeneratedAudio: true},
	}
	m.Inputs[0].FormFields = []string{"input_image", "input_image_2", "input_image_3", "input_image_4", "input_image_5", "input_image_6", "input_image_7", "input_image_8", "input_image_9"}
	m.Inputs[0].Description = "До девяти фото для видео или до пяти фото для фотокарусели."
	m.Inputs[1].Name = "Образец голоса"
	m.Inputs[1].Description = "Голос для Seed-VC."
	m.Parameters = []workflowParameterManifest{
		enumWorkflowParameter("aihub_task", "AIHubTask", "Задача по видео", "aihub", "reference", option("reference", "Новая сцена"), option("replace_character", "Заменить персонажа"), option("replace_clothing", "Заменить одежду")),
		booleanWorkflowParameter("aihub_audio_reference", "AIHubAudioReference", "Передать звук в H3", "aihub", false),
		booleanWorkflowParameter("aihub_match_duration", "AIHubMatchDuration", "Как у видеореференса", "aihub", false),
		enumWorkflowParameter("aihub_mode", "AIHubMode", "Результат", "aihub", "video", option("video", "По видео"), option("photo", "По фото"), option("carousel", "Фотокарусель")),
		enumWorkflowParameter("aihub_audio_source", "AIHubAudioSource", "Звук", "aihub", "native", option("native", "MiniMax H3"), option("original", "Из видеореференса"), option("ltx", "LTX 2.5")),
		stringWorkflowParameter("aihub_audio_prompt", "AIHubAudioPrompt", "Описание звука", "aihub", "", 12000),
		stringWorkflowParameter("aihub_audio_negative", "AIHubAudioNegative", "Исключить из звука", "aihub", "", 4000),
		booleanWorkflowParameter("aihub_audio_enhance", "AIHubAudioEnhance", "Уточнить описание через Gemma", "aihub", false),
		booleanWorkflowParameter("aihub_t8", "AIHubT8", "Синхронизация T8", "aihub", false),
		booleanWorkflowParameter("aihub_voice_conversion", "AIHubVoiceConversion", "Заменить голос", "aihub", false),
		enumWorkflowParameter("aihub_mask", "AIHubMask", "Маска видео", "aihub", "none", option("none", "Без маски"), option("sam3", "SAM3"), option("sapiens", "Sapiens2")),
		stringWorkflowParameter("aihub_mask_prompt", "AIHubMaskPrompt", "Объект маски", "aihub", "person", 500),
		booleanWorkflowParameter("aihub_remove_background", "AIHubRemoveBackground", "Удалить фон первого фото", "aihub", false),
		booleanWorkflowParameter("aihub_half_resolution", "AIHubHalfResolution", "Половинная база видео", "aihub", false),
		booleanWorkflowParameter("aihub_dlss_enabled", "AIHubDLSSEnabled", "DLSS 5", "postprocessing", false),
		numberWorkflowParameter("aihub_dlss_strength", "AIHubDLSSStrength", "Сила DLSS", "postprocessing", 1.0, 0.1, 1, 0.05),
		booleanWorkflowParameter("aihub_fg_enabled", "AIHubFGEnabled", "Плавность движения DLSS FG", "postprocessing", false),
		enumWorkflowParameter("aihub_fg_multiplier", "AIHubFGMultiplier", "Частота кадров DLSS FG", "postprocessing", 2, option(2, "48 FPS"), option(3, "72 FPS"), option(4, "96 FPS")),
		integerWorkflowParameter("aihub_steps", "AIHubSteps", "Шаги видео", "aihub", 6, 6, 25, 1),
		integerWorkflowParameter("aihub_photo_frames", "AIHubPhotoFrames", "Кадры видеореференса", "aihub", 50, 22, 124, 1),
		integerWorkflowParameter("aihub_photo_stride", "AIHubPhotoStride", "Интервал отбора кадров", "aihub", 8, 1, 32, 1),
		integerWorkflowParameter("aihub_photo_skip", "AIHubPhotoSkip", "Пропустить первые кадры", "aihub", 5, 0, 20, 1),
		enumWorkflowParameter("aihub_carousel_pipeline", "AIHubCarouselPipeline", "Обработка карусели", "aihub", "legacy", option("legacy", "H3 · прежняя"), option("v2", "H3 + доработка · V2")),
		enumWorkflowParameter("aihub_carousel_refiner", "AIHubCarouselRefiner", "Модель доработки", "aihub", "flux", option("flux", "FLUX.2 Klein 9B"), option("qwen21", "Qwen Image 2.1")),
		enumWorkflowParameter("aihub_carousel_source", "AIHubCarouselSource", "Источник карусели", "aihub", "video", option("video", "Видеореференс"), option("prompt", "Промпт и фото")),
		integerWorkflowParameter("aihub_photo_count", "AIHubPhotoCount", "Количество фотографий", "aihub", 12, 1, 24, 1),
		numberWorkflowParameter("aihub_photo_randomness", "AIHubPhotoRandomness", "Случайность отбора", "aihub", 1, 0, 1, 0.1),
		numberWorkflowParameter("aihub_latent_scale", "AIHubLatentScale", "Масштаб фотокарусели", "aihub", 2, 1, 2, 0.05),
	}
	shared := map[string]bool{"video_quality": true, "video_aspect": true, "video_swap_dimensions": true, "video_reference_start": true, "video_reference_size": true, "video_rtx_enabled": true, "video_rtx_scale": true, "video_rtx_quality": true, "video_output_crf": true}
	for _, p := range miniMaxH3WorkflowParameters() {
		if shared[p.Name] {
			m.Parameters = append(m.Parameters, p)
		}
	}
	m.Parameters = append(m.Parameters, integerWorkflowParameter("video_duration_seconds", "VideoDurationSeconds", "Длительность", "video", 5, 5, 30, 1))
	return m
}
