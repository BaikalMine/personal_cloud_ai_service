package gateway

func krea2TextWorkflowManifest() workflowManifest {
	parameters := commonImageWorkflowParameters(25, 7, 1)
	parameters = append(parameters,
		numberWorkflowParameter("base_megapixels", "BaseMegapixels", "Базовое разрешение", "generation", 1, 0.5, 2, 0.05),
		integerWorkflowParameter("upscale_steps", "UpscaleSteps", "Шаги апскейла", "upscale", 5, 1, 12, 1),
		numberWorkflowParameter("upscale_denoise", "UpscaleDenoise", "Сила апскейла", "upscale", 0.18, 0.01, 0.5, 0.01),
		booleanWorkflowParameter("upscale_auto_denoise", "UpscaleAutoDenoise", "Автонастройка перерисовки", "upscale", true),
		enumWorkflowParameter("upscale_sampler", "UpscaleSampler", "Сэмплер апскейла", "upscale", "euler_ancestral", workflowSamplerOptions()...),
		booleanWorkflowParameter("krea_sage_enabled", "KreaSageEnabled", "SageAttention / Triton", "optimization", false),
		enumWorkflowParameter("krea_sage_mode", "KreaSageMode", "Режим SageAttention", "optimization", "auto",
			option("auto", "Auto"),
			option("sageattn_qk_int8_pv_fp16_cuda", "CUDA · FP16"),
			option("sageattn_qk_int8_pv_fp16_triton", "Triton · FP16"),
			option("sageattn_qk_int8_pv_fp8_cuda", "CUDA · FP8"),
			option("sageattn_qk_int8_pv_fp8_cuda++", "CUDA · FP8++"),
			option("sageattn3", "SageAttention 3"),
			option("sageattn3_per_block_mean", "SageAttention 3 · per-block mean")),
		booleanWorkflowParameter("krea_sage_allow_compile", "KreaSageAllowCompile", "Разрешить компиляцию", "optimization", true),
		booleanWorkflowParameter("krea_fp16_accumulation", "KreaFP16Accumulation", "FP16 accumulation", "optimization", true),
		booleanWorkflowParameter("detail_enabled", "DetailEnabled", "Финальная детализация", "detail", true),
		integerWorkflowParameter("detail_steps", "DetailSteps", "Шаги детализации", "detail", 2, 1, 8, 1),
		numberWorkflowParameter("detail_denoise", "DetailDenoise", "Сила детализации", "detail", 0.03, 0.01, 0.2, 0.005),
		numberWorkflowParameter("detail_cfg", "DetailCFG", "CFG детализации", "detail", 1, 0, 30, 0.1),
		enumWorkflowParameter("detail_sampler", "DetailSampler", "Сэмплер детализации", "detail", "euler", workflowSamplerOptions()...),
		enumWorkflowParameter("detail_scheduler", "DetailScheduler", "Планировщик детализации", "detail", "simple", workflowSchedulerOptions()...),
		booleanWorkflowParameter("color_transfer", "ColorTransfer", "Перенос цвета", "postprocessing", true),
		enumWorkflowParameter("color_method", "ColorMethod", "Метод переноса цвета", "postprocessing", "reinhard_lab", option("reinhard_lab", "Reinhard Lab"), option("mkl_lab", "MKL Lab"), option("histogram", "Histogram")),
		enumWorkflowParameter("color_mode", "ColorMode", "Режим переноса цвета", "postprocessing", "per_frame", option("per_frame", "По кадру"), option("uniform", "Единый")),
		numberWorkflowParameter("color_strength", "ColorStrength", "Сила переноса цвета", "postprocessing", 1, 0, 10, 0.05),
		booleanWorkflowParameter("image_filter_enabled", "ImageFilterEnabled", "Фильтр и уровни", "filter", false),
		numberWorkflowParameter("image_filter_brightness", "ImageFilterBrightness", "Яркость фильтра", "filter", 0, -1, 1, 0.01),
		numberWorkflowParameter("image_filter_contrast", "ImageFilterContrast", "Контраст фильтра", "filter", 1, -1, 2, 0.01),
		numberWorkflowParameter("image_filter_saturation", "ImageFilterSaturation", "Насыщенность фильтра", "filter", 1, 0, 5, 0.01),
		numberWorkflowParameter("image_filter_sharpness", "ImageFilterSharpness", "Резкость фильтра", "filter", 1, -5, 5, 0.01),
		integerWorkflowParameter("image_filter_blur", "ImageFilterBlur", "Blur фильтра", "filter", 0, 0, 16, 1),
		numberWorkflowParameter("image_filter_gaussian", "ImageFilterGaussian", "Gaussian blur", "filter", 0, 0, 1024, 0.1),
		numberWorkflowParameter("image_filter_edge", "ImageFilterEdge", "Усиление краёв", "filter", 0, 0, 1, 0.01),
		booleanWorkflowParameter("image_filter_detail", "ImageFilterDetail", "Усиление деталей", "filter", false),
		numberWorkflowParameter("image_level_black", "ImageLevelBlack", "Чёрная точка", "filter", 0, 0, 255, 0.1),
		numberWorkflowParameter("image_level_mid", "ImageLevelMid", "Средняя точка", "filter", 127.5, 0, 255, 0.1),
		numberWorkflowParameter("image_level_white", "ImageLevelWhite", "Белая точка", "filter", 255, 0, 255, 0.1),
	)
	return workflowManifest{
		ID: "photoflow-krea2", Version: "2", DefinitionID: "text-to-image-krea2", TemplateID: "text-to-image", Family: modelFamilyKrea2,
		Name: "PhotoFlow Krea2", Description: "Генерация Krea2 с апскейлом и независимыми ветками обработки PhotoFlow.", ModelFamilies: []string{modelFamilyKrea2},
		Modes:  []workflowModeManifest{{ID: "text", Name: "Текст в изображение", Description: "Изображение создаётся по промту без исходного фото.", Default: true, InputLimits: map[string]workflowLimit{"image": {Minimum: 0, Maximum: 0}}}},
		Inputs: nil, Parameters: parameters,
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "CLIPTextEncode", "EmptyLatentImage", "KSampler", "VAEDecodeTiled", "VAEEncodeTiled", "ImageScale", "SaveImage"},
		Branches: []workflowBranchManifest{
			{ID: "loras", Name: "LoRA", Description: "До десяти последовательных Krea2 LoRA.", Conditions: []workflowCondition{{Field: "lora_count", Operator: "greater_than", Value: 0}}, RequiredClasses: []string{"LoraLoader"}, Order: 10},
			{ID: "sage_attention", Name: "SageAttention / Triton", Description: "Опциональная оптимизация внимания и FP16 accumulation.", ToggleParameter: "krea_sage_enabled", RequiredClasses: []string{"PathchSageAttentionKJ", "ModelPatchTorchSettings"}, Order: 20},
			{ID: "detail", Name: "Финальная детализация", Description: "Короткий refinement-проход после апскейла.", ToggleParameter: "detail_enabled", RequiredClasses: []string{"KSampler"}, Order: 30},
			{ID: "color_transfer", Name: "Согласование цвета", Description: "Финальное согласование цвета с первым проходом.", ToggleParameter: "color_transfer", RequiredClasses: []string{"ColorTransfer"}, Order: 40},
			{ID: "image_filter", Name: "Фильтр и уровни", Description: "Опциональная коррекция изображения и уровней.", ToggleParameter: "image_filter_enabled", RequiredClasses: []string{"Image Filter Adjustments", "Image Levels Adjustment"}, Order: 50},
		},
		QualityProfiles: []workflowQualityProfileManifest{
			krea2TextQualityProfile("fast", "Быстро", "Быстрый черновой результат.", false, 0.75, 1, 3, 1, 0.02),
			krea2TextQualityProfile("balanced", "Баланс", "Рекомендуемый баланс качества и времени.", true, 1, 1.9, 5, 2, 0.03),
			krea2TextQualityProfile("krea-2-5", "2.5 МП", "Больше деталей без тяжёлого максимального прохода.", false, 1.1, 2.5, 6, 2, 0.03),
			krea2TextQualityProfile("maximum", "Максимум", "Высокое качество и более долгий апскейл.", false, 1.5, 4, 8, 3, 0.035),
		},
		PromptAssistant: workflowAssistantManifest{Profile: "workflow-default", Allowed: true, Rules: []string{"Вернуть один цельный позитивный промт.", "Не создавать negative prompt для Krea2 text-to-image."}},
		Output:          workflowOutputManifest{Kinds: []string{"image"}, Postprocessing: []string{"upscale", "detail", "color_transfer", "image_filter"}},
	}
}

func krea2EditWorkflowManifest() workflowManifest {
	parameters := commonImageWorkflowParameters(8, 1, 1)
	parameters = append(parameters, commonEditFrameParameters()...)
	parameters = append(parameters,
		numberWorkflowParameter("reference_boost", "ReferenceBoost", "Сила сохранения исходника", "conditioning", 4, 0, 8, 0.05),
		integerWorkflowParameter("grounding_pixels", "GroundingPixels", "Разрешение анализа фото", "conditioning", 768, 256, 2048, 64),
		numberWorkflowParameter("upscale_factor", "UpscaleFactor", "Коэффициент апскейла", "upscale", 1.5, 1, 2, 0.05),
		integerWorkflowParameter("upscale_steps", "UpscaleSteps", "Шаги апскейла", "upscale", 4, 1, 100, 1),
		numberWorkflowParameter("upscale_denoise", "UpscaleDenoise", "Сила апскейла", "upscale", 0.15, 0.01, 0.5, 0.01),
		numberWorkflowParameter("upscale_cfg", "UpscaleCFG", "CFG апскейла", "upscale", 1, 0, 30, 0.1),
		enumWorkflowParameter("upscale_sampler", "UpscaleSampler", "Сэмплер апскейла", "upscale", "deis", workflowSamplerOptions()...),
		enumWorkflowParameter("upscale_scheduler", "UpscaleScheduler", "Планировщик апскейла", "upscale", "simple", workflowSchedulerOptions()...),
		numberWorkflowParameter("post_denoise_blur", "PostDenoiseBlur", "Blur очистки", "postprocessing", 1, 0.001, 8, 0.01),
		numberWorkflowParameter("post_denoise_edge", "PostDenoiseEdge", "Сохранение краёв", "postprocessing", 0.05, 0.001, 0.25, 0.005),
		numberWorkflowParameter("post_denoise_radius", "PostDenoiseRadius", "Радиус очистки", "postprocessing", 1, 0, 3, 0.05),
		numberWorkflowParameter("post_denoise_strength", "PostDenoiseStrength", "Сила очистки", "postprocessing", 0.75, 0, 1, 0.05),
	)
	parameters = append(parameters, krea2SkinParameters()...)
	parameters = append(parameters, colorAdjustmentParameters()...)
	return workflowManifest{
		ID: "photoflow-krea2-edit", Version: "1.2", DefinitionID: "image-to-image-krea2", TemplateID: "image-to-image", Family: modelFamilyKrea2,
		Name: "Krea 2: редактирование", Description: "Сохраняет исходное фото и применяет инструкцию через Krea2 Turbo identity workflow.", ModelFamilies: []string{modelFamilyKrea2}, ModelCapabilities: []string{"supports_image"},
		Modes:  []workflowModeManifest{{ID: "edit", Name: "Фото и промт", Description: "Основное фото обязательно; второе фото служит дополнительным референсом.", Default: true, InputLimits: map[string]workflowLimit{"image": {Minimum: 1, Maximum: 2}}, RequiredClasses: []string{"Krea2EditModelPatch", "Krea2EditGroundedEncode"}}},
		Inputs: []workflowInputManifest{krea2EditImageInput()}, Parameters: parameters,
		RequiredClasses: []string{"UNETLoader", "CLIPLoader", "VAELoader", "LoadImage", "AspectRatioSimplifier", "VAEEncode", "KSampler", "VAEDecode", "SaveImage"},
		Branches: []workflowBranchManifest{
			{ID: "identity", Name: "Identity LoRA", Description: "Обязательная привязка внешности Krea2.", RequiredClasses: []string{"LoraLoaderModelOnly"}, Order: 10},
			{ID: "upscale", Name: "Ultimate SD Upscale", Description: "Финальный апскейл с выбранной силой.", RequiredClasses: []string{"UpscaleModelLoader", "UltimateSDUpscale"}, Order: 20},
			{ID: "cleanup", Name: "Очистка", Description: "Постобработка изображения после апскейла.", RequiredClasses: []string{"LCImageDenoise"}, Order: 30},
			{ID: "skin", Name: "Обработка кожи", Description: "Опциональная локальная коррекция кожи.", Conditions: []workflowCondition{{Field: "skin_strength", Operator: "greater_than", Value: 0}}, RequiredClasses: []string{"LCSkinBeauty"}, Order: 40},
			{ID: "tone", Name: "Тон и LUT", Description: "Финальная коррекция цвета и резкости.", ToggleParameter: "lut_enabled", RequiredClasses: []string{"LCImageAdjust", "LCApplyLUT"}, Order: 50},
		},
		QualityProfiles: []workflowQualityProfileManifest{{ID: "balanced", Name: "Баланс", Description: "Проверенный профиль identity edit.", Default: true, Parameters: map[string]workflowProfileParameterManifest{"steps": profileValue(8), "cfg": profileValue(1), "reference_boost": profileValue(4), "grounding_pixels": profileValue(768), "upscale_factor": profileValue(1.5), "upscale_steps": profileValue(4), "upscale_denoise": profileValue(0.15)}}},
		PromptAssistant: workflowAssistantManifest{Profile: "flux-edit", Allowed: true, VisionReferences: true, ReferenceIdentifiers: map[string][]string{"edit": {"<Picture 1>", "<Picture 2>"}}, Rules: []string{"Сначала проанализировать оба загруженных изображения.", "Явно разделить, что сохранить из основного фото и что перенести из второго референса."}},
		Output:          workflowOutputManifest{Kinds: []string{"image"}, Postprocessing: []string{"upscale", "cleanup", "skin", "tone", "lut"}},
	}
}

func flux2EditWorkflowManifest() workflowManifest {
	parameters := commonImageWorkflowParameters(25, 1, 0.9)
	parameters = append(parameters, commonEditFrameParameters()...)
	parameters = append(parameters,
		numberWorkflowParameter("source_megapixels", "SourceMegapixels", "Разрешение исходного фото", "source", 1, 0.25, 16, 0.05),
		numberWorkflowParameter("flux_guidance", "FluxGuidance", "Flux Guidance", "conditioning", 4, 0, 10, 0.05),
		integerWorkflowParameter("flux_detailer_steps", "FluxDetailerSteps", "Detailer-шаги", "conditioning", 25, 0, 100, 1),
		numberWorkflowParameter("flux_active_scale", "FluxActiveScale", "Active scale", "conditioning", 1, 0, 10, 0.05),
		numberWorkflowParameter("flux_token_whiten", "FluxTokenWhiten", "Token whiten", "conditioning", 0, -1, 5, 0.05),
		numberWorkflowParameter("flux_norm_equalize", "FluxNormEqualize", "Norm equalize", "conditioning", 0, 0, 1, 0.05),
		enumWorkflowParameter("flux_upscale_mode", "FluxUpscaleMode", "Апскейл", "postprocessing", "none", option("none", "Без апскейла"), option("ultimate", "Ultimate SD Upscale"), option("seedvr2", "SeedVR2"), option("both", "Оба этапа")),
	)
	parameters = append(parameters, colorAdjustmentParameters()...)
	return workflowManifest{
		ID: "photoflow-flux2-edit", Version: "1", DefinitionID: "image-to-image-flux2", TemplateID: "image-to-image", Family: modelFamilyFlux2,
		Name: "Flux2 Редактирование", Description: "Редактирование основного изображения и до трёх дополнительных референсов через Flux2.", ModelFamilies: []string{modelFamilyFlux2}, ModelCapabilities: []string{"supports_image"},
		Modes:  []workflowModeManifest{{ID: "edit", Name: "Фото и промт", Description: "Основной кадр и до трёх референсов с назначенными ролями.", Default: true, InputLimits: map[string]workflowLimit{"image": {Minimum: 1, Maximum: 4}}, RequiredClasses: []string{"LCReferenceLatent", "LCPipeEdit"}}},
		Inputs: []workflowInputManifest{flux2EditImageInput()}, Parameters: parameters,
		RequiredClasses: []string{"UNETLoader", "ClipLoaderGGUF", "VAELoader", "LoadImage", "VAEEncode", "FluxGuidance", "RandomNoise", "SamplerCustomAdvanced", "VAEDecode", "SaveImage"},
		Branches: []workflowBranchManifest{
			{ID: "loras", Name: "LoRA", Description: "Опциональный стек Flux2 LoRA.", Conditions: []workflowCondition{{Field: "lora_count", Operator: "greater_than", Value: 0}}, RequiredClasses: []string{"Power Lora Loader (rgthree)"}, Order: 10},
			{ID: "ultimate", Name: "Ultimate SD Upscale", Description: "Первый опциональный этап апскейла.", Conditions: []workflowCondition{{Field: "flux_upscale_mode", Operator: "one_of", Value: []string{"ultimate", "both"}}}, RequiredClasses: []string{"UpscaleModelLoader", "UltimateSDUpscale"}, Order: 30},
			{ID: "seedvr2", Name: "SeedVR2", Description: "Опциональный финальный нейросетевой апскейл.", Conditions: []workflowCondition{{Field: "flux_upscale_mode", Operator: "one_of", Value: []string{"seedvr2", "both"}}}, RequiredClasses: []string{"LCGetImage", "ComfyMathExpression", "SeedVR2LoadVAEModel", "SeedVR2LoadDiTModel", "SeedVR2VideoUpscaler"}, Order: 40},
			{ID: "lut", Name: "LUT", Description: "Опциональная цветокоррекция.", ToggleParameter: "lut_enabled", RequiredClasses: []string{"LCApplyLUT"}, Order: 50},
		},
		QualityProfiles: []workflowQualityProfileManifest{
			{ID: "fast", Name: "Быстро", Description: "Сокращённый проход Flux2.", Parameters: map[string]workflowProfileParameterManifest{"steps": profileValue(16), "cfg": profileValue(1), "denoise": profileValue(0.8), "source_megapixels": profileValue(1)}},
			{ID: "balanced", Name: "Баланс", Description: "Проверенный профиль Flux2 Edit.", Default: true, Parameters: map[string]workflowProfileParameterManifest{"steps": profileValue(25), "cfg": profileValue(1), "denoise": profileValue(0.9), "source_megapixels": profileValue(1), "max_longest_side": profileValue(2160)}},
			{ID: "maximum", Name: "Максимум", Description: "Больше шагов и разрешение исходных референсов.", Parameters: map[string]workflowProfileParameterManifest{"steps": profileValue(32), "cfg": profileValue(1), "denoise": profileValue(0.9), "source_megapixels": profileValue(2), "max_longest_side": profileValue(3072)}},
		},
		PromptAssistant: workflowAssistantManifest{Profile: "flux-edit", Allowed: true, VisionReferences: true, ReferenceIdentifiers: map[string][]string{"edit": {"<Picture 1>", "<Picture 2>", "<Picture 3>", "<Picture 4>"}}, Rules: []string{"Проанализировать все переданные изображения до переписывания промта.", "Сохранить назначенные пользователем роли внешности, одежды, предметов, позы, стиля и фона."}},
		Output:          workflowOutputManifest{Kinds: []string{"image"}, Postprocessing: []string{"ultimate", "seedvr2", "lut"}},
	}
}

func commonImageWorkflowParameters(defaultSteps int, defaultCFG, defaultDenoise float64) []workflowParameterManifest {
	return []workflowParameterManifest{
		stringWorkflowParameter("positive_prompt", "Positive", "Позитивный промт", "prompt", "", 4000),
		stringWorkflowParameter("negative_prompt", "Negative", "Негативный промт", "prompt", "", 4000),
		integerWorkflowParameter("width", "Width", "Ширина", "source", 1024, 16, 4096, 8),
		integerWorkflowParameter("height", "Height", "Высота", "source", 1024, 16, 4096, 8),
		enumWorkflowParameter("aspect_ratio", "AspectRatio", "Соотношение сторон", "source", "3:4", workflowAspectOptions()...),
		numberWorkflowParameter("output_megapixels", "OutputMegapixels", "Итоговое разрешение", "source", 1.9, 0.1, 16, 0.05),
		enumWorkflowParameter("dimension_multiple", "DimensionMultiple", "Кратность размера", "source", 16, option(8, "8"), option(16, "16"), option(32, "32"), option(64, "64")),
		integerWorkflowParameter("max_longest_side", "MaxLongestSide", "Ограничение длинной стороны", "source", 0, 0, 4096, 8),
		integerWorkflowParameter("steps", "Steps", "Шаги", "generation", defaultSteps, 1, 100, 1),
		numberWorkflowParameter("cfg", "CFG", "CFG", "generation", defaultCFG, 1, 30, 0.1),
		numberWorkflowParameter("denoise", "Denoise", "Сила изменения", "generation", defaultDenoise, 0.05, 1, 0.01),
		enumWorkflowParameter("sampler", "Sampler", "Сэмплер", "generation", "euler", workflowSamplerOptions()...),
		enumWorkflowParameter("scheduler", "Scheduler", "Планировщик", "generation", "normal", workflowSchedulerOptions()...),
		integerWorkflowParameter("seed", "Seed", "Seed", "generation", -1, -1, 9_000_000_000_000_000, 1),
	}
}

func commonEditFrameParameters() []workflowParameterManifest {
	return []workflowParameterManifest{
		booleanWorkflowParameter("preserve_original_size", "PreserveOriginalSize", "Сохранить размер исходника", "source", true),
		booleanWorkflowParameter("edit_use_custom_size", "EditUseCustomSize", "Настроить кадр вручную", "source", false),
		enumWorkflowParameter("edit_aspect_preset", "EditAspectPreset", "Формат кадра", "source", "custom", workflowEditAspectOptions()...),
		booleanWorkflowParameter("edit_swap_dimensions", "EditSwapDimensions", "Поменять ширину и высоту", "source", false),
		enumWorkflowParameter("edit_resize_method", "EditResizeMethod", "Масштабирование", "source", "lanczos", option("nearest-exact", "Nearest exact"), option("bicubic", "Bicubic"), option("bilinear", "Bilinear"), option("lanczos", "Lanczos"), option("area", "Area")),
		enumWorkflowParameter("edit_proportion", "EditProportion", "Вписывание", "source", "crop", option("crop", "Обрезать"), option("stretch", "Растянуть"), option("resize", "Вписать"), option("pad", "Поля"), option("total_pixels", "Бюджет пикселей")),
		enumWorkflowParameter("edit_crop_location", "EditCropLocation", "Позиция обрезки", "source", "center", option("center", "По центру"), option("top", "Сверху"), option("bottom", "Снизу"), option("left", "Слева"), option("right", "Справа")),
		stringWorkflowParameter("edit_pad_color", "EditPadColor", "Цвет полей", "source", "0, 0, 0", 32),
	}
}

func colorAdjustmentParameters() []workflowParameterManifest {
	return []workflowParameterManifest{
		numberWorkflowParameter("adjust_hue", "AdjustHue", "Оттенок", "tone", 0, -1, 1, 0.01),
		numberWorkflowParameter("adjust_saturation", "AdjustSaturation", "Насыщенность", "tone", 0, -1, 1, 0.01),
		numberWorkflowParameter("adjust_brightness", "AdjustBrightness", "Яркость", "tone", 0, -1, 1, 0.01),
		numberWorkflowParameter("adjust_contrast", "AdjustContrast", "Контраст", "tone", 0, -1, 1, 0.01),
		numberWorkflowParameter("adjust_sharpness", "AdjustSharpness", "Резкость", "tone", 0, -1, 1, 0.01),
		booleanWorkflowParameter("lut_enabled", "LUTEnabled", "Применить LUT", "tone", false),
		enumWorkflowParameter("lut_name", "LUTName", "LUT", "tone", "LC_Crushed_Blacks.cube", option("LC_Crushed_Blacks.cube", "Crushed Blacks"), option("LC Highlights_Protection.cube", "Highlights Protection"), option("Cool_Natural_Breeze.cube", "Cool Natural Breeze"), option("street.cube", "Street")),
		numberWorkflowParameter("lut_strength", "LUTStrength", "Сила LUT", "tone", 0.23, 0, 1, 0.01),
	}
}

func krea2SkinParameters() []workflowParameterManifest {
	return []workflowParameterManifest{
		enumWorkflowParameter("skin_preset", "SkinPreset", "Профиль кожи", "skin", "Natural", option("Natural", "Natural"), option("Light", "Light"), option("Fresh", "Fresh"), option("Porcelain", "Porcelain"), option("Warm keep", "Warm keep"), option("Custom", "Custom")),
		numberWorkflowParameter("skin_strength", "SkinStrength", "Сила обработки кожи", "skin", 1, 0, 2, 0.01),
		numberWorkflowParameter("skin_coolness", "SkinCoolness", "Холодность кожи", "skin", 0.22, 0, 1, 0.01),
		numberWorkflowParameter("skin_brightness", "SkinBrightness", "Яркость кожи", "skin", 0.12, 0, 1, 0.01),
		numberWorkflowParameter("skin_rosy", "SkinRosy", "Розовый тон", "skin", 0.08, -0.3, 0.5, 0.01),
		numberWorkflowParameter("skin_evenness", "SkinEvenness", "Ровность кожи", "skin", 0.18, 0, 1, 0.01),
		numberWorkflowParameter("skin_shadow_lift", "SkinShadowLift", "Осветление теней", "skin", 0.15, 0, 1, 0.01),
		numberWorkflowParameter("skin_smooth", "SkinSmooth", "Сглаживание", "skin", 0.06, 0, 1, 0.01),
		numberWorkflowParameter("skin_texture_preserve", "SkinTexturePreserve", "Сохранение текстуры", "skin", 0.88, 0, 1, 0.01),
		numberWorkflowParameter("skin_saturation", "SkinSaturation", "Насыщенность кожи", "skin", -0.08, -0.5, 0.5, 0.01),
		numberWorkflowParameter("skin_highlight_protect", "SkinHighlightProtect", "Защита светов", "skin", 0.75, 0, 1, 0.01),
		numberWorkflowParameter("skin_mask_sensitivity", "SkinMaskSensitivity", "Чувствительность маски", "skin", 0.55, 0, 1, 0.01),
		numberWorkflowParameter("skin_mask_feather", "SkinMaskFeather", "Растушёвка маски", "skin", 0.45, 0, 1, 0.01),
	}
}

func krea2TextQualityProfile(id, name, description string, selected bool, base, output float64, upscaleSteps, detailSteps int, detailDenoise float64) workflowQualityProfileManifest {
	return workflowQualityProfileManifest{ID: id, Name: name, Description: description, Default: selected, Parameters: map[string]workflowProfileParameterManifest{"base_megapixels": profileValue(base), "output_megapixels": profileValue(output), "upscale_steps": profileValue(upscaleSteps), "detail_steps": profileValue(detailSteps), "detail_denoise": profileValue(detailDenoise)}}
}

func krea2EditImageInput() workflowInputManifest {
	return workflowInputManifest{ID: "image", Kind: "image", Name: "Исходник и референс", Description: "Первое фото задаёт исходник; второе добавляет внешность, объект, позу, стиль или фон.", FormFields: []string{"input_image", "input_image_2"}, MimeTypes: []string{"image/png", "image/jpeg", "image/webp"}, MaxBytes: 32 << 20, Roles: standardImageReferenceRoles()}
}

func flux2EditImageInput() workflowInputManifest {
	return workflowInputManifest{ID: "image", Kind: "image", Name: "Исходник и референсы", Description: "Основной кадр и до трёх дополнительных референсов.", FormFields: []string{"input_image", "input_image_2", "input_image_3", "input_image_4"}, MimeTypes: []string{"image/png", "image/jpeg", "image/webp"}, MaxBytes: 32 << 20, Roles: standardImageReferenceRoles()}
}

func standardImageReferenceRoles() []workflowInputRoleManifest {
	return []workflowInputRoleManifest{
		{ID: "base_scene", Name: "Основной кадр и композиция", Description: "Сцена, композиция и главный визуальный якорь."},
		{ID: "identity", Name: "Внешность и лицо", Description: "Лицо, волосы и постоянные черты персонажа."},
		{ID: "wardrobe_object", Name: "Одежда, предмет или материал", Description: "Одежда, аксессуар, предмет или фактура."},
		{ID: "pose_composition", Name: "Поза и ракурс", Description: "Положение тела, кадрирование и камера."},
		{ID: "style", Name: "Стиль, свет и цвет", Description: "Визуальная подача, свет, палитра и фактура."},
		{ID: "background", Name: "Фон и окружение", Description: "Локация, декорации и среда."},
		{ID: "details", Name: "Текст и мелкие детали", Description: "Надписи, небольшие объекты и точные признаки."},
	}
}

func workflowAspectOptions() []workflowOption {
	return []workflowOption{option("custom", "Вручную"), option("1:1", "1:1"), option("4:5", "4:5"), option("16:9", "16:9"), option("9:16", "9:16"), option("2:3", "2:3"), option("3:2", "3:2"), option("3:4", "3:4"), option("4:3", "4:3"), option("21:9", "21:9"), option("4:1", "4:1")}
}

func workflowEditAspectOptions() []workflowOption {
	return []workflowOption{option("custom", "Исходные пропорции"), option("Instagram Portrait - 1080x1350", "Instagram Portrait"), option("Instagram Square - 1080x1080", "Instagram Square"), option("Instagram Landscape - 1080x608", "Instagram Landscape"), option("Instagram Stories/Reels - 1080x1920", "Stories / Reels"), option("3:4 - 896x1152", "3:4"), option("4:3 - 1152x896", "4:3"), option("9:16 - 768x1344", "9:16"), option("16:9 - 1344x768", "16:9")}
}

func workflowSamplerOptions() []workflowOption {
	return []workflowOption{option("euler", "Euler"), option("euler_ancestral", "Euler ancestral"), option("dpmpp_2m", "DPM++ 2M"), option("dpmpp_2m_sde", "DPM++ 2M SDE"), option("heun", "Heun"), option("deis", "DEIS"), option("res_multistep", "Res Multistep"), option("gradient_estimation", "Gradient Estimation"), option("er_sde", "ER SDE")}
}

func workflowSchedulerOptions() []workflowOption {
	return []workflowOption{option("normal", "Normal"), option("simple", "Simple"), option("beta", "Beta"), option("karras", "Karras"), option("sgm_uniform", "SGM Uniform"), option("exponential", "Exponential"), option("ddim_uniform", "DDIM Uniform"), option("kl_optimal", "KL Optimal")}
}
