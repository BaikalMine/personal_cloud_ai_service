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

func TestHostDatabaseRestoreAttemptWindowsProtection(t *testing.T) {
	for _, phase := range []string{"prepared", "writer", "completed", "resolved"} {
		t.Run(phase, func(t *testing.T) {
			directory, j, head, digest := hostDatabaseRestoreAttemptFixture(t)
			if phase == "resolved" {
				var err error
				directory, j, head, digest, err = hostRestoreResolutionFixture(t, HostApplied, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(directory, databaseRestoreRecordName(head.Binding.Target, phase))
			r, _, err := j.readDatabaseRestoreRecord(context.Background(), head.Binding.Target, phase, "")
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var envelope hostRecoveryEnvelope
			if json.Unmarshal(body, &envelope) != nil || bytes.Contains(envelope.Data, []byte(head.Binding.ToolIdentity)) {
				t.Fatal("attempt binding is not encrypted")
			}
			r.Context.Purpose = databaseRestoreScope + "/different-phase"
			entropy, _ := json.Marshal(r.Context)
			if _, err := unprotectHostPlan(envelope.Data, entropy); err == nil {
				t.Fatal("wrong phase purpose accepted")
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
			if _, _, err := j.readDatabaseRestoreChain(context.Background(), head.Binding.Target, head.Phase, digest); err == nil {
				t.Fatal("broad attempt ACL accepted")
			}
		})
	}
}
