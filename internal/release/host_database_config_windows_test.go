//go:build windows

package release

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHostDatabaseConfigurationWindowsProtection(t *testing.T) {
	directory, j, configuration := hostDatabaseConfigurationFixture(t)
	file := filepath.Join(directory, j.databaseConfigName())
	payload, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(payload, &envelope) != nil || bytes.Contains(payload, []byte("fixture-private-password")) || bytes.Contains(envelope.Data, []byte(configuration.URL)) {
		t.Fatal("database configuration is not encrypted on Windows")
	}
	if _, err := unprotectHostPlan(envelope.Data, []byte("wrong-database-purpose")); err == nil {
		t.Fatal("wrong DPAPI purpose accepted")
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
	if err := windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	receipt, err := j.databaseConfigReceipt()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.readDockerDatabaseConfiguration(receipt); err == nil {
		t.Fatal("broad credential ACL accepted")
	}
}
