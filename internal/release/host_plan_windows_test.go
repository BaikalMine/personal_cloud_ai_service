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

func TestHostPlanWindowsProtection(t *testing.T) {
	directory, j := hostTestJournal(t)
	plan := hostPlanFixture(directory)
	if err := j.Begin(plan.Operation()); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareDockerPlan(plan); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, j.planName())
	payload, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(payload, &envelope) != nil || bytes.Contains(envelope.Data, []byte("private-plan-secret")) || bytes.Contains(payload, []byte("private-plan-secret")) {
		t.Fatal("plan not encrypted on Windows")
	}
	if _, err := unprotectHostPlan(envelope.Data, []byte("wrong-purpose")); err == nil {
		t.Fatal("wrong DPAPI context accepted")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FR;;;WD)")
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
	if _, err := j.ReadDockerPlan(); err == nil {
		t.Fatal("broad plan ACL accepted")
	}
}
