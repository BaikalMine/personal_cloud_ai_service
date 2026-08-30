//go:build !windows

package miningagent

import (
	"context"
	"errors"

	"ai-access-gateway/internal/mining"
)

func (unsupportedController) System(context.Context) (mining.SystemMetrics, error) {
	return mining.SystemMetrics{Message: "Метрики доступны только на Windows-хосте."}, errors.New("windows host required")
}
