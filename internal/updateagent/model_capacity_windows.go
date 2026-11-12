//go:build windows

package updateagent

import "golang.org/x/sys/windows"

func modelDiskFree(root string) (uint64, error) {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	err = windows.GetDiskFreeSpaceEx(path, &free, &total, &totalFree)
	return free, err
}
