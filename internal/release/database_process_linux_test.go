//go:build linux

package release

import (
	"os"
	"strings"
	"testing"
)

func TestDatabaseProcessLinuxStat(t *testing.T) {
	body := "123 (client ) with (odd)\nname) S " + strings.Repeat("0 ", 18) + "12345 0\n"
	if started, err := databaseProcessLinuxStart([]byte(body), 123); err != nil || started != 12345 {
		t.Fatalf("comm broke stat parsing: %d, %v", started, err)
	}
	for _, invalid := range []string{"", "123", "123 (name)", "123 (name) S 0", strings.Replace(body, "12345", "0", 1),
		strings.Replace(body, "12345", "-1", 1), strings.Replace(body, "12345", "18446744073709551616", 1),
		strings.Replace(body, "123 (", "124 (", 1)} {
		if _, err := databaseProcessLinuxStart([]byte(invalid), 123); err == nil {
			t.Fatalf("malformed stat accepted: %q", invalid)
		}
	}
}

func TestDatabaseProcessLinuxScope(t *testing.T) {
	identity, err := databaseProcessLinuxIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if exited, err := databaseProcessExited(identity); err != nil || exited {
		t.Fatalf("live process reported exited: %v, %v", exited, err)
	}
	otherNamespace := identity
	otherNamespace.PIDNamespace += "1"
	if exited, err := databaseProcessExited(otherNamespace); err == nil || exited {
		t.Fatalf("different namespace resolved: %v, %v", exited, err)
	}
	otherBoot := identity
	otherBoot.BootID = "00000000-0000-0000-0000-000000000000"
	if otherBoot.BootID == identity.BootID {
		t.Fatal("unexpected actual boot ID")
	}
	if exited, err := databaseProcessExited(otherBoot); err != nil || !exited {
		t.Fatalf("previous boot not recognized: %v, %v", exited, err)
	}
}
