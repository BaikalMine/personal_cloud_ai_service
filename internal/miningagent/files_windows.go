//go:build windows

package miningagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"ai-access-gateway/internal/mining"
)

func (c *windowsController) Files(ctx context.Context) (mining.FileCatalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return launchFiles(ctx, c.rootDir)
}

func (c *windowsController) SaveScript(ctx context.Context, input mining.SaveScriptRequest) (mining.Script, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owned != nil {
		active, err := c.owned.Active()
		if err != nil {
			return mining.Script{}, err
		}
		if active > 0 {
			return mining.Script{}, errors.New("Перед редактированием остановите работающий майнер. Сохранение не останавливает его автоматически.")
		}
	}
	path, err := c.allowedScript(input.ScriptPath)
	if err != nil {
		return mining.Script{}, err
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return mining.Script{}, err
	}
	names := map[string]string{strings.ToLower(input.ProcessName): input.ProcessName}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".exe") {
			names[strings.ToLower(entry.Name())] = entry.Name()
		}
	}
	for _, name := range names {
		state, err := c.stateLocked(ctx, name)
		if err != nil {
			return mining.Script{}, err
		}
		if state.Running {
			return mining.Script{}, errors.New("Перед редактированием остановите этот майнер. Сохранение не останавливает его автоматически.")
		}
	}
	return saveEditableScript(ctx, c.rootDir, input)
}
