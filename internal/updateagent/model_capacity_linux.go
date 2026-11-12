//go:build linux

package updateagent

import "golang.org/x/sys/unix"

func modelDiskFree(root string) (uint64, error) {
	var out unix.Statfs_t
	err := unix.Statfs(root, &out)
	return out.Bavail * uint64(out.Bsize), err
}
