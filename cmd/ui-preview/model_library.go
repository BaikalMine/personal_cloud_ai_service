package main

import (
	"ai-access-gateway/internal/domain"
	"encoding/json"
	"net/http"
)

func registerModelLibraryPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc) {
	mux.HandleFunc("/preview/model-library", render("model_library", "Модели и LoRA", map[string]any{"CanManageModels": true, "ModelAdminWorkspace": true}))
	mux.HandleFunc("/preview/model-catalog", render("model_library", "Модели и LoRA", map[string]any{"CanManageModels": false, "CurrentUser": &domain.User{ID: 42, Role: "user", Username: "viewer"}}))
	mux.HandleFunc("/models/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models/api/catalog":
			items := []map[string]any{}
			for i, name := range []string{"Flux.2 Klein - Studio light", "Krea2 - Natural textures", "MiniMax H3 - Camera motion"} {
				items = append(items, map[string]any{"name": name, "filename": name + ".safetensors", "base": []string{"Flux.2 Klein 9B", "Krea 2", "MiniMax H3"}[i], "size": int64(680 << 20), "hash": "ab1289d0", "preview": i != 1, "mature": i == 2, "token": []string{"fixture-visible", "fixture-empty", "fixture-mature"}[i], "words": []string{"studio light", "soft detail"}, "model_id": 123, "version_id": 456, "update": i == 1})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "total": 3, "page": 1, "pages": 1})
		case "/models/api/preview":
			w.Header().Del("Content-Type")
			http.ServeFile(w, r, "docs/frontend/prototype/assets/portrait.jpg")
		case "/models/api/item":
			_ = json.NewEncoder(w).Encode(map[string]string{"description": "Мягкий студийный свет и естественные текстуры.\n\nРекомендуемый вес: 0.6-0.9. Сохраняет композицию и детали исходного изображения."})
		case "/models/api/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"civitai_api_key_set": true, "default_lora_root": "C:/Work/ComfyUI/models/loras", "default_checkpoint_root": "C:/Work/ComfyUI/models/checkpoints", "default_unet_root": "C:/Work/ComfyUI/models/diffusion_models"})
		case "/models/api/installs":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": "fixture", "name": "Krea2 - Natural textures · v2", "state": "running", "progress": 62, "filename": "natural_textures_v2.safetensors"}}})
		default:
			http.NotFound(w, r)
		}
	})
}
