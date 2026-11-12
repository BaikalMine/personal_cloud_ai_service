package updateagent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/updates"
)

func comfyBackupPython(t *testing.T) string {
	t.Helper()
	python := os.Getenv("COMFY_GUARD_TEST_PYTHON")
	if python == "" {
		t.Skip("COMFY_GUARD_TEST_PYTHON is not set")
	}
	resolved, err := exec.LookPath(python)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func comfyBackupArchive(t *testing.T, payload []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "comfy-storage.zip")
	if err := os.WriteFile(name, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestComfyStorageBackupRoundtrip(t *testing.T) {
	python := comfyBackupPython(t)
	for _, mode := range []string{"empty", "completed", "expired", "pending", "held", "unreceived"} {
		t.Run(mode, func(t *testing.T) {
			f := newComfyStorageFixture(t, 2)
			ctx := context.Background()
			if mode == "completed" || mode == "expired" {
				if _, err := f.executor.Execute(ctx, f.request); err != nil {
					t.Fatal(err)
				}
				if mode == "expired" {
					if err := f.executor.Prune(ctx, time.Now().Add(91*24*time.Hour)); err != nil {
						t.Fatal(err)
					}
				}
			} else if mode != "empty" {
				_, hash, _ := updates.CanonicalComfyStorageRequest(f.request)
				result := newComfyStorageResult(f.request, hash)
				if mode != "unreceived" {
					if state, err := f.executor.guard.window(ctx, "hold", result); err != nil || state != "held" {
						t.Fatal(state, err)
					}
				}
				result.MutationStarted = mode == "pending"
				root, lock, err := f.executor.openJournal()
				if err != nil {
					t.Fatal(err)
				}
				err = f.executor.persist(root, comfyStoragePendingFile, result)
				lock.Close()
				root.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			var archive bytes.Buffer
			manifest, err := ExportComfyStorageBackup(ctx, f.target, &archive)
			if err != nil || manifest.ExecutorID != f.executor.identity.ExecutorID || manifest.GuardJournalID != f.guard.journal {
				t.Fatal(manifest, err)
			}
			destination := filepath.Join(t.TempDir(), "restored")
			restored, err := RestoreComfyStorageBackup(ctx, comfyBackupArchive(t, archive.Bytes()), destination, python)
			if err != nil || !sameComfyBackupSummary(manifest, restored) {
				t.Fatal(restored, err)
			}
			identity, err := os.ReadFile(filepath.Join(destination, "executor", "identity.json"))
			var saved comfyStorageIdentity
			if err != nil || json.Unmarshal(identity, &saved) != nil || saved != f.executor.identity {
				t.Fatal("host roots rebound by restore", saved, err)
			}
			target := f.target
			storage := *target.Storage
			storage.JournalDirectory = filepath.Join(destination, "executor")
			target.Storage = &storage
			if _, err := os.Stat(filepath.Join(storage.JournalDirectory, comfyStorageRestoreMarker)); err != nil {
				t.Fatal("restored executor lacks isolation marker", err)
			}
			if _, err := NewComfyStorageExecutor(target); !errors.Is(err, ErrComfyStorageIdentity) {
				t.Fatal("restored executor admitted live work", err)
			}
			probe := exec.Command(python, "-I", "-S", "-B", "-c", `import sqlite3,sys; db=sqlite3.connect(sys.argv[1]); print(db.execute("PRAGMA quick_check").fetchone()[0]); print(db.execute("SELECT count(*) FROM storage_windows WHERE state='held'").fetchone()[0])`, filepath.Join(destination, "guard", "receipts.sqlite"))
			output, err := probe.Output()
			want := "ok\n0"
			if mode == "pending" || mode == "held" {
				want = "ok\n1"
			}
			if err != nil || strings.ReplaceAll(strings.TrimSpace(string(output)), "\r", "") != want {
				t.Fatal("restored guard hold changed", string(output), err)
			}
			if mode == "pending" || mode == "held" || mode == "unreceived" {
				payload, err := os.ReadFile(filepath.Join(destination, "executor", comfyStoragePendingFile))
				var pending updates.ComfyStorageResult
				if err != nil || json.Unmarshal(payload, &pending) != nil || pending.ID != f.request.ID || pending.ExecutorFenced || pending.GuardClosed || pending.State != "running" {
					t.Fatal("restore settled interrupted operation", pending, err)
				}
			}
			if _, err := RestoreComfyStorageBackup(ctx, comfyBackupArchive(t, archive.Bytes()), destination, python); err == nil {
				t.Fatal("overwrote existing restore")
			}
			if after, err := os.ReadFile(filepath.Join(destination, "executor", "identity.json")); err != nil || !bytes.Equal(identity, after) {
				t.Fatal("failed restore changed existing bytes", err)
			}
		})
	}
}

type comfyBackupWriter func([]byte) (int, error)

func (write comfyBackupWriter) Write(payload []byte) (int, error) { return write(payload) }

func TestComfyStorageBackupLockAndChangedGuard(t *testing.T) {
	for _, mode := range []string{"lock", "changed", "unavailable", "write_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newComfyStorageFixture(t, 1)
			if _, err := f.executor.Execute(context.Background(), f.request); err != nil {
				t.Fatal(err)
			}
			if mode == "changed" {
				f.guard.onBackup = func() {
					if f.guard.backups == 2 {
						f.guard.journal = strings.Repeat("9", 32)
					}
				}
			}
			if mode == "unavailable" {
				f.guard.fail["backup"] = true
			}
			var archive bytes.Buffer
			checked := false
			writer := comfyBackupWriter(func(payload []byte) (int, error) {
				if mode == "write_failure" {
					return 0, io.ErrClosedPipe
				}
				if !checked {
					checked = true
					if _, err := f.executor.Fence(context.Background(), f.request); !errors.Is(err, ErrComfyStorageBusy) {
						t.Error("backup lost exclusive journal lock", err)
					}
				}
				return archive.Write(payload)
			})
			_, err := f.executor.ExportBackup(context.Background(), writer)
			if (err == nil) != (mode == "lock") {
				t.Fatal(mode, err)
			}
			root, lock, err := f.executor.openJournal()
			if err != nil {
				t.Fatal("backup leaked lock", err)
			}
			lock.Close()
			root.Close()
		})
	}
}

func TestComfyStorageBackupDoesNotInitializeMissingJournal(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	f.target.Storage.JournalDirectory = t.TempDir()
	if _, err := ExportComfyStorageBackup(context.Background(), f.target, io.Discard); err == nil {
		t.Fatal("export initialized a new host identity")
	}
	entries, err := os.ReadDir(f.target.Storage.JournalDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatal("export wrote into missing journal", entries, err)
	}
}

func TestComfyStorageBackupDoesNotCreateMissingLock(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	path := filepath.Join(f.target.Storage.JournalDirectory, ".lock")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportComfyStorageBackup(context.Background(), f.target, io.Discard); err == nil {
		t.Fatal("export accepted a missing lock")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("export created a lock", err)
	}
}

func TestComfyStorageBackupPublishedReceiptWithLeftoverPending(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	ctx := context.Background()
	result, err := f.executor.Execute(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	// finish persists fencing before close, then the receipt, then removes intent.
	result.GuardClosed = false
	root, lock, err := f.executor.openJournal()
	if err != nil {
		t.Fatal(err)
	}
	err = f.executor.persist(root, comfyStoragePendingFile, result)
	lock.Close()
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.executor.Status(ctx)
	if err != nil || status.PendingID != "" || status.GuardHeld {
		t.Fatal("closed intent prevented future dispatch", status, err)
	}
	if _, err := os.Stat(filepath.Join(f.target.Storage.JournalDirectory, comfyStoragePendingFile)); err != nil {
		t.Fatal("status mutated the journal", err)
	}
	var archive bytes.Buffer
	manifest, err := f.executor.ExportBackup(ctx, &archive)
	if err != nil || manifest.HostReceipts != 1 || manifest.PendingID != result.ID {
		t.Fatal("backup lost recoverable leftover intent", manifest, err)
	}
	f.request.ID = strings.Repeat("d", 32)
	if _, err := f.executor.Execute(ctx, f.request); err != nil {
		t.Fatal("next operation could not remove closed intent", err)
	}
}

func rewriteComfyBackup(t *testing.T, archive []byte, change func(map[string][]byte, *ComfyStorageBackupManifest)) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	var manifest ComfyStorageBackupManifest
	for _, entry := range reader.File {
		payload, err := readComfyBackupEntry(entry, maxComfyStorageBackupManifestBytes)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name == "manifest.json" {
			if err := json.Unmarshal(payload, &manifest); err != nil {
				t.Fatal(err)
			}
		} else {
			files[entry.Name] = payload
		}
	}
	change(files, &manifest)
	manifest.Files = nil
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range names {
		payload := files[name]
		manifest.Files = append(manifest.Files, ComfyStorageBackupFile{Path: name, Bytes: int64(len(payload)), SHA256: comfyBackupDigest(payload)})
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	metadata, _ := json.Marshal(manifest)
	entry, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(metadata); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestComfyStorageBackupRejectsInconsistentArchive(t *testing.T) {
	python := comfyBackupPython(t)
	f := newComfyStorageFixture(t, 1)
	if _, err := f.executor.Execute(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := f.executor.ExportBackup(context.Background(), &archive); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"version", "summary", "root", "different_guard", "missing_window", "phantom_window", "unfenced", "traversal", "duplicate_field", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			modified := rewriteComfyBackup(t, archive.Bytes(), func(files map[string][]byte, manifest *ComfyStorageBackupManifest) {
				var guard comfyGuardBackup
				_ = json.Unmarshal(files["guard/snapshot.json"], &guard)
				switch mode {
				case "version":
					manifest.Version = 99
				case "summary":
					manifest.HostReceipts++
				case "root":
					var identity comfyStorageIdentity
					_ = json.Unmarshal(files["executor/identity.json"], &identity)
					identity.InputRoot = ""
					files["executor/identity.json"], _ = json.Marshal(identity)
				case "different_guard":
					guard.JournalID, manifest.GuardJournalID = strings.Repeat("9", 32), strings.Repeat("9", 32)
				case "missing_window":
					guard.Windows, manifest.GuardWindows = []comfyGuardBackupWindow{}, 0
				case "phantom_window":
					guard.Windows = append(guard.Windows, comfyGuardBackupWindow{ID: strings.Repeat("z", 32), RequestHash: strings.Repeat("7", 64), ExecutorID: manifest.ExecutorID, State: "closed"})
					manifest.GuardWindows++
				case "unfenced":
					name := "executor/receipts/" + f.request.ID + ".json"
					var result updates.ComfyStorageResult
					_ = json.Unmarshal(files[name], &result)
					result.GuardClosed = false
					files[name], _ = json.Marshal(result)
				case "traversal":
					files["../escape.json"] = []byte("outside")
				case "duplicate_field":
					files["executor/identity.json"] = append([]byte(`{"protocol":1,`), files["executor/identity.json"][1:]...)
				case "oversize":
					files["executor/identity.json"] = bytes.Repeat([]byte(" "), maxComfyStorageRecordBytes+1)
				}
				files["guard/snapshot.json"], _ = json.Marshal(guard)
			})
			destination := filepath.Join(t.TempDir(), "restore")
			if _, err := RestoreComfyStorageBackup(context.Background(), comfyBackupArchive(t, modified), destination, python); err == nil {
				t.Fatal("accepted invalid archive", mode)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed restore published destination", err)
			}
		})
	}
}

func TestComfyStorageBackupPublicationNeverReplacesDirectory(t *testing.T) {
	parent := t.TempDir()
	source, destination := filepath.Join(parent, "source"), filepath.Join(parent, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishComfyBackupRestore(source, destination); err == nil {
		t.Fatal("replaced an existing empty directory")
	}
	after, err := os.Stat(destination)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("publication changed destination identity", err)
	}
}

func TestComfyStorageBackupPowerShellRoundtrip(t *testing.T) {
	powershell, backupTool, trainingTool := os.Getenv("COMFY_BACKUP_TEST_POWERSHELL"), os.Getenv("COMFY_BACKUP_TEST_TOOL"), os.Getenv("COMFY_BACKUP_TEST_TRAINING_TOOL")
	if powershell == "" || backupTool == "" || trainingTool == "" {
		t.Skip("Windows backup orchestration tools are not configured")
	}
	f := newComfyStorageFixture(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := f.executor.Execute(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(Config{ComfyUI: f.target})
	filename := filepath.Join(t.TempDir(), "update-config.json")
	if err := os.WriteFile(filename, config, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, powershell, "-NoProfile", "-File", "../../scripts/test-training-backup.ps1",
		"-TrainingBackupTool", trainingTool, "-ComfyStorageBackupTool", backupTool,
		"-UpdateConfig", filename, "-ComfyStorageRestorePython", comfyBackupPython(t))
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("Dual ZIP backup and isolated restore passed")) {
		t.Fatal("PowerShell orchestration failed", string(output), err)
	}
}
