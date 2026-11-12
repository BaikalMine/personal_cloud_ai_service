package release

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	runtimebuild "ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

type BinarySpec struct{ Component, Platform, Name string }

func BinarySpecs() []BinarySpec {
	var result []BinarySpec
	for _, component := range []string{runtimebuild.Gateway, runtimebuild.InferenceWorker, runtimebuild.ReleaseTool} {
		result = append(result, BinarySpec{component, "linux/amd64", "linux-amd64/" + component})
	}
	for _, component := range []string{runtimebuild.Gateway, runtimebuild.InferenceWorker, runtimebuild.UpdateAgent, runtimebuild.TrainingAgent, runtimebuild.MiningAgent, runtimebuild.SystemMonitor, runtimebuild.ReleaseTool} {
		result = append(result, BinarySpec{component, "windows/amd64", "windows-amd64/" + component + ".exe"})
	}
	return result
}

// ValidateBuildEnvironment is run before values enter the Go linker flags.
// Empty Docker defaults are development builds; explicit stamps are all-or-none.
func ValidateBuildEnvironment(getenv func(string) string) error {
	version, revision, builtAt, modified := getenv("BUILD_VERSION"), getenv("BUILD_REVISION"), getenv("BUILD_BUILT_AT"), getenv("BUILD_MODIFIED")
	if (version == "" || version == "dev") && revision == "" && builtAt == "" && (modified == "" || modified == "unknown") {
		return nil
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,79}$`).MatchString(version) ||
		!regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`).MatchString(revision) ||
		(modified != "true" && modified != "false") {
		return errors.New("invalid or partial build stamp")
	}
	parsed, err := time.Parse(time.RFC3339, builtAt)
	if err != nil || parsed.UTC().Format(time.RFC3339) != builtAt {
		return errors.New("build time must be canonical UTC RFC3339")
	}
	return nil
}

func Pack(directory, gatewayImage, inferenceImage string) (Manifest, error) {
	manifest := Manifest{SchemaVersion: ManifestVersion, Build: runtimebuild.Current(), Migrations: database.ReleaseMigrations()}
	if err := ValidStamp(manifest.Build); err != nil {
		return manifest, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	manifest.Source, err = hashFile(root, "source.tar")
	if err != nil {
		return manifest, err
	}
	for _, spec := range BinarySpecs() {
		file, err := hashFile(root, spec.Name)
		if err != nil {
			return manifest, err
		}
		// Inspect the actual object file's toolchain/platform, without executing
		// foreign Windows binaries inside the Linux packaging container.
		binary, err := buildinfo.ReadFile(filepath.Join(directory, filepath.FromSlash(spec.Name)))
		if err != nil {
			return manifest, fmt.Errorf("read build metadata %s: %w", spec.Name, err)
		}
		settings := make(map[string]string)
		for _, setting := range binary.Settings {
			settings[setting.Key] = setting.Value
		}
		if binary.GoVersion != manifest.Build.GoVersion || settings["GOOS"]+"/"+settings["GOARCH"] != spec.Platform {
			return manifest, errors.New("binary platform or toolchain differs from release")
		}
		if settings["-trimpath"] != "true" || binary.Path != "ai-access-gateway/cmd/"+spec.Component {
			return manifest, errors.New("binary command or trimpath setting differs from release")
		}
		metadata, err := hashFile(root, spec.Name+".build.json")
		if err != nil {
			return manifest, err
		}
		reportFile, err := root.Open(metadata.Name)
		if err != nil {
			return manifest, err
		}
		data, readErr := io.ReadAll(io.LimitReader(reportFile, (16<<10)+1))
		_ = reportFile.Close()
		var report runtimebuild.Report
		if readErr != nil || len(data) > 16<<10 || json.Unmarshal(data, &report) != nil ||
			runtimebuild.Compare(spec.Component, report).Status != "compatible" || !sameBuild(manifest.Build, report.Build) || report.Build.Platform != spec.Platform {
			return manifest, errors.New("actual executable report differs from release")
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{File: file, Metadata: metadata, Report: report})
	}
	for _, spec := range []struct{ component, id, name string }{
		{runtimebuild.Gateway, gatewayImage, "gateway-image.tar"}, {runtimebuild.InferenceWorker, inferenceImage, "inference-worker-image.tar"},
	} {
		archive, err := hashFile(root, spec.name)
		if err != nil {
			return manifest, err
		}
		if err := verifyImageArchive(root, spec.name, strings.TrimSpace(spec.id)); err != nil {
			return manifest, err
		}
		manifest.Images = append(manifest.Images, Image{Component: spec.component, ID: strings.TrimSpace(spec.id), Archive: archive})
	}
	if err := manifest.Validate(); err != nil {
		return manifest, err
	}
	file, err := root.OpenFile("manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return manifest, err
	}
	encodeErr := json.NewEncoder(file).Encode(manifest)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(encodeErr, syncErr, closeErr); err != nil {
		return manifest, err
	}
	return manifest, nil
}
