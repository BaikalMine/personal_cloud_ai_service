package modelmanager

import (
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"unicode"
)

func FamilyFolder(base, target string) string {
	normal := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "", ".", "").Replace(base))
	switch {
	case strings.HasPrefix(normal, "mossimage"):
		return "MOSSImage"
	case strings.HasPrefix(normal, "krea2"):
		return "Krea2"
	case strings.HasPrefix(normal, "flux2"):
		return "Flux2"
	case strings.HasPrefix(normal, "minimaxh3"):
		if target == "diffusion" {
			return "MiniMax"
		}
		return "MiniMaxH3"
	case strings.HasPrefix(normal, "ltx25"):
		if target == "diffusion" {
			return "LTX2.5"
		}
		return "LTX2"
	case strings.HasPrefix(normal, "ltx2"):
		return "LTX2"
	case strings.HasPrefix(normal, "qwenimage21"), strings.HasPrefix(normal, "qwen21"):
		return "QwenImage2.1"
	case strings.HasPrefix(normal, "wan22"):
		return "Wan2.2"
	case strings.HasPrefix(normal, "sdxl"):
		return "SDXL"
	}
	var b strings.Builder
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "Unclassified"
	}
	return "Base_" + out
}

// Target is explicit for ambiguous CivitAI "Model" files. UNet files may never go to checkpoints.
func Prepare(v Version, f File, settings Settings, target, id string) (Download, int64, error) {
	var d Download
	if !portableModelFilename(f.Name) {
		return d, 0, ErrInvalid
	}
	hash, err := hex.DecodeString(f.Hashes.SHA256)
	if err != nil || len(hash) != 32 || v.ID <= 0 || f.ID <= 0 || math.IsNaN(f.SizeKB) || math.IsInf(f.SizeKB, 0) || f.SizeKB <= 0 || f.SizeKB > 200*1024*1024 || !strings.HasSuffix(strings.ToLower(f.Name), ".safetensors") || f.Metadata.Format != "SafeTensor" {
		return d, 0, ErrInvalid
	}
	root := ""
	kind := strings.ToLower(v.Model.Type)
	if kind == "lora" || kind == "locon" || kind == "dora" {
		if target != "lora" {
			return d, 0, ErrInvalid
		}
		root = settings.LoraRoot
	} else if kind == "checkpoint" {
		switch target {
		case "checkpoint":
			if f.Type == "UNet" || f.Type == "Diffusion Model" || FamilyFolder(v.Base, "diffusion") == "MOSSImage" {
				return d, 0, ErrInvalid
			}
			root = settings.CheckpointRoot
		case "diffusion":
			root = settings.UNetRoot
		default:
			return d, 0, ErrInvalid
		}
	} else {
		return d, 0, ErrInvalid
	}
	if strings.TrimSpace(root) == "" {
		return d, 0, ErrInvalid
	}
	if f.Type != "Model" && f.Type != "UNet" && f.Type != "Diffusion Model" {
		return d, 0, ErrInvalid
	}
	params := map[string]string{"id": strconv.FormatInt(f.ID, 10), "type": f.Type, "format": f.Metadata.Format}
	if f.Metadata.Size != "" {
		params["size"] = f.Metadata.Size
	}
	if f.Metadata.FP != "" {
		params["fp"] = f.Metadata.FP
	}
	// The manager selects by ID first; reject duplicate IDs in untrusted metadata.
	matched := 0
	for _, other := range v.Files {
		if other.ID == f.ID {
			matched++
		}
	}
	if matched != 1 {
		return d, 0, ErrInvalid
	}
	d = Download{ID: id, VersionID: v.ID, Root: root, RelativePath: FamilyFolder(v.Base, target), FileParams: params}
	return d, int64(math.Ceil(f.SizeKB * 1024)), nil
}

func RequiredFree(size int64) uint64 { return uint64(size) + uint64(size/10) + (5 << 30) }

func portableModelFilename(name string) bool {
	if name == "" || len(name) > 240 || strings.ContainsAny(name, "/\\:<>\"|?*") || strings.HasPrefix(name, ".") || strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return false
	}
	return true
}
