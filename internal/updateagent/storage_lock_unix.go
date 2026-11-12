//go:build !windows

package updateagent

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockComfyStorageFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrComfyStorageBusy
	}
	return err
}

func comfyStorageFileIdentity(file *os.File) (string, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("unix:%d:%d", stat.Dev, stat.Ino), nil
}
