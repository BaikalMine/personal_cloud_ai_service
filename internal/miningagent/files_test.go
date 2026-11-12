package miningagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"ai-access-gateway/internal/mining"
)

func TestLaunchFilesConfinedCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "miner"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"start.bat", "other.CMD", "miner.exe", "secret.txt", "start.bat.bak"} {
		if err := os.WriteFile(filepath.Join(root, "miner", name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "outside.bat"), []byte("private"), 0600)
	_ = os.Symlink(outside, filepath.Join(root, "escape"))
	result, err := launchFiles(context.Background(), root)
	if err != nil || len(result.Files) != 3 {
		t.Fatalf("catalog=%+v err=%v", result, err)
	}
	for _, f := range result.Files {
		if f.Folder != "miner" {
			t.Fatalf("escaped file: %+v", f)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := launchFiles(ctx, root); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestSaveEditableScriptBackupAndConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "start.bat")
	original := []byte("@echo off\r\nminer.exe --old\r\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, script, err := editableScript(root, path)
	if err != nil {
		t.Fatal(err)
	}
	input := mining.SaveScriptRequest{Request: mining.Request{ScriptPath: path, ProcessName: "miner.exe"}, Content: "@echo off\nminer.exe --new\n", ExpectedSHA256: script.SHA256}
	updated, err := saveEditableScript(context.Background(), root, input)
	if err != nil || updated.SHA256 == script.SHA256 {
		t.Fatalf("save=%+v err=%v", updated, err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, []byte("@echo off\r\nminer.exe --new\r\n")) {
		t.Fatalf("newlines=%q", data)
	}
	backups, _ := filepath.Glob(path + ".*.bak")
	if len(backups) != 1 {
		t.Fatalf("backups=%v", backups)
	}
	backup, _ := os.ReadFile(backups[0])
	if !bytes.Equal(backup, original) {
		t.Fatal("backup damaged")
	}
	if _, err := saveEditableScript(context.Background(), root, input); err == nil {
		t.Fatal("stale hash accepted")
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, data) {
		t.Fatal("stale edit changed BAT")
	}
}

func TestSaveEditableScriptRejectsUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bat")
	_ = os.WriteFile(outside, []byte("private"), 0600)
	for _, name := range []string{"miner.exe", "legacy.bat"} {
		_ = os.WriteFile(filepath.Join(root, name), []byte{0xff, 0x80}, 0600)
	}
	for _, path := range []string{outside, filepath.Join(root, "miner.exe"), filepath.Join(root, "legacy.bat"), "start.bat"} {
		if _, _, _, err := editableScript(root, path); err == nil {
			t.Fatalf("unsafe file accepted: %s", path)
		}
	}
	link := filepath.Join(root, "link.bat")
	if os.Symlink(outside, link) == nil {
		if _, _, _, err := editableScript(root, link); err == nil {
			t.Fatal("external symlink accepted")
		}
	}
}

func TestScriptEncodingRoundTrip(t *testing.T) {
	for _, data := range [][]byte{[]byte("@echo off\r\n"), append([]byte{0xef, 0xbb, 0xbf}, []byte("@echo off\r\n")...), {0xff, 0xfe, 'A', 0, 13, 0, 10, 0}, {0xfe, 0xff, 0, 'A', 0, 13, 0, 10}} {
		content, encode, err := scriptEncoding(data)
		if err != nil || !bytes.Equal(encode(content), data) {
			t.Fatalf("roundtrip %x: %v", data, err)
		}
	}
}
