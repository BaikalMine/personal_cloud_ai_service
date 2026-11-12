package release

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

type imageDescriptor struct {
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
	Platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}
type imageNode struct {
	SchemaVersion int               `json:"schemaVersion"`
	Manifests     []imageDescriptor `json:"manifests"`
	Config        imageDescriptor   `json:"config"`
	Layers        []imageDescriptor `json:"layers"`
}
type imageBlob struct {
	hash string
	size int64
}

// Classic Docker IDs hash a config; containerd-backed Docker can return an OCI
// manifest/index digest. Verify the corresponding digest chain in the saved tar.
// No archive entries are extracted or images loaded during verification.
func verifyImageArchive(root *os.Root, name, id string) error {
	if !validDigest(id) {
		return errors.New("invalid image ID")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	type imageEntry struct {
		Config string
		Layers []string
	}
	var manifest []imageEntry
	var top imageNode
	nodes := make(map[string]imageNode)
	entries := make(map[string]imageBlob)
	for count := 0; ; count++ {
		if count > 10000 {
			return errors.New("image export contains too many entries")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || !fileName.MatchString(header.Name) || path.Clean(header.Name) != header.Name || strings.HasPrefix(header.Name, "../") || strings.HasPrefix(header.Name, "/") {
			return errors.New("image export contains links or unsafe entries")
		}
		if _, exists := entries[header.Name]; exists {
			return errors.New("image export contains duplicate entries")
		}
		hash := sha256.New()
		var data []byte
		if header.Name == "manifest.json" || header.Name == "index.json" || header.Size <= 4<<20 && strings.HasPrefix(header.Name, "blobs/sha256/") {
			if header.Size > 4<<20 {
				return errors.New("image metadata is too large")
			}
			data, err = io.ReadAll(io.TeeReader(reader, hash))
		} else {
			_, err = io.Copy(hash, reader)
		}
		if err != nil {
			return err
		}
		digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		entries[header.Name] = imageBlob{hash: digest, size: header.Size}
		switch header.Name {
		case "manifest.json":
			if json.Unmarshal(data, &manifest) != nil {
				return errors.New("invalid Docker image export manifest")
			}
		case "index.json":
			if json.Unmarshal(data, &top) != nil {
				return errors.New("invalid OCI image export index")
			}
		default:
			var node imageNode
			if len(data) > 0 && json.Unmarshal(data, &node) == nil && node.SchemaVersion == 2 && (len(node.Manifests) > 0 || node.Config.Digest != "") {
				if len(nodes) > 256 {
					return errors.New("too many OCI metadata nodes")
				}
				nodes[digest] = node
			}
		}
	}
	if len(manifest) != 1 || len(manifest[0].Layers) == 0 || len(manifest[0].Layers) > 200 {
		return errors.New("image archive must describe one image")
	}
	entry := manifest[0]
	config, exists := entries[entry.Config]
	if !exists {
		return errors.New("missing image config")
	}
	for _, layer := range entry.Layers {
		if _, ok := entries[layer]; !ok {
			return errors.New("image export is missing a layer")
		}
	}
	if config.hash == id {
		if entry.Config != strings.TrimPrefix(id, "sha256:")+".json" && entry.Config != blobPath(id) {
			return errors.New("unexpected Docker config path")
		}
		return nil
	}
	if top.SchemaVersion != 2 || len(top.Manifests) != 1 || top.Manifests[0].Digest != id {
		return errors.New("OCI export does not identify the requested image")
	}
	current := top.Manifests[0]
	for depth := 0; depth < 4; depth++ {
		blob, exists := entries[blobPath(current.Digest)]
		if !exists || blob.hash != current.Digest || blob.size != current.Size {
			return errors.New("OCI descriptor digest or size mismatch")
		}
		node, exists := nodes[current.Digest]
		if !exists {
			return errors.New("OCI descriptor is not an image node")
		}
		if len(node.Manifests) > 0 {
			var selected []imageDescriptor
			for _, descriptor := range node.Manifests {
				if descriptor.Platform.OS == "linux" && descriptor.Platform.Architecture == "amd64" {
					selected = append(selected, descriptor)
				}
			}
			if len(selected) != 1 {
				return errors.New("OCI image must contain one linux/amd64 runtime")
			}
			current = selected[0]
			continue
		}
		if !validDigest(node.Config.Digest) || node.Config.Digest != config.hash || node.Config.Size != config.size || blobPath(node.Config.Digest) != entry.Config || len(node.Layers) != len(entry.Layers) {
			return errors.New("OCI image config differs from saved runtime")
		}
		for i, layer := range node.Layers {
			blob, ok := entries[entry.Layers[i]]
			if !ok || !validDigest(layer.Digest) || blobPath(layer.Digest) != entry.Layers[i] || blob.hash != layer.Digest || blob.size != layer.Size {
				return errors.New("OCI image layer digest or size mismatch")
			}
		}
		return nil
	}
	return errors.New("OCI image index nesting is too deep")
}

func validDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && hashPattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}
func blobPath(digest string) string { return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:") }
