package main

import "testing"

func TestMatchesComfyCommand(t *testing.T) {
	tests := []struct {
		name        string
		commandLine string
		signature   string
		want        bool
	}{
		{name: "default main script", commandLine: `python.exe main.py --listen 0.0.0.0`, signature: "main.py", want: true},
		{name: "legacy reordered flags", commandLine: `python.exe main.py --listen 0.0.0.0 --enable-manager`, signature: legacyComfyCommandSignature, want: true},
		{name: "legacy shortcut without manager", commandLine: `python.exe main.py --listen 0.0.0.0`, signature: legacyComfyCommandSignature, want: true},
		{name: "unrelated python", commandLine: `python.exe worker.py`, signature: "main.py", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesComfyCommand(test.commandLine, test.signature); got != test.want {
				t.Fatalf("matchesComfyCommand() = %v, want %v", got, test.want)
			}
		})
	}
}
