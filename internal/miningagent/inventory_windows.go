//go:build windows

package miningagent

import (
	"ai-access-gateway/internal/mining"
	"context"
	"time"
)

func (c *windowsController) inventoryLocked(ctx context.Context) (mining.Inventory, error) {
	catalog, err := launchFiles(ctx, c.rootDir)
	if err != nil {
		return mining.Inventory{}, err
	}
	processes, err := windowsProcesses(ctx)
	if err != nil {
		return mining.Inventory{}, err
	}
	managed := managedMinerState{ProcessName: c.managedProcessName, PIDs: c.managedPIDs, ScriptPath: c.scriptPath, StartedAt: c.startedAt}
	return observedInventory(catalog, processes, managed, time.Now().UTC()), nil
}

func (c *windowsController) Inventory(ctx context.Context) (mining.Inventory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inventoryLocked(ctx)
}
