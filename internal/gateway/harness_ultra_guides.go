package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"ai-access-gateway/internal/promptassistant"
)

type harnessPromptGuideQuery struct {
	Mode       string                  `json:"mode,omitempty"`
	Profile    promptassistant.Profile `json:"profile,omitempty"`
	CFG        *float64                `json:"cfg,omitempty"`
	References []struct {
		Number int                                `json:"number"`
		Role   promptassistant.ImageReferenceRole `json:"role"`
	} `json:"references,omitempty"`
}

func (a *App) harnessUltraPromptGuide(w http.ResponseWriter, r *http.Request) {
	a.harnessImagePromptGuide(w, r, "krea2")
}

func (a *App) harnessImagePromptGuide(w http.ResponseWriter, r *http.Request, family string) {
	var query harnessPromptGuideQuery
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&query) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid prompt guide request", http.StatusBadRequest)
		return
	}
	if query.Mode == "" {
		query.Mode = "text"
	}
	if family == modelFamilyMoss && (query.Mode != "text" || len(query.References) != 0) {
		http.Error(w, "MOSS supports text-to-image only", http.StatusBadRequest)
		return
	}
	mode := promptassistant.ModeImageToImage
	switch query.Mode {
	case "text":
		mode = promptassistant.ModeTextToImage
	case "references", "identity", "control", "inpaint", "outpaint":
	default:
		http.Error(w, "unknown Krea2 mode", http.StatusBadRequest)
		return
	}
	profile := query.Profile
	if profile == "" {
		profile = promptassistant.ProfileWorkflowDefault
	}
	if mode == promptassistant.ModeImageToImage {
		profile = imageEditAssistantProfile(kreaProPresetID, profile)
	}
	if !promptassistant.ValidProfile(mode, profile) || profile == promptassistant.ProfileFluxEdit || profile == promptassistant.ProfileQwen21Edit {
		http.Error(w, "unknown Krea2 assistant profile", http.StatusBadRequest)
		return
	}
	cfg := 2.0
	if family == modelFamilyMoss {
		cfg = 3
	}
	if query.CFG != nil {
		cfg = *query.CFG
	}
	maxCFG := 5.0
	if family == modelFamilyMoss {
		maxCFG = 20
	}
	if cfg < 1 || cfg > maxCFG || len(query.References) > 4 || mode == promptassistant.ModeTextToImage && len(query.References) > 0 {
		http.Error(w, "invalid Krea2 guide context", http.StatusBadRequest)
		return
	}
	seen := map[int]bool{}
	references := []promptassistant.ImageReference{}
	for _, reference := range query.References {
		if reference.Number < 1 || reference.Number > 4 || seen[reference.Number] || !promptassistant.ValidImageReferenceRole(reference.Role) {
			http.Error(w, "invalid reference role", http.StatusBadRequest)
			return
		}
		seen[reference.Number] = true
		references = append(references, promptassistant.ImageReference{Number: reference.Number, Role: reference.Role})
	}
	imageFamily := ""
	if family == modelFamilyMoss {
		imageFamily = modelFamilyMoss
	}
	instruction := promptassistant.EnhancementInstruction(mode, profile, references, promptassistant.VideoContext{}, promptassistant.EditContext{CFG: cfg, ImageFamily: imageFamily})
	digest := sha256.Sum256([]byte(instruction))
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "promptassistant.EnhancementInstruction", "workflow_mode": query.Mode, "mode": mode, "profile": profile, "cfg": cfg,
		"instruction": instruction, "revision": hex.EncodeToString(digest[:]),
		"agent_scope": "Apply this shared Prompt Assistant guide when drafting request.prompt and request.negative_prompt. Its output-format directives concern those prompt fields, not your whole Harness conversation. Continue using tools and present results normally. Reference roles alone do not attach pixels or grant vision; never claim to inspect media you have not actually seen.",
	})
}
