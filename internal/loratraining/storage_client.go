package loratraining

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Operations stay bound to the configured host and credential. Changing either
// requires recovery against the original endpoint, never a new-host fallback.
func (c *Client) StorageHostKey() string {
	if !c.Configured() {
		return ""
	}
	hash := sha256.Sum256([]byte(c.baseURL.String() + "\x00" + c.token))
	return hex.EncodeToString(hash[:])
}

func (c *Client) StorageInventory(ctx context.Context, cursor string, limit int) (StorageInventory, error) {
	if limit < 1 || limit > 100 || cursor != "" && !ValidStorageTargetID(cursor) {
		return StorageInventory{}, ErrStorageRequest
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("after", cursor)
	}
	var result StorageInventory
	if err := c.storageJSON(ctx, http.MethodGet, "/v1/storage", query, nil, &result); err != nil {
		return StorageInventory{}, err
	}
	if err := ValidateStorageInventory(result); err != nil {
		return StorageInventory{}, err
	}
	previous := cursor
	for _, item := range result.Items {
		if item.ID <= previous {
			return StorageInventory{}, ErrStorageRequest
		}
		previous = item.ID
	}
	if len(result.Items) > limit {
		return StorageInventory{}, ErrStorageRequest
	}
	return result, nil
}

func (c *Client) PreviewStorageCleanup(ctx context.Context, ids []string) (StorageInventory, error) {
	if err := ValidateStorageIDs(ids); err != nil {
		return StorageInventory{}, err
	}
	var result StorageInventory
	if err := c.storageJSON(ctx, http.MethodPost, "/v1/storage/preview", nil, StoragePreviewRequest{IDs: ids}, &result); err != nil {
		return StorageInventory{}, err
	}
	if err := ValidateStorageInventory(result); err != nil {
		return StorageInventory{}, err
	}
	if len(result.Items) != len(ids) || result.TotalItems != len(ids) || result.NextCursor != "" {
		return StorageInventory{}, ErrStorageRequest
	}
	for i, item := range result.Items {
		if item.ID != ids[i] {
			return StorageInventory{}, ErrStorageRequest
		}
	}
	return result, nil
}

func (c *Client) CleanupStorage(ctx context.Context, input StorageCleanupRequest) (StorageCleanupResult, error) {
	request, hash, err := CanonicalStorageCleanupRequest(input)
	if err != nil {
		return StorageCleanupResult{}, err
	}
	var result StorageCleanupResult
	if err := c.storageJSON(ctx, http.MethodPost, "/v1/storage/cleanup", nil, request, &result); err != nil {
		return StorageCleanupResult{}, err
	}
	if err := ValidateStorageCleanupResult(result); err != nil {
		return StorageCleanupResult{}, err
	}
	if result.ID != request.ID || result.RequestHash != hash {
		return StorageCleanupResult{}, ErrStorageRequest
	}
	return result, nil
}

// Recovery is read-only. Missing/old endpoints never trigger a new cleanup.
func (c *Client) StorageCleanupResult(ctx context.Context, id string) (StorageCleanupResult, error) {
	if !ValidStorageOperationID(id) {
		return StorageCleanupResult{}, ErrStorageRequest
	}
	var result StorageCleanupResult
	if err := c.storageJSON(ctx, http.MethodGet, "/v1/storage/operations/"+id, nil, nil, &result); err != nil {
		return StorageCleanupResult{}, err
	}
	if err := ValidateStorageCleanupResult(result); err != nil {
		return StorageCleanupResult{}, err
	}
	if result.ID != id {
		return StorageCleanupResult{}, ErrStorageRequest
	}
	return result, nil
}

func (c *Client) FenceStorageCleanup(ctx context.Context, input StorageCleanupRequest) (StorageCleanupFenceResult, error) {
	request, hash, err := CanonicalStorageCleanupRequest(input)
	if err != nil {
		return StorageCleanupFenceResult{}, err
	}
	var result StorageCleanupFenceResult
	if err := c.storageJSON(ctx, http.MethodPost, "/v1/storage/fence", nil, request, &result); err != nil {
		return StorageCleanupFenceResult{}, err
	}
	if result.ProtocolVersion != StorageProtocolVersion || !result.Fenced || !result.Settled || result.Result.State == "running" || result.Result.State == "unconfirmed" || result.Result.ID != request.ID || result.Result.RequestHash != hash || ValidateStorageCleanupResult(result.Result) != nil {
		return StorageCleanupFenceResult{}, ErrStorageRequest
	}
	return result, nil
}

func (c *Client) storageJSON(ctx context.Context, method, endpoint string, query url.Values, input, output any) error {
	if !c.Configured() {
		return ErrUnavailable
	}
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	target := c.endpoint(endpoint)
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	c.authorize(request)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := *c.http
	client.Timeout = 2 * time.Minute
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeHTTPError(response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxJSONResponseBytes {
		return ErrStorageRequest
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return fmt.Errorf("decode training storage response: %w", err)
	}
	return nil
}
