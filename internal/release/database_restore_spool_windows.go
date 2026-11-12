//go:build windows

package release

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func createDatabaseRestoreSpool(j *HostJournal, name string) (*os.File, error) {
	if j.checkFiles() != nil {
		return nil, ErrDatabaseRestore
	}
	path := filepath.Join(j.directory, name)
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrDatabaseRestore
	}
	// No FILE_SHARE_WRITE: no second writable handle can alter the verified
	// bytes. Delete-on-close also removes the copy after parent process death.
	handle, err := windows.CreateFile(encoded, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, ErrDatabaseRestore
	}
	file := os.NewFile(uintptr(handle), path)
	if j.checkPrivateMaterialFile(file, name, 0) != nil {
		file.Close()
		return nil, ErrDatabaseRestore
	}
	return file, nil
}
