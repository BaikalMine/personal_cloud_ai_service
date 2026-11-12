package thumbnail

import (
	"bytes"
	"context"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStoryboardBounds(t *testing.T) {
	for _, v := range [][2]float64{{-1, 5}, {601, 5}, {0, 0}, {0, 16}, {math.NaN(), 5}, {0, math.Inf(1)}} {
		if _, err := Storyboard(context.Background(), "missing", "video/mp4", v[0], v[1]); err == nil {
			t.Fatal("invalid bounds accepted")
		}
	}
}

func TestStoryboardRetainedReference(t *testing.T) {
	requireCodec(t)
	file := os.Getenv("STORYBOARD_TEST_VIDEO")
	if file == "" {
		t.Skip("optional retained reference")
	}
	if _, err := Storyboard(context.Background(), file, "video/mp4", 0, 7.6); err != nil {
		t.Fatal(err)
	}
	if _, err := VideoPreview(context.Background(), file, "video/mp4"); err != nil {
		t.Fatal("preview:", err)
	}
}

func TestStoryboardVideo(t *testing.T) {
	requireCodec(t)
	file := filepath.Join(t.TempDir(), "sample.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x240:rate=24", "-t", "5", "-c:v", "libx264", "-threads", "1", file)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	data, err := Storyboard(context.Background(), file, "video/mp4", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil || im.Bounds().Dx() != 1024 || im.Bounds().Dy() != 1024 {
		t.Fatal("bad storyboard", err)
	}
	if _, err := Storyboard(context.Background(), file, "video/mp4", 600, 5); err == nil {
		t.Fatal("out of range source accepted")
	}
}

func TestStoryboardVideoWithAudio(t *testing.T) {
	requireCodec(t)
	file := filepath.Join(t.TempDir(), "with-audio.mp4")
	ffmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "5", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", file)
	if _, err := Storyboard(context.Background(), file, "video/mp4", 0, 5); err != nil {
		t.Fatal(err)
	}
}
