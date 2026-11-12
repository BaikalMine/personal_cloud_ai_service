package modelmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("model manager unavailable")
var ErrInvalid = errors.New("invalid model manager request")
var ErrNotFound = errors.New("model manager record not found")
var ErrRateLimited = errors.New("model manager rate limited")
var ErrUnsupportedType = errors.New("unsupported model type")

type CatalogMismatchError struct {
	Kind string
}

func (e *CatalogMismatchError) Error() string { return "model belongs to a different catalog" }
func (e *CatalogMismatchError) Unwrap() error { return ErrInvalid }

type Client struct {
	Base *url.URL
	Auth string
}
type Item struct {
	Name     string   `json:"model_name"`
	Filename string   `json:"file_name"`
	Path     string   `json:"file_path"`
	Preview  string   `json:"preview_url"`
	NSFW     int      `json:"preview_nsfw_level"`
	Base     string   `json:"base_model"`
	Size     int64    `json:"file_size"`
	Hash     string   `json:"sha256"`
	Tags     []string `json:"tags"`
	Notes    string   `json:"notes"`
	Tips     string   `json:"usage_tips"`
	Exclude  bool     `json:"exclude"`
	Update   bool     `json:"update_available"`
	Civitai  Version  `json:"civitai"`
}
type Catalog struct {
	Items []Item `json:"items"`
	Total int    `json:"total"`
	Page  int    `json:"page"`
	Pages int    `json:"total_pages"`
}
type File struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	SizeKB   float64 `json:"sizeKB"`
	Primary  bool    `json:"primary"`
	Metadata struct {
		Format string `json:"format"`
		Size   string `json:"size"`
		FP     string `json:"fp"`
	} `json:"metadata"`
	Hashes struct {
		SHA256 string `json:"SHA256"`
	} `json:"hashes"`
}
type Version struct {
	ID          int64    `json:"id"`
	ModelID     int64    `json:"modelId"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Base        string   `json:"baseModel"`
	Words       []string `json:"trainedWords"`
	Files       []File   `json:"files"`
	Exists      bool     `json:"existsLocally"`
	Model       struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"model"`
}
type Versions struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Type        string    `json:"type"`
	Versions    []Version `json:"modelVersions"`
}
type Settings struct {
	KeySet         bool   `json:"civitai_api_key_set"`
	LoraRoot       string `json:"default_lora_root"`
	CheckpointRoot string `json:"default_checkpoint_root"`
	UNetRoot       string `json:"default_unet_root"`
}
type Download struct {
	ID           string            `json:"download_id"`
	VersionID    int64             `json:"model_version_id"`
	Root         string            `json:"model_root"`
	RelativePath string            `json:"relative_path"`
	FileParams   map[string]string `json:"file_params"`
}
type Result struct {
	Success         bool     `json:"success"`
	Cancelled       bool     `json:"cancelled"`
	Status          string   `json:"status"`
	Progress        float64  `json:"progress"`
	BytesDownloaded *int64   `json:"bytes_downloaded"`
	TotalBytes      *int64   `json:"total_bytes"`
	BytesPerSecond  *float64 `json:"bytes_per_second"`
}

func Kind(s string) bool { return s == "loras" || s == "checkpoints" }

// Never follow upstream redirects with internal credentials or expose upstream error bodies.
func (c *Client) request(ctx context.Context, method, path string, query url.Values, payload any) (*http.Response, error) {
	if c == nil || c.Base == nil {
		return nil, ErrUnavailable
	}
	u := *c.Base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = query.Encode()
	u.Fragment = ""
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, ErrInvalid
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, ErrInvalid
	}
	if c.Auth != "" {
		req.Header.Set("Authorization", c.Auth)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		if response.StatusCode == http.StatusTooManyRequests {
			return nil, ErrRateLimited
		}
		if response.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		if response.StatusCode == http.StatusBadRequest && method == http.MethodGet &&
			(strings.HasPrefix(path, "/api/lm/loras/civitai/versions/") || strings.HasPrefix(path, "/api/lm/checkpoints/civitai/versions/")) {
			return nil, catalogResponseError(response.Body)
		}
		return nil, ErrUnavailable
	}
	return response, nil
}

// Only recognize the manager's type-mismatch response; never return its raw error text.
func catalogResponseError(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, 4097))
	if err != nil || len(raw) > 4096 {
		return ErrUnavailable
	}
	var result struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &result) != nil || !strings.HasPrefix(result.Error, "Model type mismatch. Expected ") {
		return ErrUnavailable
	}
	_, actual, ok := strings.Cut(result.Error, ", got ")
	if !ok || actual == "" || strings.ContainsAny(actual, "\r\n") {
		return ErrUnavailable
	}
	switch actual {
	case "Checkpoint":
		return &CatalogMismatchError{Kind: "checkpoints"}
	case "LORA", "LoCon", "DoRA", "DORA":
		return &CatalogMismatchError{Kind: "loras"}
	default:
		return ErrUnsupportedType
	}
}
func (c *Client) JSON(ctx context.Context, method, path string, query url.Values, payload, out any) error {
	response, err := c.request(ctx, method, path, query, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return ErrUnavailable
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) List(ctx context.Context, kind, search, base string, page int) (Catalog, error) {
	return c.ListFiltered(ctx, kind, search, base, page, false)
}

func (c *Client) BaseModels(ctx context.Context, kind string) ([]string, error) {
	if !Kind(kind) {
		return nil, ErrInvalid
	}
	var out struct {
		Success bool `json:"success"`
		Models  []struct {
			Name string `json:"name"`
		} `json:"base_models"`
	}
	if err := c.JSON(ctx, "GET", "/api/lm/"+kind+"/base-models", url.Values{"limit": {"0"}}, nil, &out); err != nil {
		return nil, err
	}
	if !out.Success || out.Models == nil {
		return nil, ErrUnavailable
	}
	names := make([]string, 0, len(out.Models))
	for _, model := range out.Models {
		if model.Name != "" && len(model.Name) <= 100 {
			names = append(names, model.Name)
		}
	}
	return names, nil
}
func (c *Client) ListFiltered(ctx context.Context, kind, search, base string, page int, updatesOnly bool) (Catalog, error) {
	var out Catalog
	if !Kind(kind) || page < 1 || page > 10000 || len(search) > 200 || len(base) > 100 {
		return out, ErrInvalid
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {"24"}}
	if search != "" {
		query.Set("search", search)
	}
	if base != "" {
		query.Set("base_model", base)
	}
	if updatesOnly {
		query.Set("update_available_only", "true")
	}
	err := c.JSON(ctx, "GET", "/api/lm/"+kind+"/list", query, nil, &out)
	if out.Items == nil {
		out.Items = []Item{}
	}
	return out, err
}
func (c *Client) CheckUpdate(ctx context.Context, kind string, modelID int64) (bool, error) {
	if !Kind(kind) || modelID <= 0 {
		return false, ErrInvalid
	}
	started := float64(time.Now().Unix()) - 2
	var out struct {
		Success bool `json:"success"`
		Records []struct {
			ModelID   int64 `json:"modelId"`
			HasUpdate bool  `json:"hasUpdate"`
		} `json:"records"`
	}
	err := c.JSON(ctx, "POST", "/api/lm/"+kind+"/updates/refresh", nil, map[string]any{"modelIds": []int64{modelID}, "force": true}, &out)
	if err != nil {
		return false, err
	}
	if !out.Success {
		return false, ErrUnavailable
	}
	// Refresh may silently retain stale records on individual provider failures.
	var status struct {
		Success bool `json:"success"`
		Record  struct {
			ModelID       int64   `json:"modelId"`
			HasUpdate     bool    `json:"hasUpdate"`
			LastCheckedAt float64 `json:"lastCheckedAt"`
		} `json:"record"`
	}
	if err := c.JSON(ctx, "GET", "/api/lm/"+kind+"/updates/status/"+strconv.FormatInt(modelID, 10), nil, nil, &status); err != nil {
		return false, err
	}
	if !status.Success || status.Record.ModelID != modelID || status.Record.LastCheckedAt < started {
		return false, ErrUnavailable
	}
	return status.Record.HasUpdate, nil
}
func (c *Client) Description(ctx context.Context, kind, path string) (string, error) {
	if !Kind(kind) || path == "" {
		return "", ErrInvalid
	}
	var out struct {
		Description string `json:"description"`
	}
	err := c.JSON(ctx, "GET", "/api/lm/"+kind+"/model-description", url.Values{"file_path": {path}}, nil, &out)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return PlainText(out.Description), err
}
func (c *Client) LocalVersion(ctx context.Context, kind, path string) (Version, error) {
	if !Kind(kind) || path == "" {
		return Version{}, ErrInvalid
	}
	var out struct {
		Success  bool    `json:"success"`
		Metadata Version `json:"metadata"`
	}
	err := c.JSON(ctx, "GET", "/api/lm/"+kind+"/metadata", url.Values{"file_path": {path}}, nil, &out)
	if errors.Is(err, ErrNotFound) {
		return Version{}, nil
	}
	if err != nil {
		return Version{}, err
	}
	if !out.Success {
		return Version{}, ErrUnavailable
	}
	out.Metadata.Description = PlainText(out.Metadata.Description)
	return out.Metadata, nil
}
func (c *Client) Settings(ctx context.Context) (Settings, error) {
	var out struct {
		Settings Settings `json:"settings"`
	}
	err := c.JSON(ctx, "GET", "/api/lm/settings", nil, nil, &out)
	return out.Settings, err
}
func (c *Client) SetKey(ctx context.Context, key string) error {
	if len(key) < 8 || len(key) > 512 || strings.ContainsAny(key, "\r\n\x00 ") {
		return ErrInvalid
	}
	var out Result
	err := c.JSON(ctx, "POST", "/api/lm/settings", nil, map[string]string{"civitai_api_key": key}, &out)
	if err == nil && !out.Success {
		return ErrUnavailable
	}
	return err
}
func (c *Client) Versions(ctx context.Context, kind string, id int64) (Versions, error) {
	var out Versions
	if !Kind(kind) || id <= 0 {
		return out, ErrInvalid
	}
	err := c.JSON(ctx, "GET", "/api/lm/"+kind+"/civitai/versions/"+strconv.FormatInt(id, 10), nil, nil, &out.Versions)
	if err != nil || len(out.Versions) == 0 {
		return out, err
	}
	var info Version
	if err = c.JSON(ctx, "GET", "/api/lm/"+kind+"/civitai/model/version/"+strconv.FormatInt(out.Versions[0].ID, 10), nil, nil, &info); err != nil {
		return out, err
	}
	if info.ModelID != id {
		return out, ErrUnavailable
	}
	out.Name, out.Type, out.Description = info.Model.Name, info.Model.Type, info.Model.Description
	if out.Description == "" {
		out.Description = info.Description
	}
	out.Description = PlainText(out.Description)
	for i := range out.Versions {
		out.Versions[i].Description = PlainText(out.Versions[i].Description)
	}
	return out, err
}
func (c *Client) Start(ctx context.Context, d Download) (Result, error) {
	var out Result
	err := c.JSON(ctx, "POST", "/api/lm/download-model", nil, d, &out)
	return out, err
}
func (c *Client) Progress(ctx context.Context, id string) (Result, error) {
	var out Result
	err := c.JSON(ctx, "GET", "/api/lm/download-progress/"+url.PathEscape(id), nil, nil, &out)
	return out, err
}
func (c *Client) Preview(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path != "/api/lm/previews" || u.Query().Get("path") == "" {
		return nil, "", ErrInvalid
	}
	resp, err := c.request(ctx, "GET", u.Path, u.Query(), nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, "", ErrUnavailable
	}
	contentType := http.DetectContentType(data)
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/webp" && contentType != "image/gif" {
		return nil, "", ErrInvalid
	}
	return data, contentType, nil
}
func ReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 20*time.Second)
}

// Descriptions are rendered as text, never trusted HTML from a model author.
func PlainText(raw string) string {
	d := xml.NewDecoder(strings.NewReader(raw))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	var out strings.Builder
	skip := 0
	for {
		token, err := d.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "script" || t.Name.Local == "style" {
				skip++
			}
			if skip == 0 && (t.Name.Local == "p" || t.Name.Local == "br" || t.Name.Local == "li") {
				out.WriteString("\n")
			}
		case xml.EndElement:
			if (t.Name.Local == "script" || t.Name.Local == "style") && skip > 0 {
				skip--
			}
		case xml.CharData:
			if skip == 0 {
				out.Write(t)
			}
		}
	}
	return strings.TrimSpace(out.String())
}
