//go:build !windows

package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostDatabaseTargetUnixPermissions(t *testing.T) {
	directory, j, options, receipt := hostDatabaseTargetFixture(t)
	path := filepath.Join(directory, databaseTargetMetadataName(receipt.TargetSHA256))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReadDockerRestoreDatabase(context.Background(), 0, options); err == nil {
		t.Fatal("broad target configuration permissions accepted")
	}
}
