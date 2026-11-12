//go:build windows

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHostDatabaseTargetWindowsProtection(t *testing.T) {
	directory, j, options, receipt := hostDatabaseTargetFixture(t)
	path := filepath.Join(directory, databaseTargetMetadataName(receipt.TargetSHA256))
	payload, _, err := j.readDatabaseTargetMetadata(context.Background(), receipt.TargetSHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(body, &envelope) != nil || bytes.Contains(envelope.Data, []byte(payload.Configuration.URL)) || bytes.Contains(body, []byte(payload.Configuration.URL)) {
		t.Fatal("target credentials are not encrypted")
	}
	if _, err := unprotectHostPlan(envelope.Data, []byte(databaseArchiveScope)); err == nil {
		t.Fatal("target accepted archive purpose")
	}
	account, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + account.User.Sid.String() + ")(A;;FA;;;SY)(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReadDockerRestoreDatabase(context.Background(), 0, options); err == nil {
		t.Fatal("broad target configuration ACL accepted")
	}
}
