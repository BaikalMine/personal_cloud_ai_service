package updates

import "time"

const (
	ComponentGateway   = "gateway"
	ComponentComfyUI   = "comfyui"
	ComponentOpenWebUI = "openwebui"
)

type ComponentStatus struct {
	Name            string    `json:"name"`
	DisplayName     string    `json:"display_name"`
	Configured      bool      `json:"configured"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	CanInstall      bool      `json:"can_install"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	Message         string    `json:"message,omitempty"`
}

type Status struct {
	Available  bool              `json:"available"`
	Components []ComponentStatus `json:"components,omitempty"`
	Message    string            `json:"message,omitempty"`
}

type Request struct {
	Components []string `json:"components,omitempty"`
}

// ComfyAssetFile identifies one tracked ComfyUI input or archived output. The
// agent checks every field against the local file before deleting it.
type ComfyAssetFile struct {
	Filename    string `json:"filename"`
	Subfolder   string `json:"subfolder,omitempty"`
	StorageType string `json:"storage_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type ComfyOutputFile = ComfyAssetFile

type ComfyAssetDeleteRequest struct {
	Files []ComfyAssetFile `json:"files"`
}

type ComfyAssetDeleteResult struct {
	Deleted    int                       `json:"deleted"`
	Missing    int                       `json:"missing"`
	Mismatched int                       `json:"mismatched"`
	Rejected   int                       `json:"rejected"`
	Items      []ComfyAssetDeleteOutcome `json:"items,omitempty"`
}

type ComfyAssetDeleteOutcome struct {
	Filename    string `json:"filename"`
	Subfolder   string `json:"subfolder,omitempty"`
	StorageType string `json:"storage_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	Status      string `json:"status"`
}

type ComfyOutputDeleteRequest = ComfyAssetDeleteRequest
type ComfyOutputDeleteResult = ComfyAssetDeleteResult

func ValidComponent(name string) bool {
	switch name {
	case ComponentGateway, ComponentComfyUI, ComponentOpenWebUI:
		return true
	default:
		return false
	}
}
