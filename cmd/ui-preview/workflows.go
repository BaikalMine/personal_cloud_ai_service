package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func registerWorkflowPreview(mux *http.ServeMux, render func(string, string, map[string]any) http.HandlerFunc, now time.Time) {
	component := func(class string, present bool) map[string]any {
		return map[string]any{"ClassType": class, "Present": present, "Module": "custom_nodes.preview"}
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "POST" {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method", 405)
			return
		}
		if r.Method == "POST" && r.FormValue("csrf") != "preview" {
			http.Error(w, "csrf", 403)
			return
		}
		results := []map[string]any{
			{"ID": "video-post", "Status": "error", "Scenario": "Полная обработка видео", "Family": "MiniMax H3 v5", "Description": "RIFE, RTX, ColorMatch и Sharpen", "Model": "MiniMax H3 FL2VA INT8 ConvRot", "ModelFile": "MiniMaxH3/FL2VA_INT8_ConvRot.safetensors", "DefinitionID": "minimax-h3-video", "ManifestName": "MiniMax H3", "ManifestVersion": "5", "NodeCount": 31, "Issues": []map[string]any{{"Label": "Финальный апскейл", "Message": "У ноды отсутствует обязательный параметр resize_type.", "ClassType": "RTXVideoSuperResolution", "InputName": "resize_type"}}, "Components": []map[string]any{component("RTXVideoSuperResolution", true), component("RIFE VFI", true)}, "Branches": []map[string]any{{"Name": "RTX Super Resolution", "Selected": true, "Present": 1, "Components": []map[string]any{component("RTXVideoSuperResolution", true)}}}},
			{"ID": "flux-model", "Status": "unavailable", "Scenario": "Готовность модели", "Family": "Flux2", "Model": "Flux 2 Klein 9B", "ModelFile": "Flux2/klein-9b.safetensors", "NodeCount": 0, "Issues": []map[string]any{{"Label": "Зависимости модели", "Message": "Не найден Qwen3 8B text encoder."}}},
			{"ID": "video-frames", "Status": "ok", "Scenario": "Первый и последний кадр", "Family": "MiniMax H3 v5", "Description": "FL2VA с двумя точными кадрами", "Model": "MiniMax H3 FL2VA INT8 ConvRot", "ModelFile": "MiniMaxH3/FL2VA_INT8_ConvRot.safetensors", "DefinitionID": "minimax-h3-video", "ManifestName": "MiniMax H3", "ManifestVersion": "5", "NodeCount": 24, "Components": []map[string]any{component("MiniMaxH3ImageToVideo", true), component("VAEDecode", true)}},
			{"ID": "krea-text", "Status": "ok", "Scenario": "Текст в изображение", "Family": "Krea2", "Model": "Krea 2", "ModelFile": "Krea2/krea2_base.safetensors", "DefinitionID": "text-to-image-krea2", "ManifestName": "PhotoFlow Krea2", "ManifestVersion": "2", "NodeCount": 22, "Components": []map[string]any{component("KSampler", true), component("UNETLoader", true)}},
			{"ID": "flux-edit", "Status": "ok", "Scenario": "Фото и промт", "Family": "Flux2", "Model": "Flux 2 Dev", "ModelFile": "Flux2/flux2_dev.safetensors", "DefinitionID": "image-to-image-flux2", "ManifestName": "Flux2 Редактирование", "ManifestVersion": "1", "NodeCount": 28, "Components": []map[string]any{component("LCReferenceLatent", true), component("LCPipeEdit", true)}, "Branches": []map[string]any{{"Name": "SeedVR2", "Selected": false, "Present": 1, "Components": []map[string]any{component("SeedVR2VideoUpscaler", false), component("SeedVR2LoadVAEModel", true)}}}},
			{"ID": "flux-text", "Status": "ok", "Scenario": "Текстовый граф", "Family": "Flux2", "Model": "Flux 2 Dev", "ModelFile": "Flux2/flux2_dev.safetensors", "DefinitionID": "text-to-image-flux2", "NodeCount": 12, "Components": []map[string]any{component("UNETLoader", true), component("FluxGuidance", true)}},
		}
		scenario := r.URL.Query().Get("scenario")
		results[4]["Files"] = []map[string]any{{"Name": "text_encoders/qwen3_8b_preview.gguf", "Class": "ClipLoaderGGUF", "Field": "clip_name", "NodeID": "12"}, {"Name": "vae/flux2_vae.safetensors", "Class": "VAELoader", "Field": "vae_name", "NodeID": "13"}}
		if scenario == "empty" {
			results = nil
		}
		if scenario == "long" {
			results[0]["ModelFile"] = strings.Repeat("long-model-component_", 18) + ".safetensors"
			results[0]["Scenario"] = strings.Repeat("ДлинноеНазваниеСценария", 8)
		}
		query, state, family := r.URL.Query().Get("q"), r.URL.Query().Get("state"), r.URL.Query().Get("family")
		filtered := make([]map[string]any, 0)
		counts := map[string]int{}
		for _, result := range results {
			counts[result["Status"].(string)]++
			if state != "" && result["Status"] != state || family != "" && result["Family"] != family {
				continue
			}
			if !strings.Contains(strings.ToLower(fmt.Sprint(result)), strings.ToLower(query)) {
				continue
			}
			filtered = append(filtered, result)
		}
		report := map[string]any{
			"Snapshot": map[string]any{"FetchedAt": now.Add(-2 * time.Minute)}, "SourceLabel": "получен из ComfyUI", "Fingerprint": "c42b3f9a7d81", "NodeCount": 418,
			"Compatible": counts["ok"], "Failed": counts["error"], "Unavailable": counts["unavailable"],
		}
		if scenario == "stale" {
			report["Stale"] = true
			report["SourceLabel"] = "последний рабочий каталог"
			report["Snapshot"] = map[string]any{"FetchedAt": now.Add(-2 * time.Hour), "LastError": "каталог нод ComfyUI вернул HTTP 503"}
		}
		if scenario == "offline" {
			report["Error"] = "каталог нод ComfyUI недоступен: connection refused"
		}
		message := ""
		if r.Method == "POST" {
			message = fmt.Sprintf("Проверка завершена: совместимы %d, ошибки %d, недоступны %d.", counts["ok"], counts["error"], counts["unavailable"])
			if scenario == "stale" {
				message = "Новый каталог недоступен; проверен последний рабочий снимок."
			}
			if scenario == "offline" {
				message = "Проверка не выполнена: каталог недоступен."
			}
		}
		render("admin_workflows", "Модели и workflow", map[string]any{
			"Report": report, "Message": message,
			"View": map[string]any{"Results": filtered, "Total": len(results), "Query": query, "State": state, "Family": family, "Families": []string{"Flux2", "Krea2", "MiniMax H3 v5"}},
		})(w, r)
	}
	mux.HandleFunc("/preview/workflows", handler)
	mux.HandleFunc("/admin/workflows", handler)
}
