package mediaprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodedFrameEvidence(t *testing.T) {
	source := Measurement{MediaType: "video", Width: 128, Height: 80, AverageFPS: "24000/1001"}
	data := `{"streams":[{"time_base":"1/24000","sample_aspect_ratio":"1:1"}],"frames":[{"width":128,"height":80,"best_effort_timestamp":0,"duration":1001},{"width":128,"height":80,"best_effort_timestamp":1001,"duration":1001},{"width":128,"height":80,"best_effort_timestamp":2002,"duration":1001}]}`
	got, err := parseDecodedFrames([]byte(data), source)
	if err != nil || got.Frames != 3 || got.FPSNumerator != 24000 || got.FPSDenominator != 1001 || !got.UniformCadence || got.Opaque {
		t.Fatalf("%+v %v", got, err)
	}
	for _, change := range []struct{ old, new string }{
		{`"streams":[{"time_base":"1/24000","sample_aspect_ratio":"1:1"}]`, `"streams":[]`},
		{`"time_base":"1/24000"`, `"time_base":"0/0"`},
		{`"sample_aspect_ratio":"1:1"`, `"sample_aspect_ratio":"2:1"`},
		{`"best_effort_timestamp":2002`, `"best_effort_timestamp":2003`},
		{`"best_effort_timestamp":2002`, `"best_effort_timestamp":1001`},
		{`"best_effort_timestamp":2002`, `"best_effort_timestamp":9223372036854775807`},
		{`"best_effort_timestamp":0`, `"best_effort_timestamp":-1`},
		{`"best_effort_timestamp":0`, `"best_effort_timestamp":1`},
		{`"duration":1001`, `"duration":1000`},
		{`"width":128`, `"width":129`},
		{`"height":80`, `"height":81`},
		{`"width":128`, `"tags":{"Orientation":"6"},"width":128`},
		{`"width":128`, `"tags":{"alpha_mode":"1"},"width":128`},
	} {
		if _, err := parseDecodedFrames([]byte(strings.Replace(data, change.old, change.new, 1)), source); err == nil {
			t.Fatalf("accepted %s -> %s", change.old, change.new)
		}
	}
	legacy := strings.ReplaceAll(data, `"duration"`, `"pkt_duration"`)
	if _, err := parseDecodedFrames([]byte(legacy), source); err != nil {
		t.Fatal(err)
	}
	source.AverageFPS = "24/1"
	if _, err := parseDecodedFrames([]byte(data), source); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted inconsistent header FPS", err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatal(err)
	}
	raw["frames"] = make([]any, MaxDecodeFrames+1)
	encoded, _ := json.Marshal(raw)
	if _, err := parseDecodedFrames(encoded, source); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func TestDecodedDisplayMatrix(t *testing.T) {
	identity := "\n00000000: 65536 0 0\n00000001: 0 65536 0\n00000002: 0 0 1073741824\n"
	if !normalDecodeMetadata("1:1", nil, []decodedSideData{{Matrix: identity}}) {
		t.Fatal("identity rejected")
	}
	for _, matrix := range []string{strings.Replace(identity, "65536", "-65536", 1), strings.Replace(identity, "65536", "65535", 1), identity + identity, "bad", "0: 1 2 3"} {
		if normalDecodeMetadata("1:1", nil, []decodedSideData{{Matrix: matrix}}) {
			t.Fatal("accepted nonidentity matrix", matrix)
		}
	}
}

func TestRGBAFullDecodeProofBoundaries(t *testing.T) {
	pixels := []byte{0, 1, 2, 3, 4, 5, 255, 255, 6, 7, 8, 9, 10, 11, 255, 255, 12, 13, 14, 15, 16, 17, 255, 255}
	for chunk := 1; chunk <= len(pixels); chunk++ {
		proof := rgbaDecodeProof{limit: int64(len(pixels))}
		for start := 0; start < len(pixels); start += chunk {
			end := min(start+chunk, len(pixels))
			if _, err := proof.Write(pixels[start:end]); err != nil {
				t.Fatalf("chunk=%d: %v", chunk, err)
			}
		}
		if proof.count != int64(len(pixels)) {
			t.Fatal("wrong pixel count")
		}
		if _, err := proof.Write([]byte{0}); !errors.Is(err, ErrLimit) {
			t.Fatal("excess bytes accepted", err)
		}
		for _, index := range []int{6, 7, 14, 15, 22, 23} {
			bad := append([]byte(nil), pixels...)
			bad[index] = 254
			cancelled := false
			proof := rgbaDecodeProof{limit: int64(len(bad)), cancel: func() { cancelled = true }}
			for start := 0; start < len(bad); start += chunk {
				if _, err := proof.Write(bad[start:min(start+chunk, len(bad))]); err != nil {
					break
				}
			}
			if !errors.Is(proof.failure, ErrUnsupported) || !cancelled {
				t.Fatalf("missed alpha at %d chunk %d", index, chunk)
			}
		}
	}
}

func decodeFixture(t *testing.T, args ...string) {
	t.Helper()
	if data, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error", "-threads", "1", "-filter_threads", "1"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, data)
	}
}

func TestDecodeRealOpaqueAndTransparentImages(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 128, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 128; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x), G: byte(y), B: 50, A: 255})
		}
	}
	for _, format := range []string{"png", "jpeg"} {
		var encoded bytes.Buffer
		var err error
		if format == "png" {
			err = png.Encode(&encoded, img)
		} else {
			err = jpeg.Encode(&encoded, img, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "source ; unchanged."+format)
		if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := Decode(context.Background(), path, "image/"+format)
		if err != nil || got.Frames != 1 || got.Width != 128 || got.Height != 80 || !got.Opaque || !got.NormalOrientation || !got.SquarePixels || got.UniformCadence || got.FPSNumerator != 0 || got.Source != "completed_file_full_frame_and_pixel_decode" {
			t.Fatalf("%s: %+v %v", format, got, err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, encoded.Bytes()) {
			t.Fatal("source bytes changed")
		}
	}
	img.SetNRGBA(127, 79, color.NRGBA{R: 100, A: 254})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "alpha.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(context.Background(), path, "image/png"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("alpha not rejected: %v", err)
	}
	deep := image.NewNRGBA64(image.Rect(0, 0, 128, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 128; x++ {
			deep.SetNRGBA64(x, y, color.NRGBA64{R: 1000, G: 2000, B: 3000, A: 65535})
		}
	}
	deep.SetNRGBA64(127, 79, color.NRGBA64{R: 1000, G: 2000, B: 3000, A: 65534})
	encoded.Reset()
	if err := png.Encode(&encoded, deep); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(context.Background(), path, "image/png"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("16-bit alpha rounded away: %v", err)
	}
	webp := filepath.Join(dir, "opaque.webp")
	decodeFixture(t, "-f", "lavfi", "-i", "color=c=blue:s=128x80", "-frames:v", "1", "-c:v", "libwebp", "-threads", "1", webp)
	if got, err := Decode(context.Background(), webp, "image/webp"); err != nil || !got.Opaque || got.Frames != 1 {
		t.Fatalf("webp: %+v %v", got, err)
	}
}

func TestDecodeRealVideoCadenceAndRejections(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.mp4")
	decodeFixture(t, "-f", "lavfi", "-i", "testsrc2=s=128x80:r=30000/1001", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1.001", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
	before, _ := os.ReadFile(path)
	got, err := Decode(context.Background(), path, "video/mp4")
	if err != nil || got.Frames != 30 || got.FPSNumerator != 30000 || got.FPSDenominator != 1001 || !got.UniformCadence || !got.Opaque || got.DecodedPixels != 30*128*80 {
		t.Fatalf("%+v %v", got, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("video changed")
	}
	rotated := filepath.Join(dir, "rotated.mp4")
	decodeFixture(t, "-display_rotation", "90", "-i", path, "-c", "copy", rotated)
	if _, err := Decode(context.Background(), rotated, "video/mp4"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("rotation: %v", err)
	}
	variable := filepath.Join(dir, "vfr.mp4")
	decodeFixture(t, "-i", path, "-vf", "select='not(eq(n,10))'", "-fps_mode", "vfr", "-an", "-c:v", "libx264", "-threads", "1", variable)
	if _, err := Decode(context.Background(), variable, "video/mp4"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("variable cadence: %v", err)
	}
	many := filepath.Join(dir, "many.mp4")
	decodeFixture(t, "-f", "lavfi", "-i", "color=c=red:s=64x64:r=60", "-frames:v", "451", "-c:v", "libx264", "-threads", "1", many)
	if _, err := Decode(context.Background(), many, "video/mp4"); !errors.Is(err, ErrLimit) {
		t.Fatalf("frame limit: %v", err)
	}
	for _, cut := range []int{len(before) / 2, len(before) - 100} {
		truncated := filepath.Join(dir, "truncated.mp4")
		if err := os.WriteFile(truncated, before[:cut], 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(context.Background(), truncated, "video/mp4"); err == nil {
			t.Fatal("truncated video accepted")
		}
	}
	webm := filepath.Join(dir, "source.webm")
	decodeFixture(t, "-f", "lavfi", "-i", "testsrc2=s=128x80:r=25", "-t", "0.4", "-c:v", "libvpx-vp9", "-threads", "1", webm)
	if got, err := Decode(context.Background(), webm, "video/webm"); err != nil || got.Frames != 10 || got.FPSNumerator != 25 || got.FPSDenominator != 1 {
		t.Fatalf("WebM: %+v %v", got, err)
	}
}

func TestDecodeRealEXIFOrientation(t *testing.T) {
	requireNative(t)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 128, 80)), nil); err != nil {
		t.Fatal(err)
	}
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	data := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	data = append(data, encoded.Bytes()[2:]...)
	path := filepath.Join(t.TempDir(), "oriented.jpg")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(context.Background(), path, "image/jpeg"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("oriented JPEG: %v", err)
	}
	for _, orientation := range []byte{2, 3, 4, 5, 7, 8} {
		variant := append([]byte(nil), data...)
		variant[8+24] = orientation
		if err := os.WriteFile(path, variant, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(context.Background(), path, "image/jpeg"); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("JPEG orientation %d: %v", orientation, err)
		}
	}
}

func TestDecodeEXIFInPNGAndWebP(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	img := image.NewRGBA(image.Rect(0, 0, 128, 80))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, len(tiff)+12)
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(tiff)))
	copy(chunk[4:8], "eXIf")
	copy(chunk[8:], tiff)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	data := append([]byte(nil), encoded.Bytes()[:33]...)
	data = append(data, chunk...)
	data = append(data, encoded.Bytes()[33:]...)
	path := filepath.Join(dir, "oriented.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(context.Background(), path, "image/png"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PNG EXIF: %v", err)
	}
	webp := filepath.Join(dir, "source.webp")
	decodeFixture(t, "-f", "lavfi", "-i", "color=c=blue:s=128x80", "-frames:v", "1", "-c:v", "libwebp", "-threads", "1", webp)
	original, err := os.ReadFile(webp)
	if err != nil {
		t.Fatal(err)
	}
	webpChunk := func(kind string, payload []byte) []byte {
		out := make([]byte, 8+len(payload)+len(payload)%2)
		copy(out[:4], kind)
		binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
		copy(out[8:], payload)
		return out
	}
	body := append([]byte("WEBP"), webpChunk("VP8X", []byte{8, 0, 0, 0, 127, 0, 0, 79, 0, 0})...)
	body = append(body, original[12:]...)
	body = append(body, webpChunk("EXIF", tiff)...)
	data = make([]byte, 8)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(body)))
	data = append(data, body...)
	if err := os.WriteFile(webp, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(context.Background(), webp, "image/webp"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("WebP EXIF: %v", err)
	}
}

func TestDecodeRejectsRealAnimatedImages(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	apng := filepath.Join(dir, "animated.png")
	decodeFixture(t, "-f", "lavfi", "-i", "testsrc2=s=128x80:r=2", "-frames:v", "2", "-c:v", "apng", "-pix_fmt", "rgb24", "-threads", "1", "-f", "apng", apng)
	if _, err := Decode(context.Background(), apng, "image/png"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("animated PNG: %v", err)
	}
	webp := filepath.Join(dir, "animated.webp")
	decodeFixture(t, "-f", "lavfi", "-i", "testsrc2=s=128x80:r=2", "-frames:v", "2", "-c:v", "libwebp_anim", "-threads", "1", "-loop", "0", webp)
	if _, err := Decode(context.Background(), webp, "image/webp"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("animated WebP: %v", err)
	}
}

func TestDecodeInputAndCancellation(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.png")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, mime string }{{path, "image/png"}, {dir, "image/png"}, {path, "image/svg+xml"}, {filepath.Join(dir, "missing"), "image/png"}} {
		if _, err := Decode(context.Background(), item.path, item.mime); err == nil {
			t.Fatal("invalid input accepted", item)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, path, "image/png"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
