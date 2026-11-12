//go:build windows

package processowner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 10's JOB_LIST assigns ownership inside CreateProcess, before any
// launcher code runs. Creating first and assigning later leaves a crash window.
const procThreadAttributeJobList = 0x0002000d

type jobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
	PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
}

type Group struct {
	mu        sync.Mutex
	job, lock windows.Handle
	name      string
}

type Process struct {
	PID    int
	handle windows.Handle
}

func Open(lockPath, namespace string) (*Group, error) {
	if namespace == "" || strings.IndexFunc(namespace, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-')
	}) >= 0 {
		return nil, errors.New("invalid process owner namespace")
	}
	abs, err := filepath.Abs(lockPath)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	path, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return nil, err
	}
	// No sharing: the OS releases this handle when its owner dies. Acquire it
	// before touching the named job, so a second live agent cannot kill the first.
	lock, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("mining process owner lock: %w", err)
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	jobName := fmt.Sprintf(`Global\AI-Gateway-%s-%x`, namespace, digest)
	name, _ := windows.UTF16PtrFromString(jobName)
	job, err := windows.CreateJobObject(nil, name)
	if err != nil {
		windows.CloseHandle(lock)
		return nil, err
	}
	group := &Group{job: job, lock: lock, name: jobName}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = group.Stop(ctx)
		cancel()
	}
	if err != nil {
		group.Close()
		return nil, fmt.Errorf("recover previous mining process group: %w", err)
	}
	return group, nil
}

func (g *Group) activeLocked() (uint32, error) {
	if g.job == 0 {
		return 0, errors.New("mining process group is closed")
	}
	var accounting jobAccounting
	err := windows.QueryInformationJobObject(g.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	return accounting.ActiveProcesses, err
}

func (g *Group) Active() (uint32, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.activeLocked()
}

func (g *Group) stopLocked(ctx context.Context) error {
	if g.job == 0 {
		return nil
	}
	handles, err := g.processHandlesLocked()
	if err != nil {
		return err
	}
	defer func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}()
	if err := windows.TerminateJobObject(g.job, 1); err != nil {
		return err
	}
	for {
		active, err := g.activeLocked()
		if err != nil {
			return err
		}
		if active == 0 {
			// Job accounting can reach zero before descendant handles signal.
			// Retain the original handles so PID reuse cannot falsify stop proof.
			for _, handle := range handles {
				for {
					state, err := windows.WaitForSingleObject(handle, 20)
					if err != nil {
						return err
					}
					if state == windows.WAIT_OBJECT_0 {
						break
					}
					if state != uint32(windows.WAIT_TIMEOUT) {
						return errors.New("owned process wait was not confirmed")
					}
					if err := ctx.Err(); err != nil {
						return err
					}
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (g *Group) processHandlesLocked() ([]windows.Handle, error) {
	for capacity := 64; capacity <= 65536; capacity *= 2 {
		width := int(unsafe.Sizeof(uintptr(0)))
		buffer := make([]byte, 8+capacity*width)
		err := windows.QueryInformationJobObject(g.job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&buffer[0])), uint32(len(buffer)), nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			continue
		}
		if err != nil {
			return nil, err
		}
		assigned, count := binary.LittleEndian.Uint32(buffer[:4]), binary.LittleEndian.Uint32(buffer[4:8])
		if assigned > count || count > uint32(capacity) {
			continue
		}
		var handles []windows.Handle
		fail := func(err error) ([]windows.Handle, error) {
			for _, handle := range handles {
				windows.CloseHandle(handle)
			}
			return nil, err
		}
		for i := 0; i < int(count); i++ {
			pid := uint32(*(*uintptr)(unsafe.Pointer(&buffer[8+i*width])))
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				continue
			}
			if err != nil {
				return fail(err)
			}
			var belongs int32
			ok, _, callErr := isProcessInJob.Call(uintptr(handle), uintptr(g.job), uintptr(unsafe.Pointer(&belongs)))
			if ok == 0 {
				windows.CloseHandle(handle)
				return fail(callErr)
			}
			if belongs != 0 {
				handles = append(handles, handle)
			} else {
				windows.CloseHandle(handle)
			}
		}
		return handles, nil
	}
	return nil, errors.New("owned process list exceeded stop-proof limit")
}

func (g *Group) Stop(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lock == 0 {
		return errors.New("mining process group is closed")
	}
	if err := g.stopLocked(ctx); err != nil {
		return err
	}
	// TerminateJobObject permanently terminates this job's admission. Once every
	// process has exited, replace the kernel object before the next launch.
	if g.job != 0 {
		if err := windows.CloseHandle(g.job); err != nil {
			return err
		}
		g.job = 0
	}
	name, _ := windows.UTF16PtrFromString(g.name)
	job, err := windows.CreateJobObject(nil, name)
	if err != nil {
		return err
	}
	g.job = job
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		g.job = 0
	}
	return err
}

func (g *Group) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 && g.lock == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := g.stopLocked(ctx)
	cancel()
	var closeErr error
	if g.job != 0 {
		closeErr = windows.CloseHandle(g.job)
	}
	g.job = 0
	var lockErr error
	if g.lock != 0 {
		lockErr = windows.CloseHandle(g.lock)
	}
	g.lock = 0
	return errors.Join(err, closeErr, lockErr)
}

func (g *Group) Start(ctx context.Context, command *exec.Cmd, visible bool) (*Process, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if command == nil || command.Err != nil {
		return nil, errors.New("mining launcher executable is unavailable")
	}
	if command.Stdin != nil || command.Stdout != nil || command.Stderr != nil {
		return nil, errors.New("owned process custom streams are not supported")
	}
	environment, err := environmentBlock(command.Environ())
	if err != nil {
		return nil, err
	}
	active, err := g.activeLocked()
	if err != nil {
		return nil, err
	}
	if active != 0 {
		return nil, errors.New("previous mining process group is still active")
	}
	app, err := windows.UTF16PtrFromString(command.Path)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(command.Args))
	if err != nil {
		return nil, err
	}
	var directory *uint16
	if command.Dir != "" {
		directory, err = windows.UTF16PtrFromString(command.Dir)
		if err != nil {
			return nil, err
		}
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	if err = attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&g.job), unsafe.Sizeof(g.job)); err != nil {
		return nil, err
	}
	var startup windows.StartupInfoEx
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.ProcThreadAttributeList = attributes.List()
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_UNICODE_ENVIRONMENT)
	if visible {
		flags |= windows.CREATE_NEW_CONSOLE
	} else {
		flags |= windows.CREATE_NO_WINDOW
	}
	var process windows.ProcessInformation
	if err = windows.CreateProcess(app, line, nil, nil, false, flags, &environment[0], directory, &startup.StartupInfo, &process); err != nil {
		return nil, fmt.Errorf("create owned mining launcher: %w", err)
	}
	windows.CloseHandle(process.Thread)
	return &Process{PID: int(process.ProcessId), handle: process.Process}, nil
}

func (p *Process) Wait() error {
	defer windows.CloseHandle(p.handle)
	_, err := windows.WaitForSingleObject(p.handle, windows.INFINITE)
	return err
}

func (p *Process) Running() (bool, error) {
	state, err := windows.WaitForSingleObject(p.handle, 0)
	return state == uint32(windows.WAIT_TIMEOUT), err
}

func environmentBlock(environment []string) ([]uint16, error) {
	values := append([]string(nil), environment...)
	for _, value := range values {
		if strings.ContainsRune(value, 0) || !strings.Contains(value, "=") {
			return nil, errors.New("invalid owned process environment")
		}
	}
	sort.Slice(values, func(i, j int) bool { return strings.ToUpper(values[i]) < strings.ToUpper(values[j]) })
	block := utf16.Encode([]rune(strings.Join(values, "\x00") + "\x00\x00"))
	return block, nil
}
