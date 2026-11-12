package thumbnail

import (
	"context"
	"fmt"
	"math"
)

type StoryboardPage struct {
	Image []byte
	Times []float64
}

// StoryboardSequence preserves chronology at four samples per second. Each
// bounded page contains up to twelve tiles, never extra identity references.
func StoryboardSequence(ctx context.Context, path, mime string, start, duration float64) ([]StoryboardPage, error) {
	if start < 0 || start > 600 || duration <= 0 || duration > 30 || math.IsNaN(start) || math.IsNaN(duration) || math.IsInf(start, 0) || math.IsInf(duration, 0) {
		return nil, ErrInvalid
	}
	if mime != "video/mp4" && mime != "video/quicktime" && mime != "video/webm" && mime != "video/x-matroska" {
		return nil, ErrUnsupported
	}
	count := int(math.Ceil(duration * 4))
	pages := make([]StoryboardPage, 0, (count+11)/12)
	bytes := 0
	for offset := 0; offset < count; offset += 12 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(12, count-offset)
		at := start + float64(offset)/4
		filter := fmt.Sprintf("fps=4:start_time=0,scale=320:320:force_original_aspect_ratio=decrease:reset_sar=1,pad=320:320:(ow-iw)/2:(oh-ih)/2,tile=4x3:nb_frames=%d", n)
		data, err := render(ctx, path, mime, 512<<20, 2<<20, 1280, at, filter)
		if err != nil {
			return nil, err
		}
		bytes += len(data)
		if bytes > 16<<20 {
			return nil, ErrLimit
		}
		times := make([]float64, n)
		for i := range times {
			times[i] = at + float64(i)/4
		}
		pages = append(pages, StoryboardPage{Image: data, Times: times})
	}
	return pages, nil
}
