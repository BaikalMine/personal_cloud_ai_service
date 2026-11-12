package mediaprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"io"
	"testing"
	"testing/iotest"
)

func TestDecodeContainerFraming(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	data := pngData.Bytes()
	if err := checkDecodePNG(iotest.OneByteReader(bytes.NewReader(data)), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{data[:len(data)-1], append(append([]byte(nil), data...), 0), []byte("bad PNG")} {
		if err := checkDecodePNG(bytes.NewReader(bad), int64(len(bad))); err == nil {
			t.Fatal("malformed PNG accepted")
		}
	}
	badCRC := append([]byte(nil), data...)
	badCRC[29] ^= 1
	if err := checkDecodePNG(bytes.NewReader(badCRC), int64(len(badCRC))); !errors.Is(err, ErrInvalid) {
		t.Fatal("CRC not checked", err)
	}
	box := []byte{0, 0, 0, 8, 'f', 'r', 'e', 'e'}
	extended := []byte{0, 0, 0, 1, 'f', 'r', 'e', 'e', 0, 0, 0, 0, 0, 0, 0, 16}
	zero := []byte{0, 0, 0, 0, 'm', 'd', 'a', 't', 1, 2, 3}
	for _, data := range [][]byte{box, extended, zero, append(append([]byte(nil), box...), extended...)} {
		if err := checkDecodeMOV(iotest.OneByteReader(bytes.NewReader(data)), int64(len(data))); err != nil {
			t.Fatal("valid BMFF framing", err)
		}
	}
	for _, data := range [][]byte{box[:7], extended[:15], {0, 0, 0, 7, 'f', 'r', 'e', 'e'}, {0, 0, 0, 9, 'f', 'r', 'e', 'e'}, {0, 0, 0, 1, 'f', 'r', 'e', 'e', 255, 255, 255, 255, 255, 255, 255, 255}} {
		if err := checkDecodeMOV(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatal("invalid BMFF framing accepted")
		}
	}
	if err := checkDecodeMOV(bytes.NewReader(bytes.Repeat(box, 10001)), int64(8*10001)); !errors.Is(err, ErrLimit) {
		t.Fatal("box count not bounded", err)
	}
	webp := []byte{'R', 'I', 'F', 'F', 14, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' ', 1, 0, 0, 0, 7, 0}
	if err := checkDecodeWebP(iotest.OneByteReader(bytes.NewReader(webp)), int64(len(webp))); err != nil {
		t.Fatal("RIFF padding", err)
	}
	for _, data := range [][]byte{webp[:len(webp)-1], append(append([]byte(nil), webp...), 0), []byte("RIFF")} {
		if err := checkDecodeWebP(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Fatal("invalid RIFF framing accepted")
		}
	}
	badLength := append([]byte(nil), webp...)
	binary.LittleEndian.PutUint32(badLength[16:20], 0xffffffff)
	if err := checkDecodeWebP(bytes.NewReader(badLength), int64(len(badLength))); err == nil {
		t.Fatal("RIFF chunk size not bounded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := decodeContextReader{ctx: ctx, r: bytes.NewReader(box)}
	if _, err := io.Copy(io.Discard, reader); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func FuzzDecodeContainer(f *testing.F) {
	f.Add([]byte{0, 0, 0, 8, 'f', 'r', 'e', 'e'})
	f.Add([]byte("RIFF\x04\x00\x00\x00WEBP"))
	f.Add([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		_ = checkDecodePNG(bytes.NewReader(data), int64(len(data)))
		_ = checkDecodeWebP(bytes.NewReader(data), int64(len(data)))
		_ = checkDecodeMOV(bytes.NewReader(data), int64(len(data)))
	})
}
