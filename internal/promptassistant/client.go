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
	maxPromptBytes   = 16 << 10
	maxResponseBytes = 32 << 10
	maxImageBytes    = 32 << 20
)

var ErrUnsupportedImage = errors.New("изображение не поддерживается локальной vision-проверкой")

type Client struct {
	baseURL *url.URL
	model   string
	http    *http.Client
	policy  ModelPolicy
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
	Format    string    `json:"format,omitempty"`
	Options   struct {
		Temperature float64 `json:"temperature"`
		TopP        float64 `json:"top_p"`
		NumPredict  int     `json:"num_predict"`
	} `json:"options"`
}

type chatResponse struct {
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
	return &Client{
		baseURL: baseURL,
		model:   strings.TrimSpace(model),
		policy:  policy,
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

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != nil && c.model != ""
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
	return c.enhance(ctx, mode, profile, prompt, references, VideoContext{}, think)
}

func (c *Client) EnhanceVideoResult(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (Result, error) {
	return c.enhance(ctx, mode, profile, prompt, references, video, think)
}

func (c *Client) PolicyFor(mode Mode, profile Profile, think bool) RequestPolicy {
	if c == nil {
		return DefaultModelPolicy().request(mode, profile, think)
	}
	return c.policy.request(mode, profile, think)
}

func (c *Client) enhance(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{}, errors.New("введите исходный промт")
	}
	if len(prompt) > maxPromptBytes {
		return Result{}, errors.New("промт слишком длинный")
	}
	if !c.Configured() {
		return Result{}, errors.New("локальный промт-ассистент не настроен")
	}
	if !ValidProfile(mode, profile) {
		return Result{}, errors.New("неизвестный шаблон промт-ассистента")
	}
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) && !MiniMaxH3ProfileMatchesMode(profile, video.Mode) {
		return Result{}, errors.New("шаблон промт-ассистента не соответствует ветке MiniMax H3")
	}
	target := *c.baseURL
	target.Path = joinPath(target.Path, "/api/chat")
	target.RawQuery = ""
	target.Fragment = ""
	systemPrompt := SystemPromptWithReferences(mode, profile, references)
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) {
		systemPrompt = SystemPromptWithVideoContextAndReferences(mode, profile, references, video)
	}
	systemPrompt += structuredResponseInstruction(mode, references, video)
	requestPolicy := c.PolicyFor(mode, profile, think)
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
	for _, reference := range references {
		if len(reference.Image) == 0 {
			continue
		}
		if len(reference.Image) > maxImageBytes {
			return Result{}, fmt.Errorf("%w: изображение %d слишком большое", ErrUnsupportedImage, reference.Number)
		}
		switch strings.ToLower(strings.TrimSpace(reference.MIMEType)) {
		case "image/jpeg", "image/png", "image/webp":
		default:
			return Result{}, fmt.Errorf("%w: формат изображения %d", ErrUnsupportedImage, reference.Number)
		}
		payload.Messages[1].Images = append(payload.Messages[1].Images, base64.StdEncoding.EncodeToString(reference.Image))
	}
	payload.Options.Temperature = 0.35
	payload.Options.TopP = 0.9
	payload.Options.NumPredict = requestPolicy.NumPredict
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) {
		payload.Options.Temperature = 0.3
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClientFor(requestPolicy).Do(request)
	if err != nil {
		return Result{Policy: requestPolicy}, fmt.Errorf("локальная модель недоступна: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Result{Policy: requestPolicy}, err
	}
	if len(responseBody) > maxResponseBytes {
		return Result{Policy: requestPolicy}, errors.New("локальная модель вернула слишком большой ответ")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Result{Policy: requestPolicy}, fmt.Errorf("локальная модель ответила HTTP %d", response.StatusCode)
	}
	var result chatResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return Result{Policy: requestPolicy}, fmt.Errorf("некорректный ответ локальной модели: %w", err)
	}
	parsed, err := parseModelResult(result.Message.Content, mode, references, video)
	if err != nil {
		return Result{Policy: requestPolicy}, err
	}
	parsed.Policy = requestPolicy
	parsed.Usage = Usage{
		PromptTokens: result.PromptEvalCount, CompletionTokens: result.EvalCount,
		TotalDurationMS: durationMilliseconds(result.TotalDuration), LoadDurationMS: durationMilliseconds(result.LoadDuration),
		PromptDurationMS: durationMilliseconds(result.PromptEvalDuration), CompletionTimeMS: durationMilliseconds(result.EvalDuration),
		DoneReason: strings.TrimSpace(result.DoneReason),
	}
	return parsed, nil
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
	if !c.Configured() {
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
		Model: c.model,
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
	response, err := c.http.Do(request)
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
	if len(value) > maxPromptBytes {
		value = value[:maxPromptBytes]
	}
	return strings.TrimSpace(value)
}

func joinPath(base, suffix string) string {
	return strings.TrimRight(base, "/") + suffix
}
