package gateway

import (
	"encoding/json"
	"net/http"
)

func (a *App) handleGenerationCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	catalog, err := GenerationCapabilitiesForUser(a.currentUser(r))
	if err != nil {
		http.Error(w, "контракт workflow недоступен", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_ = json.NewEncoder(w).Encode(catalog)
}

// GenerationCapabilitiesForUser shares the real manifest with the isolated UI preview.
func GenerationCapabilitiesForUser(user *User) (workflowManifestCatalog, error) {
	allManifests := workflowManifests()
	if err := validateWorkflowManifests(allManifests); err != nil {
		return workflowManifestCatalog{}, err
	}
	manifests := workflowManifestsForUser(allManifests, user)
	return workflowManifestCatalog{SchemaVersion: workflowManifestSchemaVersion, Workflows: sortedWorkflowManifests(manifests)}, nil
}

func workflowManifestsForUser(manifests []workflowManifest, user *User) []workflowManifest {
	if user == nil {
		return nil
	}
	result := make([]workflowManifest, 0, len(manifests))
	for _, manifest := range manifests {
		if user.CanUseQuickGenerationType(manifest.TemplateID) {
			result = append(result, manifest)
		}
	}
	return result
}
