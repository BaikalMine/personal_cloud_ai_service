package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func requireCodec(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux codec resource limits required")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable; run scripts/thumbnail.Dockerfile")
	}
}

func TestCodecFailureCategoriesHideDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		diagnostic string
		want       error
	}{
		{"Codec mpeg4 is not on whitelist /private/upload.mp4", ErrUnsupported},
		{"Unknown decoder /private/upload.mp4", ErrUnsupported},
		{"Cannot allocate memory /private/upload.mp4", ErrLimit},
		{"Invalid data found /private/upload.mp4", ErrInvalid},
	} {
		got := codecFailure(errors.New("exit status 1"), tc.diagnostic)
		if !errors.Is(got, tc.want) || bytes.Contains([]byte(got.Error()), []byte("private")) {
			t.Fatalf("unsafe or incorrect category: %v", got)
		}
	}
	if !errors.Is(codecFailure(ErrLimit, ""), ErrLimit) {
		t.Fatal("output limit lost")
	}
}

func fixtureImage(w, h int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.NRGBA{B: 255, A: 255}
			}
			im.SetNRGBA(x, y, c)
		}
	}
	return im
}

func writeFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func renderFixture(t *testing.T, path, mime string, w, h int) image.Image {
	t.Helper()
	data, err := Render(context.Background(), path, mime)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxOutputBytes {
		t.Fatal("unbounded output")
	}
	for offset := 8; offset+12 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if length > len(data)-offset-12 {
			t.Fatal("invalid PNG chunk")
		}
		kind := string(data[offset+4 : offset+8])
		if kind == "tEXt" || kind == "zTXt" || kind == "iTXt" || kind == "eXIf" {
			t.Fatalf("source metadata retained in %s", kind)
		}
		offset += length + 12
	}
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if im.Bounds().Dx() != w || im.Bounds().Dy() != h {
		t.Fatalf("dimensions: %v want %dx%d", im.Bounds(), w, h)
	}
	return im
}

func TestCodecStillSizePixelsAndNoUpscale(t *testing.T) {
	requireCodec(t)
	for _, size := range []image.Point{{1600, 800}, {80, 40}, {800, 1600}} {
		var src bytes.Buffer
		if err := png.Encode(&src, fixtureImage(size.X, size.Y)); err != nil {
			t.Fatal(err)
		}
		path := writeFixture(t, "source with spaces.png", src.Bytes())
		w, h := size.X, size.Y
		if w > 160 {
			h = h * 160 / w
			w = 160
		}
		if h > 160 {
			w = w * 160 / h
			h = 160
		}
		im := renderFixture(t, path, "image/png", w, h)
		r, _, b, _ := im.At(w/4, h/2).RGBA()
		if r < 60000 || b > 1000 {
			t.Fatal("left source pixels changed")
		}
		r, _, b, _ = im.At(w*3/4, h/2).RGBA()
		if b < 60000 || r > 1000 {
			t.Fatal("right source pixels changed")
		}
	}
}

func TestCodecJPEGOrientation(t *testing.T) {
	requireCodec(t)
	var src bytes.Buffer
	if err := jpeg.Encode(&src, fixtureImage(800, 400), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	// EXIF TIFF IFD0 orientation=6: display rotation 90 degrees clockwise.
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	data := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	data = append(data, src.Bytes()[2:]...)
	im := renderFixture(t, writeFixture(t, "rotated.jpg", data), "image/jpeg", 80, 160)
	r, _, b, _ := im.At(40, 30).RGBA()
	if r < 50000 || b > 10000 {
		t.Fatal("EXIF top is not original left")
	}
	r, _, b, _ = im.At(40, 130).RGBA()
	if b < 50000 || r > 10000 {
		t.Fatal("EXIF bottom is not original right")
	}
}

func ffmpegFixture(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-threads", "1", "-filter_threads", "1"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
}

func TestCodecVideoFirstFrameAndRotation(t *testing.T) {
	requireCodec(t)
	path := filepath.Join(t.TempDir(), "video.mp4")
	ffmpegFixture(t, "-f", "lavfi", "-i", "color=c=red:s=640x320:r=2:d=1", "-f", "lavfi", "-i", "color=c=blue:s=640x320:r=2:d=1", "-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-c:v", "libx264", "-threads", "1", path)
	im := renderFixture(t, path, "video/mp4", 160, 80)
	r, _, b, _ := im.At(80, 40).RGBA()
	if r < 50000 || b > 10000 {
		t.Fatal("not the first frame")
	}
	rotated := filepath.Join(t.TempDir(), "rotated.mp4")
	ffmpegFixture(t, "-display_rotation", "90", "-i", path, "-c", "copy", rotated)
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream_side_data=rotation", "-of", "csv=p=0", rotated).Output()
	if err != nil || !bytes.Contains(probe, []byte("90")) {
		t.Fatalf("fixture has no display rotation: %q %v", probe, err)
	}
	renderFixture(t, rotated, "video/mp4", 80, 160)
	anamorphic := filepath.Join(t.TempDir(), "anamorphic.mp4")
	ffmpegFixture(t, "-i", path, "-vf", "setsar=2", "-c:v", "libx264", "-threads", "1", anamorphic)
	renderFixture(t, anamorphic, "video/mp4", 160, 40)
}

func TestCodecRealPhotoReductionAndDeadline(t *testing.T) {
	requireCodec(t)
	path := "../promptassistant/testdata/visual-v1/images/03.jpg"
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Render(context.Background(), path, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data))*2 >= info.Size() {
		t.Fatalf("insufficient reduction: %d -> %d", info.Size(), len(data))
	}
	t.Logf("real photo: %d -> %d bytes", info.Size(), len(data))
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Render(ctx, path, "image/jpeg"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("codec did not stop promptly")
	}
	if _, err := Render(context.Background(), path, "image/jpeg"); err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
}

func TestCodecWebPWebMAndOversizedHeader(t *testing.T) {
	requireCodec(t)
	var src bytes.Buffer
	if err := png.Encode(&src, fixtureImage(256, 512)); err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, "source.png", src.Bytes())
	webp := filepath.Join(t.TempDir(), "source.webp")
	ffmpegFixture(t, "-i", path, "-c:v", "libwebp", "-lossless", "1", webp)
	renderFixture(t, webp, "image/webp", 80, 160)
	webm := filepath.Join(t.TempDir(), "source.webm")
	ffmpegFixture(t, "-i", path, "-c:v", "libvpx-vp9", "-threads", "1", "-pix_fmt", "yuv420p", webm)
	renderFixture(t, webm, "video/webm", 80, 160)
	deceptive := append([]byte(nil), src.Bytes()...)
	binary.BigEndian.PutUint32(deceptive[16:20], 1000000)
	binary.BigEndian.PutUint32(deceptive[20:24], 1000000)
	binary.BigEndian.PutUint32(deceptive[29:33], crc32.ChecksumIEEE(deceptive[12:29]))
	if _, err := Render(context.Background(), writeFixture(t, "oversized-header.png", deceptive), "image/png"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized decode header: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linked.png")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(context.Background(), link, "image/png"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink source: %v", err)
	}
}

func TestCodecRejectsInvalidUnboundedAndRemoteInputs(t *testing.T) {
	requireCodec(t)
	for _, data := range [][]byte{[]byte("garbage"), []byte("#EXTM3U\nhttp://127.0.0.1:1/media.ts\n"), []byte("file:///etc/passwd")} {
		for _, mime := range []string{"image/png", "image/jpeg", "video/mp4", "video/webm"} {
			if _, err := Render(context.Background(), writeFixture(t, "input", data), mime); err == nil {
				t.Fatalf("accepted %s", mime)
			}
		}
	}
	if _, err := Render(context.Background(), "https://example.com/video.mp4", "video/mp4"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := Render(context.Background(), "unused", "application/vnd.apple.mpegurl"); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "oversized")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(MaxInputBytes + 1)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(context.Background(), path, "video/mp4"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var src bytes.Buffer
	_ = png.Encode(&src, fixtureImage(40, 20))
	if _, err := Render(ctx, writeFixture(t, "cancel.png", src.Bytes()), "image/png"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
