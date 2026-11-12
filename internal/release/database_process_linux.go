//go:build linux

package release

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func databaseProcessLinuxScope() (string, string, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || !databaseBootID.MatchString(strings.TrimSpace(string(boot))) {
		return "", "", errDatabaseWriter
	}
	// Refuse a procfs mounted for another PID namespace.
	self, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return "", "", errDatabaseWriter
	}
	if _, err := databaseProcessLinuxStart(self, os.Getpid()); err != nil {
		return "", "", errDatabaseWriter
	}
	info, err := os.Stat("/proc/self/ns/pid")
	if err != nil {
		return "", "", errDatabaseWriter
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return "", "", errDatabaseWriter
	}
	return strings.TrimSpace(string(boot)), fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func databaseProcessLinuxStart(body []byte, pid int) (uint64, error) {
	text := string(body)
	open, close := strings.Index(text, " ("), strings.LastIndex(text, ") ")
	if open < 1 || close <= open+1 || text[:open] != strconv.Itoa(pid) {
		return 0, errDatabaseWriter
	}
	// comm may contain spaces, parentheses and newlines. Field 22 is starttime;
	// splitting only after the final ')' keeps those bytes out of the fields.
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 {
		return 0, errDatabaseWriter
	}
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || started == 0 {
		return 0, errDatabaseWriter
	}
	return started, nil
}

func databaseProcessLinuxIdentity(pid int) (databaseProcessIdentity, error) {
	boot, namespace, err := databaseProcessLinuxScope()
	if err != nil {
		return databaseProcessIdentity{}, errDatabaseWriter
	}
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return databaseProcessIdentity{}, errDatabaseWriter
	}
	started, err := databaseProcessLinuxStart(body, pid)
	identity := databaseProcessIdentity{Platform: "linux", PID: pid, Started: started, BootID: boot, PIDNamespace: namespace}
	if err != nil || !identity.valid() {
		return databaseProcessIdentity{}, errDatabaseWriter
	}
	return identity, nil
}

func databaseProcessExited(identity databaseProcessIdentity) (bool, error) {
	if !identity.valid() || identity.Platform != "linux" {
		return false, errDatabaseWriter
	}
	boot, namespace, err := databaseProcessLinuxScope()
	if err != nil {
		return false, errDatabaseWriter
	}
	if boot != identity.BootID {
		return true, nil
	}
	if namespace != identity.PIDNamespace {
		return false, errDatabaseWriter
	}
	// pidfd pins the observed process across exit/reuse while /proc is read.
	fd, err := unix.PidfdOpen(identity.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, errDatabaseWriter
	}
	defer unix.Close(fd)
	body, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", identity.PID))
	if readErr == nil {
		started, err := databaseProcessLinuxStart(body, identity.PID)
		if err != nil {
			return false, errDatabaseWriter
		}
		if started != identity.Started {
			return true, nil
		}
	} else if !os.IsNotExist(readErr) {
		return false, errDatabaseWriter
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if _, err := unix.Poll(poll, 0); err != nil || poll[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
		return false, errDatabaseWriter
	}
	if poll[0].Revents&unix.POLLIN != 0 {
		return true, nil
	}
	if readErr != nil {
		return false, errDatabaseWriter
	}
	return false, nil
}
