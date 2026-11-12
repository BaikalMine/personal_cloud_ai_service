package main

import "testing"

func TestExecutionMode(t *testing.T) {
	for _, value := range []string{"", "direct", "windows-owned", " windows-owned ", "windows", "true"} {
		mode, err := executionMode(value)
		valid := value != "windows" && value != "true"
		if (err == nil) != valid || (valid && mode != "direct" && mode != "windows-owned") {
			t.Fatalf("mode %q: %q %v", value, mode, err)
		}
	}
}
