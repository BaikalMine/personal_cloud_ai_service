//go:build !linux && !windows

package updateagent

import "errors"

func modelDiskFree(string) (uint64, error) { return 0, errors.New("model disk capacity unsupported") }
