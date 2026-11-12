//go:build windows

package release

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHostJournalWindowsPermissions(t *testing.T) {
	for _, target := range []string{"directory", "file"} {
		for _, mode := range []string{"read_everyone", "write_everyone", "inheritance", "null_dacl"} {
			t.Run(target+"/"+mode, func(t *testing.T) {
				directory, j := hostTestJournal(t)
				name := directory
				if target == "file" {
					name = filepath.Join(directory, hostJournalName)
				}
				user, err := windows.GetCurrentProcessToken().GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				sddl := "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)"
				flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
				switch mode {
				case "read_everyone":
					sddl += "(A;OICI;FR;;;WD)"
				case "write_everyone":
					sddl += "(A;OICI;FW;;;WD)"
				case "inheritance":
					flags = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
				}
				sd, err := windows.SecurityDescriptorFromString(sddl)
				if err != nil {
					t.Fatal(err)
				}
				dacl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "null_dacl" {
					dacl = nil
				}
				if err := windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
					t.Fatal(err)
				}
				// Inheritance of a private parent on a regular file is the normal
				// creation policy. A directory itself must prohibit inheritance.
				if target == "file" && mode == "inheritance" {
					return
				}
				if _, err := j.Snapshot(); !errors.Is(err, ErrHostJournalIdentity) {
					t.Fatalf("live broad ACL accepted: %v", err)
				}
				j.Close()
				other, err := OpenHostJournal(directory, hostTestInstallation)
				if !errors.Is(err, ErrHostJournalIdentity) {
					if other != nil {
						other.Close()
					}
					t.Fatalf("broad ACL accepted: %v", err)
				}
			})
		}
	}
}

func TestHostJournalWindowsReparse(t *testing.T) {
	directory, j := hostTestJournal(t)
	j.Close()
	alias := filepath.Join(t.TempDir(), "alias")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, directory)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("temporary junction: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(alias); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	if other, err := OpenHostJournal(alias, hostTestInstallation); !errors.Is(err, ErrHostJournalIdentity) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("reparse directory accepted: %v", err)
	}
}

func TestHostJournalWindowsLocalPath(t *testing.T) {
	for _, directory := range []string{`\\unavailable-host\share\release`, `\\?\C:\release`, `C:relative`} {
		if validHostDirectory(directory) {
			t.Fatalf("non-local or ambiguous path accepted: %q", directory)
		}
	}
}
