//go:build !linux && !windows

package release

func databaseProcessExited(databaseProcessIdentity) (bool, error) {
	return false, errDatabaseWriter
}
