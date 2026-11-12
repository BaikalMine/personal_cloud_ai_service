package release

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

func fixtureManifest(t *testing.T) (string, Manifest) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	write := func(name string, data []byte) File {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(directory, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
		file, err := hashFile(root, name)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	info := buildinfo.Info{Version: "v2026.09.10", Revision: strings.Repeat("a", 40), BuiltAt: "2026-09-10T00:00:00Z", Modified: "false", GoVersion: "go1.26.6", Platform: "linux/amd64"}
	report := buildinfo.NewReport(buildinfo.Gateway)
	report.Build = info
	metadata, _ := json.Marshal(report)
	image, id := fixtureImage(t, "", false)
	manifest := Manifest{SchemaVersion: 1, Build: info, Source: write("source.tar", []byte("synthetic source fixture")), Migrations: database.ReleaseMigrations(), Artifacts: []Artifact{
		{File: write("linux-amd64/gateway", []byte("synthetic binary fixture")), Metadata: write("linux-amd64/gateway.build.json", metadata), Report: report},
	}, Images: []Image{{Component: buildinfo.Gateway, ID: id, Archive: write("gateway-image.tar", image)}}}
	return directory, manifest
}

func fixtureImage(t *testing.T, configName string, missingLayer bool) ([]byte, string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux","config":{"Entrypoint":["/app/gateway"]}}`)
	hash := sha256.Sum256(config)
	id := hex.EncodeToString(hash[:])
	if configName == "" {
		configName = id + ".json"
	}
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	write := func(name string, data []byte) {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write(configName, config)
	if !missingLayer {
		write("example/layer.tar", []byte("synthetic layer"))
	}
	manifest, _ := json.Marshal([]map[string]any{{"Config": configName, "Layers": []string{"example/layer.tar"}, "RepoTags": []string{}}})
	write("manifest.json", manifest)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes(), "sha256:" + id
}

func TestReleaseManifestRoundTripAndTampering(t *testing.T) {
	directory, manifest := fixtureManifest(t)
	if err := manifest.VerifyFiles(directory); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(manifest)
	if _, err := Read(bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"version", func(m *Manifest) { m.SchemaVersion++ }},
		{"dirty", func(m *Manifest) { m.Build.Modified = "true" }},
		{"unknown", func(m *Manifest) { m.Build.Revision = "" }},
		{"traversal", func(m *Manifest) { m.Artifacts[0].File.Name = "../gateway" }},
		{"absolute", func(m *Manifest) { m.Source.Name = "/source.tar" }},
		{"backslash", func(m *Manifest) { m.Artifacts[0].File.Name = `folder\gateway` }},
		{"samepath", func(m *Manifest) { m.Artifacts[0].File.Name = m.Source.Name }},
		{"samecomponent", func(m *Manifest) {
			m.Artifacts = append(m.Artifacts, m.Artifacts[0])
			m.Artifacts[1].File.Name = "another"
		}},
		{"differentbuild", func(m *Manifest) { m.Artifacts[0].Report.Build.Version = "other" }},
		{"hash", func(m *Manifest) { m.Source.SHA256 = "oops" }},
		{"missingmigration", func(m *Manifest) { m.Migrations = m.Migrations[1:] }},
		{"duplicatemigration", func(m *Manifest) { m.Migrations[1].Name = m.Migrations[0].Name }},
		{"mutableimage", func(m *Manifest) { m.Images[0].ID = "gateway:latest" }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			var copy Manifest
			if err := json.Unmarshal(body, &copy); err != nil {
				t.Fatal(err)
			}
			edit.change(&copy)
			if copy.Validate() == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	for _, bad := range [][]byte{append(append([]byte{}, body...), []byte(`{}`)...), []byte(`{"schema_version":1,"unknown":true}`), bytes.Repeat([]byte(" "), maxManifestBytes+1)} {
		if _, err := Read(bytes.NewReader(bad)); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(directory, manifest.Artifacts[0].File.Name), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if manifest.VerifyFiles(directory) == nil {
		t.Fatal("changed artifact accepted")
	}
}

func TestReleaseVerifyRejectsLinksAndImageMismatch(t *testing.T) {
	directory, manifest := fixtureManifest(t)
	manifest.Images[0].ID = "sha256:" + strings.Repeat("b", 64)
	if manifest.VerifyFiles(directory) == nil {
		t.Fatal("archive accepted with different image ID")
	}
	_, manifest = fixtureManifest(t)
	if err := os.Remove(filepath.Join(directory, "source.tar")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "source.tar")
	if err := os.WriteFile(outside, []byte("synthetic source fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "source.tar")); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if manifest.VerifyFiles(directory) == nil {
		t.Fatal("source link accepted")
	}
}

func TestImageArchiveRequiresConfigIdentityAndLayers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing bool
		want    bool
	}{{"", false, true}, {"unexpected.json", false, false}, {"", true, false}} {
		directory := t.TempDir()
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		body, id := fixtureImage(t, tc.name, tc.missing)
		if err := root.WriteFile("image.tar", body, 0600); err != nil {
			t.Fatal(err)
		}
		err = verifyImageArchive(root, "image.tar", id)
		root.Close()
		if (err == nil) != tc.want {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestReleaseSchemaRequiresFullIdentityForBinaryRollback(t *testing.T) {
	catalog := database.ReleaseMigrations()
	if result := CompareSchema(catalog, catalog); !result.CanUpgrade || !result.BinaryRollback || result.RestoreRequired {
		t.Fatal(result)
	}
	if result := CompareSchema(catalog[:len(catalog)-1], catalog); !result.CanUpgrade || result.BinaryRollback || !result.RestoreRequired {
		t.Fatal(result)
	}
	if result := CompareSchema(catalog, catalog[:len(catalog)-1]); result.CanUpgrade || result.BinaryRollback {
		t.Fatal(result)
	}
	changed := database.ReleaseMigrations()
	changed[0].Checksum = strings.Repeat("b", 64)
	if result := CompareSchema(changed, catalog); result.CanUpgrade || result.BinaryRollback {
		t.Fatal(result)
	}
	if result := CompareSchema(nil, catalog); result.CanUpgrade || result.BinaryRollback {
		t.Fatal(result)
	}
}

func TestBuildStampRejectsLinkerInjectionAndPartialIdentity(t *testing.T) {
	valid := map[string]string{"BUILD_VERSION": "v2026.09.10", "BUILD_REVISION": strings.Repeat("a", 40), "BUILD_BUILT_AT": "2026-09-10T00:00:00Z", "BUILD_MODIFIED": "false"}
	if err := ValidateBuildEnvironment(func(key string) string { return valid[key] }); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBuildEnvironment(func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{
		{"BUILD_VERSION", "v1 -extld=anything"}, {"BUILD_REVISION", "HEAD"}, {"BUILD_MODIFIED", "unknown"}, {"BUILD_BUILT_AT", ""}, {"BUILD_BUILT_AT", "2026-09-10T03:00:00+03:00"}, {"BUILD_VERSION", "$(anything)"},
	} {
		if ValidateBuildEnvironment(func(key string) string {
			if key == tc.key {
				return tc.value
			}
			return valid[key]
		}) == nil {
			t.Fatal("invalid stamp accepted", tc)
		}
	}
}
