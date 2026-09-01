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
	user := a.currentUser(r)
	allManifests := workflowManifests()
	if err := validateWorkflowManifests(allManifests); err != nil {
		http.Error(w, "контракт workflow недоступен", http.StatusInternalServerError)
		return
	}
	manifests := workflowManifestsForUser(allManifests, user)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_ = json.NewEncoder(w).Encode(workflowManifestCatalog{SchemaVersion: workflowManifestSchemaVersion, Workflows: sortedWorkflowManifests(manifests)})
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
