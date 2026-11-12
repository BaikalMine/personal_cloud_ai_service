//go:build linux || windows

package release

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHostDatabaseRestoreSpool(t *testing.T) {
	for _, mode := range []string{"copy-isolated", "changed-hash", "changed-identity", "truncated", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			directory, j, _, receipt := hostDatabaseArchiveFixture(t)
			digest, err := j.databaseArchiveReceipt(receipt.TargetSHA256)
			if err != nil {
				t.Fatal(err)
			}
			payload, _, err := j.readDatabaseArchiveMetadata(context.Background(), receipt.TargetSHA256, digest)
			if err != nil {
				t.Fatal(err)
			}
			archive, _, _, _ := databaseArchiveNames(payload.Target)
			path := filepath.Join(directory, archive)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "changed-hash":
				payload.Archive.SHA256 = strings.Repeat("f", 64)
			case "changed-identity":
				payload.Archive.Identity = strings.Repeat("f", 64)
			case "truncated":
				if err := os.WriteFile(path, before[:len(before)-1], 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			spool, err := j.databaseRestoreSpool(ctx, payload)
			if mode != "copy-isolated" {
				if err == nil || spool != nil {
					if spool != nil {
						spool.Close()
					}
					t.Fatal("invalid archive produced a usable spool", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer spool.Close()
				if runtime.GOOS == "windows" {
					other, err := os.OpenFile(spool.Name(), os.O_WRONLY, 0)
					if err == nil {
						other.Close()
						t.Fatal("another writer can modify the spool")
					}
				} else {
					entries, err := os.ReadDir(directory)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), ".restore-spool-") {
							t.Fatal("Linux spool remains reachable by pathname")
						}
					}
				}
				if err := os.WriteFile(path, []byte("PGDMPchanged-after-verification"), 0600); err != nil {
					t.Fatal(err)
				}
				copied, err := io.ReadAll(spool)
				if err != nil || !bytes.Equal(copied, before) {
					t.Fatal("verified spool followed mutable archive", err)
				}
				if err := spool.Close(); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".restore-spool-") {
					t.Fatal("restore spool retained after close/failure")
				}
			}
		})
	}
}
