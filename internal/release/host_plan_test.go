package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

// A structural fixture only; live observation is covered in docker_plan_test.
func hostPlanFixture(directory string) DockerHostPlan {
	configuration, _ := json.Marshal(map[string]any{"Config": map[string]any{"Env": []string{"DATABASE_URL=private-plan-secret", strings.Repeat("x", 64<<10)}},
		"HostConfig": map[string]any{}, "Mounts": []any{}, "NetworkSettings": map[string]any{}})
	return DockerHostPlan{data: dockerHostPlanData{Version: 1, Scope: dockerHostPlanScope, OperationID: hostTestSpec().ID,
		Previous:       hostPlanPackage{Directory: filepath.Join(directory, "previous"), DirectoryID: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64)},
		Candidate:      hostPlanPackage{Directory: filepath.Join(directory, "candidate"), DirectoryID: strings.Repeat("c", 64), ManifestSHA256: strings.Repeat("d", 64)},
		EngineEndpoint: "private-engine-endpoint", GatewayPort: 8090, Origin: "http://127.0.0.1:18120",
		Identity: DockerGatewayIdentity{EngineID: dockerFixtureEngineID, ContainerID: dockerFixtureContainerID, ImageID: "sha256:" + strings.Repeat("e", 64),
			ContainerStartedAt: "2026-09-12T01:00:00Z", InitPID: 1234, GatewayPID: 1234, ExecutableSHA256: strings.Repeat("f", 64)},
		Observed: dockerPlanObservation{Configuration: configuration, Database: database.RuntimeDatabaseIdentity{SystemIdentifier: "123456789", DatabaseOID: 123,
			DatabaseName: "private-plan-database", PostmasterStartedAt: "2026-09-12T00:00:00Z"}}}}
}

func TestHostPlanLifecycleAndRedaction(t *testing.T) {
	directory, j := hostTestJournal(t)
	plan := hostPlanFixture(directory)
	if err := j.Begin(plan.Operation()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReadDockerPlan(); !errors.Is(err, ErrHostPlanUnavailable) {
		t.Fatal(err)
	}
	if err := j.PrepareDockerPlan(plan); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(directory, j.planName()))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareDockerPlan(plan); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("overwrote committed plan", err)
	}
	j.Close()
	j, err = OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	loaded, err := j.ReadDockerPlan()
	if err != nil || loaded.Operation() != plan.Operation() || !bytes.Equal(loaded.data.Observed.Configuration, plan.data.Observed.Configuration) {
		t.Fatal("plan changed across owner epochs", err)
	}
	after, err := os.ReadFile(filepath.Join(directory, j.planName()))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read rewrote plan")
	}
	snapshot, events, err := j.OperationHistory()
	if err != nil || len(events) != 4 || events[1].Effect != HostPrepareDockerPlan || events[2].Observation != HostApplied {
		t.Fatal("missing receipt", err)
	}
	public, _ := json.Marshal(loaded)
	journal, _ := os.ReadFile(filepath.Join(directory, hostJournalName))
	state, _ := json.Marshal(struct {
		Snapshot HostJournalSnapshot
		Events   []HostJournalEvent
	}{snapshot, events})
	for _, exposed := range []string{string(public), string(journal), string(state), fmt.Sprintf("%v %+v %#v", loaded, loaded, loaded)} {
		for _, secret := range []string{"private-plan-secret", "private-plan-database", "private-engine-endpoint", directory, plan.data.Origin} {
			if strings.Contains(exposed, secret) {
				t.Fatal("private plan material escaped")
			}
		}
	}
	if err := j.Finish(strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReadDockerPlan(); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("finished operation returned live plan", err)
	}
}

func TestHostPlanDigestCoversConcreteData(t *testing.T) {
	plan := hostPlanFixture(t.TempDir())
	for _, mutate := range []func(*dockerHostPlanData){
		func(p *dockerHostPlanData) { p.Previous.Directory += "other" },
		func(p *dockerHostPlanData) { p.Candidate.ManifestSHA256 = strings.Repeat("e", 64) },
		func(p *dockerHostPlanData) { p.Candidate.DirectoryID = strings.Repeat("e", 64) },
		func(p *dockerHostPlanData) { p.EngineEndpoint += "other" },
		func(p *dockerHostPlanData) { p.GatewayPort++ },
		func(p *dockerHostPlanData) { p.Origin = "http://127.0.0.1:18121" },
		func(p *dockerHostPlanData) { p.Identity.ContainerStartedAt = "2026-09-12T02:00:00Z" },
		func(p *dockerHostPlanData) { p.Observed.RestartCount++ },
		func(p *dockerHostPlanData) { p.Observed.Database.DatabaseOID++ },
		func(p *dockerHostPlanData) { p.Observed.Database.PostmasterStartedAt += "different" },
		func(p *dockerHostPlanData) {
			p.Observed.Configuration = bytes.ReplaceAll(p.Observed.Configuration, []byte("private-plan-secret"), []byte("changed-plan-secret"))
		},
	} {
		changed := plan
		mutate(&changed.data)
		if changed.Operation().ID == "" || changed.Operation().PlanSHA256 == plan.Operation().PlanSHA256 {
			t.Fatal("concrete input missing from digest")
		}
	}
	if (DockerHostPlan{}).Operation() != (HostOperationSpec{}) {
		t.Fatal("zero plan accepted")
	}
}

func TestHostPlanRejectsChangedMaterial(t *testing.T) {
	for _, mode := range []string{"missing", "truncated", "replaced", "wrong_receipt", "unbound_operation"} {
		t.Run(mode, func(t *testing.T) {
			directory, j := hostTestJournal(t)
			plan := hostPlanFixture(directory)
			if err := j.Begin(plan.Operation()); err != nil {
				t.Fatal(err)
			}
			if mode == "unbound_operation" {
				changed := plan
				changed.data.GatewayPort++
				before, _ := j.Snapshot()
				if err := j.PrepareDockerPlan(changed); !errors.Is(err, ErrHostJournalState) {
					t.Fatal(err)
				}
				after, _ := j.Snapshot()
				if before != after {
					t.Fatal("unbound plan changed journal")
				}
				return
			}
			if mode == "wrong_receipt" {
				intent, err := j.Intent(HostPrepareDockerPlan, plan.Operation().PlanSHA256)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := j.createDockerPlan(plan); err != nil {
					t.Fatal(err)
				}
				if err := j.Observe(intent.Sequence, HostApplied, strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := j.PrepareDockerPlan(plan); err != nil {
					t.Fatal(err)
				}
				name := filepath.Join(directory, j.planName())
				payload, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "missing":
					if err := os.Remove(name); err != nil {
						t.Fatal(err)
					}
				case "truncated":
					if err := os.WriteFile(name, payload[:len(payload)/2], 0o600); err != nil {
						t.Fatal(err)
					}
				case "replaced":
					if err := os.Rename(name, name+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, payload, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := j.ReadDockerPlan(); !errors.Is(err, ErrHostPlanUnavailable) {
				t.Fatal("accepted changed private material", err)
			}
		})
	}
}

func TestHostPlanReconcilePartial(t *testing.T) {
	directory, j := hostTestJournal(t)
	plan := hostPlanFixture(directory)
	if err := j.Begin(plan.Operation()); err != nil {
		t.Fatal(err)
	}
	intent, err := j.Intent(HostPrepareDockerPlan, plan.Operation().PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, j.planName()), []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReconcileDockerPlan(); !errors.Is(err, ErrHostPlanUnavailable) {
		t.Fatal(err)
	}
	state, _ := j.Snapshot()
	if state.Pending != intent {
		t.Fatal("partial plan cleared uncertainty")
	}
}

func TestHostPlanProcessHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_HOST_PLAN_HELPER")
	if mode == "" {
		return
	}
	directory := os.Getenv("RELEASE_HOST_JOURNAL_DIRECTORY")
	j, err := OpenHostJournal(directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	plan := hostPlanFixture(directory)
	if err := j.Begin(plan.Operation()); err != nil {
		t.Fatal(err)
	}
	if mode == "prepared" {
		if err := j.PrepareDockerPlan(plan); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := j.Intent(HostPrepareDockerPlan, plan.Operation().PlanSHA256); err != nil {
			t.Fatal(err)
		}
		if mode == "before_observe" {
			if _, err := j.createDockerPlan(plan); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "plan-ready"), []byte(plan.Operation().PlanSHA256), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestHostPlanProcessCrash(t *testing.T) {
	for _, mode := range []string{"prepared", "before_observe", "before_file"} {
		t.Run(mode, func(t *testing.T) {
			directory, j := hostTestJournal(t)
			j.Close()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(binary, "-test.run=^TestHostPlanProcessHelper$", "-test.timeout=30s")
			hideHostRecoveryProcess(child)
			child.Env = append(os.Environ(), "RELEASE_HOST_PLAN_HELPER="+mode, "RELEASE_HOST_JOURNAL_DIRECTORY="+directory)
			var output bytes.Buffer
			child.Stdout, child.Stderr = &output, &output
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					child.Process.Kill()
					child.Wait()
				}
			}()
			deadline := time.Now().Add(12 * time.Second)
			for {
				if ready, err := os.ReadFile(filepath.Join(directory, "plan-ready")); err == nil && validHostHash(string(ready)) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("plan child did not reach crash point")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if other, err := OpenHostJournal(directory, hostTestInstallation); !errors.Is(err, ErrHostJournalBusy) {
				if other != nil {
					other.Close()
				}
				t.Fatal("owner lock lost", err)
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			waited = true
			j, err = OpenHostJournal(directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if mode != "prepared" {
				if _, err := j.ReadDockerPlan(); !errors.Is(err, ErrHostPlanUnavailable) {
					t.Fatal("unreceipted plan available", err)
				}
				observed, err := j.ReconcileDockerPlan()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "before_file" {
					if observed != HostNotApplied {
						t.Fatal("absent file accepted")
					}
					if err := j.PrepareDockerPlan(hostPlanFixture(directory)); err != nil {
						t.Fatal(err)
					}
				} else if observed != HostApplied {
					t.Fatal("complete file not recovered")
				}
			}
			loaded, err := j.ReadDockerPlan()
			if err != nil || loaded.Operation() != hostPlanFixture(directory).Operation() {
				t.Fatal("crash changed plan", err)
			}
		})
	}
}
