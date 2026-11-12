package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"ai-access-gateway/internal/updates"
)

func TestCleanupInspectionAndDeleteRejectWindowsJunction(t *testing.T) {
	inputDirectory, outsideDirectory := t.TempDir(), t.TempDir()
	payload := []byte("outside junction bytes")
	outside := filepath.Join(outsideDirectory, "outside.png")
	if err := os.WriteFile(outside, payload, 0600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(inputDirectory, "escape")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, outsideDirectory)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create temporary junction: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	digest := sha256.Sum256(payload)
	files := []updates.ComfyAssetFile{{Filename: "outside.png", Subfolder: "escape", StorageType: "input", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}}
	target := ComfyTarget{InputDirectory: inputDirectory}
	inventory, inventoryErr := ComfyFileInventory(context.Background(), target, updates.ComfyFileInventoryQuery{Kind: "input", Limit: 50})
	if inventoryErr != nil || len(inventory.Items) != 1 || inventory.Items[0].EntryType != "link" || inventory.LogicalBytes != 0 {
		t.Fatal("inventory followed Windows junction", inventory, inventoryErr)
	}
	for _, candidate := range []string{junction, junction + string(filepath.Separator)} {
		rootInventory, rootErr := ComfyFileInventory(context.Background(), ComfyTarget{InputDirectory: candidate}, updates.ComfyFileInventoryQuery{Kind: "input", Limit: 50})
		if rootErr != nil || rootInventory.Issue != "root_rejected" || rootInventory.Complete || len(rootInventory.Items) != 0 {
			t.Fatal("inventory accepted junction root", rootInventory, rootErr)
		}
	}
	if result, err := InspectComfyAssetFiles(context.Background(), target, files); err == nil || result.Complete {
		t.Fatal("junction inspection escaped root", result, err)
	}
	if result, err := DeleteComfyAssetFiles(context.Background(), target, files); err == nil || result.Deleted != 0 {
		t.Fatal("junction deletion escaped root", result, err)
	}
	if actual, err := os.ReadFile(outside); err != nil || string(actual) != string(payload) {
		t.Fatal("outside file changed", err)
	}
}
