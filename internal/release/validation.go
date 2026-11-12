package release

import (
	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

const ValidationTokenHeader = "X-Gateway-Release-Token"

// A validation report describes a restricted candidate, never a ready deployment.
// The pointer makes an omitted or null readiness value distinguishable from false.
type ValidationStatus struct {
	SchemaVersion   int                               `json:"schema_version"`
	Mode            string                            `json:"mode"`
	ProductionReady *bool                             `json:"production_ready"`
	Operation       database.RuntimeValidationRequest `json:"operation"`
	Gateway         buildinfo.Report                  `json:"gateway"`
}
