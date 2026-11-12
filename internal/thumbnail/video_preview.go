package thumbnail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const MaxVideoPreviewBytes = 16 << 20

// VideoPreview produces a CPU-only, bounded browser copy. Never use it as a
// generation input: only the first 30 seconds are included, at preview quality.
func VideoPreview(ctx context.Context, filename, mime string) ([]byte, error) {
	format, codecs := inputFormat(mime)
	if runtime.GOOS != "linux" || (format != "mov" && format != "matroska") {
		return nil, ErrUnsupported
	}
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, ErrInvalid
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, ErrInvalid
	}
	if info.Size() > 512<<20 {
		return nil, ErrLimit
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-xerror",
		"-max_alloc", "134217728", "-filter_threads", "1", "-filter_complex_threads", "1",
		"-hwaccel", "none", "-threads", "1", "-max_pixels", "36000000",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", format,
		"-codec_whitelist", codecs + ",aac,mp3,alac,pcm_s16le,opus,vorbis,flac",
		"-probesize", "1048576", "-analyzeduration", "1000000", "-f", format}
	if format == "mov" {
		args = append(args, "-enable_drefs", "0", "-use_absolute_path", "0")
	}
	args = append(args, "-i", filename, "-t", "30", "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn",
		"-vf", "scale=640:640:force_original_aspect_ratio=decrease:force_divisible_by=2:reset_sar=1,fps=24",
		"-map_metadata", "-1", "-map_chapters", "-1", "-threads", "1", "-c:v", "libx264",
		"-preset", "veryfast", "-crf", "26", "-pix_fmt", "yuv420p", "-maxrate", "2M", "-bufsize", "4M",
		"-c:a", "aac", "-b:a", "96k", "-ac", "2", "-ar", "48000",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	wrapper := `ulimit -c 0 && ulimit -t 20 && ulimit -v 786432 && exec ffmpeg "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", wrapper, "video-preview"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "OMP_NUM_THREADS=1"}
	cmd.WaitDelay = time.Second
	output, diagnostic := limitedBuffer{limit: MaxVideoPreviewBytes}, limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: preview codec failed", ErrInvalid)
	}
	if output.Len() < 32 {
		return nil, ErrInvalid
	}
	return output.Bytes(), nil
}
