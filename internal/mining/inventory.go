package mining

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var ErrInventoryUnsupported = errors.New("mining inventory is not supported")

type Inventory struct {
	CollectedAt time.Time   `json:"collected_at"`
	Catalog     FileCatalog `json:"catalog"`
	States      []State     `json:"states"`
	Message     string      `json:"message,omitempty"`
}

func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	if !c.Configured() {
		return Inventory{}, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolve("/v1/inventory").String(), nil)
	if err != nil {
		return Inventory{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return Inventory{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented {
		return Inventory{}, ErrInventoryUnsupported
	}
	var result Inventory
	if err = json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&result); err != nil {
		return Inventory{}, err
	}
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("inventory returned HTTP %d", response.StatusCode)
	}
	if err := validateInventory(result, time.Now()); err != nil {
		return result, err
	}
	return result, nil
}

func validateInventory(result Inventory, now time.Time) error {
	if result.CollectedAt.IsZero() || now.Sub(result.CollectedAt) > 45*time.Second || result.CollectedAt.Sub(now) > 5*time.Second {
		return errors.New("inventory collection timestamp is missing or stale")
	}
	if result.States == nil {
		return errors.New("inventory process snapshot is missing")
	}
	seen := map[int]bool{}
	for _, state := range result.States {
		if !state.Available || state.ProcessName == "" || state.CollectedAt.IsZero() ||
			state.CollectedAt.Sub(result.CollectedAt) > 5*time.Second || result.CollectedAt.Sub(state.CollectedAt) > 5*time.Second ||
			(state.Running && len(state.PIDs) == 0) || (!state.Running && len(state.PIDs) != 0) {
			return errors.New("inventory contains an incomplete process state")
		}
		for _, pid := range state.PIDs {
			if pid <= 0 || seen[pid] {
				return errors.New("inventory contains invalid or duplicate process identifiers")
			}
			seen[pid] = true
		}
	}
	return nil
}
