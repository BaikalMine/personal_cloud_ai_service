package modelmanager

import "testing"

func TestModelFilenameWindowsCompatibility(t *testing.T) {
	for _, name := range []string{"CON.safetensors", "aux.safetensors", "LPT1.safetensors", "COM9.safetensors", "x?.safetensors", "x|y.safetensors", "x<y.safetensors", "trailing.safetensors ", "a\x01.safetensors", "../x.safetensors"} {
		if portableModelFilename(name) {
			t.Errorf("accepted incompatible filename %q", name)
		}
	}
	if !portableModelFilename("studio_light-v2.safetensors") {
		t.Fatal("valid filename rejected")
	}
}
