//go:build !linux && !windows

package release

import "os"

func createDatabaseRestoreSpool(*HostJournal, string) (*os.File, error) {
	return nil, ErrDatabaseRestore
}
