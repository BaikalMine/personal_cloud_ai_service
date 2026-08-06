//go:build windows

package miningagent

import "testing"

func TestArchiveMatchesMinerByProcessName(t *testing.T) {
	if !archiveMatchesMiner("SRBMiner-Multi-3-5-0-win64.zip", "SRBMiner Pearl", "SRBMiner-MULTI.exe") {
		t.Fatal("expected SRBMiner archive to match the process name")
	}
	if archiveMatchesMiner("xmrig-6-24-0-win64.zip", "SRBMiner Pearl", "SRBMiner-MULTI.exe") {
		t.Fatal("unexpected match for a different miner")
	}
}

func TestSafeArchivePathRejectsTraversal(t *testing.T) {
	for _, value := range []string{"../start.bat", `..\\start.bat`, `/start.bat`, `C:\\start.bat`} {
		if _, err := safeArchivePath(value); err == nil {
			t.Fatalf("expected unsafe archive path %q to be rejected", value)
		}
	}
}

func TestSafeArchivePathNormalizesNestedFile(t *testing.T) {
	path, err := safeArchivePath(`SRBMiner\\start.bat`)
	if err != nil || path != "SRBMiner/start.bat" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}
