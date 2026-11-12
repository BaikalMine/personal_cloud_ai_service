package thumbnail

import (
	"bytes"
	"context"
	"image/png"
	"math"
	"path/filepath"
	"testing"
)

func TestStoryboardSequenceBounds(t *testing.T) {
	for _, v := range [][2]float64{{-1, 5}, {601, 5}, {0, 0}, {0, 30.01}, {math.NaN(), 5}, {0, math.Inf(1)}} {
		if _, err := StoryboardSequence(context.Background(), "missing", "video/mp4", v[0], v[1]); err == nil {
			t.Fatal("invalid bounds accepted")
		}
	}
}

func TestStoryboardSequenceChronology(t *testing.T) {
	requireCodec(t)
	file := filepath.Join(t.TempDir(), "sequence.mp4")
	ffmpegFixture(t, "-f", "lavfi", "-i", "testsrc2=size=160x240:rate=24", "-t", "31", "-c:v", "libx264", "-threads", "1", file)
	for _, duration := range []float64{3.1, 30} {
		pages, err := StoryboardSequence(context.Background(), file, "video/mp4", .5, duration)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, page := range pages {
			config, err := png.DecodeConfig(bytes.NewReader(page.Image))
			if err != nil || config.Width != 1280 || config.Height != 960 {
				t.Fatal("invalid page", err)
			}
			for _, at := range page.Times {
				if at != .5+float64(count)/4 {
					t.Fatal("incorrect time", at)
				}
				count++
			}
		}
		if count != int(math.Ceil(duration*4)) || len(pages) != (count+11)/12 {
			t.Fatal("missing samples", count, len(pages))
		}
	}
}
