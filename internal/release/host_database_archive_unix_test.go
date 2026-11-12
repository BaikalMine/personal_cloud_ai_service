//go:build !windows

package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostDatabaseArchiveUnixPermissions(t *testing.T) {
	for _, kind := range []string{"archive", "metadata", "password"} {
		t.Run(kind, func(t *testing.T) {
			directory, j, options, receipt := hostDatabaseArchiveFixture(t)
			archive, metadata, password, _ := databaseArchiveNames(receipt.TargetSHA256)
			name := archive
			if kind == "metadata" {
				name = metadata
			} else if kind == "password" {
				name = password
				if err := j.writeArchivePassword(name, []byte("private-pgpass")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(filepath.Join(directory, name), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := j.archiveProof(context.Background(), name, options.MaxBytes); err == nil {
				t.Fatal("broad archive/credential permissions accepted")
			}
		})
	}
}
