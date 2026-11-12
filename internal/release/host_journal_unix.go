//go:build !windows

package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func createPrivateHostDirectory(directory string) error { return os.Mkdir(directory, 0o700) }

func localHostDirectory(_ string) bool { return true }

func privateHostFile(file *os.File, directory bool) error {
	var filesystem syscall.Statfs_t
	if err := syscall.Fstatfs(int(file.Fd()), &filesystem); err != nil {
		return err
	}
	// Supported Linux local filesystems only. In particular, do not rely on a
	// network filesystem's lock surviving reconnect or lease loss.
	switch uint32(filesystem.Type) {
	case 0xef53, 0x58465342, 0x9123683e, 0x01021994, 0x794c7630: // ext, XFS, Btrfs, tmpfs, overlay
	default:
		return ErrHostJournalIdentity
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 || (!directory && stat.Nlink != 1) {
		return ErrHostJournalIdentity
	}
	return nil
}

func hostFileIdentity(file *os.File) (string, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("release-host/unix/v1:%d:%d", stat.Dev, stat.Ino)))
	return hex.EncodeToString(digest[:]), nil
}

func lockHostJournal(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrHostJournalBusy
	}
	if err != nil {
		return ErrHostJournalIdentity
	}
	return nil
}

func syncHostDirectory(file *os.File) error {
	parent, err := os.Open(filepath.Dir(file.Name()))
	if err != nil {
		return err
	}
	// Persist both the journal entry and the newly created directory's name.
	return errors.Join(file.Sync(), parent.Sync(), parent.Close())
}
