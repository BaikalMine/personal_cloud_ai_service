//go:build linux

package release

import (
	"os"
	"testing"
)

func broadenRestoreCredentialPermissions(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
}
