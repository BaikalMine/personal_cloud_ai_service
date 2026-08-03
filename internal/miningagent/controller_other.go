//go:build !windows

package miningagent

import (
	"context"
	"errors"

	"ai-access-gateway/internal/mining"
)

type unsupportedController struct{}

func NewController(_, _ string) (Controller, error) {
	return unsupportedController{}, nil
}

func (unsupportedController) State(context.Context, string) (mining.State, error) {
	return mining.State{}, errors.New("mining agent process control requires Windows")
}

func (unsupportedController) Script(context.Context, string) (mining.Script, error) {
	return mining.Script{}, errors.New("mining agent script access requires Windows")
}

func (unsupportedController) Start(context.Context, mining.Request) (mining.State, error) {
	return mining.State{}, errors.New("mining agent process control requires Windows")
}

func (unsupportedController) Stop(context.Context, mining.Request) (mining.State, error) {
	return mining.State{}, errors.New("mining agent process control requires Windows")
}
