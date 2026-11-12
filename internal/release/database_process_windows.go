//go:build windows

package release

import (
	"errors"

	"golang.org/x/sys/windows"
)

func databaseProcessWindowsIdentity(process windows.Handle) (databaseProcessIdentity, error) {
	pid, err := windows.GetProcessId(process)
	if err != nil {
		return databaseProcessIdentity{}, errDatabaseWriter
	}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(process, &created, &exited, &kernel, &user) != nil {
		return databaseProcessIdentity{}, errDatabaseWriter
	}
	identity := databaseProcessIdentity{Platform: "windows", PID: int(pid),
		Started: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}
	if !identity.valid() {
		return databaseProcessIdentity{}, errDatabaseWriter
	}
	return identity, nil
}

func databaseProcessExited(identity databaseProcessIdentity) (bool, error) {
	if !identity.valid() || identity.Platform != "windows" {
		return false, errDatabaseWriter
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(identity.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true, nil
	}
	if err != nil {
		return false, errDatabaseWriter
	}
	defer windows.CloseHandle(process)
	current, err := databaseProcessWindowsIdentity(process)
	if err != nil {
		return false, errDatabaseWriter
	}
	if current != identity {
		return true, nil
	}
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil {
		return false, errDatabaseWriter
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, errDatabaseWriter
	}
}
