//go:build !linux && !windows

package release

import "os"

func startDatabaseWriter(string, []string, []string, string, *os.File, *os.File) (databaseWriterProcess, error) {
	return nil, errDatabaseWriter
}
