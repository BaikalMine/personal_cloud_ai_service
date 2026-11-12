package promptassistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxPromptBytes   = 32 << 10
	maxResponseBytes = 64 << 10
	maxImageBytes    = 32 << 20
)

var ErrUnsupportedImage = errors.New("изображение не поддерживается локальной vision-проверкой")

type Client struct {
	baseURL         *url.URL
	model           string
	visionModel     string
	visionTimeout   time.Duration
	visionKeepAlive string
	http            *http.Client
	policy          ModelPolicy
}

type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	Think     bool      `json:"think"`
	KeepAlive string    `json:"keep_alive"`
	Format    any       `json:"format,omitempty"`
	Options   struct {
		Temperature float64 `json:"temperature"`
		TopP        float64 `json:"top_p"`
		NumPredict  int     `json:"num_predict"`
		NumCtx      int     `json:"num_ctx,omitempty"`
	} `json:"options"`
}

type chatResponse struct {
	Done               bool    `json:"done"`
	Message            Message `json:"message"`
	TotalDuration      int64   `json:"total_duration"`
	LoadDuration       int64   `json:"load_duration"`
	PromptEvalCount    int     `json:"prompt_eval_count"`
	PromptEvalDuration int64   `json:"prompt_eval_duration"`
	EvalCount          int     `json:"eval_count"`
	EvalDuration       int64   `json:"eval_duration"`
	DoneReason         string  `json:"done_reason"`
}

func NewClient(baseURL *url.URL, model string) *Client {
	return NewClientWithPolicy(baseURL, model, DefaultModelPolicy())
}

func NewClientWithPolicy(baseURL *url.URL, model string, policy ModelPolicy) *Client {
	policy = policy.normalized()
	model = strings.TrimSpace(model)
	return &Client{
		baseURL:         baseURL,
		model:           model,
		visionModel:     model,
		visionTimeout:   policy.ImageTimeout,
		visionKeepAlive: policy.KeepAlive,
		policy:          policy,
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          4,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 80 * time.Second,
			},
		},
	}
}

// WithVisionModel keeps fast text-only prompting independent from requests
// that genuinely need a multimodal model. It is intended for application
// startup, before the client is shared between handlers.
func (c *Client) WithVisionModel(model string, timeout time.Duration, keepAlive string) *Client {
	if c == nil {
		return c
	}
	if model = strings.TrimSpace(model); model != "" {
		c.visionModel = model
	}
	if timeout > 0 {
		c.visionTimeout = timeout
	}
	if keepAlive = strings.TrimSpace(keepAlive); keepAlive != "" {
		c.visionKeepAlive = keepAlive
	}
	return c
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != nil && c.model != ""
}

// ForModels returns a request-local client; it never changes the shared defaults.
// An empty vision model intentionally disables image input for this choice.
func (c *Client) ForModels(model, visionModel string, timeout time.Duration) *Client {
	if c == nil {
		return nil
	}
	selected := *c
	selected.model, selected.visionModel = model, visionModel
	if timeout > 0 {
		selected.policy.ImageTimeout, selected.policy.VideoTimeout = timeout, timeout
		selected.policy.ReasoningTimeout = timeout
		selected.visionTimeout = timeout
	}
	return &selected
}

func (c *Client) VisionConfigured() bool {
	return c != nil && c.baseURL != nil && c.visionModel != ""
}

func (c *Client) visionPolicy(policy RequestPolicy) RequestPolicy {
	if c.visionTimeout > 0 {
		policy.Timeout = c.visionTimeout
		policy.TimeoutMS = c.visionTimeout.Milliseconds()
	}
	if c.visionKeepAlive != "" {
		policy.KeepAlive = c.visionKeepAlive
	}
	return policy
}

func (c *Client) Enhance(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, think bool) (string, error) {
	result, err := c.EnhanceResult(ctx, mode, profile, prompt, references, think)
	return result.Prompt, err
}

// EnhanceVideo uses the MiniMax H3 workflow facts and attached image
// references to select a compatible Context-IR structure.
func (c *Client) EnhanceVideo(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (string, error) {
	result, err := c.EnhanceVideoResult(ctx, mode, profile, prompt, references, video, think)
	return result.Prompt, err
}

func (c *Client) EnhanceResult(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, think bool) (Result, error) {
	return c.enhance(ctx, mode, profile, prompt, references, VideoContext{}, EditContext{}, think)
}

type EditContext struct {
	NegativePrompt string
	CFG            float64
	ImageFamily    string
}

func (c *Client) EnhanceEditResult(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, edit EditContext, think bool) (Result, error) {
	return c.enhance(ctx, mode, profile, prompt, references, VideoContext{}, edit, think)
}

func (c *Client) EnhanceVideoResult(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (Result, error) {
	return c.enhance(ctx, mode, profile, prompt, references, video, EditContext{}, think)
}

func (c *Client) PolicyFor(mode Mode, profile Profile, think bool) RequestPolicy {
	if c == nil {
		return DefaultModelPolicy().request(mode, profile, think)
	}
	return c.policy.request(mode, profile, think)
}

func (c *Client) PolicyForRequest(mode Mode, profile Profile, think, hasImages bool) RequestPolicy {
	policy := c.PolicyFor(mode, profile, think)
	if hasImages && c != nil {
		policy = c.visionPolicy(policy)
	}
	if think {
		limit := DefaultModelPolicy().ReasoningTimeout
		if c != nil {
			limit = c.policy.normalized().ReasoningTimeout
		}
		policy.Timeout = max(policy.Timeout, limit)
		policy.TimeoutMS = policy.Timeout.Milliseconds()
	}
	return policy
}

func (c *Client) enhance(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, edit EditContext, think bool) (output Result, err error) {
	prepared, err := c.PrepareEnhancementWithEditContext(mode, profile, prompt, references, video, edit, think)
	output = Result{Model: prepared.Model, Policy: prepared.Policy, Execution: ExecutionNotDispatched}
	if err != nil {
		return output, err
	}
	defer clear(prepared.Body)
	target := *c.baseURL
	target.Path = joinPath(target.Path, "/api/chat")
	target.RawQuery = ""
	target.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(prepared.Body))
	if err != nil {
		return output, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	output.Execution = ExecutionUnconfirmed
	response, err := c.httpClientFor(prepared.Policy).Do(request)
	if err != nil {
		return output, fmt.Errorf("локальная модель недоступна: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(prepared.ResponseByteLimit()+1)))
	if err != nil {
		return output, err
	}
	defer clear(body)
	return ParseEnhancementResponse(prepared, response.StatusCode, body)
}

// PrepareEnhancement freezes the selected instruction, model, reference roles
// and output contract before a durable executor accepts the request.
func (c *Client) PrepareEnhancement(mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (PreparedEnhancement, error) {
	return c.PrepareEnhancementWithEditContext(mode, profile, prompt, references, video, EditContext{}, think)
}

func (c *Client) PrepareEnhancementWithEditContext(mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, edit EditContext, think bool) (PreparedEnhancement, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		if mode != ModeTextToVideo || len(references) == 0 {
			return PreparedEnhancement{}, errors.New("введите исходный промт")
		}
		prompt = "Use only the supplied visual references to derive a complete, natural video. Do not ask for clarification."
	}
	promptByteLimit := maxPromptBytes
	if video.LongParts != 0 {
		if video.LongTransition != "" && video.LongTransition != "continuous" && video.LongTransition != "cinematic" {
			return PreparedEnhancement{}, errors.New("неизвестный режим стыков длинного видео")
		}
		if mode != ModeTextToVideo || !IsMiniMaxH3Profile(profile) || video.Mode != VideoModeREF2VA || video.AIHubMode != "" || !ValidLongVideoParts(video.LongParts) {
			return PreparedEnhancement{}, errors.New("некорректный режим длинного видео MiniMax H3")
		}
		promptByteLimit = 4 * PromptCharacterLimitForVideo(mode, video.LongParts)
	}
	if len(prompt) > promptByteLimit {
		return PreparedEnhancement{}, errors.New("промт слишком длинный")
	}
	if !c.Configured() {
		return PreparedEnhancement{}, errors.New("локальный промт-ассистент не настроен")
	}
	if !ValidProfile(mode, profile) {
		return PreparedEnhancement{}, errors.New("неизвестный шаблон промт-ассистента")
	}
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) && !MiniMaxH3ProfileMatchesMode(profile, video.Mode) {
		return PreparedEnhancement{}, errors.New("шаблон промт-ассистента не соответствует ветке MiniMax H3")
	}
	systemPrompt := EnhancementInstruction(mode, profile, references, video, edit)
	negativeContext := profile == ProfileQwen21Edit || edit.CFG > 0
	requestPolicy := c.PolicyForVideoRequest(mode, profile, video, think, false)
	payload := chatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream:    false,
		Think:     think,
		KeepAlive: requestPolicy.KeepAlive,
		Format:    "json",
	}
	if negativeContext {
		if edit.CFG == 0 {
			edit.CFG = 1
		}
		if !(edit.CFG >= 1 && edit.CFG <= 30) || edit.CFG == 1 && strings.TrimSpace(edit.NegativePrompt) != "" {
			return PreparedEnhancement{}, errors.New("негативный промт работает только при CFG больше 1")
		}
		context, _ := json.Marshal(map[string]any{"cfg": edit.CFG, "existing_negative_prompt": strings.TrimSpace(edit.NegativePrompt)})
		payload.Messages[1].Content += "\n\nEdit settings (keep this separate from the requested positive edit): " + string(context)
	}
	if video.AIHubMode != "" && video.AudioSource == "ltx" {
		sound, _ := json.Marshal(map[string]string{"ltx_sound_specification": video.AudioPrompt})
		payload.Messages[1].Content += "\n\nSeparate user sound specification:\n" + string(sound)
	}
	for _, reference := range references {
		if len(reference.Image) == 0 {
			continue
		}
		if len(reference.Image) > maxImageBytes {
			return PreparedEnhancement{}, fmt.Errorf("%w: изображение %d слишком большое", ErrUnsupportedImage, reference.Number)
		}
		switch strings.ToLower(strings.TrimSpace(reference.MIMEType)) {
		case "image/jpeg", "image/png", "image/webp":
		default:
			return PreparedEnhancement{}, fmt.Errorf("%w: формат изображения %d", ErrUnsupportedImage, reference.Number)
		}
		payload.Messages[1].Images = append(payload.Messages[1].Images, base64.StdEncoding.EncodeToString(reference.Image))
		if note := strings.TrimSpace(reference.Correction); note != "" {
			metadata, _ := json.Marshal(struct {
				Picture    int    `json:"picture"`
				Correction string `json:"user_correction"`
			}{reference.Number, note})
			payload.Messages[1].Content += "\n\nReference-specific user observation (only for this picture; do not transfer it to another source):\n" + string(metadata)
		}
	}
	if len(video.VideoStoryboards) > 0 {
		if len(video.VideoStoryboards) > 10 || len(video.VideoStoryboards) != len(video.VideoStoryboardTimes) {
			return PreparedEnhancement{}, ErrUnsupportedImage
		}
		total := 0
		for i, page := range video.VideoStoryboards {
			total += len(page)
			if len(page) == 0 || len(page) > 2<<20 || total > 16<<20 || len(video.VideoStoryboardTimes[i]) == 0 || len(video.VideoStoryboardTimes[i]) > 12 {
				return PreparedEnhancement{}, ErrUnsupportedImage
			}
			payload.Messages[1].Images = append(payload.Messages[1].Images, base64.StdEncoding.EncodeToString(page))
			times, err := json.Marshal(video.VideoStoryboardTimes[i])
			if err != nil {
				return PreparedEnhancement{}, ErrUnsupportedImage
			}
			payload.Messages[1].Content += fmt.Sprintf("\nAttachment %d is Video 1 storyboard page %d/%d, NOT a Picture reference. Grid: 4 columns, 3 rows, left-to-right then top-to-bottom. Valid tile timestamps in source seconds: %s. Ignore remaining blank tiles.", len(payload.Messages[1].Images), i+1, len(video.VideoStoryboards), times)
		}
		payload.Messages[1].Content += "\nInspect every video page in chronological order. Describe timecoded phases, camera changes, completed key actions and the final visible state. Distinguish observed events from gaps between samples. These frames contain no heard or transcribed audio; never infer exact spoken words from mouth shapes."
	} else if len(video.VideoStoryboard) > 0 {
		if len(video.VideoStoryboard) > 4<<20 {
			return PreparedEnhancement{}, ErrUnsupportedImage
		}
		payload.Messages[1].Images = append(payload.Messages[1].Images, base64.StdEncoding.EncodeToString(video.VideoStoryboard))
		payload.Messages[1].Content += fmt.Sprintf("\nThe last attached image is a 2x2 sampled storyboard of Video 1, NOT Picture %d. Read left-to-right, top-to-bottom. Approximate sample offsets in the source: %.2f, %.2f, %.2f, %.2f seconds. Empty tiles are not evidence. Samples do not show every motion or any audio.", len(references)+1, video.VideoStart, video.VideoStart+video.VideoSampleDuration/4, video.VideoStart+video.VideoSampleDuration/2, video.VideoStart+video.VideoSampleDuration*3/4)
	}
	if len(payload.Messages[1].Images) > 0 || mode == ModeTextToVideo && c.VisionConfigured() {
		if !c.VisionConfigured() {
			return PreparedEnhancement{}, errors.New("локальная vision-модель не настроена")
		}
		payload.Model = c.visionModel
		requestPolicy = c.PolicyForVideoRequest(mode, profile, video, think, true)
		payload.KeepAlive = requestPolicy.KeepAlive
	}
	payload.Options.Temperature = 0.35
	payload.Options.TopP = 0.9
	payload.Options.NumPredict = requestPolicy.NumPredict
	payload.Options.NumCtx = requestPolicy.NumCtx
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) {
		payload.Options.Temperature = 0.3
	}
	if ValidLongVideoParts(video.LongParts) {
		payload.Format = longVideoResponseSchema(video.LongParts)
	} else if wantsLTXAudio(video) {
		payload.Format = ltxResponseSchema(mode)
	} else if negativeContext {
		payload.Format = qwen21ResponseSchema(mode)
	}
	body, err := json.Marshal(payload)
	return PreparedEnhancement{Body: body, Model: payload.Model, Mode: mode, Policy: requestPolicy, LongParts: video.LongParts, LongAudio: ValidLongVideoParts(video.LongParts), LTXAudio: wantsLTXAudio(video),
		References: expectedReferenceMap(mode, references, video)}, err
}

func (c *Client) httpClientFor(policy RequestPolicy) *http.Client {
	client := *c.http
	client.Timeout = policy.Timeout
	if transport, ok := c.http.Transport.(*http.Transport); ok {
		requestTransport := transport.Clone()
		requestTransport.ResponseHeaderTimeout = policy.Timeout
		client.Transport = requestTransport
	}
	return &client
}

func durationMilliseconds(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return time.Duration(value).Milliseconds()
}

// ClassifyImage uses the locally configured vision model to decide whether an
// image should be hidden behind the privacy curtain. Media never leaves Ollama
// on this host, and an uncertain response is returned as an error for retry.
func (c *Client) ClassifyImage(ctx context.Context, image []byte, mimeType string) (bool, error) {
	if !c.VisionConfigured() {
		return false, errors.New("локальная vision-модель не настроена")
	}
	if len(image) == 0 || len(image) > maxImageBytes {
		return false, fmt.Errorf("%w: недопустимый размер", ErrUnsupportedImage)
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return false, fmt.Errorf("%w: формат %q", ErrUnsupportedImage, mimeType)
	}

	target := *c.baseURL
	target.Path = joinPath(target.Path, "/api/chat")
	target.RawQuery = ""
	target.Fragment = ""
	payload := chatRequest{
		Model: c.visionModel,
		Messages: []Message{
			{Role: "system", Content: "You are a strict local 18+ privacy classifier. Reply with exactly one word: SENSITIVE or SAFE. The privacy curtain is not a moral judgement: choose SENSITIVE whenever the image visibly contains nudity, exposed breasts, buttocks or intimate body parts, explicit sexual activity, fetish context, erotic lingerie, an intentionally sexualized pose or framing, implied nudity, or clearly adult erotic imagery. Prefer SENSITIVE whenever the sexual intent is plausible from visible context; this protects viewers from accidental exposure. Reply SAFE only for clearly non-sexual content such as ordinary portraits, everyday clothing, neutral fashion, landscapes, ordinary beach or sport scenes, and non-erotic art. Do not infer age, identity, or sexual content from a person's appearance alone."},
			{Role: "user", Content: "Classify this image.", Images: []string{base64.StdEncoding.EncodeToString(image)}},
		},
		Stream:    false,
		Think:     false,
		KeepAlive: "0",
	}
	payload.Options.Temperature = 0
	payload.Options.TopP = 0.1
	payload.Options.NumPredict = 3
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	requestPolicy := c.PolicyForRequest(ModeImageToImage, ProfileWorkflowDefault, false, true)
	response, err := c.httpClientFor(requestPolicy).Do(request)
	if err != nil {
		return false, fmt.Errorf("локальная vision-модель недоступна: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return false, err
	}
	if len(responseBody) > maxResponseBytes {
		return false, errors.New("локальная vision-модель вернула слишком большой ответ")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("локальная vision-модель ответила HTTP %d", response.StatusCode)
	}
	var result chatResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return false, fmt.Errorf("некорректный ответ локальной vision-модели: %w", err)
	}
	answer := strings.Fields(strings.ToUpper(strings.TrimSpace(result.Message.Content)))
	if len(answer) == 0 {
		return false, errors.New("локальная vision-модель не вернула результат")
	}
	switch strings.Trim(answer[0], ".!,:;\"'") {
	case "SENSITIVE":
		return true, nil
	case "SAFE":
		return false, nil
	default:
		return false, errors.New("локальная vision-модель вернула неопределённый результат")
	}
}

func cleanOutput(value string) string {
	value = strings.TrimSpace(value)
	if end := strings.LastIndex(value, "</think>"); end >= 0 {
		value = strings.TrimSpace(value[end+len("</think>"):])
	}
	value = strings.Trim(value, "\"` ")
	return strings.TrimSpace(value)
}

func joinPath(base, suffix string) string {
	return strings.TrimRight(base, "/") + suffix
}
