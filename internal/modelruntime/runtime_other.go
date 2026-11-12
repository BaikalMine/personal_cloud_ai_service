//go:build !windows

package modelruntime

import (
	"context"
	"errors"

	"ai-access-gateway/internal/domain"
)

type Runtime struct{}

func New(Config) (*Runtime, error) { return nil, errors.New("owned model runtime requires Windows") }
func (*Runtime) Execute(context.Context, []byte) (Result, error) {
	return Result{}, errors.New("owned model runtime requires Windows")
}
func (*Runtime) ExecuteHTTP(context.Context, HTTPRequest, HTTPConsumer) (Result, error) {
	return Result{}, errors.New("owned model runtime requires Windows")
}
func (*Runtime) Advance(context.Context) (domain.GPUInferenceExecutor, error) {
	return domain.GPUInferenceExecutor{}, errors.New("owned model runtime requires Windows")
}
func (*Runtime) Close() error { return nil }
func (*Runtime) StopRetained(context.Context) (bool, error) {
	return false, errors.New("owned model runtime requires Windows")
}
