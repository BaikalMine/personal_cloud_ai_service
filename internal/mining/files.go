package mining

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type LaunchFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Kind   string `json:"kind"`
}

type FileCatalog struct {
	Root    string       `json:"root"`
	Files   []LaunchFile `json:"files"`
	Message string       `json:"message,omitempty"`
}

type SaveScriptRequest struct {
	Request
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

func (c *Client) Files(ctx context.Context) (FileCatalog, error) {
	var result FileCatalog
	err := c.fileRequest(ctx, http.MethodGet, "/v1/files", nil, &result)
	return result, err
}

func (c *Client) SaveScript(ctx context.Context, input SaveScriptRequest) (Script, error) {
	var result Script
	body, err := json.Marshal(input)
	if err == nil {
		err = c.fileRequest(ctx, http.MethodPost, "/v1/script/save", body, &result)
	}
	return result, err
}

func (c *Client) fileRequest(ctx context.Context, method, path string, body []byte, result any) error {
	if !c.Configured() {
		return ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, method, c.resolve(path).String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented {
		return fmt.Errorf("для выбора и редактирования BAT необходимо обновить агент майнинга")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(result); err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("mining agent returned %s", response.Status)
	}
	return nil
}
