package gateway

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

func (a *App) handleModelMaintenance(w http.ResponseWriter, r *http.Request, ctx context.Context, action string) {
	var in struct {
		Token   string `json:"token"`
		Confirm string `json:"confirm"`
	}
	if !a.modelBody(w, r, &in) {
		return
	}
	scope := "item"
	if action == "delete-lora" || action == "delete-model" {
		scope = "delete-lora"
	}
	item, err := a.readModelToken(in.Token, scope, a.currentUser(r).ID)
	if err != nil || !modelmanager.Kind(item.Kind) || item.Path == "" {
		http.Error(w, "Откройте карточку из каталога ещё раз.", 400)
		return
	}
	if action == "delete-lora" && item.Kind != "loras" {
		http.Error(w, "Выберите файл LoRA.", http.StatusBadRequest)
		return
	}
	client := a.modelManager()
	if action == "refresh-metadata" {
		if err = client.RefreshMetadata(ctx, item.Kind, item.Path); err != nil {
			http.Error(w, "Не удалось обновить описание из CivitAI. Файл модели не изменён.", 502)
			return
		}
		modelJSON(w, map[string]bool{"ok": true})
		return
	}
	hash, hashErr := hex.DecodeString(item.SHA256)
	if hashErr != nil || len(hash) != 32 || item.Size <= 0 {
		http.Error(w, "Для удаления нужны подтверждённые размер и SHA-256. Обновите метаданные.", 409)
		return
	}
	settings, err := client.Settings(ctx)
	if err != nil {
		http.Error(w, "Не удалось проверить папку модели.", 502)
		return
	}
	roots := []string{settings.LoraRoot}
	if item.Kind == "checkpoints" {
		roots = []string{settings.CheckpointRoot, settings.UNetRoot}
	}
	var root, relative string
	for _, candidate := range roots {
		if rel, resolveErr := modelmanager.LoraRelativePath(candidate, item.Path); resolveErr == nil {
			root, relative = candidate, rel
			break
		}
	}
	if root == "" {
		http.Error(w, "Файл вне настроенных папок моделей. Удаление через Gateway недоступно.", 409)
		return
	}
	verify := func() error {
		v, err := a.updates.VerifyModel(ctx, updates.ModelVerifyRequest{Root: root, RelativePath: relative, SHA256: item.SHA256})
		if err != nil {
			return err
		}
		if v.State != "verified" || v.Size != item.Size {
			return modelmanager.ErrInvalid
		}
		return nil
	}
	filename := path.Base(relative)
	if action == "delete-preview" {
		item.Scope = "delete-lora"
		item.Expires = time.Now().Add(2 * time.Minute).Unix()
		token, err := a.modelToken(item)
		if err != nil {
			http.Error(w, "Не удалось подготовить удаление.", 500)
			return
		}
		modelJSON(w, map[string]any{"token": token, "filename": filename, "relative_path": relative, "size": item.Size})
		return
	}
	if in.Confirm != filename {
		http.Error(w, "Подтвердите удаление выбранного файла.", 400)
		return
	}
	if a.store == nil || !a.cfg.ComfyGPUGuardRequired {
		http.Error(w, "Не удалось подтвердить защиту активных задач. Удаление недоступно.", 409)
		return
	}
	mutationStarted := false
	err = a.store.WithGPUAdmissionGuard(ctx, func() error {
		return a.store.WithModelRemovalGuard(ctx, func() error {
			if err := verify(); err != nil {
				return err
			}
			mutationStarted = true
			return client.DeleteModel(ctx, item.Kind, item.Path)
		})
	})
	if err != nil {
		if mutationStarted {
			http.Error(w, "Результат удаления не подтверждён. Проверьте каталог; запрос автоматически не повторяется.", 502)
			return
		}
		if errors.Is(err, store.ErrModelInstallConflict) || errors.Is(err, store.ErrGPUWorkConflict) {
			http.Error(w, "Сейчас есть активные задачи или установки. Дождитесь завершения и подтвердите удаление заново.", 409)
			return
		}
		http.Error(w, "Повторная проверка файла не пройдена. Обновите каталог.", 409)
		return
	}
	modelJSON(w, map[string]any{"deleted": true, "filename": strings.TrimSpace(filename)})
}
