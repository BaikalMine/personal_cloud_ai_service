//go:build windows

package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"ai-access-gateway/internal/database"
	"golang.org/x/sys/windows"
)

func hideHostRecoveryProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func TestHostRecoveryWindowsDPAPI(t *testing.T) {
	plaintext, entropy := []byte("fixture-private-runtime-token"), []byte("fixture-exact-file-context")
	sealed, err := protectHostRecovery(plaintext, entropy)
	if err != nil || bytes.Contains(sealed, plaintext) {
		t.Fatal("DPAPI protection failed")
	}
	actual, err := unprotectHostRecovery(sealed, entropy)
	if err != nil || !bytes.Equal(actual, plaintext) {
		t.Fatal("DPAPI round trip failed")
	}
	if _, err := unprotectHostRecovery(sealed, []byte("another-file-context")); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal("wrong DPAPI context accepted")
	}
	sealed[len(sealed)/2] ^= 1
	if actual, err := unprotectHostRecovery(sealed, entropy); err == nil && bytes.Equal(actual, plaintext) {
		t.Fatal("changed DPAPI blob accepted unchanged")
	}
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := j.WithRuntimeRecovery(binding, func(request database.RuntimeReleaseRequest) error { token = request.Token; return nil }); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(directory, j.recoveryName(binding)))
	if err != nil {
		t.Fatal(err)
	}
	var envelope hostRecoveryEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Protection != "windows-user-dpapi-v1" ||
		bytes.Contains(payload, []byte(token)) || bytes.Contains(envelope.Data, []byte(token)) {
		t.Fatal("plaintext token persisted on Windows")
	}
}

func TestHostRecoveryWindowsACL(t *testing.T) {
	directory, j := recoveryTestJournal(t)
	binding := hostRecoveryTestBinding()
	if err := j.PrepareRuntimeRecovery(binding); err != nil {
		t.Fatal(err)
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
	if err := windows.SetNamedSecurityInfo(filepath.Join(directory, j.recoveryName(binding)), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := j.WithRuntimeRecovery(binding, func(database.RuntimeReleaseRequest) error { t.Fatal("broad ACL callback"); return nil }); !errors.Is(err, ErrHostRecoveryUnavailable) {
		t.Fatal(err)
	}
}
