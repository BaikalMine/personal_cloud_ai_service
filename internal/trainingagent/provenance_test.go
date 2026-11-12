package trainingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func provenanceTestFiles(t *testing.T) (*jobRecord, ProfileConfig, Config) {
	t.Helper()
	root := t.TempDir()
	write := func(name, data string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	profile := ProfileConfig{ID: "test-krea", Family: "krea2", BaseModel: "base.safetensors",
		DiT: write("base.safetensors", "base bytes"), VAE: write("vae.safetensors", "vae bytes"),
		TextEncoder: write("encoder.safetensors", "encoder bytes")}
	write("tuner/src/train.py", "print('fixture, no GPU')\n")
	write("tuner/src/__init__.py", "")
	write("tuner/requirements.txt", "torch==fixture\n")
	config := Config{RootDir: root, TunerDir: filepath.Join(root, "tuner"), Profiles: []ProfileConfig{profile}}
	record := &jobRecord{DatasetPath: write("dataset.zip", "archive fixture"), ProfileSnapshot: &profile,
		Spec: loratraining.JobSpec{GatewayJobID: "provenance-test", ProfileID: profile.ID,
			Resolution: 512, MaxSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 14, SampleCount: 2}}
	return record, profile, config
}

func TestExecutionUsesRecordedProfileNotCurrentCatalog(t *testing.T) {
	record, profile, config := provenanceTestFiles(t)
	config.Profiles[0].DiT = "changed-current-model"
	controller := &Controller{config: config}
	actual, err := controller.executionProfile(record)
	if err != nil || actual.DiT != profile.DiT {
		t.Fatal("current profile replaced recorded execution", err)
	}
	controller.config.Profiles = nil
	if _, err := controller.executionProfile(record); err != nil {
		t.Fatal("removed current profile invalidated recorded profile", err)
	}
	record.ProfileSnapshot.ID = "another-job"
	if _, err := controller.executionProfile(record); err == nil {
		t.Fatal("foreign profile snapshot accepted")
	}
	record.ProfileSnapshot = nil
	if _, err := controller.executionProfile(record); err == nil {
		t.Fatal("missing legacy profile was fabricated")
	}
	controller.config = config
	if actual, err := controller.executionProfile(record); err != nil || actual.DiT != "changed-current-model" {
		t.Fatal("legacy execution fallback unavailable", err)
	}
}

func TestTrainingProvenanceActualFilesAndMutation(t *testing.T) {
	record, profile, config := provenanceTestFiles(t)
	p, sources, err := captureTrainingProvenance(context.Background(), record, profile, config, profile.DiT)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("base bytes"))
	if p.Models[1].SHA256 != hex.EncodeToString(want[:]) || p.Models[1].Name != "base.safetensors" || p.Artifact != nil {
		t.Fatalf("wrong execution identity: %+v", p)
	}
	data, err := json.Marshal(p)
	if err != nil || strings.Contains(string(data), config.RootDir) || strings.Contains(string(data), "tuner/src") {
		t.Fatal("passport contains a host path", err)
	}
	if err := verifyTrainingProvenance(context.Background(), p, sources); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{profile.DiT, profile.VAE, profile.TextEncoder, record.DatasetPath, filepath.Join(config.TunerDir, "src", "train.py")} {
		original, _ := os.ReadFile(name)
		if err := os.WriteFile(name, []byte("changed bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyTrainingProvenance(context.Background(), p, sources); err == nil {
			t.Fatal("changed component was accepted", filepath.Base(name))
		}
		if err := os.WriteFile(name, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := captureTrainingProvenance(ctx, record, profile, config, profile.DiT); !errors.Is(err, context.Canceled) {
		t.Fatal("hashing ignored cancellation", err)
	}
}

func TestTunerIdentityStableScopeAndNewSource(t *testing.T) {
	_, _, config := provenanceTestFiles(t)
	first, err := tunerSourceIdentity(context.Background(), config.TunerDir)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(config.TunerDir, "src", "__pycache__")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "ignored.py"), []byte("generated cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := tunerSourceIdentity(context.Background(), config.TunerDir)
	if err != nil || second.SourceSHA256 != first.SourceSHA256 {
		t.Fatal("generated cache changed source digest", err)
	}
	if err := os.WriteFile(filepath.Join(config.TunerDir, "src", "new.py"), []byte("# new code"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := tunerSourceIdentity(context.Background(), config.TunerDir)
	if err != nil || third.SourceSHA256 == first.SourceSHA256 {
		t.Fatal("new source not identified", err)
	}
}

func TestProvenancePersistenceAndStatusCopies(t *testing.T) {
	record, profile, config := provenanceTestFiles(t)
	p, _, err := captureTrainingProvenance(context.Background(), record, profile, config, profile.DiT)
	if err != nil {
		t.Fatal(err)
	}
	record.Status.ID = "test-id"
	if err := os.MkdirAll(filepath.Join(config.RootDir, "jobs", "test-id"), 0o700); err != nil {
		t.Fatal(err)
	}
	controller := &Controller{config: config, jobs: map[string]*jobRecord{"test-id": record}}
	if err := controller.persistProvenance("test-id", p); err != nil {
		t.Fatal(err)
	}
	status, err := controller.Status("test-id")
	if err != nil || status.Provenance == nil {
		t.Fatal(err)
	}
	status.Provenance.Models[0].SHA256 = strings.Repeat("0", 64)
	if record.Status.Provenance.Models[0].SHA256 == status.Provenance.Models[0].SHA256 {
		t.Fatal("status shares internal evidence")
	}
	changed := p.Clone()
	changed.Parameters.Seed++
	if err := controller.persistProvenance("test-id", changed); !errors.Is(err, domain.ErrLoraProvenance) {
		t.Fatal("execution evidence was overwritten", err)
	}
	data, err := os.ReadFile(filepath.Join(config.RootDir, "jobs", "test-id", "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored jobRecord
	if err := json.Unmarshal(data, &stored); err != nil || stored.Status.Provenance == nil || stored.Status.Provenance.Validate() != nil {
		t.Fatal("persisted evidence is incomplete", err)
	}
	var writers sync.WaitGroup
	for writer := 0; writer < 3; writer++ {
		writers.Add(1)
		go func(writer int) {
			defer writers.Done()
			for i := 0; i < 20; i++ {
				switch writer {
				case 0:
					controller.setProgress("test-id", "preparing", "fixture", i, "fixture progress")
				case 1:
					if err := controller.persistProvenance("test-id", p); err != nil {
						t.Error(err)
					}
				case 2:
					data, err := os.ReadFile(filepath.Join(config.RootDir, "jobs", "test-id", "status.json"))
					var snapshot jobRecord
					if err != nil || json.Unmarshal(data, &snapshot) != nil || snapshot.Status.Provenance == nil {
						t.Error("committed passport disappeared during status write", err)
					}
				}
			}
		}(writer)
	}
	writers.Wait()
	if err := os.Mkdir(filepath.Join(config.RootDir, "jobs", "test-id", "status.json.partial"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := controller.persistProvenance("test-id", p); err == nil {
		t.Fatal("blocked temporary write unexpectedly succeeded")
	}
	data, err = os.ReadFile(filepath.Join(config.RootDir, "jobs", "test-id", "status.json"))
	if err != nil || json.Unmarshal(data, &stored) != nil || stored.Status.Provenance == nil {
		t.Fatal("failed write removed committed evidence", err)
	}
}

func TestTrainingRunCapturesAndRestoresPassportWithoutGPU(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic shell executor runs in the Linux test container")
	}
	for _, changeDuringRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "changed_input_rejected"}[changeDuringRun], func(t *testing.T) {
			record, profile, config := provenanceTestFiles(t)
			config.PythonExe = filepath.Join(config.RootDir, "fixture-executor")
			config.ComfyLoraDir = t.TempDir()
			config.MaxDatasetBytes = 1 << 20
			script := `#!/bin/sh
set -eu
output=''
name=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output_dir) output="$2"; shift ;;
    --output_name) name="$2"; shift ;;
  esac
  shift
done
if [ -n "$output" ]; then
  printf 'synthetic adapter, no model execution' > "$output/$name.safetensors"
  if [ -n "$TRAINING_FIXTURE_MUTATE" ]; then printf 'changed' > "$TRAINING_FIXTURE_MUTATE"; fi
fi
`
			if err := os.WriteFile(config.PythonExe, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TRAINING_FIXTURE_MUTATE", "")
			if changeDuringRun {
				t.Setenv("TRAINING_FIXTURE_MUTATE", profile.DiT)
			}
			config.Profiles[0].DiT = "/missing/current-catalog-model"
			record.Status.ID, record.Status.State = "synthetic-run", "preparing"
			record.Spec.OutputName, record.Spec.Owner, record.Spec.SampleCount = "fixture_output", "fixture-owner", 1
			jobDir := filepath.Join(config.RootDir, "jobs", record.Status.ID)
			if err := os.MkdirAll(jobDir, 0o700); err != nil {
				t.Fatal(err)
			}
			record.DatasetPath = filepath.Join(jobDir, "dataset.zip")
			writeTestZip(t, record.DatasetPath, map[string]string{"images/01.png": "synthetic", "images/01.txt": "fixture"})
			controller := &Controller{config: config, jobs: map[string]*jobRecord{record.Status.ID: record}}
			err := controller.run(context.Background(), record.Status.ID)
			if changeDuringRun {
				if err == nil || controller.jobs[record.Status.ID].Status.Provenance.Artifact != nil {
					t.Fatal("changed input produced a verified completed passport", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			status, err := controller.Status(record.Status.ID)
			if err != nil || status.State != "completed" || status.Provenance == nil || status.Provenance.Artifact == nil || status.Provenance.Validate() != nil {
				t.Fatalf("completion: %+v %v", status, err)
			}
			restored := &Controller{config: config, jobs: make(map[string]*jobRecord), byGateway: make(map[string]string)}
			if err := restored.loadJobs(); err != nil {
				t.Fatal(err)
			}
			reloaded, err := restored.Status(record.Status.ID)
			if err != nil || reloaded.Provenance == nil || reloaded.Provenance.Artifact.SHA256 != status.Provenance.Artifact.SHA256 {
				t.Fatal("restart lost completed evidence", err)
			}
			restored.jobs[record.Status.ID].Status.Provenance.Models[0].SHA256 = "corrupt"
			if err := restored.persist(restored.jobs[record.Status.ID]); err != nil {
				t.Fatal(err)
			}
			if err := restored.loadJobs(); !errors.Is(err, domain.ErrLoraProvenance) {
				t.Fatal("corrupt persisted evidence accepted on restart", err)
			}
		})
	}
}
