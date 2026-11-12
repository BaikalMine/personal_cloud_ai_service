package trainingagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func TestRepeatExecutionRejectsChangedProfileParametersAndComponents(t *testing.T) {
	record, profile, config := provenanceTestFiles(t)
	record.Spec.GatewayJobID, record.Spec.SampleCount = "repeat-source-fixture", 5
	p, _, err := captureTrainingProvenance(context.Background(), record, profile, config, profile.DiT)
	if err != nil {
		t.Fatal(err)
	}
	c := &domain.LoraTrainingRepeatConstraint{Version: 1, SourceJobID: record.Spec.GatewayJobID,
		ProfileSHA256: p.ProfileSHA256, Parameters: p.Parameters, Models: p.Models, Tuner: &p.Tuner}
	record.Spec.Repeat = c.Clone()
	if err := validateRepeatSpec(record.Spec, profile); err != nil {
		t.Fatal(err)
	}
	if err := verifyRepeatProvenance(c, p); err != nil {
		t.Fatal(err)
	}
	changedProfile := profile
	changedProfile.FP8Base = !profile.FP8Base
	if validateRepeatSpec(record.Spec, changedProfile) == nil {
		t.Fatal("changed profile accepted")
	}
	changedSpec := record.Spec
	changedSpec.Seed++
	if validateRepeatSpec(changedSpec, profile) == nil {
		t.Fatal("changed numeric parameters accepted")
	}
	for _, edit := range []func(*domain.LoraTrainingProvenance){
		func(p *domain.LoraTrainingProvenance) { p.Models[0].SHA256 = strings.Repeat("a", 64) },
		func(p *domain.LoraTrainingProvenance) { p.Models[1].SHA256 = strings.Repeat("a", 64) },
		func(p *domain.LoraTrainingProvenance) { p.Models[2].SHA256 = strings.Repeat("a", 64) },
		func(p *domain.LoraTrainingProvenance) { p.Models[3].SHA256 = strings.Repeat("a", 64) },
		func(p *domain.LoraTrainingProvenance) { p.Tuner.SourceSHA256 = strings.Repeat("a", 64) },
		func(p *domain.LoraTrainingProvenance) { p.Parameters.MaxSteps++ },
	} {
		changed := p.Clone()
		edit(changed)
		if verifyRepeatProvenance(c, changed) == nil {
			t.Fatal("changed execution accepted")
		}
	}
	// ZIP container bytes are not the identity of the immutable dataset snapshot.
	changed := p.Clone()
	changed.DatasetArchive.SHA256 = strings.Repeat("d", 64)
	if err := verifyRepeatProvenance(c, changed); err != nil {
		t.Fatal(err)
	}
	copy := cloneRecord(record)
	copy.Spec.Repeat.Models[0].SHA256 = "mutated"
	if record.Spec.Repeat.Models[0].SHA256 == "mutated" {
		t.Fatal("record aliases constraint")
	}
	legacy := c.Clone()
	legacy.Models, legacy.Tuner = nil, nil
	if err := verifyRepeatProvenance(legacy, p); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatRunRejectsChangedInputsBeforeAnyExecutorCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic shell executor uses the Linux test container")
	}
	for _, change := range []string{"none", "profile", "parameters", "base", "vae", "encoder", "tuner"} {
		t.Run(change, func(t *testing.T) {
			record, profile, config := provenanceTestFiles(t)
			config.PythonExe = filepath.Join(config.RootDir, "fixture-executor")
			config.ComfyLoraDir = t.TempDir()
			config.MaxDatasetBytes = 1 << 20
			marker := filepath.Join(config.RootDir, "executor-was-called")
			t.Setenv("REPEAT_EXECUTOR_MARKER", marker)
			script := `#!/bin/sh
set -eu
printf 'called' > "$REPEAT_EXECUTOR_MARKER"
output=''
name=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output_dir) output="$2"; shift ;;
    --output_name) name="$2"; shift ;;
  esac
  shift
done
if [ -n "$output" ]; then printf 'synthetic repeated adapter' > "$output/$name.safetensors"; fi
`
			if err := os.WriteFile(config.PythonExe, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			record.Status.ID, record.Status.State = "repeat-run", "preparing"
			record.Spec.GatewayJobID, record.Spec.SampleCount = "repeat-new-job-0001", 5
			record.Spec.OutputName, record.Spec.Owner = "fixture_output", "fixture-owner"
			jobDir := filepath.Join(config.RootDir, "jobs", record.Status.ID)
			if err := os.MkdirAll(jobDir, 0o700); err != nil {
				t.Fatal(err)
			}
			record.DatasetPath = filepath.Join(jobDir, "dataset.zip")
			files := map[string]string{}
			for i := 0; i < 5; i++ {
				files[fmt.Sprintf("images/%02d.png", i)] = "synthetic image"
				files[fmt.Sprintf("images/%02d.txt", i)] = "fixture caption"
			}
			writeTestZip(t, record.DatasetPath, files)
			p, _, err := captureTrainingProvenance(context.Background(), record, profile, config, profile.DiT)
			if err != nil {
				t.Fatal(err)
			}
			record.Spec.Repeat = &domain.LoraTrainingRepeatConstraint{Version: 1, SourceJobID: "repeat-source-0001", ProfileSHA256: p.ProfileSHA256, Parameters: p.Parameters, Models: p.Models, Tuner: &p.Tuner}
			originalRelative, err := loratraining.ArtifactRelativeName(record.Spec.Owner, profile.Family, record.Spec.OutputName+".safetensors")
			if err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(config.ComfyLoraDir, filepath.FromSlash(originalRelative))
			if err := os.MkdirAll(filepath.Dir(original), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(original, []byte("original adapter"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "profile":
				record.ProfileSnapshot.FP8Base = !record.ProfileSnapshot.FP8Base
			case "parameters":
				record.Spec.Seed++
			case "base", "vae", "encoder", "tuner":
				path := map[string]string{"base": profile.DiT, "vae": profile.VAE, "encoder": profile.TextEncoder, "tuner": filepath.Join(config.TunerDir, "src", "train.py")}[change]
				if err := os.WriteFile(path, []byte("changed component"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			controller := &Controller{config: config, jobs: map[string]*jobRecord{record.Status.ID: record}}
			err = controller.run(context.Background(), record.Status.ID)
			if change != "none" {
				if err == nil {
					t.Fatal("changed repeat executed")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("executor called before repeat validation", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				status, _ := controller.Status(record.Status.ID)
				if status.State != "completed" || status.ArtifactName != record.Spec.OutputName+"-"+record.Spec.GatewayJobID+".safetensors" {
					t.Fatal("new artifact not isolated", status)
				}
			}
			data, err := os.ReadFile(original)
			if err != nil || string(data) != "original adapter" {
				t.Fatal("source adapter changed", err)
			}
		})
	}
}
