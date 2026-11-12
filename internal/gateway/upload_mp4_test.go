package gateway

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestUploadMP4Structure(t *testing.T) {
	box := func(kind string, data []byte) []byte {
		b := make([]byte, 8+len(data))
		binary.BigEndian.PutUint32(b, uint32(len(b)))
		copy(b[4:], kind)
		copy(b[8:], data)
		return b
	}
	valid := append(box("ftyp", []byte("isom0000")), box("mdat", make([]byte, 1024))...)
	valid = append(valid, box("moov", []byte("metadata"))...)
	for _, step := range []int{1, 7, 8, 15, 4096} {
		var check uploadMP4Check
		for p := valid; len(p) > 0; {
			n := step
			if n > len(p) {
				n = len(p)
			}
			_, _ = check.Write(p[:n])
			p = p[n:]
		}
		if err := check.validate(); err != nil {
			t.Fatal(step, err)
		}
	}
	for _, length := range []int{0, 3, 16, len(valid) - 1, len(valid) - 16} {
		var check uploadMP4Check
		_, _ = check.Write(valid[:length])
		if check.validate() == nil {
			t.Fatal("truncation accepted", length)
		}
	}
	extended := make([]byte, 24)
	binary.BigEndian.PutUint32(extended, 1)
	copy(extended[4:], "moov")
	binary.BigEndian.PutUint64(extended[8:], 24)
	var check uploadMP4Check
	for _, b := range extended {
		_, _ = check.Write([]byte{b})
	}
	if err := check.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestUploadMP4RetainedReference(t *testing.T) {
	name := os.Getenv("STORYBOARD_TEST_VIDEO")
	if name == "" {
		t.Skip("optional reference")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var full, partial uploadMP4Check
	_, _ = full.Write(data)
	if err := full.validate(); err != nil {
		t.Fatal(err)
	}
	_, _ = partial.Write(data[:len(data)/2])
	if partial.validate() == nil {
		t.Fatal("partial accepted")
	}
}
