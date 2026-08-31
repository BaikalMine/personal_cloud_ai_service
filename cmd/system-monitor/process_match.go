package main

import "strings"

const legacyComfyCommandSignature = "main.py --enable-manager"

func matchesComfyCommand(commandLine, signature string) bool {
	commandLine = strings.ToLower(strings.TrimSpace(commandLine))
	signature = strings.ToLower(strings.TrimSpace(signature))
	if commandLine == "" || signature == "" {
		return false
	}
	matched := true
	for _, token := range strings.Fields(signature) {
		if !strings.Contains(commandLine, token) {
			matched = false
			break
		}
	}
	if matched {
		return true
	}
	// Older installations required the Manager flag to appear immediately
	// after main.py. Keep them compatible with shortcuts that omit or reorder it.
	return signature == legacyComfyCommandSignature && strings.Contains(commandLine, "main.py")
}
