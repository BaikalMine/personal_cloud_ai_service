package trainingagent

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSafeTensorPrefixPreservesTensorBytes(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(directory, "source.safetensors")
	target := filepath.Join(directory, "normalized", "model.safetensors")
	tensorBytes := []byte{1, 2, 3, 4, 5, 6}
	writeTestSafeTensor(t, source, map[string]any{
		"__metadata__":                       map[string]string{"format": "pt"},
		"model.diffusion_model.first.weight": map[string]any{"dtype": "U8", "shape": []int{4}, "data_offsets": []int{0, 4}},
		"model.diffusion_model.last.bias":    map[string]any{"dtype": "U8", "shape": []int{2}, "data_offsets": []int{4, 6}},
	}, tensorBytes)

	if err := normalizeSafeTensorPrefix(source, target, "model.diffusion_model."); err != nil {
		t.Fatalf("normalizeSafeTensorPrefix: %v", err)
	}
	entries, gotBytes := readTestSafeTensor(t, target)
	if _, ok := entries["first.weight"]; !ok {
		t.Fatalf("normalized header does not contain first.weight: %#v", entries)
	}
	if _, ok := entries["last.bias"]; !ok {
		t.Fatalf("normalized header does not contain last.bias: %#v", entries)
	}
	if _, ok := entries["model.diffusion_model.first.weight"]; ok {
		t.Fatal("source prefix remained in normalized header")
	}
	if string(gotBytes) != string(tensorBytes) {
		t.Fatalf("tensor bytes changed: got %v want %v", gotBytes, tensorBytes)
	}
}

func TestNormalizeSafeTensorPrefixRejectsUnmatchedModel(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(directory, "source.safetensors")
	writeTestSafeTensor(t, source, map[string]any{
		"other.weight": map[string]any{"dtype": "U8", "shape": []int{1}, "data_offsets": []int{0, 1}},
	}, []byte{1})
	if err := normalizeSafeTensorPrefix(source, filepath.Join(directory, "target.safetensors"), "model.diffusion_model."); err == nil {
		t.Fatal("expected unmatched prefix to fail")
	}
}

func TestNormalizeSafeTensorForFlux2RenamesComfyNormWeights(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(directory, "source.safetensors")
	target := filepath.Join(directory, "target.safetensors")
	tensorBytes := []byte{1, 2, 3, 4}
	writeTestSafeTensor(t, source, map[string]any{
		"model.diffusion_model.double_blocks.0.img_attn.norm.key_norm.weight": map[string]any{"dtype": "U8", "shape": []int{2}, "data_offsets": []int{0, 2}},
		"model.diffusion_model.single_blocks.0.norm.query_norm.weight":        map[string]any{"dtype": "U8", "shape": []int{1}, "data_offsets": []int{2, 3}},
		"model.diffusion_model.single_blocks.0.linear1.weight":                map[string]any{"dtype": "U8", "shape": []int{1}, "data_offsets": []int{3, 4}},
	}, tensorBytes)

	if err := normalizeSafeTensorForProfile(source, target, "model.diffusion_model.", "flux2-klein"); err != nil {
		t.Fatalf("normalizeSafeTensorForProfile: %v", err)
	}
	entries, gotBytes := readTestSafeTensor(t, target)
	for _, key := range []string{
		"double_blocks.0.img_attn.norm.key_norm.scale",
		"single_blocks.0.norm.query_norm.scale",
		"single_blocks.0.linear1.weight",
	} {
		if _, ok := entries[key]; !ok {
			t.Fatalf("normalized Flux.2 header does not contain %s: %#v", key, entries)
		}
	}
	if string(gotBytes) != string(tensorBytes) {
		t.Fatalf("tensor bytes changed: got %v want %v", gotBytes, tensorBytes)
	}
}

func writeTestSafeTensor(t *testing.T, filename string, entries map[string]any, tensorBytes []byte) {
	t.Helper()
	header, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	header = append(header, []byte(strings.Repeat(" ", (8-len(header)%8)%8))...)
	payload := make([]byte, 8, 8+len(header)+len(tensorBytes))
	binary.LittleEndian.PutUint64(payload, uint64(len(header)))
	payload = append(payload, header...)
	payload = append(payload, tensorBytes...)
	if err := os.WriteFile(filename, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestSafeTensor(t *testing.T, filename string) (map[string]json.RawMessage, []byte) {
	t.Helper()
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	headerLength := int(binary.LittleEndian.Uint64(payload[:8]))
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(payload[8:8+headerLength], &entries); err != nil {
		t.Fatal(err)
	}
	return entries, payload[8+headerLength:]
}
