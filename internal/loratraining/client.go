package loratraining

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const maxJSONResponseBytes = 4 << 20

var ErrUnavailable = errors.New("LoRA training agent is unavailable")

type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *HTTPError) Error() string {
	if err.Message != "" {
		return err.Message
	}
	return fmt.Sprintf("LoRA training agent returned HTTP %d", err.StatusCode)
}

type Client struct {
	baseURL  *url.URL
	token    string
	http     *http.Client
	upload   *http.Client
	artifact *http.Client
}

func NewClient(baseURL *url.URL, token string) *Client {
	return &Client{
		baseURL:  baseURL,
		token:    strings.TrimSpace(token),
		http:     &http.Client{Timeout: 15 * time.Second},
		upload:   &http.Client{Timeout: 15 * time.Minute},
		artifact: &http.Client{Timeout: 30 * time.Minute},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != nil && len(c.token) >= 32
}

func (c *Client) Profiles(ctx context.Context) (ProfilesResponse, error) {
	var response ProfilesResponse
	err := c.doJSON(c.http, ctx, http.MethodGet, "/v1/profiles", nil, "", &response)
	return response, err
}

func (c *Client) Submit(ctx context.Context, spec JobSpec, dataset io.Reader, datasetBytes int64) (JobStatus, error) {
	if !c.Configured() {
		return JobStatus{}, ErrUnavailable
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return JobStatus{}, err
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeErr := make(chan error, 1)
	go func() {
		defer close(writeErr)
		part, partErr := multipartWriter.CreateFormField("spec")
		if partErr == nil {
			_, partErr = part.Write(specJSON)
		}
		if partErr == nil {
			part, partErr = multipartWriter.CreateFormFile("dataset", "dataset.zip")
		}
		if partErr == nil {
			_, partErr = io.Copy(part, dataset)
		}
		if closeErr := multipartWriter.Close(); partErr == nil {
			partErr = closeErr
		}
		_ = writer.CloseWithError(partErr)
		writeErr <- partErr
	}()

	var response JobStatus
	err = c.doJSON(c.upload, ctx, http.MethodPost, "/v1/jobs", reader, multipartWriter.FormDataContentType(), &response)
	_ = reader.CloseWithError(err)
	if streamErr := <-writeErr; err == nil && streamErr != nil {
		err = streamErr
	}
	_ = datasetBytes
	return response, err
}

func (c *Client) Status(ctx context.Context, id string) (JobStatus, error) {
	var response JobStatus
	err := c.doJSON(c.http, ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, "", &response)
	return response, err
}

func (c *Client) Cancel(ctx context.Context, id string) (JobStatus, error) {
	var response JobStatus
	err := c.doJSON(c.http, ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/cancel", bytes.NewReader([]byte("{}")), "application/json", &response)
	return response, err
}

func (c *Client) Delete(ctx context.Context, id string) (JobStatus, error) {
	var response JobStatus
	err := c.doJSON(c.http, ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), nil, "", &response)
	return response, err
}

func (c *Client) Artifact(ctx context.Context, id string) (io.ReadCloser, string, int64, error) {
	if !c.Configured() {
		return nil, "", 0, ErrUnavailable
	}
	target := c.endpoint("/v1/jobs/" + url.PathEscape(id) + "/artifact")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", 0, err
	}
	c.authorize(request)
	response, err := c.artifact.Do(request)
	if err != nil {
		return nil, "", 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, "", 0, decodeHTTPError(response)
	}
	name := strings.TrimSpace(response.Header.Get("X-Artifact-Name"))
	size, _ := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	return response.Body, name, size, nil
}

func (c *Client) doJSON(client *http.Client, ctx context.Context, method, endpoint string, body io.Reader, contentType string, result any) error {
	if !c.Configured() {
		return ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(endpoint), body)
	if err != nil {
		return err
	}
	c.authorize(request)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeHTTPError(response)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxJSONResponseBytes)).Decode(result); err != nil {
		return fmt.Errorf("decode LoRA training agent response: %w", err)
	}
	return nil
}

func (c *Client) endpoint(endpoint string) string {
	target := *c.baseURL
	target.Path = path.Clean("/" + endpoint)
	target.RawQuery = ""
	target.Fragment = ""
	return target.String()
}

func (c *Client) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
}

func decodeHTTPError(response *http.Response) error {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, maxJSONResponseBytes)).Decode(&payload)
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = strings.TrimSpace(payload.Error)
	}
	return &HTTPError{StatusCode: response.StatusCode, Code: strings.TrimSpace(payload.Code), Message: message}
}
