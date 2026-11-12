package miningagent

import (
	"ai-access-gateway/internal/mining"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestObservedInventoryExternalMinerTree(t *testing.T) {
	catalog := mining.FileCatalog{Files: []mining.LaunchFile{
		{Path: `C:\Майнинг\OneZero\quantus.bat`, Name: "quantus.bat", Kind: "script"},
		{Path: `C:\Майнинг\OneZero\other.bat`, Name: "other.bat", Kind: "script"},
		{Path: `C:\Майнинг\OneZero\onezerominer.exe`, Name: "onezerominer.exe", Kind: "executable"},
	}}
	processes := []windowsProcess{
		{PID: 10, Name: "cmd.exe", CommandLine: `cmd /c "C:\Майнинг\OneZero\quantus.bat" secret-wallet`, CreationUnix: 100},
		{PID: 11, ParentPID: 10, Name: "onezerominer.exe", CreationUnix: 101},
		{PID: 12, ParentPID: 11, Name: "onezerominer.exe", CreationUnix: 102},
		{PID: 13, Name: "onezerominer.exe", CreationUnix: 103},
		{PID: 99, Name: "unrelated.exe"},
	}
	result := observedInventory(catalog, processes, managedMinerState{}, time.Now())
	if len(result.States) != 2 {
		t.Fatalf("states=%+v", result.States)
	}
	known, unknown := 0, 0
	for _, s := range result.States {
		if s.ScriptPath != "" {
			known += len(s.PIDs)
			if s.ScriptPath != catalog.Files[0].Path {
				t.Fatal(s)
			}
		} else {
			unknown += len(s.PIDs)
		}
	}
	if known != 2 || unknown != 1 {
		t.Fatal(result.States)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "secret-wallet") || strings.Contains(string(data), "CommandLine") {
		t.Fatal("command parameters leaked")
	}
	if inventoryAllowsStart(result, mining.Request{ProcessName: "onezerominer.exe", ScriptPath: catalog.Files[0].Path}) == nil {
		t.Fatal("unknown process allowed a start")
	}
	result = observedInventory(catalog, processes[:3], managedMinerState{}, time.Now())
	if err := inventoryAllowsStart(result, mining.Request{ProcessName: "onezerominer.exe", ScriptPath: catalog.Files[0].Path}); err != nil {
		t.Fatal(err)
	}
	if inventoryAllowsStart(result, mining.Request{ProcessName: "onezerominer.exe", ScriptPath: catalog.Files[1].Path}) == nil {
		t.Fatal("wrong BAT allowed")
	}
	processes[0].CommandLine = `cmd /c "C:\Майнинг\OneZero\quantus.bat" "C:\Майнинг\OneZero\other.bat"`
	result = observedInventory(catalog, processes[:3], managedMinerState{}, time.Now())
	if len(result.States) != 1 || result.States[0].ScriptPath != "" {
		t.Fatal("ambiguous script attributed", result.States)
	}
}

func TestCommandReferencesScriptBoundaries(t *testing.T) {
	path := `C:\Mining\miner\go.bat`
	for _, command := range []string{`cmd /c "C:\Mining\miner\go.bat"`, `cmd /c C:\Mining\miner\go.bat & pause`} {
		if !commandReferencesScript(command, path) {
			t.Fatal(command)
		}
	}
	for _, command := range []string{`cmd /c C:\Mining\miner\go.bat.old`, `cmd /c X:\otherC:\Mining\miner\go.bat`, `cmd /c go.bat`, `cmd /c C:\Mining\other\go.bat`} {
		if commandReferencesScript(command, path) {
			t.Fatal(command)
		}
	}
}

func TestInventoryEndpointRequiresAuthentication(t *testing.T) {
	// The existing fake intentionally lacks the optional inventory capability.
	server, err := NewServer(strings.Repeat("a", 32), &fakeController{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/inventory", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
}

func TestInventoryKeepsIndependentLaunchesSeparate(t *testing.T) {
	catalog := mining.FileCatalog{Files: []mining.LaunchFile{
		{Path: `C:\Mining\miner\go.bat`, Name: "go.bat", Kind: "script"},
		{Path: `C:\Mining\miner\miner.exe`, Name: "miner.exe", Kind: "executable"},
	}}
	processes := []windowsProcess{
		{PID: 1, Name: "cmd.exe", CommandLine: `cmd /c "C:\Mining\miner\go.bat"`, CreationUnix: 100},
		{PID: 2, ParentPID: 1, Name: "miner.exe", CreationUnix: 101},
		{PID: 3, ParentPID: 2, Name: "miner.exe", CreationUnix: 102},
		{PID: 4, ParentPID: 1, Name: "miner.exe", CreationUnix: 103},
	}
	result := observedInventory(catalog, processes, managedMinerState{}, time.Now())
	if len(result.States) != 2 || len(result.States[0].PIDs) != 2 || len(result.States[1].PIDs) != 1 {
		t.Fatal(result.States)
	}
	if inventoryAllowsStart(result, mining.Request{ProcessName: "miner.exe", ScriptPath: catalog.Files[0].Path}) == nil {
		t.Fatal("multiple independent roots allowed an idempotent start")
	}
	processes[1].ExecutablePath = `C:\Other\miner.exe`
	result = observedInventory(catalog, processes[:2], managedMinerState{}, time.Now())
	if result.States[0].ScriptPath != "" {
		t.Fatal("foreign executable attributed to catalog BAT")
	}
}

func TestInventoryRejectsReusedParentAndCycles(t *testing.T) {
	for _, parent := range []windowsProcess{
		{PID: 1, Name: "cmd.exe", CommandLine: `cmd /c "C:\Mining\go.bat"`, CreationUnix: 200},
		{PID: 1, ParentPID: 2, Name: "miner.exe", CreationUnix: 100},
	} {
		catalog := mining.FileCatalog{Files: []mining.LaunchFile{{Path: `C:\Mining\go.bat`, Kind: "script"}, {Name: "miner.exe", Kind: "executable"}}}
		result := observedInventory(catalog, []windowsProcess{parent, {PID: 2, ParentPID: 1, Name: "miner.exe", CreationUnix: 101}}, managedMinerState{}, time.Now())
		for _, state := range result.States {
			if state.ScriptPath != "" {
				t.Fatal(state)
			}
		}
	}
}

func TestCommandScriptPathRejectsAmbiguity(t *testing.T) {
	scripts := []string{`C:\Mining\go.bat`, `C:\Mining\other\go.bat`}
	for _, command := range []string{`cmd /c "C:\Mining\go.bat.old"`, `cmd /c go.bat`, `cmd /c "C:\Mining\go.bat" & "C:\Mining\other\go.bat"`} {
		if commandScriptPath(command, scripts) != "" {
			t.Fatal(command)
		}
	}
	if commandReferencesScript("cmd /c something", "") {
		t.Fatal("empty path matched")
	}
}

func TestInventoryManagedWorkerInheritsVerifiedRoot(t *testing.T) {
	now := time.Unix(100, 0)
	catalog := mining.FileCatalog{Files: []mining.LaunchFile{{Path: `C:\Mining\go.bat`, Kind: "script"}, {Name: "miner.exe", Kind: "executable"}}}
	managed := managedMinerState{ProcessName: "miner.exe", ScriptPath: `C:\Mining\go.bat`, PIDs: []int{1}, StartedAt: now}
	processes := []windowsProcess{{PID: 1, Name: "miner.exe", CreationUnix: 101}, {PID: 2, ParentPID: 1, Name: "miner.exe", CreationUnix: 500}}
	result := observedInventory(catalog, processes, managed, time.Now())
	if len(result.States) != 1 || result.States[0].ScriptPath != managed.ScriptPath || len(result.States[0].PIDs) != 2 {
		t.Fatal(result.States)
	}
	processes[0].CreationUnix = 499
	result = observedInventory(catalog, processes, managed, time.Now())
	if result.States[0].ScriptPath != "" {
		t.Fatal("reused managed PID was trusted")
	}
}
