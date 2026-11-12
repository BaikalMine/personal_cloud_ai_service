package loratraining

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func architectureHeader(t *testing.T, architecture, prefix string) ([]byte, int64, map[string]modelTensor) {
	t.Helper()
	tensors := map[string]modelTensor{}
	add := func(name string, shape ...int64) { tensors[prefix+name] = modelTensor{Shape: shape, DType: "F8_E4M3"} }
	if architecture == "krea2" {
		add("first.weight", 6144, 64)
		add("txtfusion.projector.weight", 1, 12)
		for i := 0; i < 28; i++ {
			add(fmt.Sprintf("blocks.%d.attn.wq.weight", i), 6144, 6144)
			add(fmt.Sprintf("blocks.%d.attn.wk.weight", i), 1536, 6144)
		}
		for i := 0; i < 2; i++ {
			add(fmt.Sprintf("txtfusion.layerwise_blocks.%d.prenorm.scale", i), 2560)
			add(fmt.Sprintf("txtfusion.refiner_blocks.%d.prenorm.scale", i), 2560)
		}
	} else {
		h, c, d, s := int64(4096), int64(12288), 8, 24
		if architecture == "flux2-klein-4b" {
			h, c, d, s = 3072, 7680, 5, 20
		}
		add("img_in.weight", h, 128)
		add("txt_in.weight", h, c)
		add("double_stream_modulation_img.lin.weight", 6*h, h)
		add("double_stream_modulation_txt.lin.weight", 6*h, h)
		for i := 0; i < d; i++ {
			add(fmt.Sprintf("double_blocks.%d.img_attn.qkv.weight", i), 3*h, h)
			add(fmt.Sprintf("double_blocks.%d.txt_attn.qkv.weight", i), 3*h, h)
		}
		for i := 0; i < s; i++ {
			add(fmt.Sprintf("single_blocks.%d.linear1.weight", i), 9*h, h)
			add(fmt.Sprintf("single_blocks.%d.linear2.weight", i), h, 4*h)
		}
	}
	header, size := encodeArchitectureHeader(t, tensors)
	return header, size, tensors
}

func encodeArchitectureHeader(t *testing.T, tensors map[string]modelTensor) ([]byte, int64) {
	t.Helper()
	names := make([]string, 0, len(tensors))
	for name := range tensors {
		names = append(names, name)
	}
	sort.Strings(names)
	var offset int64
	for _, name := range names {
		tensor := tensors[name]
		n := int64(1)
		if tensor.DType == "F32" {
			n = 4
		}
		for _, dim := range tensor.Shape {
			n *= dim
		}
		tensor.Offsets = []int64{offset, offset + n}
		offset += n
		tensors[name] = tensor
	}
	header, err := json.Marshal(tensors)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8+len(header))
	binary.LittleEndian.PutUint64(data, uint64(len(header)))
	copy(data[8:], header)
	return data, int64(len(data)) + offset
}

func TestSafetensorsArchitectureKreaInt8WithChannelScales(t *testing.T) {
	for _, prefix := range []string{"", "model.diffusion_model."} {
		_, _, tensors := architectureHeader(t, "krea2", prefix)
		for i := 0; i < 28; i++ {
			for _, spec := range []struct {
				name string
				rows int64
			}{{"wq", 6144}, {"wk", 1536}} {
				stem := fmt.Sprintf("%sblocks.%d.attn.%s", prefix, i, spec.name)
				weight := tensors[stem+".weight"]
				weight.DType = "I8"
				tensors[stem+".weight"] = weight
				tensors[stem+".weight_scale"] = modelTensor{Shape: []int64{spec.rows, 1}, DType: "F32"}
			}
		}
		header, size := encodeArchitectureHeader(t, tensors)
		if got, err := SafetensorsArchitecture(bytes.NewReader(header), size); err != nil || got != "krea2" {
			t.Fatalf("%q: %s %v", prefix, got, err)
		}
		key := prefix + "blocks.0.attn.wq.weight_scale"
		delete(tensors, key)
		header, size = encodeArchitectureHeader(t, tensors)
		if _, err := SafetensorsArchitecture(bytes.NewReader(header), size); err == nil {
			t.Fatal("accepted int8 weight without scale")
		}
		tensors[key] = modelTensor{Shape: []int64{6144, 2}, DType: "F32"}
		header, size = encodeArchitectureHeader(t, tensors)
		if _, err := SafetensorsArchitecture(bytes.NewReader(header), size); err == nil {
			t.Fatal("accepted incorrect scale shape")
		}
		tensors[key] = modelTensor{Shape: []int64{6144, 1}, DType: "F8_E4M3"}
		header, size = encodeArchitectureHeader(t, tensors)
		if _, err := SafetensorsArchitecture(bytes.NewReader(header), size); err == nil {
			t.Fatal("accepted incorrect scale type")
		}
		tensors[key] = modelTensor{Shape: []int64{6144, 1}, DType: "F32"}
		weightKey := prefix + "blocks.0.attn.wq.weight"
		weight := tensors[weightKey]
		weight.DType = "U8"
		tensors[weightKey] = weight
		header, size = encodeArchitectureHeader(t, tensors)
		if _, err := SafetensorsArchitecture(bytes.NewReader(header), size); err == nil {
			t.Fatal("accepted unsupported unsigned quantization")
		}
	}
}

func TestSafetensorsArchitectureUsesDimensionsWithoutReadingWeights(t *testing.T) {
	for _, arch := range []string{"krea2", "flux2-klein-4b", "flux2-klein-9b"} {
		for _, prefix := range []string{"", "diffusion_model.", "model.diffusion_model."} {
			t.Run(arch+prefix, func(t *testing.T) {
				header, size, _ := architectureHeader(t, arch, prefix)
				got, err := SafetensorsArchitecture(bytes.NewReader(header), size)
				if err != nil || got != arch {
					t.Fatalf("%s %v", got, err)
				}
			})
		}
	}
}

func TestSafetensorsArchitectureRejectsUnprovenHeaders(t *testing.T) {
	header, size, tensors := architectureHeader(t, "flux2-klein-9b", "")
	for _, data := range [][]byte{nil, header[:7], header[:len(header)-1], append([]byte{255, 255, 255, 255, 255, 255, 255, 255}, header[8:]...)} {
		if _, err := SafetensorsArchitecture(bytes.NewReader(data), size); err == nil {
			t.Fatal("accepted invalid header")
		}
	}
	if _, err := SafetensorsArchitecture(bytes.NewReader(header), size-1); err == nil {
		t.Fatal("accepted truncated data section")
	}
	delete(tensors, "double_blocks.4.img_attn.qkv.weight")
	bad, badSize := encodeArchitectureHeader(t, tensors)
	if _, err := SafetensorsArchitecture(bytes.NewReader(bad), badSize); err == nil {
		t.Fatal("accepted incomplete blocks")
	}
	_, _, tensors = architectureHeader(t, "krea2", "")
	tensor := tensors["first.weight"]
	tensor.Shape = []int64{3072, 64}
	tensors["first.weight"] = tensor
	bad, badSize = encodeArchitectureHeader(t, tensors)
	if _, err := SafetensorsArchitecture(bytes.NewReader(bad), badSize); err == nil {
		t.Fatal("accepted a different Krea architecture")
	}
	_, _, tensors = architectureHeader(t, "krea2", "")
	tensor = tensors["first.weight"]
	tensor.DType = "U8"
	tensors["first.weight"] = tensor
	bad, badSize = encodeArchitectureHeader(t, tensors)
	if _, err := SafetensorsArchitecture(bytes.NewReader(bad), badSize); err == nil {
		t.Fatal("accepted packed weights as floating weights")
	}
}

func TestInspectionCandidatePaths(t *testing.T) {
	for _, name := range []string{"../outside.safetensors", "C:/model.safetensors", "//server/model.safetensors", "foo/../model.safetensors", "foo//model.safetensors", "foo./model.safetensors", " foo/model.safetensors", "a\x00.safetensors", "model.pt"} {
		if _, ok := ModelRelativeName(name); ok {
			t.Fatal("accepted", name)
		}
	}
	if name, ok := ModelRelativeName(`Flux2\model.safetensors`); !ok || name != "Flux2/model.safetensors" {
		t.Fatal(name, ok)
	}
	if (ArtifactInspectionRequest{Models: []string{"a.safetensors", "A.safetensors"}}).Valid() {
		t.Fatal("accepted aliases")
	}
	if (ArtifactInspectionRequest{Models: strings.Split(strings.Repeat("a.safetensors,", MaxModelInspectionCandidates+1), ",")}).Valid() {
		t.Fatal("accepted too many candidates")
	}
}
