package gateway

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const kreaProPresetID = "krea2-pro"
const kreaProDefinitionID = "text-to-image-krea2-pro"
const kreaProMaxOutputSide = 12288
const kreaProMaxOutputPixels = qwen21MaxOutputPixels * 9

func kreaProFinalOutputs(outputs map[string]json.RawMessage) map[string]json.RawMessage {
	final := map[string]json.RawMessage{}
	if raw, ok := outputs["4071"]; ok {
		final["4071"] = raw
	}
	return final
}

//go:embed krea_pro_graphs/*.json
var kreaProFiles embed.FS

type kreaProField struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Choices []any    `json:"choices"`
	Min     *float64 `json:"min"`
	Max     *float64 `json:"max"`
}
type kreaProOption struct {
	ID      string         `json:"id"`
	Toggle  bool           `json:"toggle"`
	Enabled bool           `json:"enabled"`
	Fields  []kreaProField `json:"fields"`
}
type kreaProSettings struct {
	Enabled map[string]bool           `json:"enabled"`
	Nodes   map[string]map[string]any `json:"nodes"`
}

func kreaProOptions() []kreaProOption {
	data, _ := kreaProFiles.ReadFile("krea_pro_graphs/options.json")
	var result []kreaProOption
	_ = json.Unmarshal(data, &result)
	return result
}

func parseKreaProSettings(raw string) (kreaProSettings, error) {
	return parseKreaProSettingsOptions(raw, kreaProOptions())
}

func parseKreaProSettingsOptions(raw string, available []kreaProOption) (kreaProSettings, error) {
	settings := kreaProSettings{Enabled: map[string]bool{}, Nodes: map[string]map[string]any{}}
	for _, option := range available {
		if option.Toggle {
			settings.Enabled[option.ID] = option.Enabled
		}
	}
	if raw == "" {
		return settings, nil
	}
	if len(raw) > 32768 {
		return settings, errors.New("Ультра: слишком много дополнительных параметров")
	}
	var supplied kreaProSettings
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&supplied); err != nil {
		return settings, errors.New("Ультра: некорректные дополнительные параметры")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return settings, errors.New("Ультра: некорректные дополнительные параметры")
	}
	options := map[string]kreaProOption{}
	for _, option := range available {
		options[option.ID] = option
	}
	for id, enabled := range supplied.Enabled {
		option, ok := options[id]
		if !ok || !option.Toggle {
			return settings, errors.New("Ультра: неизвестная ветка обработки")
		}
		settings.Enabled[id] = enabled
	}
	for id, fields := range supplied.Nodes {
		option, ok := options[id]
		if !ok && id == "1292" {
			option, ok = options["4102"]
		}
		if !ok {
			return settings, errors.New("Ультра: неизвестный блок настроек")
		}
		allowed := map[string]kreaProField{}
		for _, field := range option.Fields {
			allowed[field.Name] = field
		}
		for name, value := range fields {
			field, ok := allowed[name]
			if !ok {
				return settings, errors.New("Ультра: неизвестный параметр")
			}
			valid := false
			switch field.Type {
			case "COMBO":
				switch value.(type) {
				case string, float64, bool:
					for _, choice := range field.Choices {
						if value == choice {
							valid = true
						}
					}
				}
			case "INT", "FLOAT":
				n, ok := value.(float64)
				valid = ok && !math.IsNaN(n) && !math.IsInf(n, 0) && (field.Type != "INT" || n == math.Trunc(n)) && (field.Min == nil || n >= *field.Min) && (field.Max == nil || n <= *field.Max)
			case "BOOLEAN":
				_, valid = value.(bool)
			case "STRING":
				text, ok := value.(string)
				valid = ok && len(text) <= 8000 && !strings.ContainsRune(text, 0)
				if valid && (strings.Contains(name, "positive") || name == "prompt" || name == "text") {
					valid = validateGenerationPrompt(text) == nil
				}
			}
			if !valid {
				return settings, fmt.Errorf("Ультра: некорректное значение %s", name)
			}
		}
		settings.Nodes[id] = fields
	}
	return settings, nil
}

func kreaProWorkflowManifest() workflowManifest {
	manifest := kreaUltraWorkflowManifest()
	manifest.ID, manifest.DefinitionID, manifest.Version = kreaProPresetID, kreaProDefinitionID, "29.0.1-full.1"
	manifest.Name = "Krea2 · Ультра качество · полный"
	manifest.Description = "Полный Lonecat: референсы, AnyPaint, VOSR2 или RelayVSR, SkinMaster и постобработка."
	manifest.ModeParameter = "krea_pro_mode"
	manifest.Modes = []workflowModeManifest{}
	for _, mode := range []struct {
		id, label string
		min, max  int
	}{
		{"text", "Текст в изображение", 0, 0}, {"references", "Фото и промт", 1, 4},
		{"identity", "Сохранить внешность", 1, 2}, {"control", "Depth ControlNet", 1, 1},
		{"inpaint", "Изменить область · маска в Фото 2", 2, 2}, {"outpaint", "Расширить кадр", 1, 1},
	} {
		manifest.Modes = append(manifest.Modes, workflowModeManifest{ID: mode.id, Name: mode.label, Default: mode.id == "text", InputLimits: map[string]workflowLimit{"image": {Minimum: mode.min, Maximum: mode.max}}})
	}
	manifest.Inputs = []workflowInputManifest{{ID: "image", Kind: "image", Name: "Фото", FormFields: []string{"input_image", "input_image_2", "input_image_3", "input_image_4"}, Roles: standardImageReferenceRoles(), MimeTypes: []string{"image/png", "image/jpeg", "image/webp"}, MaxBytes: 32 << 20}}
	manifest.Parameters = append(manifest.Parameters,
		booleanWorkflowParameter("krea_pro_match_source", "KreaProMatchSource", "Пропорции исходного фото", "source", true),
		enumWorkflowParameter("krea_pro_mode", "KreaProMode", "Задача", "source", "text", option("text", "Текст в изображение"), option("references", "Фото и промт"), option("identity", "Сохранить внешность"), option("control", "Depth ControlNet"), option("inpaint", "Изменить область"), option("outpaint", "Расширить кадр")),
		enumWorkflowParameter("krea_pro_upscale", "KreaProUpscale", "Апскейл VOSR2", "upscale", 2, option(1, "1×"), option(2, "2×"), option(3, "3×")),
		enumWorkflowParameter("krea_pro_upscaler", "KreaProUpscaler", "Апскейлер", "upscale", nil, option("none", "Без апскейла"), option("vosr", "VOSR2"), option("relay", "RelayVSR")),
		enumWorkflowParameter("krea_pro_relay_scale", "KreaProRelayScale", "Масштаб RelayVSR", "upscale", 2, option(2, "2×"), option(4, "4×")),
		stringWorkflowParameter("krea_pro_settings", "KreaProSettings", "Дополнительные настройки", "processing", "", 32768),
	)
	classes, _ := kreaProFiles.ReadFile("krea_pro_graphs/classes.json")
	_ = json.Unmarshal(classes, &manifest.RequiredClasses)
	// Relay is opt-in; its absence must not hide the VOSR/native workflow.
	for i := 0; i < len(manifest.RequiredClasses); {
		if manifest.RequiredClasses[i] == "LCRelayVSRUpscale" {
			manifest.RequiredClasses = append(manifest.RequiredClasses[:i], manifest.RequiredClasses[i+1:]...)
		} else {
			i++
		}
	}
	manifest.Output.Postprocessing = []string{"vosr2", "relayvsr", "skin_master", "sam3_detailers", "anypaint", "depth_control", "local_effects"}
	return manifest
}

func normalizeKreaPro(input *generationForm) error {
	if input.KreaProMode == "" {
		input.KreaProMode = "text"
	}
	if input.KreaProUpscale == 0 {
		input.KreaProUpscale = 2
	}
	if err := normalizeKreaUltra(input); err != nil {
		return err
	}
	settings, err := parseKreaProSettings(input.KreaProSettings)
	if err != nil {
		return err
	}
	upscaler, scale, err := kreaProUpscaler(*input, settings)
	if err != nil {
		return err
	}
	input.KreaProUpscaler = upscaler
	if input.KreaProRelayScale == 0 {
		input.KreaProRelayScale = 2
	}
	missing := false
	for _, image := range append([]string{input.InputImage}, input.ReferenceImages[:]...) {
		if image == "" {
			missing = true
		} else if missing {
			return errors.New("добавляйте фотографии по порядку, без пустых слотов")
		}
	}
	if input.KreaProMode == "text" && (settings.Enabled["3646"] || settings.Enabled["3621"]) {
		return errors.New("для переноса цвета или водяного знака добавьте фото")
	}
	if settings.Enabled["3621"] && input.ReferenceImages[2] == "" {
		return errors.New("для водяного знака добавьте изображение в Фото 4")
	}
	for index, id := range []string{"3680", "3681", "3682", "3683"} {
		if settings.Enabled[id] && input.imageCount() <= index {
			return fmt.Errorf("для удаления фона добавьте Фото %d", index+1)
		}
	}
	if input.KreaProMode == "inpaint" && (settings.Enabled["3621"] || settings.Enabled["3646"]) {
		return errors.New("маска в Фото 2 не используется как цветовой референс или водяной знак")
	}
	if err := kreaProValidateOutput(*input, settings, scale); err != nil {
		return err
	}
	return validateWorkflowManifestInput(kreaProWorkflowManifest(), *input)
}

func kreaProOutputDimensions(width, height, scale int) (int, int, error) {
	if width <= 0 || height <= 0 || scale < 1 || scale > 4 || width > kreaProMaxOutputSide/scale || height > kreaProMaxOutputSide/scale {
		return 0, 0, errors.New("Ультра: итоговый размер превышает 12288 пикселей по стороне или 38,43 Мп")
	}
	w, h := width*scale, height*scale
	if int64(w)*int64(h) > int64(kreaProMaxOutputPixels) {
		return 0, 0, errors.New("Ультра: итоговый размер превышает 12288 пикселей по стороне или 38,43 Мп")
	}
	return w, h, nil
}

func kreaProValidateOutput(input generationForm, settings kreaProSettings, scale int) error {
	w, h := input.Width, input.Height
	if input.KreaProMode == "outpaint" {
		var err error
		w, h, _, err = kreaProOutpaintFrame(w, h, settings)
		if err != nil {
			return err
		}
	}
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 || int64(w)*int64(h) > qwen21MaxOutputPixels {
		return errors.New("Ультра: базовый или расширенный кадр превышает 4096 пикселей по стороне или 4,27 Мп")
	}
	_, _, err := kreaProOutputDimensions(w, h, scale)
	return err
}

func kreaProUpscaler(input generationForm, settings kreaProSettings) (string, int, error) {
	scale, relayScale := input.KreaProUpscale, input.KreaProRelayScale
	if scale == 0 {
		scale = 2
	}
	if relayScale == 0 {
		relayScale = 2
	}
	if scale < 1 || scale > 3 || (relayScale != 2 && relayScale != 4) {
		return "", 0, errors.New("Ультра: некорректный масштаб апскейла")
	}
	kind := input.KreaProUpscaler
	if kind == "" {
		switch {
		case scale == 1:
			kind = "none"
		case settings.Enabled["4150"]:
			kind = "relay"
		case settings.Enabled["4046"]:
			kind = "vosr"
		default:
			kind = "none"
		}
	}
	switch kind {
	case "none":
		return kind, 1, nil
	case "vosr":
		if scale == 1 {
			return "none", 1, nil
		}
		return kind, scale, nil
	case "relay":
		return kind, relayScale, nil
	default:
		return "", 0, errors.New("Ультра: неизвестный апскейлер")
	}
}

func kreaProPanelValues(graph map[string]any, id string, values map[int]any) error {
	raw, ok := graph[id].(map[string]any)
	if !ok {
		return nil
	}
	fields := raw["inputs"].(map[string]any)
	var rows []map[string]any
	encoded, ok := fields["rows"].(string)
	if !ok || json.Unmarshal([]byte(encoded), &rows) != nil {
		return fmt.Errorf("Ультра: некорректная панель %s", id)
	}
	for index, value := range values {
		if index < 0 || index >= len(rows) {
			return fmt.Errorf("Ультра: отсутствует параметр панели %s", id)
		}
		rows[index]["value"] = value
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	fields["rows"] = string(data)
	return nil
}

// Mirror LC Image Rotate + Pad's percentage-based frame, including snap rounding.
func kreaProOutpaintFrame(width, height int, settings kreaProSettings) (int, int, map[string]any, error) {
	values := map[string]any{"left": float64(10), "right": float64(10), "top": float64(0), "bottom": float64(0), "angle": float64(0), "fit": "expand", "aspect": "free", "snap_to": "16"}
	for name, value := range settings.Nodes["3870"] {
		values[name] = value
	}
	number := func(name string) float64 { return values[name].(float64) }
	w, h := float64(width), float64(height)
	a := number("angle") * math.Pi / 180
	c, s := math.Abs(math.Cos(a)), math.Abs(math.Sin(a))
	bw, bh := w*c+h*s, w*s+h*c
	if values["fit"] == "crop" {
		k := math.Min(w/bw, h/bh)
		margin := float64(0)
		if math.Abs(number("angle")/90-math.RoundToEven(number("angle")/90)) >= 1e-9 {
			margin = 2
		}
		bw, bh = math.Max(1, w*k-margin), math.Max(1, h*k-margin)
	}
	ow, oh := math.Max(16, bw+(number("left")+number("right"))*w/100), math.Max(16, bh+(number("top")+number("bottom"))*h/100)
	ratio := float64(0)
	if values["aspect"] == "original" {
		ratio = w / h
	} else if parts := strings.Split(fmt.Sprint(values["aspect"]), ":"); len(parts) == 2 {
		a, _ := strconv.ParseFloat(parts[0], 64)
		b, _ := strconv.ParseFloat(parts[1], 64)
		if b > 0 {
			ratio = a / b
		}
	}
	if ratio > 0 {
		if ow/oh > ratio {
			delta := ow/ratio - oh
			values["bottom"] = number("bottom") + delta/h*100
			oh += delta
		} else {
			delta := oh*ratio - ow
			values["right"] = number("right") + delta/w*100
			ow += delta
		}
		if number("right") > 400 || number("bottom") > 400 {
			return 0, 0, values, errors.New("Ультра: слишком большое расширение для выбранного формата")
		}
	}
	snap, _ := strconv.Atoi(fmt.Sprint(values["snap_to"]))
	if snap < 1 {
		snap = 16
	}
	outW, outH := int(math.RoundToEven(ow)), int(math.RoundToEven(oh))
	outW += (snap - outW%snap) % snap
	outH += (snap - outH%snap) % snap
	return outW, outH, values, nil
}

func buildKreaProPrompt(input generationForm) (map[string]any, error) {
	settings, err := parseKreaProSettings(input.KreaProSettings)
	if err != nil {
		return nil, err
	}
	mode := input.KreaProMode
	if mode == "references" && input.imageCount() == 1 {
		mode = "reference"
	} else if (mode == "references" && input.imageCount() < 4 || mode == "identity" || mode == "control") && input.imageCount() > 1 {
		mode += fmt.Sprintf("-%d", input.imageCount())
	}
	data, err := kreaProFiles.ReadFile("krea_pro_graphs/" + mode + ".json")
	if err != nil {
		return nil, errors.New("Ультра: неизвестный режим")
	}
	graph := map[string]any{}
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, err
	}
	return buildKreaProGraph(input, settings, graph, kreaProOptions())
}

// The graph exports are regenerated independently of the adapter. Accept both
// the legacy layout and V29 without inventing missing processing branches.
func buildKreaProGraph(input generationForm, settings kreaProSettings, graph map[string]any, options []kreaProOption) (map[string]any, error) {
	upscaler, scale, err := kreaProUpscaler(input, settings)
	if err != nil {
		return nil, err
	}
	if err := kreaProValidateOutput(input, settings, scale); err != nil {
		return nil, err
	}
	for id, raw := range graph {
		n, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Ультра: некорректная нода %s", id)
		}
		if _, ok := n["inputs"].(map[string]any); !ok {
			return nil, fmt.Errorf("Ультра: некорректные входы ноды %s", id)
		}
	}
	for _, id := range []string{"3857", "2910", "3761", "3760", "3481", "3014", "3907", "3981", "4071"} {
		if graph[id] == nil {
			return nil, fmt.Errorf("Ультра: отсутствует нода %s", id)
		}
	}
	fields := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
	link := func(id string, slot int) []any { return []any{id, slot} }
	node := func(kind string, values map[string]any) map[string]any {
		return map[string]any{"class_type": kind, "inputs": values}
	}
	for id, values := range settings.Nodes {
		if id == "1292" && graph[id] == nil && graph["4102"] != nil {
			id = "4102"
		}
		if graph[id] == nil {
			continue
		}
		for name, value := range values {
			fields(id)[name] = value
		}
	}
	for i, id := range []string{"4109", "4106", "4107", "4108"} {
		useImage := input.imageCount() > i
		removeBackground := useImage && settings.Enabled[fmt.Sprint(3680+i)]
		values := map[int]any{0: useImage, 1: removeBackground}
		if i == 0 {
			// Width/height already incorporate the service's match-source policy.
			values = map[int]any{0: useImage, 1: false, 2: removeBackground}
			if !useImage {
				values[4] = false
			}
		}
		if err := kreaProPanelValues(graph, id, values); err != nil {
			return nil, err
		}
	}
	if graph["4096"] != nil {
		fields("4096")["seed"] = input.Seed
		if enabled, present := settings.Enabled["3662"]; present && !enabled {
			delete(fields("4096"), "mask")
		}
	}
	if graph["4098"] != nil {
		useMask, present := settings.Enabled["3662"]
		if !present {
			useMask = settings.Enabled["4096"]
		}
		if err := kreaProPanelValues(graph, "4098", map[int]any{0: settings.Enabled["3659"], 1: useMask, 2: settings.Enabled["4096"]}); err != nil {
			return nil, err
		}
	}
	if graph["4089"] != nil {
		if _, supplied := settings.Nodes["4089"]["selections"]; !supplied {
			fields("4089")["selections"] = "[]"
		}
	}
	if graph["3525"] != nil {
		original := fields("3525")
		graph["3525"] = node("LCDepthAnythingV2", map[string]any{"image": original["image"], "resolution": original["resolution"], "model": "Depth Anything V2 Large (CC-BY-NC-4.0)", "invert": false})
	}
	fields("3857")["model_file"], fields("3857")["text_encoder"], fields("3857")["vae"] = input.ModelName, input.TextEncoder, input.VAE
	fields("3857")["speed_ups"] = "Manual"
	fields("3857")["torch_compile"] = "off"
	// The service owns the prompt, not the author's prompt/vision widgets.
	if graph["4105"] != nil {
		fields("4105")["text"] = input.Positive
	} else if graph["1302"] != nil {
		fields("1302")["text"] = input.Positive
	} else if graph["2719"] != nil {
		fields("2719")["prompt"] = input.Positive
	} else {
		return nil, errors.New("Ультра: отсутствует вход промта")
	}
	if graph["2909"] != nil {
		fields("2909")["positive"] = input.Positive
	}
	if graph["4114"] != nil {
		fields("4114")["text"], fields("4114")["weights"] = input.Positive, "[]"
		fields("4114")["block"] = "never"
		fields("4114")["prompt"] = link("2719", 0)
		delete(fields("4114"), "last_in")
		delete(fields("4114"), "lc_weighted_prompt_face")
	}
	fields("2910")["negative"] = input.Negative
	fields("3761")["base_seed"] = input.Seed
	fields("3760")["lora_rows"] = "[]"
	if graph["4020"] != nil {
		fields("4020")["lora_rows"] = "[]"
	}
	fields("3481")["custom_width"], fields("3481")["custom_height"] = input.Width, input.Height
	fields("3481")["resolution_source"], fields("3481")["divisible_by"] = false, 16
	fields("3481")["max_resolution"], fields("3481")["aspect_ratio"], fields("3481")["batch_size"] = 0, "custom", 1
	for name, value := range map[string]any{"total_steps": input.Steps, "step_swap": input.KreaUltraStepSwap, "cfg_1": input.CFG, "cfg_2": input.KreaUltraLowCFG, "denoise": input.Denoise, "sampler_name": input.Sampler, "scheduler": input.Scheduler} {
		fields("3014")[name] = value
	}
	fields("3907")["amount"] = input.KreaUltraDetailAmount
	// LC's auto=false selects manual growth; only a zero growth step disables it.
	fields("3981")["auto"], fields("3981")["grow_at_step"] = false, 0
	if input.KreaUltraSpeedBoost {
		fields("3981")["grow_at_step"] = input.KreaUltraGrowStep
	}
	// LC pipe enum outputs may contain more options than a downstream COMBO.
	// Bind the selected value, not the producer's complete option set.
	for _, raw := range graph {
		n := raw.(map[string]any)
		values := n["inputs"].(map[string]any)
		for _, name := range []string{"sampler_name", "scheduler", "base_scheduler"} {
			if _, linked := values[name].([]any); linked {
				if name == "sampler_name" {
					values[name] = input.Sampler
				} else {
					values[name] = input.Scheduler
				}
			}
		}
		if n["class_type"] == "VAEEncode" && values["vae"] == nil {
			values["vae"] = link("3857", 2)
		}
	}
	if upscaler == "vosr" {
		if graph["4046"] == nil || graph["4047"] == nil {
			return nil, errors.New("Ультра: VOSR2 недоступен в графе")
		}
		fields("4047")["value"] = fmt.Sprintf("%dx", scale)
	}
	if graph["4150"] != nil {
		if upscaler == "relay" {
			fields("4150")["source"], fields("4150")["upscale"], fields("4150")["seed"] = "image", fmt.Sprintf("%dx", scale), input.Seed
			fields("4071")["images"] = link("4150", 0)
		} else {
			graph["4150"] = node("GatewayKreaProBypass", map[string]any{"v0": fields("4150")["image"]})
		}
	} else if upscaler == "relay" {
		return nil, errors.New("Ультра: RelayVSR недоступен в графе")
	}
	if graph["4046"] != nil && upscaler != "vosr" {
		original := fields("4046")
		graph["4046"] = node("GatewayKreaProBypass", map[string]any{"v0": original["pipe"], "v1": original["image"]})
	}
	fields("4071")["filename"], fields("4071")["path"], fields("4071")["filename_prefix"] = "Krea2-Pro", "gateway-krea-pro", "AI-Gateway-Krea2-Pro"
	fields("4071")["hash_resources"] = false
	for i, id := range []string{"3489", "3535", "3556", "3566"} {
		if graph[id] == nil {
			continue
		}
		name := input.InputImage
		if i > 0 {
			name = input.ReferenceImages[i-1]
		}
		if name == "" {
			graph[id] = node("GatewayKreaProBypass", map[string]any{})
		} else {
			fields(id)["image"] = name
		}
	}
	if input.InputImage != "" {
		for _, raw := range graph {
			values := raw.(map[string]any)["inputs"].(map[string]any)
			for name, value := range values {
				if v, ok := value.([]any); ok && len(v) == 2 && v[0] == "3489" && fmt.Sprint(v[1]) == "0" {
					values[name] = link("9600", 0)
				}
			}
		}
		graph["9600"] = node("ImageScale", map[string]any{"image": link("3489", 0), "upscale_method": "lanczos", "width": input.Width, "height": input.Height, "crop": "center"})
	}
	if graph["3620"] != nil {
		if settings.Enabled["3621"] {
			fields("3620")["image"] = input.ReferenceImages[2]
		} else {
			graph["3620"] = node("GatewayKreaProBypass", map[string]any{})
		}
	}
	modelID := "4102"
	if graph[modelID] == nil {
		modelID = "1292"
	}
	if graph[modelID] == nil {
		return nil, errors.New("Ультра: отсутствует вход модели")
	}
	modelTail, clipTail := link(modelID, 0), link("3857", 1)
	for i, name := range input.LoraNames {
		if name == "" {
			continue
		}
		id := fmt.Sprint(9000 + i)
		graph[id] = node("LoraLoader", map[string]any{"model": modelTail, "clip": clipTail, "lora_name": name, "strength_model": input.LoraModel[i], "strength_clip": input.LoraClip[i]})
		modelTail, clipTail = link(id, 0), link(id, 1)
	}
	fields("3760")["model"], fields("3760")["clip"] = modelTail, clipTail
	if input.KreaProMode == "inpaint" || input.KreaProMode == "outpaint" {
		if graph["3870"] == nil || graph["3878"] == nil {
			return nil, errors.New("Ультра: AnyPaint недоступен в графе")
		}
		fields("3870")["image"] = link("3481", 1)
		fields("3878")["steps"], fields("3878")["prompt"] = input.Steps, input.Positive
		fields("3878")["seed"] = input.Seed
		if input.KreaProMode == "inpaint" {
			graph["9500"] = node("LoadImage", map[string]any{"image": input.ReferenceImages[0]})
			graph["9501"] = node("ImageScale", map[string]any{"image": link("9500", 0), "upscale_method": "nearest-exact", "width": input.Width, "height": input.Height, "crop": "disabled"})
			graph["9502"] = node("ImageToMask", map[string]any{"image": link("9501", 0), "channel": "red"})
			fields("3878")["image"], fields("3878")["mask"] = link("3481", 1), link("9502", 0)
		} else {
			_, _, padding, err := kreaProOutpaintFrame(input.Width, input.Height, settings)
			if err != nil {
				return nil, err
			}
			for name, value := range padding {
				fields("3870")[name] = value
			}
			fields("3878")["image"], fields("3878")["mask"] = link("3870", 0), link("3870", 1)
		}
	}
	for _, option := range options {
		if option.ID == "4046" || option.ID == "4150" {
			continue
		}
		if !option.Toggle || settings.Enabled[option.ID] || graph[option.ID] == nil {
			continue
		}
		original := fields(option.ID)
		values := map[string]any{"v0": original["image"]}
		if graph[option.ID].(map[string]any)["class_type"] == "LCSmartInpaintPipe" {
			values = map[string]any{"v0": original["pipe"], "v1": original["image"]}
		}
		if strings.HasPrefix(option.ID, "368") {
			values = map[string]any{"v0": original["image"], "v2": original["image"]}
		}
		graph[option.ID] = node("GatewayKreaProBypass", values)
	}
	kept, visiting := map[string]any{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return errors.New("Ультра: цикл в графе")
		}
		if kept[id] != nil {
			return nil
		}
		n, ok := graph[id].(map[string]any)
		if !ok {
			return fmt.Errorf("Ультра: отсутствует нода %s", id)
		}
		visiting[id] = true
		for _, value := range n["inputs"].(map[string]any) {
			if v, ok := value.([]any); ok && len(v) == 2 {
				if source, ok := v[0].(string); ok {
					if err := visit(source); err != nil {
						return err
					}
				}
			}
		}
		visiting[id] = false
		kept[id] = n
		return nil
	}
	if err := visit("4071"); err != nil {
		return nil, err
	}
	return kept, nil
}
