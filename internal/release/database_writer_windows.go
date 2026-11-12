//go:build windows

package release

import (
	"os"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This Windows 10 attribute assigns the job before the initial thread runs.
// A suspended-create/assign/resume sequence would still orphan a suspended
// process if its parent died before assignment.
const databaseWriterJobListAttribute = 0x0002000d

type windowsDatabaseWriter struct {
	process windows.Handle
	job     windows.Handle
}

func startDatabaseWriter(executable string, args, environment []string, directory string, input, output *os.File) (_ databaseWriterProcess, err error) {
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, errDatabaseWriter
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{executable}, args...)))
	if err != nil {
		return nil, errDatabaseWriter
	}
	cwd, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return nil, errDatabaseWriter
	}
	env, err := databaseWriterWindowsEnvironment(environment)
	if err != nil {
		return nil, errDatabaseWriter
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, errDatabaseWriter
	}
	defer func() {
		if err != nil {
			windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, errDatabaseWriter
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, errDatabaseWriter
	}
	defer attributes.Delete()
	if err = attributes.Update(databaseWriterJobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return nil, errDatabaseWriter
	}

	// Duplicate just the pipe and NUL handles, never the job or archive handle.
	// HANDLE_LIST prevents accidental inheritance of other concurrent resources.
	var handles [2]windows.Handle
	defer func() {
		for _, handle := range handles {
			if handle != 0 {
				windows.CloseHandle(handle)
			}
		}
	}()
	for i, file := range []*os.File{input, output} {
		if err = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(),
			&handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, errDatabaseWriter
		}
	}
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return nil, errDatabaseWriter
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[1]
	var process windows.ProcessInformation
	err = windows.CreateProcess(application, command, nil, nil, true,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		&env[0], cwd, &startup.StartupInfo, &process)
	runtime.KeepAlive(env)
	if err != nil {
		return nil, errDatabaseWriter
	}
	windows.CloseHandle(process.Thread)
	return &windowsDatabaseWriter{process: process.Process, job: job}, nil
}

func (p *windowsDatabaseWriter) wait() error {
	state, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		return errDatabaseWriter
	}
	var code uint32
	if windows.GetExitCodeProcess(p.process, &code) != nil || code != 0 {
		return errDatabaseWriter
	}
	return nil
}

func (p *windowsDatabaseWriter) terminate() error { return windows.TerminateJobObject(p.job, 1) }
func (p *windowsDatabaseWriter) identity() (databaseProcessIdentity, error) {
	return databaseProcessWindowsIdentity(p.process)
}

func (p *windowsDatabaseWriter) close() {
	windows.CloseHandle(p.job)
	windows.CloseHandle(p.process)
}

func databaseWriterWindowsEnvironment(environment []string) ([]uint16, error) {
	values := append([]string(nil), environment...)
	keys := make(map[string]bool, len(values))
	for _, value := range values {
		key, _, ok := strings.Cut(value, "=")
		key = strings.ToUpper(key)
		if !ok || key == "" || keys[key] || strings.ContainsRune(value, 0) {
			return nil, errDatabaseWriter
		}
		keys[key] = true
	}
	sort.Slice(values, func(i, j int) bool { return strings.ToUpper(values[i]) < strings.ToUpper(values[j]) })
	var block []uint16
	for _, value := range values {
		block = append(block, utf16.Encode([]rune(value))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0), nil
}
