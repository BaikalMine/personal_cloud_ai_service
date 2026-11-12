package loratraining

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

var inspectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

type ArtifactInspectionRequest struct {
	Models []string `json:"models"`
}
type ModelInspection struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Architecture string `json:"architecture,omitempty"`
}
type ArtifactInspection struct {
	Version       int                      `json:"version"`
	AgentJobID    string                   `json:"agent_job_id"`
	GatewayJobID  string                   `json:"gateway_job_id"`
	Owner         string                   `json:"owner"`
	Family        string                   `json:"family"`
	ProfileSHA256 string                   `json:"profile_sha256,omitempty"`
	RelativeName  string                   `json:"relative_name,omitempty"`
	Artifact      *domain.LoraFileIdentity `json:"artifact,omitempty"`
	State         string                   `json:"state"`
	CheckedAt     time.Time                `json:"checked_at"`
	Models        []ModelInspection        `json:"models"`
}

func (request ArtifactInspectionRequest) Valid() bool {
	if len(request.Models) > MaxModelInspectionCandidates {
		return false
	}
	seen := map[string]bool{}
	for _, model := range request.Models {
		name, ok := ModelRelativeName(model)
		if !ok || seen[strings.ToLower(name)] {
			return false
		}
		seen[strings.ToLower(name)] = true
	}
	return true
}

func (c *Client) InspectArtifact(ctx context.Context, id string, models []string) (ArtifactInspection, error) {
	var result ArtifactInspection
	request := ArtifactInspectionRequest{Models: models}
	if !request.Valid() || !inspectionIDPattern.MatchString(id) {
		return result, errors.New("invalid artifact inspection")
	}
	if !c.Configured() {
		return result, ErrUnavailable
	}
	data, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	// This POST only inspects files. It cannot enqueue training or generation.
	err = c.doJSON(&http.Client{Timeout: 45 * time.Second}, ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/inspect", bytes.NewReader(data), "application/json", &result)
	if err != nil {
		return result, err
	}
	if result.Version != 1 || result.AgentJobID != id || result.GatewayJobID == "" || result.CheckedAt.IsZero() || len(result.Models) > len(models) {
		return ArtifactInspection{}, errors.New("mismatched artifact inspection")
	}
	requested, seen := map[string]bool{}, map[string]bool{}
	for _, value := range models {
		name, _ := ModelRelativeName(value)
		requested[name] = true
	}
	for _, model := range result.Models {
		if !requested[model.Name] || seen[model.Name] || model.State != "compatible" && model.State != "incompatible" && model.State != "unavailable" && model.State != "unsupported" {
			return ArtifactInspection{}, errors.New("mismatched model inspection")
		}
		seen[model.Name] = true
	}
	if result.State == "verified" && (result.Artifact == nil || !result.Artifact.Valid() || len(result.Models) != len(models)) {
		return ArtifactInspection{}, errors.New("incomplete artifact inspection")
	}
	return result, nil
}
