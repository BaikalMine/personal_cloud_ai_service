// Package release describes immutable artifacts and conservative schema decisions.
// A manifest is not a signature and never authorizes a deployment by itself.
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

const ManifestVersion = 1
const maxManifestBytes = 1 << 20

type File struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Artifact struct {
	File     File             `json:"file"`
	Metadata File             `json:"metadata"`
	Report   buildinfo.Report `json:"report"`
}

type Image struct {
	Component string `json:"component"`
	ID        string `json:"id"`
	Archive   File   `json:"archive"`
}

type Manifest struct {
	SchemaVersion int                        `json:"schema_version"`
	Build         buildinfo.Info             `json:"build"`
	Source        File                       `json:"source"`
	Migrations    []database.MigrationRecord `json:"migrations"`
	Artifacts     []Artifact                 `json:"artifacts"`
	Images        []Image                    `json:"images"`
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var migrationName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
var fileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)

func ValidStamp(info buildinfo.Info) error {
	if !info.ReleaseIdentified() {
		return errors.New("release requires a version, full commit, RFC3339 build time and unmodified source archive")
	}
	return nil
}

func validateFile(file File) error {
	if !fileName.MatchString(file.Name) || strings.Contains(file.Name, "//") || path.Clean(file.Name) != file.Name ||
		strings.HasPrefix(file.Name, "/") || strings.HasPrefix(file.Name, "../") || file.Bytes <= 0 || !hashPattern.MatchString(file.SHA256) {
		return errors.New("invalid release file identity")
	}
	return nil
}

func validateMigrations(items []database.MigrationRecord) error {
	if len(items) == 0 || len(items) > 10000 {
		return errors.New("invalid release migration catalog")
	}
	seen := make(map[string]bool)
	for i, item := range items {
		if item.Version != int64(i+1) || !migrationName.MatchString(item.Name) || !hashPattern.MatchString(item.Checksum) || seen[item.Name] {
			return errors.New("invalid release migration identity")
		}
		seen[item.Name] = true
	}
	return nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestVersion {
		return errors.New("unsupported release manifest")
	}
	if err := ValidStamp(m.Build); err != nil {
		return err
	}
	if err := validateMigrations(m.Migrations); err != nil {
		return err
	}
	if m.Source.Name != "source.tar" {
		return errors.New("release source archive is missing")
	}
	if err := validateFile(m.Source); err != nil {
		return err
	}
	seen := map[string]bool{m.Source.Name: true}
	components := make(map[string]bool)
	if len(m.Artifacts) < 1 || len(m.Artifacts) > 32 || len(m.Images) < 1 || len(m.Images) > 2 {
		return errors.New("invalid release artifact count")
	}
	for _, artifact := range m.Artifacts {
		if err := validateFile(artifact.File); err != nil {
			return err
		}
		if seen[artifact.File.Name] {
			return errors.New("duplicate release file")
		}
		seen[artifact.File.Name] = true
		if err := validateFile(artifact.Metadata); err != nil {
			return err
		}
		if seen[artifact.Metadata.Name] {
			return errors.New("duplicate release metadata file")
		}
		seen[artifact.Metadata.Name] = true
		r := artifact.Report
		if check := buildinfo.Compare(r.Component, r); check.Status != "compatible" && check.Status != "incompatible" {
			return errors.New("invalid release artifact contract report")
		}
		if !sameBuild(m.Build, r.Build) {
			return errors.New("release artifact build identity differs")
		}
		key := r.Component + "/" + r.Build.Platform
		if components[key] {
			return errors.New("duplicate component platform")
		}
		components[key] = true
	}
	imageComponents := make(map[string]bool)
	for _, image := range m.Images {
		if image.Component != buildinfo.Gateway && image.Component != buildinfo.InferenceWorker {
			return errors.New("unsupported release image component")
		}
		if !strings.HasPrefix(image.ID, "sha256:") || !hashPattern.MatchString(strings.TrimPrefix(image.ID, "sha256:")) {
			return errors.New("invalid immutable image ID")
		}
		if imageComponents[image.Component] || !components[image.Component+"/linux/amd64"] {
			return errors.New("image component is duplicated or has no Linux artifact")
		}
		imageComponents[image.Component] = true
		if err := validateFile(image.Archive); err != nil {
			return err
		}
		if seen[image.Archive.Name] {
			return errors.New("duplicate release file")
		}
		seen[image.Archive.Name] = true
	}
	return nil
}

func sameBuild(a, b buildinfo.Info) bool {
	return a.Version == b.Version && a.Revision == b.Revision && a.BuiltAt == b.BuiltAt && a.Modified == b.Modified && a.GoVersion == b.GoVersion
}

func Read(reader io.Reader) (Manifest, error) {
	var manifest Manifest
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return manifest, errors.New("release manifest is unreadable or too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return manifest, errors.New("trailing release manifest data")
	}
	return manifest, manifest.Validate()
}

// VerifyFiles does not load images or execute artifacts. Rooted reads reject
// links so the manifest cannot select files outside its own artifact directory.
func (m Manifest) VerifyFiles(directory string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	files := []File{m.Source}
	for _, artifact := range m.Artifacts {
		files = append(files, artifact.File, artifact.Metadata)
	}
	for _, image := range m.Images {
		files = append(files, image.Archive)
	}
	for _, expected := range files {
		actual, err := hashFile(root, expected.Name)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("release file checksum or length mismatch: %s", expected.Name)
		}
	}
	for _, image := range m.Images {
		if err := verifyImageArchive(root, image.Archive.Name, image.ID); err != nil {
			return err
		}
	}
	return nil
}

func hashFile(root *os.Root, name string) (File, error) {
	parts := strings.Split(name, "/")
	for index := range parts {
		info, err := root.Lstat(strings.Join(parts[:index+1], "/"))
		if err != nil {
			return File{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return File{}, errors.New("release file links are forbidden")
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return File{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return File{}, errors.New("release artifact is not a regular file")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return File{}, err
	}
	return File{Name: name, Bytes: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type SchemaDecision struct {
	CanUpgrade      bool   `json:"can_upgrade"`
	BinaryRollback  bool   `json:"binary_rollback"`
	RestoreRequired bool   `json:"restore_required"`
	Reason          string `json:"reason"`
}

// CompareSchema is intentionally exact: additive-looking SQL is not proof that
// an old binary accepts a new schema. Unknown old releases require a restore.
func CompareSchema(previous, candidate []database.MigrationRecord) SchemaDecision {
	decision := SchemaDecision{RestoreRequired: true, Reason: "unknown_or_invalid_schema"}
	if validateMigrations(previous) != nil || validateMigrations(candidate) != nil {
		return decision
	}
	if len(previous) > len(candidate) {
		decision.Reason = "candidate_schema_is_older"
		return decision
	}
	for i, item := range previous {
		if item != candidate[i] {
			decision.Reason = "migration_identity_mismatch"
			return decision
		}
	}
	decision.CanUpgrade = true
	if len(previous) == len(candidate) {
		decision.BinaryRollback = true
		decision.RestoreRequired = false
		decision.Reason = "identical_migration_catalog"
	} else {
		decision.Reason = "forward_migrations_require_prepared_restore"
	}
	return decision
}
