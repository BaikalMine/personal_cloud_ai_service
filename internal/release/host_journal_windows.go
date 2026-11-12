//go:build windows

package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const hostFileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func localHostDirectory(directory string) bool {
	volume := filepath.VolumeName(directory)
	if len(volume) != 2 || volume[1] != ':' || strings.HasPrefix(volume, `\`) {
		return false
	}
	name, err := windows.UTF16PtrFromString(volume + `\`)
	return err == nil && windows.GetDriveType(name) == windows.DRIVE_FIXED
}

func createPrivateHostDirectory(directory string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	return windows.CreateDirectory(name, &attributes)
}

func privateHostFile(file *os.File, directory bool) error {
	handle := windows.Handle(file.Fd())
	path := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(handle, &path[0], uint32(len(path)), 0)
	if err != nil || n == 0 || n >= uint32(len(path)) {
		return ErrHostJournalIdentity
	}
	resolved := windows.UTF16ToString(path[:n])
	if !strings.HasPrefix(resolved, `\\?\`) || !localHostDirectory(strings.TrimPrefix(resolved, `\\?\`)) {
		return ErrHostJournalIdentity
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (!directory && info.NumberOfLinks != 1) {
		return ErrHostJournalIdentity
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(sd)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) {
		return ErrHostJournalIdentity
	}
	control, _, err := sd.Control()
	if err != nil || (directory && control&windows.SE_DACL_PROTECTED == 0) {
		return ErrHostJournalIdentity
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return ErrHostJournalIdentity
	}
	ownerFull := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 20 {
			return ErrHostJournalIdentity
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len()+8 > int(ace.Header.AceSize) {
			return ErrHostJournalIdentity
		}
		isOwner := windows.EqualSid(sid, user.User.Sid)
		if !isOwner && !sid.IsWellKnown(windows.WinLocalSystemSid) {
			return ErrHostJournalIdentity
		}
		if isOwner && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 && uint32(ace.Mask)&hostFileAllAccess == hostFileAllAccess {
			ownerFull = true
		}
	}
	if !ownerFull {
		return ErrHostJournalIdentity
	}
	return nil
}

func hostFileIdentity(file *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("release-host/windows/v1:%08x:%08x%08x:%08x%08x", info.VolumeSerialNumber,
		info.FileIndexHigh, info.FileIndexLow, info.CreationTime.HighDateTime, info.CreationTime.LowDateTime)))
	return hex.EncodeToString(digest[:]), nil
}

func lockHostJournal(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrHostJournalBusy
	}
	if err != nil {
		return ErrHostJournalIdentity
	}
	return nil
}

// Windows flushes the journal handle with FlushFileBuffers (os.File.Sync).
// There is no portable directory fsync equivalent; power-loss/storage recovery
// must still validate the complete chain and may require operator intervention.
func syncHostDirectory(_ *os.File) error { return nil }
