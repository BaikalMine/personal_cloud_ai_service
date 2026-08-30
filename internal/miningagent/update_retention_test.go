package miningagent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneMinerUpdateArtifactsKeepsNewestDirectories(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "miner")
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{".backup-", ".failed-"} {
		for index := 0; index < 5; index++ {
			path := current + marker + fmt.Sprintf("%02d", index)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			stamp := time.Unix(int64(index+1), 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := pruneMinerUpdateArtifacts(current); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{".backup-", ".failed-"} {
		matches, err := filepath.Glob(current + marker + "*")
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != maxRetainedMinerUpdateDirectories {
			t.Fatalf("%s directories = %d", marker, len(matches))
		}
		if _, err := os.Stat(current + marker + "04"); err != nil {
			t.Fatalf("newest %s directory was removed: %v", marker, err)
		}
	}
}
