//go:build windows

package miningagent

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"ai-access-gateway/internal/mining"
)

const (
	maxMinerArchiveBytes   = 1 << 30
	maxMinerExpandedBytes  = 3 << 30
	maxMinerArchiveEntries = 20000
	maxPreservedBATBytes   = 1 << 20
	maxPreservedBATTotal   = 16 << 20
)

type preservedBAT struct {
	relative string
	data     []byte
	mode     os.FileMode
}

func (c *windowsController) Update(ctx context.Context, request mining.UpdateRequest) (mining.UpdateResult, error) {
	result := mining.UpdateResult{}
	scriptPath, err := c.allowedScript(request.ScriptPath)
	if err != nil {
		return result, fmt.Errorf("текущий скрипт майнера: %w", err)
	}
	currentDir := filepath.Dir(scriptPath)
	if samePath(currentDir, c.rootDir) {
		return result, errors.New("обновление требует отдельной папки майнера внутри корня майнинга")
	}

	archivePath, archiveName, err := downloadMinerArchive(ctx, request.ArchiveURL)
	if err != nil {
		return result, err
	}
	defer os.Remove(archivePath)
	result.ArchiveName = archiveName
	if !archiveMatchesMiner(archiveName, request.MinerName, request.ProcessName) {
		return result, fmt.Errorf("имя архива %q не совпадает с майнером %s", archiveName, request.ProcessName)
	}

	preserved, err := collectPreservedBAT(currentDir)
	if err != nil {
		return result, fmt.Errorf("сохранение BAT-файлов: %w", err)
	}
	staging, packageRoot, err := extractMinerArchive(archivePath, c.rootDir, request.ProcessName)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(staging)
	if err := restorePreservedBAT(packageRoot, preserved); err != nil {
		return result, fmt.Errorf("перенос пользовательских BAT-файлов: %w", err)
	}
	result.PreservedScripts = len(preserved)

	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.stateLocked(ctx, request.ProcessName)
	if err != nil {
		return result, err
	}
	wasRunning := state.Running
	if wasRunning {
		if _, err := c.stopLocked(ctx, mining.Request{ScriptPath: scriptPath, ProcessName: request.ProcessName}); err != nil {
			return result, fmt.Errorf("остановка майнера перед обновлением: %w", err)
		}
	}

	backupDir := currentDir + ".backup-" + time.Now().UTC().Format("20060102-150405")
	backupDir, err = uniquePath(backupDir)
	if err != nil {
		return result, err
	}
	if err := replaceMinerDirectory(currentDir, packageRoot, backupDir); err != nil {
		if wasRunning {
			_, _ = c.startLocked(context.Background(), mining.Request{ScriptPath: scriptPath, ProcessName: request.ProcessName})
		}
		return result, err
	}
	result.InstalledPath = currentDir
	result.BackupPath = backupDir

	if wasRunning {
		if _, err := c.startLocked(ctx, mining.Request{ScriptPath: scriptPath, ProcessName: request.ProcessName}); err != nil {
			rollbackErr := rollbackMinerDirectory(currentDir, backupDir)
			_, restartErr := c.startLocked(context.Background(), mining.Request{ScriptPath: scriptPath, ProcessName: request.ProcessName})
			if rollbackErr != nil {
				return result, fmt.Errorf("новый майнер не запустился, а откат не выполнен: %v; %w", rollbackErr, err)
			}
			if restartErr != nil {
				return result, fmt.Errorf("обновление откатилось, но старый майнер не запустился: %v; новая ошибка: %w", restartErr, err)
			}
			return result, fmt.Errorf("новый майнер не запустился, выполнен откат к предыдущей версии: %w", err)
		}
		result.Restarted = true
	}
	result.Success = true
	if wasRunning {
		result.Message = "Майнер обновлён, пользовательские BAT-файлы сохранены, новая версия запущена."
	} else {
		result.Message = "Майнер обновлён, пользовательские BAT-файлы сохранены. Майнер оставлен остановленным."
	}
	return result, nil
}

func downloadMinerArchive(ctx context.Context, rawURL string) (string, string, error) {
	parsed, err := validateArchiveURL(rawURL)
	if err != nil {
		return "", "", err
	}
	client := &http.Client{
		Timeout: 35 * time.Minute,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if _, err := validateArchiveURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", "", fmt.Errorf("ссылка на архив: %w", err)
	}
	request.Header.Set("User-Agent", "AI-Access-Gateway-miner-updater/1.0")
	response, err := client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("загрузка архива: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("загрузка архива: сервер ответил %s", response.Status)
	}
	archiveName, err := archiveNameFromResponse(response, parsed)
	if err != nil {
		return "", "", err
	}
	if response.ContentLength > maxMinerArchiveBytes {
		return "", "", errors.New("архив майнера больше 1 ГБ")
	}
	temporary, err := os.CreateTemp("", "ai-miner-update-*.zip")
	if err != nil {
		return "", "", fmt.Errorf("создание временного файла: %w", err)
	}
	temporaryPath := temporary.Name()
	removeOnError := true
	defer func() {
		_ = temporary.Close()
		if removeOnError {
			_ = os.Remove(temporaryPath)
		}
	}()
	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxMinerArchiveBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("сохранение архива: %w", err)
	}
	if written > maxMinerArchiveBytes {
		return "", "", errors.New("архив майнера больше 1 ГБ")
	}
	if err := temporary.Close(); err != nil {
		return "", "", fmt.Errorf("закрытие временного архива: %w", err)
	}
	removeOnError = false
	return temporaryPath, archiveName, nil
}

func validateArchiveURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("ссылка на обновление должна быть HTTPS-ссылкой без логина и пароля")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return nil, errors.New("ссылки на локальные и приватные адреса запрещены")
	}
	return parsed, nil
}

func archiveNameFromURL(parsed *url.URL) (string, error) {
	name, err := url.PathUnescape(path.Base(parsed.Path))
	if err != nil {
		return "", errors.New("ссылка должна вести на ZIP-архив")
	}
	return validateArchiveName(name)
}

func archiveNameFromResponse(response *http.Response, original *url.URL) (string, error) {
	if raw := response.Header.Get("Content-Disposition"); raw != "" {
		_, parameters, err := mime.ParseMediaType(raw)
		if err == nil {
			for _, key := range []string{"filename", "filename*"} {
				if value := parameters[key]; value != "" {
					value = strings.TrimPrefix(value, "UTF-8''")
					if decoded, decodeErr := url.PathUnescape(value); decodeErr == nil {
						if name, nameErr := validateArchiveName(decoded); nameErr == nil {
							return name, nil
						}
					}
				}
			}
		}
	}
	for _, candidate := range []*url.URL{response.Request.URL, original} {
		if candidate == nil {
			continue
		}
		if name, err := archiveNameFromURL(candidate); err == nil {
			return name, nil
		}
	}
	return "", errors.New("ссылка должна вести на ZIP-архив")
}

func validateArchiveName(raw string) (string, error) {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	if name == "." || name == "/" || !strings.EqualFold(filepath.Ext(name), ".zip") {
		return "", errors.New("ссылка должна вести на ZIP-архив")
	}
	return name, nil
}

func archiveMatchesMiner(archiveName, minerName, processName string) bool {
	archiveKey := productKey(strings.TrimSuffix(archiveName, filepath.Ext(archiveName)))
	for _, candidate := range []string{processName, minerName} {
		candidateKey := productKey(strings.TrimSuffix(candidate, filepath.Ext(candidate)))
		if len(candidateKey) >= 5 && strings.Contains(archiveKey, candidateKey) {
			return true
		}
	}
	return false
}

func productKey(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func collectPreservedBAT(root string) ([]preservedBAT, error) {
	var files []preservedBAT
	total := int64(0)
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(entry.Name()), ".bat") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxPreservedBATBytes || total+info.Size() > maxPreservedBATTotal {
			return errors.New("размер пользовательских BAT-файлов превышает допустимый лимит")
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || !safeRelativePath(relative) {
			return errors.New("некорректный относительный путь BAT-файла")
		}
		files = append(files, preservedBAT{relative: relative, data: data, mode: info.Mode().Perm()})
		total += info.Size()
		return nil
	})
	return files, err
}

func restorePreservedBAT(root string, files []preservedBAT) error {
	for _, file := range files {
		destination := filepath.Join(root, file.relative)
		if !withinDirectory(root, destination) {
			return errors.New("BAT-файл выходит за пределы папки майнера")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func extractMinerArchive(archivePath, miningRoot, processName string) (string, string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("открытие ZIP-архива: %w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxMinerArchiveEntries {
		return "", "", errors.New("архив пустой или содержит слишком много файлов")
	}
	staging, err := os.MkdirTemp(miningRoot, ".ai-miner-staging-*")
	if err != nil {
		return "", "", fmt.Errorf("создание временной папки: %w", err)
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	var expanded int64
	for _, file := range reader.File {
		relative, err := safeArchivePath(file.Name)
		if err != nil {
			return "", "", fmt.Errorf("архив содержит небезопасный путь: %w", err)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("symlink внутри архива запрещён")
		}
		if file.UncompressedSize64 > maxMinerExpandedBytes || expanded+int64(file.UncompressedSize64) > maxMinerExpandedBytes {
			return "", "", errors.New("распакованный архив майнера больше 3 ГБ")
		}
		destination := filepath.Join(staging, filepath.FromSlash(relative))
		if !withinDirectory(staging, destination) {
			return "", "", errors.New("архив выходит за пределы временной папки")
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return "", "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return "", "", err
		}
		input, err := file.Open()
		if err != nil {
			return "", "", err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			_ = input.Close()
			return "", "", err
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, maxMinerExpandedBytes+1))
		closeInputErr := input.Close()
		closeOutputErr := output.Close()
		if copyErr != nil || closeInputErr != nil || closeOutputErr != nil || written != int64(file.UncompressedSize64) {
			return "", "", errors.New("ошибка распаковки ZIP-архива")
		}
		expanded += written
	}
	packageRoot, found := processExecutableDirectory(staging, processName)
	if !found {
		return "", "", fmt.Errorf("в архиве не найден ожидаемый файл %s", processName)
	}
	keepStaging = true
	return staging, packageRoot, nil
}

func processExecutableDirectory(root, processName string) (string, bool) {
	var directory string
	_ = filepath.WalkDir(root, func(current string, entry os.DirEntry, err error) error {
		if err != nil || directory != "" {
			return err
		}
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.EqualFold(entry.Name(), processName) {
			directory = filepath.Dir(current)
		}
		return nil
	})
	return directory, directory != ""
}

func safeArchivePath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') {
		return "", errors.New("пустой путь или NUL")
	}
	normalized := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(normalized, "/") || len(normalized) >= 2 && normalized[1] == ':' {
		return "", errors.New("абсолютный путь")
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("путь с переходом к родительской папке")
	}
	return cleaned, nil
}

func safeRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) {
		return false
	}
	cleaned := filepath.Clean(value)
	return cleaned != "." && cleaned != ".." && cleaned != "" && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func withinDirectory(root, target string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	targetAbs, targetErr := filepath.Abs(target)
	if rootErr != nil || targetErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func uniquePath(candidate string) (string, error) {
	for index := 0; index < 100; index++ {
		path := candidate
		if index > 0 {
			path = fmt.Sprintf("%s-%d", candidate, index)
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("не удалось выбрать имя резервной папки")
}

func replaceMinerDirectory(currentDir, packageRoot, backupDir string) error {
	if err := os.Rename(currentDir, backupDir); err != nil {
		return fmt.Errorf("создание резервной копии майнера: %w", err)
	}
	if err := os.Rename(packageRoot, currentDir); err != nil {
		_ = os.Rename(backupDir, currentDir)
		return fmt.Errorf("установка новой папки майнера: %w", err)
	}
	return nil
}

func rollbackMinerDirectory(currentDir, backupDir string) error {
	failedDir := currentDir + ".failed-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(currentDir, failedDir); err != nil {
		return fmt.Errorf("перемещение неудачной версии: %w", err)
	}
	if err := os.Rename(backupDir, currentDir); err != nil {
		_ = os.Rename(failedDir, currentDir)
		return fmt.Errorf("возврат резервной копии: %w", err)
	}
	return nil
}
