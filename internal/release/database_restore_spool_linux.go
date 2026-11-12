//go:build linux

package release

import "os"

func createDatabaseRestoreSpool(j *HostJournal, name string) (*os.File, error) {
	file, err := j.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrDatabaseRestore
	}
	if j.checkPrivateMaterialFile(file, name, 0) != nil {
		file.Close()
		return nil, ErrDatabaseRestore
	}
	// Unlink while open. Even a later archive-path replacement cannot change
	// this copy, and process death releases its disk space without a janitor.
	if j.root.Remove(name) != nil {
		file.Close()
		return nil, ErrDatabaseRestore
	}
	return file, nil
}
