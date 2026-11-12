//go:build linux

package release

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostDatabaseConfigurationUnixProtection(t *testing.T) {
	directory, j, _ := hostDatabaseConfigurationFixture(t)
	path := filepath.Join(directory, j.databaseConfigName())
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	receipt, err := j.databaseConfigReceipt()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.readDockerDatabaseConfiguration(receipt); err == nil {
		t.Fatal("world-readable DB credentials accepted")
	}
}
