package mediaprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseMeasurement(t *testing.T) {
	data := `{"streams":[{"codec_name":"h264","width":640,"height":360,"sample_aspect_ratio":"2:2","avg_frame_rate":"60000/2002","duration":"1.001000","side_data_list":[{"rotation":90}]}]}`
	m, err := parse([]byte(data), "video")
	if err != nil || m.Width != 640 || m.Height != 360 || m.AverageFPS != "30000/1001" || m.DurationSeconds != "1.001" || m.SampleAspectRatio != "1:1" || m.RotationDegrees == nil || *m.RotationDegrees != 90 {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = parse([]byte(data), "image")
	if err != nil || m.AverageFPS != "" || m.DurationSeconds != "" {
		t.Fatal("image acquired artificial frame rate", m, err)
	}
	for _, bad := range []string{`{}`, `{"streams":[]}`, `{"streams":[{},{}]}`, strings.Replace(data, `"width":640`, `"width":0`, 1), strings.Replace(data, `"width":640`, `"width":999999`, 1), strings.Replace(data, `"rotation":90`, `"rotation":90.5`, 1), strings.Replace(data, `"h264"`, `"sensitive file path"`, 1), data + `{}`} {
		if _, err := parse([]byte(bad), "video"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s: %v", bad, err)
		}
	}
	for _, value := range []string{"N/A", "0/0", "-1/2", "NaN/1", "1/0", "1/2/3", "99999999999999/1"} {
		if rational(value, "/") != "" {
			t.Fatal(value)
		}
	}
	unknown := strings.Replace(strings.Replace(data, "60000/2002", "0/0", 1), "1.001000", "NaN", 1)
	m, err = parse([]byte(unknown), "video")
	if err != nil || m.AverageFPS != "" || m.DurationSeconds != "" {
		t.Fatal(m, err)
	}
}

func requireNative(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("native probe uses Linux hard limits")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Fatal("ffprobe is required in the media test image")
	}
}

func TestProbeRealPNGAndLimits(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source ; not shell.png")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 321, 123))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Probe(context.Background(), path, "image/png")
	sum := sha256.Sum256(b.Bytes())
	if err != nil || m.Width != 321 || m.Height != 123 || m.AverageFPS != "" || m.SHA256 != hex.EncodeToString(sum[:]) || m.SizeBytes != int64(b.Len()) || m.MeasuredAt.IsZero() {
		t.Fatalf("%+v %v", m, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, b.Bytes()) {
		t.Fatal("source modified", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, path, "image/png"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, mime string
		want       error
	}{{path, "image/svg+xml", ErrUnsupported}, {dir, "image/png", ErrInvalid}, {filepath.Join(dir, "missing"), "image/png", ErrInvalid}, {path, "video/mp4", ErrInvalid}} {
		if _, err := Probe(context.Background(), tc.path, tc.mime); !errors.Is(err, tc.want) {
			t.Fatal(tc, err)
		}
	}
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), link, "image/png"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxInputBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Probe(context.Background(), large, "image/png"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a media file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(context.Background(), path, "image/png"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestProbeRealVideoRationalFPSRotationAndAudio(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.mp4")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error", "-threads", "1"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	run("-f", "lavfi", "-i", "color=c=red:s=320x180:r=30000/1001", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1.001", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
	m, err := Probe(context.Background(), path, "video/mp4")
	if err != nil || m.Width != 320 || m.Height != 180 || m.AverageFPS != "30000/1001" || m.DurationSeconds != "1.001" {
		t.Fatalf("%+v %v", m, err)
	}
	rotated := filepath.Join(dir, "rotated.mp4")
	run("-display_rotation", "90", "-i", path, "-c", "copy", rotated)
	m, err = Probe(context.Background(), rotated, "video/mp4")
	if err != nil || m.Width != 320 || m.Height != 180 || m.RotationDegrees == nil || *m.RotationDegrees != 90 {
		t.Fatalf("coded dimensions must not silently rotate: %+v %v", m, err)
	}
	webm := filepath.Join(dir, "source.webm")
	run("-f", "lavfi", "-i", "color=c=blue:s=160x90:r=24", "-t", "0.5", "-c:v", "libvpx-vp9", "-threads", "1", webm)
	m, err = Probe(context.Background(), webm, "video/webm")
	if err != nil || m.Width != 160 || m.Height != 90 || m.AverageFPS != "24/1" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestProbeRealJPEGAndWebP(t *testing.T) {
	requireNative(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jpg")
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 125, 91)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Probe(context.Background(), path, "image/jpeg")
	if err != nil || m.Width != 125 || m.Height != 91 || m.AverageFPS != "" || m.Codec != "mjpeg" {
		t.Fatalf("%+v %v", m, err)
	}
	path = filepath.Join(dir, "source.webp")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=green:s=128x80", "-frames:v", "1", "-c:v", "libwebp", "-threads", "1", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	m, err = Probe(context.Background(), path, "image/webp")
	if err != nil || m.Width != 128 || m.Height != 80 || m.AverageFPS != "" || m.Codec != "webp" {
		t.Fatalf("%+v %v", m, err)
	}
}
