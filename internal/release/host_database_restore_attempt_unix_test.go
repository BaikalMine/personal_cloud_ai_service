//go:build !windows

package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostDatabaseRestoreAttemptUnixPermissions(t *testing.T) {
	for _, phase := range []string{"prepared", "writer", "completed"} {
		t.Run(phase, func(t *testing.T) {
			directory, j, head, digest := hostDatabaseRestoreAttemptFixture(t)
			path := filepath.Join(directory, databaseRestoreRecordName(head.Binding.Target, phase))
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := j.readDatabaseRestoreChain(context.Background(), head.Binding.Target, "completed", digest); err == nil {
				t.Fatal("broad attempt permissions accepted")
			}
		})
	}
}
