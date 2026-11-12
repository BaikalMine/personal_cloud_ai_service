package thumbnail

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVideoPreviewCompatibleAndBounded(t *testing.T) {
	requireCodec(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	ffmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=s=160x96:r=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "31", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", source)
	data, err := VideoPreview(context.Background(), source, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxVideoPreviewBytes {
		t.Fatal("preview exceeds bound")
	}
	output := filepath.Join(dir, "preview.mp4")
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "json", output).Output()
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Streams []struct {
			Codec         string `json:"codec_name"`
			Width, Height int
		}
	}
	if err := json.Unmarshal(probe, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Streams) != 2 || metadata.Streams[0].Codec != "h264" || metadata.Streams[1].Codec != "aac" || metadata.Streams[0].Width > 640 || metadata.Streams[0].Height > 640 {
		t.Fatalf("unexpected preview: %s", probe)
	}
	if _, err := Storyboard(context.Background(), source, "video/mp4", 0, 30); err != nil {
		t.Fatal("30s storyboard", err)
	}
	if _, err := Storyboard(context.Background(), source, "video/mp4", 0, 30.1); err == nil {
		t.Fatal("unbounded storyboard")
	}
}

func TestVideoPreviewActualHEVC(t *testing.T) {
	requireCodec(t)
	source := os.Getenv("VIDEO_PREVIEW_TEST_SOURCE")
	if source == "" {
		t.Skip("optional actual HEVC source")
	}
	data, err := VideoPreview(context.Background(), source, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("VIDEO_PREVIEW_TEST_OUTPUT"); output != "" {
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
