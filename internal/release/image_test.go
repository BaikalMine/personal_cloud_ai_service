package release

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func ociFixture(t *testing.T, mode string) ([]byte, string) {
	t.Helper()
	files := map[string][]byte{}
	add := func(data []byte) imageDescriptor {
		hash := sha256.Sum256(data)
		descriptor := imageDescriptor{Digest: "sha256:" + hex.EncodeToString(hash[:]), Size: int64(len(data))}
		files[blobPath(descriptor.Digest)] = data
		return descriptor
	}
	config := add([]byte(`{"architecture":"amd64","os":"linux"}`))
	layer := add([]byte("synthetic compressed layer"))
	data, _ := json.Marshal(imageNode{SchemaVersion: 2, Config: config, Layers: []imageDescriptor{layer}})
	runtime := add(data)
	runtime.Platform.OS = "linux"
	runtime.Platform.Architecture = "amd64"
	if mode == "platform" {
		runtime.Platform.Architecture = "arm64"
	}
	data, _ = json.Marshal(imageNode{SchemaVersion: 2, Manifests: []imageDescriptor{runtime}})
	index := add(data)
	files["index.json"], _ = json.Marshal(imageNode{SchemaVersion: 2, Manifests: []imageDescriptor{index}})
	files["manifest.json"], _ = json.Marshal([]map[string]any{{"Config": blobPath(config.Digest), "Layers": []string{blobPath(layer.Digest)}}})
	if mode == "layer" {
		files[blobPath(layer.Digest)] = []byte("changed compressed layer")
	}
	if mode == "missing" {
		delete(files, blobPath(layer.Digest))
	}
	if mode == "config" {
		files[blobPath(config.Digest)] = []byte(`{"os":"changed"}`)
	}
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(files[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes(), index.Digest
}

func TestOCIImageIndexVerifiesRuntimeAndLayerDigests(t *testing.T) {
	for _, mode := range []string{"ok", "platform", "layer", "missing", "config"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			body, id := ociFixture(t, mode)
			if err := root.WriteFile("image.tar", body, 0600); err != nil {
				t.Fatal(err)
			}
			err = verifyImageArchive(root, "image.tar", id)
			if (err == nil) != (mode == "ok") {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestRealDockerArchiveFixture(t *testing.T) {
	archive := os.Getenv("RELEASE_TEST_IMAGE_ARCHIVE")
	id := os.Getenv("RELEASE_TEST_IMAGE_ID")
	if archive == "" || id == "" {
		t.Skip("real saved image fixture not supplied")
	}
	root, err := os.OpenRoot(filepath.Dir(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := verifyImageArchive(root, filepath.Base(archive), id); err != nil {
		t.Fatal(err)
	}
}
