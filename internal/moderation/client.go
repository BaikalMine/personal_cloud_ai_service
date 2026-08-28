package moderation

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

const maxImageBytes = 64 << 20

var ErrUnsupportedImage = errors.New("изображение не поддерживается NSFW-классификатором")

type Client struct {
	baseURL *url.URL
	http    *http.Client
}

type classifyResponse struct {
	NSFWScore float64 `json:"nsfw_score"`
}

func NewClient(baseURL *url.URL) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 75 * time.Second}}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != nil
}

func (c *Client) ClassifyImage(ctx context.Context, image []byte, mimeType string) (bool, error) {
	if !c.Configured() {
		return false, errors.New("локальный NSFW-классификатор не настроен")
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
	target.Path = strings.TrimRight(target.Path, "/") + "/classify"
	target.RawQuery = ""
	target.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(image))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", mimeType)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return false, fmt.Errorf("локальный NSFW-классификатор недоступен: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	if err != nil {
		return false, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("локальный NSFW-классификатор ответил HTTP %d", response.StatusCode)
	}
	var result classifyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("некорректный ответ локального NSFW-классификатора: %w", err)
	}
	if result.NSFWScore < 0 || result.NSFWScore > 1 {
		return false, errors.New("локальный NSFW-классификатор вернул недопустимую оценку")
	}
	// The lower threshold prevents accidental exposure. A user can reveal a
	// blurred result, but an unblurred sensitive image cannot be unseen.
	return result.NSFWScore >= 0.15, nil
}
