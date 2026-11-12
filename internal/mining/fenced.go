package mining

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var ErrCommandNeedsStop = errors.New("previous miner operation requires confirmed stop")

type CommandSession struct {
	Protocol int    `json:"protocol"`
	Session  string `json:"session"`
	Required bool   `json:"required"`
}

type FencedCommand struct {
	Session  string         `json:"session"`
	Revision int64          `json:"revision"`
	Action   string         `json:"action"`
	Miner    Request        `json:"miner"`
	Update   *UpdateRequest `json:"update,omitempty"`
}

type CommandResult struct {
	Session  string        `json:"session"`
	Revision int64         `json:"revision"`
	State    State         `json:"state"`
	Update   *UpdateResult `json:"update,omitempty"`
}

// A new session invalidates every command created before an agent restart.
// The caller allocates revisions durably and serializes admission with starts.
func (c *Client) Fenced(ctx context.Context, command FencedCommand) (CommandResult, error) {
	if !c.Configured() {
		return CommandResult{}, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolve("/v2/session").String(), nil)
	if err != nil {
		return CommandResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	probeClient := *c.http
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := probeClient.Do(request)
	if err != nil {
		return CommandResult{}, err
	}
	var session CommandSession
	err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&session)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || session.Protocol != 2 || !session.Required || len(session.Session) != 32 {
		return CommandResult{}, errors.New("mining agent requires coordinated command protocol 2")
	}
	command.Session = session.Session
	payload, err := json.Marshal(command)
	if err != nil {
		return CommandResult{}, err
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, c.resolve("/v2/command").String(), bytes.NewReader(payload))
	if err != nil {
		return CommandResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	client := c.commandHTTP
	if command.Action == "update" {
		client = c.updateHTTP
	}
	// Never redirect/replay a command to a different executor.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err = bounded.Do(request)
	if err != nil {
		return CommandResult{}, err
	}
	defer response.Body.Close()
	var result CommandResult
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return result, err
	}
	if len(body) > maxResponseBytes {
		return result, errors.New("mining command response too large")
	}
	if response.StatusCode == http.StatusConflict && string(body) == ErrCommandNeedsStop.Error()+"\n" {
		return result, ErrCommandNeedsStop
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return result, fmt.Errorf("mining command unconfirmed: HTTP %d (invalid response)", response.StatusCode)
	}
	if response.StatusCode != 200 || result.Session != session.Session || result.Revision != command.Revision {
		if result.Session == session.Session && result.Revision == command.Revision && result.State.Message != "" {
			message := strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return ' '
				}
				return r
			}, result.State.Message)
			if len(message) > 2048 {
				message = message[:2048]
			}
			return result, fmt.Errorf("mining command unconfirmed: HTTP %d: %s", response.StatusCode, message)
		}
		return result, fmt.Errorf("mining command unconfirmed: HTTP %d", response.StatusCode)
	}
	if !result.State.Available || (command.Action == "update" && (result.Update == nil || !result.Update.Success)) {
		return result, errors.New("mining command result is incomplete")
	}
	return result, nil
}
