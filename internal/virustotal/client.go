package virustotal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://www.virustotal.com/api/v3"

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

type Analysis struct {
	ID         string
	Status     string
	Malicious  int
	Suspicious int
	Harmless   int
	Undetected int
	Timeout    int
}

func NewClient(apiKey string) *Client {
	return NewClientWithBaseURL(apiKey, defaultBaseURL, nil)
}

func NewClientWithBaseURL(apiKey, baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{apiKey: strings.TrimSpace(apiKey), baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

func (c *Client) Configured() bool {
	return c != nil && c.apiKey != ""
}

func (c *Client) SubmitURL(ctx context.Context, target string) (Analysis, error) {
	form := url.Values{"url": {target}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/urls"), strings.NewReader(form.Encode()))
	if err != nil {
		return Analysis{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.doAnalysis(request)
}

func (c *Client) SubmitJSON(ctx context.Context, filename string, payload []byte) (Analysis, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return Analysis{}, err
	}
	if _, err := part.Write(payload); err != nil {
		return Analysis{}, err
	}
	if err := writer.Close(); err != nil {
		return Analysis{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/files"), &body)
	if err != nil {
		return Analysis{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return c.doAnalysis(request)
}

func (c *Client) Analysis(ctx context.Context, id string) (Analysis, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/analyses/"+url.PathEscape(id)), http.NoBody)
	if err != nil {
		return Analysis{}, err
	}
	return c.doAnalysis(request)
}

func (c *Client) endpoint(path string) string {
	return c.baseURL + path
}

func (c *Client) doAnalysis(request *http.Request) (Analysis, error) {
	if !c.Configured() {
		return Analysis{}, fmt.Errorf("VirusTotal API key is not configured")
	}
	request.Header.Set("x-apikey", c.apiKey)
	response, err := c.http.Do(request)
	if err != nil {
		return Analysis{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Analysis{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Analysis{}, fmt.Errorf("VirusTotal returned %d", response.StatusCode)
	}
	var document struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				Status string `json:"status"`
				Stats  struct {
					Malicious  int `json:"malicious"`
					Suspicious int `json:"suspicious"`
					Harmless   int `json:"harmless"`
					Undetected int `json:"undetected"`
					Timeout    int `json:"timeout"`
				} `json:"stats"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return Analysis{}, fmt.Errorf("decode VirusTotal response: %w", err)
	}
	if document.Data.ID == "" {
		return Analysis{}, fmt.Errorf("VirusTotal response has no analysis identifier")
	}
	return Analysis{ID: document.Data.ID, Status: document.Data.Attributes.Status, Malicious: document.Data.Attributes.Stats.Malicious, Suspicious: document.Data.Attributes.Stats.Suspicious, Harmless: document.Data.Attributes.Stats.Harmless, Undetected: document.Data.Attributes.Stats.Undetected, Timeout: document.Data.Attributes.Stats.Timeout}, nil
}
