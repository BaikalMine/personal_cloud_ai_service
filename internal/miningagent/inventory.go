package miningagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ai-access-gateway/internal/mining"
)

type windowsProcess struct {
	PID            int    `json:"ProcessId"`
	ParentPID      int    `json:"ParentProcessId"`
	Name           string `json:"Name"`
	CommandLine    string `json:"CommandLine"`
	ExecutablePath string `json:"ExecutablePath"`
	CreationUnix   int64  `json:"CreationUnix"`
}

type inventoryController interface {
	Inventory(context.Context) (mining.Inventory, error)
}

func inventoryAllowsStart(inventory mining.Inventory, request mining.Request) error {
	running := 0
	for _, state := range inventory.States {
		if state.Running {
			running++
		}
		if state.Running && (!strings.EqualFold(state.ProcessName, request.ProcessName) || !sameWindowsPath(state.ScriptPath, request.ScriptPath)) {
			return errors.New("Работает другой майнер или BAT не подтверждён. Новый запуск не выполнялся.")
		}
	}
	if running > 1 {
		return errors.New("Найдено несколько независимых запусков майнера. Новый запуск не выполнялся.")
	}
	return nil
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	controller, ok := s.controller.(inventoryController)
	if !ok {
		http.Error(w, "inventory not supported", http.StatusNotImplemented)
		return
	}
	result, err := controller.Inventory(r.Context())
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		result.Message = "Не удалось подтвердить состояние всех майнеров."
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// Match only complete catalogued paths, never a filename or a guessed default BAT.
func commandReferencesScript(command, path string) bool {
	if path == "" {
		return false
	}
	command = strings.ToLower(strings.ReplaceAll(command, "/", `\`))
	path = strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	for offset := 0; offset < len(command); {
		index := strings.Index(command[offset:], path)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(path)
		boundary := func(c byte) bool { return unicode.IsSpace(rune(c)) || strings.ContainsRune(`"'&|()`, rune(c)) }
		if (index == 0 || boundary(command[index-1])) && (end == len(command) || boundary(command[end])) {
			return true
		}
		offset = index + 1
	}
	return false
}

func commandScriptPath(command string, scripts []string) string {
	match := ""
	for _, path := range scripts {
		if commandReferencesScript(command, path) {
			if match != "" && !sameWindowsPath(match, path) {
				return ""
			}
			match = path
		}
	}
	return match
}

// Child workers belong to one launch; separate roots remain separate even for the same BAT.
func minerProcessRoot(process windowsProcess, byPID map[int]windowsProcess) windowsProcess {
	seen := map[int]bool{process.PID: true}
	for hops := 0; hops < 16; hops++ {
		parent, ok := byPID[process.ParentPID]
		if !ok || seen[parent.PID] || !strings.EqualFold(parent.Name, process.Name) ||
			(process.CreationUnix > 0 && parent.CreationUnix > process.CreationUnix) ||
			(process.ExecutablePath != "" && parent.ExecutablePath != "" && !sameWindowsPath(process.ExecutablePath, parent.ExecutablePath)) {
			break
		}
		seen[parent.PID] = true
		process = parent
	}
	return process
}

func observedInventory(catalog mining.FileCatalog, processes []windowsProcess, managed managedMinerState, now time.Time) mining.Inventory {
	result := mining.Inventory{CollectedAt: now, Catalog: catalog, States: []mining.State{}}
	names := map[string]bool{}
	executables := map[string]bool{}
	scripts := []string{}
	for _, file := range catalog.Files {
		if file.Kind == "executable" {
			names[strings.ToLower(file.Name)] = true
			executables[strings.ToLower(strings.ReplaceAll(file.Path, "/", `\`))] = true
		}
		if file.Kind == "script" {
			scripts = append(scripts, file.Path)
		}
	}
	byPID := map[int]windowsProcess{}
	for _, process := range processes {
		byPID[process.PID] = process
	}
	grouped := map[string]*mining.State{}
	for _, process := range processes {
		if process.PID <= 0 || !names[strings.ToLower(process.Name)] {
			continue
		}
		script := ""
		root := minerProcessRoot(process, byPID)
		if managedProcessStateMatches(process.PID, process.Name, process.CreationUnix, managed.ProcessName, managed.ScriptPath, managed) ||
			managedProcessStateMatches(root.PID, root.Name, root.CreationUnix, managed.ProcessName, managed.ScriptPath, managed) {
			for _, path := range scripts {
				if sameWindowsPath(path, managed.ScriptPath) {
					script = path
					break
				}
			}
		}
		current := process
		seen := map[int]bool{current.PID: true}
		for hops := 0; script == "" && hops < 16; hops++ {
			parent, ok := byPID[current.ParentPID]
			if !ok || seen[parent.PID] || (current.CreationUnix > 0 && parent.CreationUnix > current.CreationUnix) {
				break
			}
			seen[parent.PID] = true
			if strings.EqualFold(parent.Name, "cmd.exe") || strings.EqualFold(parent.Name, "powershell.exe") || strings.EqualFold(parent.Name, "pwsh.exe") {
				matches := []string{}
				for _, path := range scripts {
					if commandReferencesScript(parent.CommandLine, path) {
						matches = append(matches, path)
					}
				}
				if len(matches) > 1 {
					break
				}
				if len(matches) == 1 {
					script = matches[0]
					break
				}
			}
			current = parent
		}
		if process.ExecutablePath != "" && !executables[strings.ToLower(strings.ReplaceAll(process.ExecutablePath, "/", `\`))] {
			script = ""
		}
		key := strings.ToLower(process.Name) + "|" + strings.ToLower(script) + "|" + strconv.Itoa(root.PID)
		state := grouped[key]
		if state == nil {
			state = &mining.State{Available: true, Running: true, ProcessName: process.Name, ScriptPath: script, CollectedAt: now}
			grouped[key] = state
		}
		state.PIDs = append(state.PIDs, process.PID)
		if process.CreationUnix > 0 {
			created := time.Unix(process.CreationUnix, 0).UTC()
			if state.StartedAt.IsZero() || created.Before(state.StartedAt) {
				state.StartedAt = created
			}
		}
		if script == "" {
			state.Message = "Процесс найден, но BAT не подтверждён."
		}
	}
	for _, state := range grouped {
		sort.Ints(state.PIDs)
		result.States = append(result.States, *state)
	}
	sort.Slice(result.States, func(i, j int) bool {
		a, b := result.States[i], result.States[j]
		if a.ProcessName+"|"+a.ScriptPath == b.ProcessName+"|"+b.ScriptPath {
			return a.PIDs[0] < b.PIDs[0]
		}
		return a.ProcessName+"|"+a.ScriptPath < b.ProcessName+"|"+b.ScriptPath
	})
	return result
}
