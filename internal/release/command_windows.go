//go:build windows

package release

import (
	"os/exec"
	"syscall"
)

func configureReleaseCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
