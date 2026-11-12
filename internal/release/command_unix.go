//go:build !windows

package release

import "os/exec"

func configureReleaseCommand(*exec.Cmd) {}
