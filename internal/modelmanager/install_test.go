package modelmanager

import (
	"strings"
	"testing"
)

func TestModelInstallDestinations(t *testing.T) {
	s := Settings{LoraRoot: "C:/ComfyUI/models/loras", CheckpointRoot: "C:/ComfyUI/models/checkpoints", UNetRoot: "C:/ComfyUI/models/diffusion_models"}
	f := File{ID: 7, Name: "studio.safetensors", Type: "Model", SizeKB: 1024}
	f.Metadata.Format = "SafeTensor"
	f.Hashes.SHA256 = strings.Repeat("a", 64)
	for _, tc := range []struct{ kind, target, base, folder, root, fileType string }{
		{"LORA", "lora", "Krea 2", "Krea2", s.LoraRoot, "Model"},
		{"LORA", "lora", "MOSS Image", "MOSSImage", s.LoraRoot, "Model"},
		{"Checkpoint", "diffusion", "MOSS Image", "MOSSImage", s.UNetRoot, "Model"},
		{"LORA", "lora", "Flux.2 Klein 9B", "Flux2", s.LoraRoot, "Model"},
		{"LORA", "lora", "LTX 2.5", "LTX2", s.LoraRoot, "Model"},
		{"Checkpoint", "checkpoint", "SDXL 1.0", "SDXL", s.CheckpointRoot, "Model"},
		{"Checkpoint", "diffusion", "MiniMax H3", "MiniMax", s.UNetRoot, "UNet"},
		{"Checkpoint", "diffusion", "LTX 2.5", "LTX2.5", s.UNetRoot, "UNet"},
		{"Checkpoint", "diffusion", "Qwen Image 2.1", "QwenImage2.1", s.UNetRoot, "Diffusion Model"},
	} {
		t.Run(tc.kind+tc.target+tc.base, func(t *testing.T) {
			v := Version{ID: 12, Base: tc.base}
			v.Model.Type = tc.kind
			file := f
			file.Type = tc.fileType
			v.Files = []File{file}
			d, size, err := Prepare(v, file, s, tc.target, "id")
			if err != nil || d.Root != tc.root || d.RelativePath != tc.folder || size != 1<<20 {
				t.Fatalf("download=%+v size=%d err=%v", d, size, err)
			}
		})
	}
	moss := Version{ID: 12, Base: "MOSS Image", Files: []File{f}}
	moss.Model.Type = "Checkpoint"
	if _, _, err := Prepare(moss, f, s, "checkpoint", "id"); err == nil {
		t.Fatal("MOSS diffusion weights routed to CheckpointLoader folder")
	}
	v := Version{ID: 12, Base: "../../escape"}
	v.Model.Type = "Checkpoint"
	f.Type = "UNet"
	v.Files = []File{f}
	if _, _, err := Prepare(v, f, s, "checkpoint", "id"); err == nil {
		t.Fatal("UNet routed to checkpoints")
	}
	v.Files = append(v.Files, f)
	if _, _, err := Prepare(v, f, s, "diffusion", "id"); err == nil {
		t.Fatal("duplicate file ID accepted")
	}
	other := f
	other.ID = 8
	v.Files = []File{f, other}
	d, _, err := Prepare(v, f, s, "diffusion", "id")
	if err != nil || d.FileParams["id"] != "7" {
		t.Fatalf("identical attributes must select exact file by ID: %+v, %v", d, err)
	}
	v.Files = []File{f}
	f.Name = "../escape.safetensors"
	if _, _, err := Prepare(v, f, s, "diffusion", "id"); err == nil {
		t.Fatal("unsafe filename accepted")
	}
	if strings.Contains(FamilyFolder("../../escape", "diffusion"), "/") {
		t.Fatal("unsafe family folder")
	}
	if RequiredFree(1<<30) != (1<<30)+(1<<30)/10+(5<<30) {
		t.Fatal("missing disk reserve")
	}
}
