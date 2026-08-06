//go:build windows

package miningagent

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

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

func TestArchiveNameUsesContentDispositionAfterGitHubRedirect(t *testing.T) {
	finalURL, _ := url.Parse("https://release-assets.githubusercontent.com/asset/123")
	originalURL, _ := url.Parse("https://github.com/example/miner/releases/download/v1/SRBMiner-Multi-3-5-0-win64.zip")
	response := &http.Response{Request: &http.Request{URL: finalURL}, Header: make(http.Header)}
	response.Header.Set("Content-Disposition", "attachment; filename=SRBMiner-Multi-3-5-0-win64.zip")
	name, err := archiveNameFromResponse(response, originalURL)
	if err != nil || name != "SRBMiner-Multi-3-5-0-win64.zip" {
		t.Fatalf("name=%q err=%v", name, err)
	}

	response.Header.Set("Content-Disposition", "attachment; filename*=UTF-8''SRBMiner-Multi-3-5-0-win64.zip")
	name, err = archiveNameFromResponse(response, originalURL)
	if err != nil || !strings.HasSuffix(name, ".zip") {
		t.Fatalf("encoded name=%q err=%v", name, err)
	}
}
