//go:build linux

package release

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

type linuxDatabaseWriter struct{ command *exec.Cmd }

func startDatabaseWriter(executable string, args, environment []string, directory string, input, output *os.File) (databaseWriterProcess, error) {
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return nil, errDatabaseWriter
	}
	// Credential-changing exec can clear PR_SET_PDEATHSIG. PostgreSQL client
	// installations used for restore must have neither set-ID bits nor file caps.
	if size, err := unix.Getxattr(executable, "security.capability", nil); size > 0 ||
		(err != nil && err != unix.ENODATA && err != unix.ENOTSUP) {
		return nil, errDatabaseWriter
	}
	command := exec.Command(executable, args...)
	command.Env, command.Dir = environment, directory
	command.Stdin, command.Stdout, command.Stderr = input, output, output
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		return nil, errDatabaseWriter
	}
	return &linuxDatabaseWriter{command: command}, nil
}

func (p *linuxDatabaseWriter) wait() error      { return p.command.Wait() }
func (p *linuxDatabaseWriter) terminate() error { return p.command.Process.Kill() }
func (p *linuxDatabaseWriter) close()           {}

func (p *linuxDatabaseWriter) identity() (databaseProcessIdentity, error) {
	return databaseProcessLinuxIdentity(p.command.Process.Pid)
}
