//go:build windows

package miningagent

import "ai-access-gateway/internal/processowner"

type ownedProcessGroup = processowner.Group

func openOwnedProcessGroup(lockPath string) (*ownedProcessGroup, error) {
	return processowner.Open(lockPath, "Mining")
}
