package updateagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestComfyStorageWindowsPrimitives(t *testing.T) {
	directory := t.TempDir()
	if _, err := storageRootIdentity(directory); err != nil {
		t.Fatalf("root identity: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("rooted lock file open: %v", err)
	}
	defer file.Close()
	if err := lockComfyStorageFile(file); err != nil {
		t.Fatalf("LockFileEx: %v", err)
	}
	if err := writeComfyStorageJSON(root, "fixture.json", map[string]int{"protocol": 1}); err != nil {
		t.Fatalf("write journal: %v", err)
	}
}

func TestComfyStorageManualCleanupRejectsWindowsJunction(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	outside := t.TempDir()
	file := filepath.Join(outside, "file-000.png")
	if err := os.WriteFile(file, []byte("tracked fixture file 0"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(f.target.InputDirectory, "escape")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, outside)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("temporary junction: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	f.request.Items[0].File.Subfolder = "escape"
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "rejected" || result.MutationStarted || result.Items[0].FreedBytes != 0 {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, []string{file}, true)
}
