//go:build !windows

package release

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostPlanUnixFileBoundary(t *testing.T) {
	for _, mode := range []string{"permissions", "hardlink", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			directory, j := hostTestJournal(t)
			plan := hostPlanFixture(directory)
			if err := j.Begin(plan.Operation()); err != nil {
				t.Fatal(err)
			}
			if err := j.PrepareDockerPlan(plan); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(directory, j.planName())
			switch mode {
			case "permissions":
				if err := os.Chmod(name, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, name+".link"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".old", name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := j.ReadDockerPlan(); err == nil {
				t.Fatal("unsafe plan file accepted", mode)
			}
		})
	}
}
