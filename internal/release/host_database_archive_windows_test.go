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

func TestHostDatabaseArchiveWindowsProtection(t *testing.T) {
	for _, kind := range []string{"archive", "metadata", "password"} {
		t.Run(kind, func(t *testing.T) {
			directory, j, options, receipt := hostDatabaseArchiveFixture(t)
			archiveName, metadataName, passwordName, _ := databaseArchiveNames(receipt.TargetSHA256)
			body, err := os.ReadFile(filepath.Join(directory, metadataName))
			if err != nil {
				t.Fatal(err)
			}
			var envelope hostRecoveryEnvelope
			if json.Unmarshal(body, &envelope) != nil || bytes.Contains(envelope.Data, []byte(options.Tool.Path)) {
				t.Fatal("archive metadata is not encrypted")
			}
			if _, err := unprotectHostPlan(envelope.Data, []byte("wrong-archive-purpose")); err == nil {
				t.Fatal("wrong archive purpose accepted")
			}
			name := archiveName
			if kind == "metadata" {
				name = metadataName
			} else if kind == "password" {
				name = passwordName
				if err := j.writeArchivePassword(name, []byte("private-pgpass")); err != nil {
					t.Fatal(err)
				}
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
			if err := windows.SetNamedSecurityInfo(filepath.Join(directory, name), windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := j.archiveProof(context.Background(), name, options.MaxBytes); err == nil {
				t.Fatal("broad archive/credential ACL accepted")
			}
			if kind != "password" {
				if _, err := j.ReadDockerDatabaseArchive(context.Background(), 0, options); err == nil {
					t.Fatal("unsafe archive accepted")
				}
			}
		})
	}
}
