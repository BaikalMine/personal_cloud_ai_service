package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func (c *Client) ComfyFileInventory(ctx context.Context, query ComfyFileInventoryQuery) (ComfyFileInventory, error) {
	var result ComfyFileInventory
	if query.Validate() != nil {
		return result, ErrComfyInventoryRequest
	}
	if !c.Configured() {
		return result, ErrUnavailable
	}
	endpoint := *c.baseURL
	endpoint.Path, endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "/v1/comfy-asset/inventory", "", query.Values().Encode(), ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	client := *c.cleanup
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return result, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return result, ErrComfyInventoryChanged
	}
	if response.StatusCode != http.StatusOK {
		return result, ErrUnavailable
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return result, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return ComfyFileInventory{}, errors.New("invalid ComfyUI file inventory response")
	}
	if err := ValidateComfyFileInventory(result, query); err != nil {
		return ComfyFileInventory{}, err
	}
	return result, nil
}
