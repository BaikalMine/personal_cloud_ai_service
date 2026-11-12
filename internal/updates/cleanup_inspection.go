package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func (c *Client) InspectComfyAssets(ctx context.Context, files []ComfyAssetFile) (ComfyAssetInspectResult, error) {
	if !c.Configured() {
		return ComfyAssetInspectResult{}, ErrUnavailable
	}
	if len(files) == 0 || len(files) > 50 {
		return ComfyAssetInspectResult{}, errors.New("invalid cleanup inspection batch")
	}
	payload, err := json.Marshal(ComfyAssetDeleteRequest{Files: files})
	if err != nil {
		return ComfyAssetInspectResult{}, err
	}
	target := *c.baseURL
	target.Path, target.RawPath, target.RawQuery, target.Fragment = "/v1/comfy-asset/inspect", "", "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return ComfyAssetInspectResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := *c.cleanup
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return ComfyAssetInspectResult{}, fmt.Errorf("%w: cleanup inspection transport", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return ComfyAssetInspectResult{}, fmt.Errorf("%w: cleanup inspection HTTP %d", ErrUnavailable, response.StatusCode)
	}
	var result ComfyAssetInspectResult
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes+1))
	if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF || !validComfyInspection(result, files) {
		return ComfyAssetInspectResult{}, errors.New("invalid cleanup inspection response")
	}
	if response.StatusCode != http.StatusOK || !result.Complete {
		return result, fmt.Errorf("%w: cleanup inspection incomplete", ErrUnavailable)
	}
	return result, nil
}

func validComfyInspection(result ComfyAssetInspectResult, files []ComfyAssetFile) bool {
	if result.Protocol != 1 || result.CheckedAt.IsZero() || len(result.Items) != len(files) {
		return false
	}
	for i, item := range result.Items {
		file := files[i]
		if item.Filename != file.Filename || item.Subfolder != file.Subfolder || item.StorageType != file.StorageType || item.SizeBytes != file.SizeBytes || item.SHA256 != file.SHA256 {
			return false
		}
		switch item.Status {
		case "matched", "missing", "mismatched", "rejected":
		case "io_error":
			if result.Complete {
				return false
			}
		default:
			return false
		}
	}
	return true
}
