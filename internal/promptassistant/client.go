package promptassistant

import (
	"bytes"
	"context"
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
)

type Client struct {
	baseURL *url.URL
	model   string
	http    *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
	payload := chatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: SystemPromptWithReferences(mode, profile, references)},
			{Role: "user", Content: prompt},
		},
		Stream:    false,
		Think:     think,
		KeepAlive: "0",
	}
	payload.Options.Temperature = 0.35
	payload.Options.TopP = 0.9
	payload.Options.NumPredict = 700
	if think {
		payload.Options.NumPredict = 1400
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
	response, err := c.http.Do(request)
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
