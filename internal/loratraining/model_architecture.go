package loratraining

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const MaxModelInspectionCandidates = 32

// ModelRelativeName is a catalog reference, never an absolute host path.
func ModelRelativeName(value string) (string, bool) {
	value = strings.ReplaceAll(value, `\`, "/")
	if value == "" || len(value) > 1024 || strings.Contains(value, ":") || strings.HasPrefix(value, "/") ||
		strings.IndexFunc(value, unicode.IsControl) >= 0 || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") ||
		!strings.EqualFold(path.Ext(value), ".safetensors") {
		return "", false
	}
	for _, part := range strings.Split(value, "/") {
		if strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return "", false
		}
	}
	return value, true
}

type modelTensor struct {
	Shape   []int64 `json:"shape"`
	DType   string  `json:"dtype"`
	Offsets []int64 `json:"data_offsets"`
}

// SafetensorsArchitecture reads only the bounded JSON header. Dimensions mirror
// ComfyUI's model_detection and the supported Musubi Krea2/Klein configurations;
// neither a filename nor self-declared metadata establishes compatibility.
func SafetensorsArchitecture(reader io.ReaderAt, size int64) (string, error) {
	invalid := errors.New("unsupported or invalid model header")
	var prefix [8]byte
	if size < 10 {
		return "", invalid
	}
	if _, err := reader.ReadAt(prefix[:], 0); err != nil {
		return "", invalid
	}
	n := binary.LittleEndian.Uint64(prefix[:])
	if n < 2 || n > 16<<20 || n > uint64(size-8) {
		return "", invalid
	}
	header := make([]byte, int(n))
	if _, err := reader.ReadAt(header, 8); err != nil {
		return "", invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(header))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", invalid
	}
	tensors := map[string]modelTensor{}
	seen := map[string]bool{}
	widths := map[string]int64{"BOOL": 1, "U8": 1, "I8": 1, "F8_E4M3": 1, "F8_E5M2": 1, "F8_E4M3FN": 1, "F8_E8M0": 1, "I16": 2, "U16": 2, "F16": 2, "BF16": 2, "I32": 4, "U32": 4, "F32": 4, "I64": 8, "U64": 8, "F64": 8}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || name == "" || len(name) > 2048 || seen[name] || len(seen) >= 100000 {
			return "", invalid
		}
		seen[name] = true
		if name == "__metadata__" {
			var metadata map[string]string
			if decoder.Decode(&metadata) != nil {
				return "", invalid
			}
			continue
		}
		var tensor modelTensor
		if decoder.Decode(&tensor) != nil || len(tensor.Shape) > 8 || len(tensor.Offsets) != 2 || widths[tensor.DType] == 0 ||
			tensor.Offsets[0] < 0 || tensor.Offsets[1] < tensor.Offsets[0] || tensor.Offsets[1] > size-8-int64(n) {
			return "", invalid
		}
		count := widths[tensor.DType]
		for _, dimension := range tensor.Shape {
			if dimension < 0 || dimension > 1<<30 || dimension != 0 && count > math.MaxInt64/dimension {
				return "", invalid
			}
			count *= dimension
		}
		if tensor.Offsets[1]-tensor.Offsets[0] != count {
			return "", invalid
		}
		tensors[name] = tensor
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return "", invalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return "", invalid
	}
	ordered := make([]modelTensor, 0, len(tensors))
	for _, tensor := range tensors {
		ordered = append(ordered, tensor)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Offsets[0] == ordered[j].Offsets[0] {
			return ordered[i].Offsets[1] < ordered[j].Offsets[1]
		}
		return ordered[i].Offsets[0] < ordered[j].Offsets[0]
	})
	var offset int64
	for _, tensor := range ordered {
		if tensor.Offsets[0] != offset {
			return "", invalid
		}
		offset = tensor.Offsets[1]
	}
	if offset != size-8-int64(n) {
		return "", invalid
	}
	found := ""
	for _, prefix := range []string{"", "diffusion_model.", "model.diffusion_model."} {
		shape := func(name string, expected ...int64) bool {
			actual, ok := tensors[prefix+name]
			if !ok || !strings.HasPrefix(actual.DType, "F") && actual.DType != "BF16" || len(actual.Shape) != len(expected) {
				return false
			}
			for i := range expected {
				if actual.Shape[i] != expected[i] {
					return false
				}
			}
			return true
		}
		kreaWeight := func(name string, rows, cols int64) bool {
			if shape(name, rows, cols) {
				return true
			}
			weight, ok := tensors[prefix+name]
			if !ok || weight.DType != "I8" || len(weight.Shape) != 2 || weight.Shape[0] != rows || weight.Shape[1] != cols {
				return false
			}
			scaleName := strings.TrimSuffix(name, ".weight") + ".weight_scale"
			scale, ok := tensors[prefix+scaleName]
			return ok && scale.DType == "F32" && len(scale.Shape) == 2 && scale.Shape[0] == rows && scale.Shape[1] == 1
		}
		blocks := func(name string, expected int) bool {
			indices := map[int]bool{}
			for key := range tensors {
				if !strings.HasPrefix(key, prefix+name) {
					continue
				}
				index, _, ok := strings.Cut(strings.TrimPrefix(key, prefix+name), ".")
				value, err := strconv.Atoi(index)
				if !ok || err != nil || value < 0 || value >= expected || strconv.Itoa(value) != index {
					return false
				}
				indices[value] = true
			}
			return len(indices) == expected
		}
		_, kreaMarker := tensors[prefix+"txtfusion.projector.weight"]
		_, fluxMarker := tensors[prefix+"double_stream_modulation_img.lin.weight"]
		if kreaMarker && fluxMarker {
			return "", invalid
		}
		if shape("first.weight", 6144, 64) && shape("txtfusion.projector.weight", 1, 12) && blocks("blocks.", 28) && blocks("txtfusion.layerwise_blocks.", 2) && blocks("txtfusion.refiner_blocks.", 2) {
			valid := true
			for i := 0; i < 28; i++ {
				valid = valid && kreaWeight("blocks."+strconv.Itoa(i)+".attn.wq.weight", 6144, 6144) && kreaWeight("blocks."+strconv.Itoa(i)+".attn.wk.weight", 1536, 6144)
			}
			for i := 0; i < 2; i++ {
				valid = valid && shape("txtfusion.layerwise_blocks."+strconv.Itoa(i)+".prenorm.scale", 2560) && shape("txtfusion.refiner_blocks."+strconv.Itoa(i)+".prenorm.scale", 2560)
			}
			if valid {
				if found != "" {
					return "", invalid
				}
				found = "krea2"
			}
		}
		for _, variant := range []struct {
			name            string
			hidden, context int64
			double, single  int
		}{
			{"flux2-klein-4b", 3072, 7680, 5, 20}, {"flux2-klein-9b", 4096, 12288, 8, 24},
		} {
			h := variant.hidden
			if !shape("img_in.weight", h, 128) || !shape("txt_in.weight", h, variant.context) || !shape("double_stream_modulation_img.lin.weight", 6*h, h) ||
				!shape("double_stream_modulation_txt.lin.weight", 6*h, h) || !blocks("double_blocks.", variant.double) || !blocks("single_blocks.", variant.single) {
				continue
			}
			valid := true
			for i := 0; i < variant.double; i++ {
				stem := "double_blocks." + strconv.Itoa(i)
				valid = valid && shape(stem+".img_attn.qkv.weight", 3*h, h) && shape(stem+".txt_attn.qkv.weight", 3*h, h)
			}
			for i := 0; i < variant.single; i++ {
				stem := "single_blocks." + strconv.Itoa(i)
				valid = valid && shape(stem+".linear1.weight", 9*h, h) && shape(stem+".linear2.weight", h, 4*h)
			}
			if valid {
				if found != "" {
					return "", invalid
				}
				found = variant.name
			}
		}
	}
	if found != "" {
		return found, nil
	}
	return "", invalid
}

func TrainingArchitecture(family, version string) string {
	switch {
	case family == "krea2":
		return "krea2"
	case family == "flux2-klein" && version == "klein-base-4b":
		return "flux2-klein-4b"
	case family == "flux2-klein" && version == "klein-base-9b":
		return "flux2-klein-9b"
	default:
		return ""
	}
}
