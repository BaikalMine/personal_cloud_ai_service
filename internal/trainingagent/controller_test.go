package trainingagent

import (
	"archive/zip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ai-access-gateway/internal/loratraining"
)

func TestTrainingArgsUseFamilySpecificMusubiScripts(t *testing.T) {
	t.Parallel()
	controller := &Controller{config: Config{TunerDir: `C:\musubi`}}
	spec := loratraining.JobSpec{NetworkDim: 32, NetworkAlpha: 32, MaxSteps: 800, LearningRate: 0.0001, Seed: 42, OutputName: "portrait_v1"}
	krea := controller.trainingArgs(ProfileConfig{Family: "krea2", VAE: "vae", FP8Base: true}, spec, "dit", "dataset", "output")
	flux := controller.trainingArgs(ProfileConfig{Family: "flux2-klein", VAE: "vae", TextEncoder: "qwen", ModelVersion: "klein-base-9b", FP8TextEncoder: true}, spec, "dit", "dataset", "output")
	if !containsSuffix(krea, "krea2_train_network.py") || !containsPair(krea, "--network_module", "networks.lora_krea2") || !slices.Contains(krea, "--fp8_scaled") {
		t.Fatalf("unexpected Krea2 command: %v", krea)
	}
	if !containsSuffix(flux, "flux_2_train_network.py") || !containsPair(flux, "--network_module", "networks.lora_flux_2") || !containsPair(flux, "--model_version", "klein-base-9b") || !slices.Contains(flux, "--fp8_text_encoder") {
		t.Fatalf("unexpected Flux.2 command: %v", flux)
	}
}

func TestValidateSpecRejectsSeedOutsideNumPyRange(t *testing.T) {
	controller := &Controller{config: Config{Profiles: []ProfileConfig{{ID: "krea2-test", Family: "krea2", Name: "Krea", BaseModel: "krea.safetensors"}}}}
	spec := loratraining.JobSpec{
		GatewayJobID: "gateway-job-012345", ProfileID: "krea2-test", Owner: "tester", Name: "Portrait", OutputName: "portrait_v1",
		TriggerWord: "subject_token", ConceptType: "character", Resolution: 1024, MaxSteps: 800, NetworkDim: 32,
		NetworkAlpha: 32, LearningRate: 0.0001, Seed: loratraining.MaxNumpySeed + 1, SampleCount: 12,
	}
	if _, err := controller.validateSpec(spec); err == nil {
		t.Fatal("seed outside the NumPy range was accepted")
	}
}

func TestNormalizeLegacyTrainingSeed(t *testing.T) {
	if actual := normalizeTrainingSeed(1226991759110583725); actual != 136197549 {
		t.Fatalf("normalized seed = %d, want 136197549", actual)
	}
}

func TestExtractDatasetRequiresPairedCaptionsAndRejectsTraversal(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.zip")
	writeTestZip(t, valid, map[string]string{"images/0001.png": "png", "images/0001.txt": "trigger, portrait"})
	if err := extractDataset(valid, filepath.Join(directory, "valid"), 1, 1<<20); err != nil {
		t.Fatalf("valid dataset rejected: %v", err)
	}

	unsafe := filepath.Join(directory, "unsafe.zip")
	writeTestZip(t, unsafe, map[string]string{"../outside.png": "png", "images/0001.txt": "caption"})
	if err := extractDataset(unsafe, filepath.Join(directory, "unsafe"), 1, 1<<20); err == nil {
		t.Fatal("path traversal archive was accepted")
	}

	missingCaption := filepath.Join(directory, "missing-caption.zip")
	writeTestZip(t, missingCaption, map[string]string{"images/0001.png": "png"})
	if err := extractDataset(missingCaption, filepath.Join(directory, "missing-caption"), 1, 1<<20); err == nil {
		t.Fatal("image without caption was accepted")
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	t.Parallel()
	result := truncate("  обучение завершено  ", 7)
	if result != "обучени" || strings.ToValidUTF8(result, "") != result {
		t.Fatalf("truncate returned %q", result)
	}
}

func writeTestZip(t *testing.T, filename string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func containsPair(values []string, key, wanted string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == key && values[index+1] == wanted {
			return true
		}
	}
	return false
}

func containsSuffix(values []string, suffix string) bool {
	for _, value := range values {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}
