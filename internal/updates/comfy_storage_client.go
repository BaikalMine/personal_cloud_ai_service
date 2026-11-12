package updates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// A changed endpoint or credential cannot settle an operation from the old host.
func (c *Client) ComfyStorageHostKey() string {
	if !c.Configured() {
		return ""
	}
	hash := sha256.Sum256([]byte(c.baseURL.String() + "\x00" + c.token))
	return hex.EncodeToString(hash[:])
}

func (c *Client) ComfyStorageStatus(ctx context.Context) (ComfyStorageStatus, error) {
	var status ComfyStorageStatus
	if err := c.comfyStorageCall(ctx, http.MethodGet, "/v1/comfy-storage", nil, &status); err != nil {
		return ComfyStorageStatus{}, err
	}
	if status.Protocol != ComfyStorageProtocol || !ComfyStorageHex(status.ExecutorID, 32) || !ComfyStorageHex(status.GuardJournalID, 16) || !status.GuardEnforced || status.PendingID != "" && !ValidComfyStorageID(status.PendingID) {
		return ComfyStorageStatus{}, ErrComfyStorageRequest
	}
	return status, nil
}

func (c *Client) ExecuteComfyStorage(ctx context.Context, request ComfyStorageRequest) (ComfyStorageResult, error) {
	return c.comfyStorageOperation(ctx, request, "execute")
}

func (c *Client) FenceComfyStorage(ctx context.Context, request ComfyStorageRequest) (ComfyStorageResult, error) {
	return c.comfyStorageOperation(ctx, request, "fence")
}

func (c *Client) ReadComfyStorage(ctx context.Context, request ComfyStorageRequest) (ComfyStorageResult, error) {
	return c.comfyStorageOperation(ctx, request, "read")
}

func (c *Client) comfyStorageOperation(ctx context.Context, input ComfyStorageRequest, action string) (ComfyStorageResult, error) {
	request, _, err := CanonicalComfyStorageRequest(input)
	if err != nil {
		return ComfyStorageResult{}, err
	}
	method, endpoint := http.MethodPost, "/v1/comfy-storage/"+action
	payload, _ := json.Marshal(request)
	if action == "read" {
		method, endpoint, payload = http.MethodGet, "/v1/comfy-storage/operations/"+request.ID, nil
	}
	var result ComfyStorageResult
	callErr := c.comfyStorageCall(ctx, method, endpoint, payload, &result)
	if result.Protocol == 0 && callErr != nil {
		return ComfyStorageResult{}, callErr
	}
	if err := MatchComfyStorageResult(result, request); err != nil {
		return ComfyStorageResult{}, err
	}
	return result, callErr
}

func (c *Client) comfyStorageCall(ctx context.Context, method, endpoint string, payload []byte, target any) error {
	if !c.Configured() {
		return ErrUnavailable
	}
	url := *c.baseURL
	url.Path, url.RawPath, url.RawQuery, url.Fragment, url.ForceQuery = endpoint, "", "", "", false
	request, err := http.NewRequestWithContext(ctx, method, url.String(), bytes.NewReader(payload))
	if err != nil {
		return ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	client := *c.cleanup
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return ErrComfyStorageRequest
	}
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Protocol int    `json:"protocol"`
			Error    string `json:"error"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&problem) == nil && decoder.Decode(new(any)) == io.EOF && problem.Protocol == 1 {
			switch {
			case response.StatusCode == http.StatusNotFound && problem.Error == "storage_operation_not_found":
				return ErrComfyStorageNotFound
			case response.StatusCode == http.StatusConflict && problem.Error == "storage_busy":
				return ErrComfyStorageBusy
			case response.StatusCode == http.StatusConflict && problem.Error == "storage_operation_conflict":
				return ErrComfyStorageConflict
			default:
				return ErrUnavailable
			}
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			return ErrUnavailable
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrComfyStorageRequest
	}
	if response.StatusCode != http.StatusOK {
		return errors.Join(ErrUnavailable, errors.New("ComfyUI storage guard closure remains unconfirmed"))
	}
	return nil
}
