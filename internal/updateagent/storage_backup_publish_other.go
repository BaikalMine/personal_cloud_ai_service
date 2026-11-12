//go:build !windows && !linux

package updateagent

import "errors"

func publishComfyBackupRestore(source, destination string) error {
	return errors.New("ComfyUI journal restore publication requires Windows or Linux")
}
