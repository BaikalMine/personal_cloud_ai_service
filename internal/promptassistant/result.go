package promptassistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultImageNumPredict      = 900
	DefaultImageThinkNumPredict = 1600
	DefaultVideoNumPredict      = 2400
	DefaultVideoThinkNumPredict = 4096
	DefaultImageTimeout         = 120 * time.Second
	DefaultVideoTimeout         = 240 * time.Second
	DefaultKeepAlive            = "0"
)

// ModelPolicy controls the resource envelope for the configured local model.
// Image and video requests are separated because the MiniMax Context-IR response
// is materially longer than an image prompt.
type ModelPolicy struct {
	ImageNumPredict      int
	ImageThinkNumPredict int
	VideoNumPredict      int
	VideoThinkNumPredict int
	ImageTimeout         time.Duration
	VideoTimeout         time.Duration
	KeepAlive            string
}

type RequestPolicy struct {
	NumPredict int           `json:"num_predict"`
	Timeout    time.Duration `json:"-"`
	TimeoutMS  int64         `json:"timeout_ms"`
	KeepAlive  string        `json:"keep_alive"`
}

type Usage struct {
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalDurationMS  int64  `json:"total_duration_ms"`
	LoadDurationMS   int64  `json:"load_duration_ms"`
	PromptDurationMS int64  `json:"prompt_duration_ms"`
	CompletionTimeMS int64  `json:"completion_duration_ms"`
	DoneReason       string `json:"done_reason,omitempty"`
}

// ReferenceUnderstanding is both the user-facing review and the authoritative
// machine-readable map used to keep numbered workflow references distinct.
type ReferenceUnderstanding struct {
	Identifier string `json:"id"`
	Kind       string `json:"kind"`
	Role       string `json:"role"`
	Summary    string `json:"summary"`
	Use        string `json:"use"`
	Inspected  bool   `json:"inspected"`
}

type Result struct {
	Prompt     string                   `json:"prompt"`
	References []ReferenceUnderstanding `json:"references"`
	Usage      Usage                    `json:"usage"`
	Policy     RequestPolicy            `json:"policy"`
}

type modelResult struct {
	Prompt     string `json:"prompt"`
	References []struct {
		Identifier string `json:"id"`
		Summary    string `json:"summary"`
		Use        string `json:"use"`
	} `json:"references"`
}

func DefaultModelPolicy() ModelPolicy {
	return ModelPolicy{
		ImageNumPredict: DefaultImageNumPredict, ImageThinkNumPredict: DefaultImageThinkNumPredict,
		VideoNumPredict: DefaultVideoNumPredict, VideoThinkNumPredict: DefaultVideoThinkNumPredict,
		ImageTimeout: DefaultImageTimeout, VideoTimeout: DefaultVideoTimeout, KeepAlive: DefaultKeepAlive,
	}
}

func (policy ModelPolicy) normalized() ModelPolicy {
	defaults := DefaultModelPolicy()
	if policy.ImageNumPredict <= 0 {
		policy.ImageNumPredict = defaults.ImageNumPredict
	}
	if policy.ImageThinkNumPredict <= 0 {
		policy.ImageThinkNumPredict = defaults.ImageThinkNumPredict
	}
	if policy.VideoNumPredict <= 0 {
		policy.VideoNumPredict = defaults.VideoNumPredict
	}
	if policy.VideoThinkNumPredict <= 0 {
		policy.VideoThinkNumPredict = defaults.VideoThinkNumPredict
	}
	if policy.ImageTimeout <= 0 {
		policy.ImageTimeout = defaults.ImageTimeout
	}
	if policy.VideoTimeout <= 0 {
		policy.VideoTimeout = defaults.VideoTimeout
	}
	policy.KeepAlive = strings.TrimSpace(policy.KeepAlive)
	if policy.KeepAlive == "" {
		policy.KeepAlive = defaults.KeepAlive
	}
	return policy
}

func (policy ModelPolicy) request(mode Mode, profile Profile, think bool) RequestPolicy {
	policy = policy.normalized()
	video := mode == ModeTextToVideo && IsMiniMaxH3Profile(profile)
	result := RequestPolicy{NumPredict: policy.ImageNumPredict, Timeout: policy.ImageTimeout, KeepAlive: policy.KeepAlive}
	if think {
		result.NumPredict = policy.ImageThinkNumPredict
	}
	if video {
		result.NumPredict = policy.VideoNumPredict
		result.Timeout = policy.VideoTimeout
		if think {
			result.NumPredict = policy.VideoThinkNumPredict
		}
	}
	result.TimeoutMS = result.Timeout.Milliseconds()
	return result
}

func structuredResponseInstruction(mode Mode, references []ImageReference, video VideoContext) string {
	expected := expectedReferenceMap(mode, references, video)
	identifiers := make([]string, 0, len(expected))
	for _, reference := range expected {
		identifiers = append(identifiers, reference.Identifier)
	}
	list := "none"
	if len(identifiers) > 0 {
		list = strings.Join(identifiers, ", ")
	}
	return fmt.Sprintf(`

Transport response contract (this overrides earlier output-only formatting instructions): return exactly one valid JSON object and nothing else:
{"prompt":"the final production-ready English prompt","references":[{"id":"Picture 1","summary":"short concrete understanding","use":"how this reference is used"}]}

The references array must contain exactly these identifiers in this order: %s. Do not add identifiers that are not listed. For every Picture entry, inspect the attached image and summarize concrete visible identity, appearance, clothing, object, pose, style, or background details relevant to its assigned role. Keep summary and use concise and write them in the same human language as the user's request. Audio 1 and Video 1 are workflow attachments that are not available to your vision input: describe only their declared role and never claim that you inspected their contents. When no identifiers are listed, return an empty references array. The prompt field must still follow all preceding model-specific prompt rules.`, list)
}

func expectedReferenceMap(mode Mode, references []ImageReference, video VideoContext) []ReferenceUnderstanding {
	result := make([]ReferenceUnderstanding, 0, len(references)+2)
	for _, reference := range references {
		role := string(reference.Role)
		if mode == ModeTextToVideo && video.Mode == "frames" {
			if reference.Number == 1 {
				role = "first_frame"
			} else if reference.Number == 2 {
				role = "last_frame"
			}
		}
		result = append(result, ReferenceUnderstanding{
			Identifier: fmt.Sprintf("Picture %d", reference.Number), Kind: "image", Role: role,
			Use: referenceUse(role),
		})
	}
	if mode == ModeTextToVideo && video.VideoReference {
		result = append(result, ReferenceUnderstanding{
			Identifier: "Video 1", Kind: "video", Role: "motion_timing",
			Summary: "Видеореференс подключён к workflow; его содержимое ассистент не анализировал.",
			Use:     "Ориентир для движения, темпа и временной структуры.", Inspected: false,
		})
	}
	if mode == ModeTextToVideo && video.AudioReference {
		result = append(result, ReferenceUnderstanding{
			Identifier: "Audio 1", Kind: "audio", Role: "voice_sound",
			Summary: "Аудиореференс подключён к workflow; его содержимое ассистент не анализировал.",
			Use:     "Ориентир для голоса, звучания и синхронизации.", Inspected: false,
		})
	}
	return result
}

func referenceUse(role string) string {
	switch ImageReferenceRole(role) {
	case ImageReferenceBaseScene:
		return "Сохранить основную сцену, персонажа, композицию и кадрирование."
	case ImageReferenceIdentity:
		return "Сохранить или перенести внешность, лицо, волосы и отличительные черты."
	case ImageReferenceWardrobeObject:
		return "Перенести одежду, предмет, материал или аксессуар."
	case ImageReferencePoseComposition:
		return "Использовать позу, ракурс и композицию как ориентир."
	case ImageReferenceStyle:
		return "Использовать визуальный стиль, свет и цветовую обработку."
	case ImageReferenceBackground:
		return "Использовать окружение и детали фона."
	case ImageReferenceDetails:
		return "Сохранить или перенести выбранные мелкие детали и текстуры."
	}
	switch role {
	case "first_frame":
		return "Использовать как точный первый кадр видео."
	case "last_frame":
		return "Использовать как точный последний кадр видео."
	default:
		return "Использовать как визуальный ориентир согласно выбранной роли."
	}
}

func parseModelResult(raw string, mode Mode, references []ImageReference, video VideoContext) (Result, error) {
	content := stripModelEnvelope(raw)
	if content == "" {
		return Result{}, errors.New("локальная модель не вернула вариант промта")
	}
	expected := expectedReferenceMap(mode, references, video)
	var decoded modelResult
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		if len(expected) > 0 {
			return Result{}, fmt.Errorf("локальная модель не вернула структурированный разбор референсов: %w", err)
		}
		prompt := cleanOutput(content)
		if prompt == "" {
			return Result{}, errors.New("локальная модель не вернула вариант промта")
		}
		if len(prompt) > 4000 {
			return Result{}, errors.New("локальная модель вернула промт длиннее 4000 символов")
		}
		return Result{Prompt: prompt, References: []ReferenceUnderstanding{}}, nil
	}
	decoded.Prompt = cleanOutput(decoded.Prompt)
	if decoded.Prompt == "" {
		return Result{}, errors.New("локальная модель не вернула вариант промта")
	}
	if len(decoded.Prompt) > 4000 {
		return Result{}, errors.New("локальная модель вернула промт длиннее 4000 символов")
	}
	provided := make(map[string]struct{ summary, use string }, len(decoded.References))
	for _, reference := range decoded.References {
		identifier := normalizeReferenceIdentifier(reference.Identifier)
		if identifier == "" {
			continue
		}
		provided[strings.ToLower(identifier)] = struct{ summary, use string }{
			summary: limitText(reference.Summary, 500), use: limitText(reference.Use, 500),
		}
	}
	for index := range expected {
		value, ok := provided[strings.ToLower(expected[index].Identifier)]
		if expected[index].Kind == "image" {
			if !ok || value.summary == "" {
				return Result{}, fmt.Errorf("локальная модель не описала референс %s", expected[index].Identifier)
			}
			expected[index].Summary = value.summary
			expected[index].Inspected = true
		}
		if ok && value.use != "" {
			expected[index].Use = value.use
		}
	}
	return Result{Prompt: decoded.Prompt, References: expected}, nil
}

func stripModelEnvelope(value string) string {
	value = strings.TrimSpace(value)
	if end := strings.LastIndex(value, "</think>"); end >= 0 {
		value = strings.TrimSpace(value[end+len("</think>"):])
	}
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```json")
		value = strings.TrimPrefix(value, "```JSON")
		value = strings.TrimPrefix(value, "```")
		value = strings.TrimSpace(value)
		value = strings.TrimSuffix(value, "```")
	}
	return strings.TrimSpace(value)
}

func normalizeReferenceIdentifier(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "<>[]")
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return ""
	}
	kind := strings.ToLower(parts[0])
	if kind != "picture" && kind != "video" && kind != "audio" {
		return ""
	}
	return strings.ToUpper(kind[:1]) + kind[1:] + " " + parts[1]
}

func limitText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		value = value[:maximum]
	}
	return strings.TrimSpace(value)
}
