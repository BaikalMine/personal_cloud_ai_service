//go:build !windows

package updateagent

import (
	"context"
	"errors"
)

type nativeComfyRuntimeBackend struct{}

func (nativeComfyRuntimeBackend) Inspect(context.Context, ComfyTarget) ([]comfyProcess, error) {
	return nil, errors.New("Windows runtime required")
}
func (nativeComfyRuntimeBackend) Start(ComfyTarget, string, func(comfyProcess, uint32)) error {
	return errors.New("Windows runtime required")
}
func (nativeComfyRuntimeBackend) Stop(context.Context, ComfyTarget, comfyProcess) error {
	return errors.New("Windows runtime required")
}
func (nativeComfyRuntimeBackend) Watch(comfyProcess, func(uint32)) error {
	return errors.New("Windows runtime required")
}
