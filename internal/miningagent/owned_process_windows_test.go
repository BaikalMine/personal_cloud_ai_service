//go:build windows

package miningagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ai-access-gateway/internal/mining"

	"golang.org/x/sys/windows"
)

type ownedFixturePIDs struct{ Launcher, Child int }

func ownedHelperCommand(t *testing.T, mode, path, lock string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestOwnedProcessHelper$", "--", mode, path, lock)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	return command
}

func TestOwnedProcessHelper(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 3 {
		t.Skip("owned process subprocess helper")
	}
	mode, path, lock := args[0], args[1], args[2]
	write := func(value ownedFixturePIDs) {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	switch mode {
	case "leaf":
		write(ownedFixturePIDs{Child: os.Getpid()})
	case "launcher", "delayed-launcher":
		if mode == "delayed-launcher" {
			write(ownedFixturePIDs{Launcher: os.Getpid()})
			time.Sleep(time.Second)
		}
		child := ownedHelperCommand(t, "leaf", path+".leaf", lock)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		write(ownedFixturePIDs{Launcher: os.Getpid(), Child: child.Process.Pid})
		defer func() { child.Process.Kill(); child.Wait() }()
	case "owner", "delayed-owner":
		group, err := openOwnedProcessGroup(lock)
		if err != nil {
			t.Fatal(err)
		}
		defer group.Close()
		launcherMode := "launcher"
		if mode == "delayed-owner" {
			launcherMode = "delayed-launcher"
		}
		process, err := group.Start(context.Background(), ownedHelperCommand(t, launcherMode, path, lock), false)
		if err != nil {
			t.Fatal(err)
		}
		go process.Wait()
	default:
		t.Fatal("unknown fixture mode")
	}
	time.Sleep(30 * time.Second)
}

func waitOwnedPIDs(t *testing.T, path string, child bool) ownedFixturePIDs {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var pids ownedFixturePIDs
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &pids) == nil && pids.Launcher > 0 && (!child || pids.Child > 0) {
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("owned fixture did not start")
	return ownedFixturePIDs{}
}

func waitOwnedExit(t *testing.T, handle windows.Handle) {
	t.Helper()
	state, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned process survived: state=%d error=%v", state, err)
	}
}

func TestOwnedGroupStopsWholeTreeAndCanRestart(t *testing.T) {
	dir := t.TempDir()
	group, err := openOwnedProcessGroup(filepath.Join(dir, "owner.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	for i := 0; i < 2; i++ {
		path := filepath.Join(dir, []string{"first.json", "second.json"}[i])
		process, err := group.Start(context.Background(), ownedHelperCommand(t, "launcher", path, ""), false)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- process.Wait() }()
		pids := waitOwnedPIDs(t, path, true)
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pids.Child))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = group.Stop(ctx)
		cancel()
		if err != nil {
			windows.CloseHandle(handle)
			t.Fatal(err)
		}
		waitOwnedExit(t, handle)
		windows.CloseHandle(handle)
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if active, err := group.Active(); err != nil || active != 0 {
			t.Fatal(active, err)
		}
	}
}

func TestOwnedGroupRejectsSecondOwnerWithoutStoppingFirst(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "owner.lock")
	first, err := openOwnedProcessGroup(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	process, err := first.Start(context.Background(), ownedHelperCommand(t, "leaf", filepath.Join(dir, "leaf.json"), ""), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { first.Close(); process.Wait() }()
	if second, err := openOwnedProcessGroup(lock); err == nil {
		second.Close()
		t.Fatal("second owner accepted")
	}
	if active, err := first.Active(); err != nil || active < 1 {
		t.Fatal("second owner touched first job", active, err)
	}
	if state, err := process.Running(); err != nil || !state {
		t.Fatal("second owner stopped the first launcher", state, err)
	}
	first.Stop(context.Background())
}

func TestOwnedGroupCannotReopenAfterClose(t *testing.T) {
	group, err := openOwnedProcessGroup(filepath.Join(t.TempDir(), "owner.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Close(); err != nil {
		t.Fatal(err)
	}
	if err = group.Stop(context.Background()); err == nil {
		t.Fatal("closed group reopened")
	}
	if _, err = group.Start(context.Background(), ownedHelperCommand(t, "leaf", "unused", ""), false); err == nil {
		t.Fatal("closed group started a process")
	}
}

func newOwnedFixtureController(t *testing.T) (*windowsController, string) {
	t.Helper()
	dir := t.TempDir()
	controller, err := NewOwnedController(dir, filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	c := controller.(*windowsController)
	c.ownedHidden = true
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, dir
}

func TestOwnedControllerStartStopAndRestart(t *testing.T) {
	c, dir := newOwnedFixtureController(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	digest := sha256.Sum256([]byte(dir))
	name := fmt.Sprintf("owned-fixture-%x.exe", digest[:6])
	copyPath := filepath.Join(dir, name)
	target, err := os.Create(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal(copyErr, closeErr)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := filepath.Join(dir, "fixture.ps1")
	content := "& " + quote(copyPath) + " '-test.run=^TestOwnedProcessHelper$' '--' 'leaf' " + quote(filepath.Join(dir, "leaf.json")) + " 'unused'\r\n"
	if err = os.WriteFile(script, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	request := mining.Request{ScriptPath: script, ProcessName: name}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		state, err := c.Start(ctx, request)
		cancel()
		if err != nil || !state.Running || len(state.PIDs) != 1 {
			t.Fatal("start", state, err)
		}
		if !c.ownedScriptMatches(request.ScriptPath) || !c.ownedScriptMatches(c.scriptPath) {
			t.Fatal("submitted and canonical paths must identify the same launcher")
		}
		if i == 1 {
			if err := os.Remove(script); err != nil {
				t.Fatal(err)
			}
		}
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(state.PIDs[0]))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		state, err = c.Stop(ctx, request)
		cancel()
		if err != nil || state.Running {
			windows.CloseHandle(handle)
			t.Fatalf("stop: state=%+v err=%v ownedPath=%q requestedPath=%q ownedName=%q requestedName=%q", state, err, c.scriptPath, request.ScriptPath, c.managedProcessName, request.ProcessName)
		}
		waitOwnedExit(t, handle)
		windows.CloseHandle(handle)
	}
}

func TestOwnedControllerSecondInstanceCannotTouchState(t *testing.T) {
	c, dir := newOwnedFixtureController(t)
	marker := []byte("incomplete state belonging to the first instance")
	if err := os.WriteFile(c.stateFile, marker, 0600); err != nil {
		t.Fatal(err)
	}
	if second, err := NewOwnedController(dir, c.outputLog); err == nil {
		second.(*windowsController).Close()
		t.Fatal("second controller accepted")
	}
	state, err := os.ReadFile(c.stateFile)
	if err != nil || string(state) != string(marker) {
		t.Fatal("second controller modified first owner's state", string(state), err)
	}
}

func TestOwnedControllerPendingTreeIsNotIdle(t *testing.T) {
	c, dir := newOwnedFixtureController(t)
	request := mining.Request{ScriptPath: filepath.Join(dir, "pending.ps1"), ProcessName: "owned-pending-nonexistent.exe"}
	process, err := c.owned.Start(context.Background(), ownedHelperCommand(t, "leaf", filepath.Join(dir, "leaf.json"), ""), false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	c.scriptPath, c.managedProcessName, c.managedPID = request.ScriptPath, request.ProcessName, process.PID
	if state, err := c.State(context.Background(), request.ProcessName); err == nil || state.Running {
		t.Fatal("pending tree reported idle", state, err)
	}
	if state, err := c.State(context.Background(), "other-inactive-profile.exe"); err != nil || state.Running {
		t.Fatal("owned tree made an unrelated inactive profile unavailable", state, err)
	}
	other := request
	other.ScriptPath = filepath.Join(dir, "other.ps1")
	if _, err := c.Stop(context.Background(), other); err == nil {
		t.Fatal("wrong profile stopped tree")
	}
	if active, err := c.owned.Active(); err != nil || active == 0 {
		t.Fatal("wrong profile touched tree", active, err)
	}
	server, err := NewFencedServer(fenceTestToken, c)
	if err != nil {
		t.Fatal(err)
	}
	for i, action := range []string{"stop", "start"} {
		command := mining.FencedCommand{Session: server.session, Revision: int64(i + 1), Action: action, Miner: other}
		if reply := sendFence(server, context.Background(), command); reply.Code != 409 || server.commandUnconfirmed {
			t.Fatal("rejected profile poisoned command reconciliation", reply.Code, reply.Body.String())
		}
	}
	if _, err := c.Start(context.Background(), request); err == nil {
		t.Fatal("second launcher accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := mining.FencedCommand{Session: server.session, Revision: 3, Action: "stop", Miner: request}
	if reply := sendFence(server, ctx, command); reply.Code != 200 {
		t.Fatal("correct profile cannot reconcile", reply.Code, reply.Body.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state, err := c.State(ctx, request.ProcessName); err != nil || state.Running {
		t.Fatal("drained tree not idle", state, err)
	}
}

func TestOwnedGroupOwnerCrashKillsOnlyOwnedProcesses(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole-tree", true: "before-child-start"}[delayed], func(t *testing.T) {
			dir := t.TempDir()
			lock := filepath.Join(dir, "owner.lock")
			path := filepath.Join(dir, "tree.json")
			mode := "owner"
			if delayed {
				mode = "delayed-owner"
			}
			owner := ownedHelperCommand(t, mode, path, lock)
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					owner.Process.Kill()
					owner.Wait()
				}
			}()
			pids := waitOwnedPIDs(t, path, !delayed)
			var handles []windows.Handle
			for _, pid := range []int{pids.Launcher, pids.Child} {
				if pid > 0 {
					h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
					if err != nil {
						t.Fatal(err)
					}
					handles = append(handles, h)
				}
			}
			defer func() {
				for _, h := range handles {
					windows.CloseHandle(h)
				}
			}()
			sentinel := ownedHelperCommand(t, "leaf", filepath.Join(dir, "unrelated.json"), "")
			if err := sentinel.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { sentinel.Process.Kill(); sentinel.Wait() }()
			sentinelHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(sentinel.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(sentinelHandle)
			if err = owner.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = owner.Wait(); err == nil {
				t.Fatal("owner did not crash")
			}
			waited = true
			for _, h := range handles {
				waitOwnedExit(t, h)
			}
			if state, err := windows.WaitForSingleObject(sentinelHandle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
				t.Fatal("unrelated fixture was stopped", state, err)
			}
			if delayed {
				time.Sleep(1200 * time.Millisecond)
				if _, err = os.Stat(path + ".leaf"); !os.IsNotExist(err) {
					t.Fatal("late child started after owner crash")
				}
			}
			for _, h := range handles {
				windows.CloseHandle(h)
			}
			handles = nil
			restarted, err := openOwnedProcessGroup(lock)
			if err != nil {
				t.Fatal("owner restart", err)
			}
			defer restarted.Close()
			if active, err := restarted.Active(); err != nil || active != 0 {
				t.Fatal(active, err)
			}
		})
	}
}
