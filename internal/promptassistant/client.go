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
	maxPromptBytes         = 16 << 10
	maxResponseBytes       = 32 << 10
	maxImageBytes          = 32 << 20
	defaultNumPredict      = 700
	thinkNumPredict        = 1400
	miniMaxNumPredict      = 1800
	miniMaxThinkNumPredict = 2600
)

var ErrUnsupportedImage = errors.New("изображение не поддерживается локальной vision-проверкой")

type Client struct {
	baseURL *url.URL
	model   string
	http    *http.Client
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
	Options   struct {
		Temperature float64 `json:"temperature"`
		TopP        float64 `json:"top_p"`
		NumPredict  int     `json:"num_predict"`
	} `json:"options"`
}

type chatResponse struct {
	Message Message `json:"message"`
}

func NewClient(baseURL *url.URL, model string) *Client {
	return &Client{
		baseURL: baseURL,
		model:   strings.TrimSpace(model),
		http: &http.Client{
			Timeout: 90 * time.Second,
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
	return c.enhance(ctx, mode, profile, prompt, references, VideoContext{}, think)
}

// EnhanceVideo uses the MiniMax H3 workflow facts to select a compatible
// Context-IR structure without giving the local LLM any image bytes.
func (c *Client) EnhanceVideo(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (string, error) {
	return c.enhance(ctx, mode, profile, prompt, references, video, think)
}

func (c *Client) enhance(ctx context.Context, mode Mode, profile Profile, prompt string, references []ImageReference, video VideoContext, think bool) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("введите исходный промт")
	}
	if len(prompt) > maxPromptBytes {
		return "", errors.New("промт слишком длинный")
	}
	if !c.Configured() {
		return "", errors.New("локальный промт-ассистент не настроен")
	}
	if !ValidProfile(mode, profile) {
		return "", errors.New("неизвестный шаблон промт-ассистента")
	}
	target := *c.baseURL
	target.Path = joinPath(target.Path, "/api/chat")
	target.RawQuery = ""
	target.Fragment = ""
	systemPrompt := SystemPromptWithReferences(mode, profile, references)
	if mode == ModeTextToVideo && profile == ProfileMiniMaxH3 {
		systemPrompt = SystemPromptWithVideoContextAndReferences(mode, profile, references, video)
	}
	payload := chatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream:    false,
		Think:     think,
		KeepAlive: "0",
	}
	for _, reference := range references {
		if len(reference.Image) == 0 {
			continue
		}
		if len(reference.Image) > maxImageBytes {
			return "", fmt.Errorf("%w: изображение %d слишком большое", ErrUnsupportedImage, reference.Number)
		}
		switch strings.ToLower(strings.TrimSpace(reference.MIMEType)) {
		case "image/jpeg", "image/png", "image/webp":
		default:
			return "", fmt.Errorf("%w: формат изображения %d", ErrUnsupportedImage, reference.Number)
		}
		payload.Messages[1].Images = append(payload.Messages[1].Images, base64.StdEncoding.EncodeToString(reference.Image))
	}
	payload.Options.Temperature = 0.35
	payload.Options.TopP = 0.9
	payload.Options.NumPredict = defaultNumPredict
	if mode == ModeTextToVideo && profile == ProfileMiniMaxH3 {
		payload.Options.Temperature = 0.3
		payload.Options.NumPredict = miniMaxNumPredict
	}
	if think {
		payload.Options.NumPredict = thinkNumPredict
		if mode == ModeTextToVideo && profile == ProfileMiniMaxH3 {
			payload.Options.NumPredict = miniMaxThinkNumPredict
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClientFor(mode, profile).Do(request)
	if err != nil {
		return "", fmt.Errorf("локальная модель недоступна: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(responseBody) > maxResponseBytes {
		return "", errors.New("локальная модель вернула слишком большой ответ")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("локальная модель ответила HTTP %d", response.StatusCode)
	}
	var result chatResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("некорректный ответ локальной модели: %w", err)
	}
	answer := cleanOutput(result.Message.Content)
	if answer == "" {
		return "", errors.New("локальная модель не вернула вариант промта")
	}
	return answer, nil
}

func (c *Client) httpClientFor(mode Mode, profile Profile) *http.Client {
	if mode != ModeTextToVideo || profile != ProfileMiniMaxH3 {
		return c.http
	}
	client := *c.http
	client.Timeout = 150 * time.Second
	if transport, ok := c.http.Transport.(*http.Transport); ok {
		videoTransport := transport.Clone()
		videoTransport.ResponseHeaderTimeout = 140 * time.Second
		client.Transport = videoTransport
	}
	return &client
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
