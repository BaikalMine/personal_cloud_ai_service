package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/updates"
)

func (a *App) verifyModelInstall(ctx context.Context) (int64, error) {
	j, release, err := a.store.ClaimModelVerification(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer release()
	var download modelmanager.Download
	if json.Unmarshal(j.Request, &download) != nil {
		return 0, modelmanager.ErrInvalid
	}
	result, err := a.updates.VerifyModel(ctx, updates.ModelVerifyRequest{Root: download.Root, RelativePath: path.Join(download.RelativePath, j.Filename), SHA256: j.SHA256})
	if err == nil && (result.State == "missing" || result.State == "mismatch") {
		if alternate, found := a.verifyRenamedModel(ctx, j.Kind, j.Filename, j.SHA256, download); found {
			result = alternate
		}
	}
	state, message, progress := "reconciling", "Не удалось проверить файл. Проверка повторится автоматически.", j.Progress
	if err == nil {
		switch result.State {
		case "verified":
			state, message, progress = "completed", "Файл проверен и установлен в ComfyUI.", 100
		case "missing":
			message = "Файл пока не найден. Проверяем завершение загрузки; повторное скачивание не запускается."
		case "incomplete", "changed":
			message = "Файл ещё изменяется. Проверим его после завершения загрузки."
		case "mismatch":
			message = "Контрольная сумма файла не совпала. Установка не подтверждена; файл не удалён."
		}
	}
	save, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return 1, a.store.UpdateModelInstall(save, j.ID, state, message, progress)
}

func (a *App) verifyRenamedModel(ctx context.Context, kind, filename, hash string, download modelmanager.Download) (updates.ModelVerification, bool) {
	if !modelmanager.Kind(kind) {
		return updates.ModelVerification{}, false
	}
	search := strings.TrimSuffix(filename, path.Ext(filename))
	for page := 1; page <= 20; page++ {
		catalog, err := a.modelManager().List(ctx, kind, search, "", page)
		if err != nil {
			break
		}
		for _, item := range catalog.Items {
			relative, ok := installedModelRelativePath(item, download, hash)
			if !ok || relative == path.Join(download.RelativePath, filename) {
				continue
			}
			result, err := a.updates.VerifyModel(ctx, updates.ModelVerifyRequest{Root: download.Root, RelativePath: relative, SHA256: hash})
			if err == nil && result.State == "verified" {
				return result, true
			}
		}
		if page >= catalog.Pages {
			break
		}
	}
	return updates.ModelVerification{}, false
}

func installedModelRelativePath(item modelmanager.Item, download modelmanager.Download, hash string) (string, bool) {
	if !strings.EqualFold(item.Hash, hash) {
		return "", false
	}
	root := strings.TrimRight(strings.ReplaceAll(download.Root, "\\", "/"), "/")
	folder := strings.Trim(download.RelativePath, "/")
	full := strings.ReplaceAll(item.Path, "\\", "/")
	prefix := root + "/" + folder + "/"
	if !strings.HasPrefix(strings.ToLower(full), strings.ToLower(prefix)) {
		return "", false
	}
	name := full[len(prefix):]
	if name == "" || strings.Contains(name, "/") || !strings.HasSuffix(strings.ToLower(name), ".safetensors") || strings.HasPrefix(name, ".") || strings.ContainsAny(name, ":<>\"|?*") {
		return "", false
	}
	return path.Join(folder, name), true
}
