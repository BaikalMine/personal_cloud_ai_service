// Package thumbnail produces bounded, static derivatives. It does not authorize
// source access; callers must recheck access and retention even on cache hits.
package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	MaxInputBytes  = 64 << 20
	MaxOutputBytes = 128 << 10
	MaxEdge        = 160
	codecTimeout   = 8 * time.Second
)

var (
	ErrUnsupported = errors.New("thumbnail: unsupported source")
	ErrLimit       = errors.New("thumbnail: resource limit")
	ErrInvalid     = errors.New("thumbnail: invalid media")
)

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrLimit
	}
	return b.Buffer.Write(p)
}

func inputFormat(mime string) (string, string) {
	switch mime {
	case "image/png":
		return "png_pipe", "png"
	case "image/jpeg":
		return "jpeg_pipe", "mjpeg"
	case "image/webp":
		return "webp_pipe", "webp"
	case "video/mp4", "video/quicktime":
		return "mov", "h264,hevc,vp9,av1,libdav1d,mjpeg,png"
	case "video/webm", "video/x-matroska":
		return "matroska", "vp8,vp9,av1,libdav1d"
	default:
		return "", ""
	}
}

// Render reads only a locally materialized regular file. Linux is required so
// hard CPU/address-space limits also constrain malformed inputs in native codecs.
func Render(ctx context.Context, path, mime string) ([]byte, error) {
	return render(ctx, path, mime, MaxInputBytes, MaxOutputBytes, MaxEdge, 0, "scale=w='min(160,iw)':h='min(160,ih)':force_original_aspect_ratio=decrease:reset_sar=1")
}

// Storyboard is one bounded vision image, not four new Picture references.
// Four evenly spaced samples are arranged left-to-right, then top-to-bottom.
func Storyboard(ctx context.Context, path, mime string, start, duration float64) ([]byte, error) {
	if start < 0 || start > 600 || duration <= 0 || duration > 30 || math.IsNaN(start) || math.IsNaN(duration) || math.IsInf(start, 0) || math.IsInf(duration, 0) {
		return nil, ErrInvalid
	}
	if mime != "video/mp4" && mime != "video/quicktime" && mime != "video/webm" && mime != "video/x-matroska" {
		return nil, ErrUnsupported
	}
	filter := fmt.Sprintf("fps=%.8f:start_time=0,scale=512:512:force_original_aspect_ratio=decrease:reset_sar=1,pad=512:512:(ow-iw)/2:(oh-ih)/2,tile=2x2:nb_frames=4", 4/duration)
	return render(ctx, path, mime, 512<<20, 4<<20, 1024, start, filter)
}

func render(ctx context.Context, path, mime string, maxInput, maxOutput, maxEdge int, start float64, filter string) ([]byte, error) {
	format, codecs := inputFormat(mime)
	if format == "" || runtime.GOOS != "linux" {
		return nil, ErrUnsupported
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, ErrInvalid
	}
	if info.Size() > int64(maxInput) {
		return nil, ErrLimit
	}
	ctx, cancel := context.WithTimeout(ctx, codecTimeout)
	defer cancel()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-xerror",
		"-max_alloc", "134217728", "-filter_threads", "1", "-filter_complex_threads", "1",
		"-hwaccel", "none", "-threads", "1", "-max_pixels", "36000000",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", format,
		"-codec_whitelist", codecs, "-probesize", "1048576", "-analyzeduration", "1000000", "-f", format}
	if format == "mov" {
		args = append(args, "-enable_drefs", "0", "-use_absolute_path", "0")
	}
	if start > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.4f", start))
	}
	args = append(args, "-i", path, "-map", "0:v:0", "-an", "-sn", "-dn",
		"-frames:v", "1", "-vf", filter,
		"-map_metadata", "-1", "-map_chapters", "-1", "-threads", "1", "-c:v", "png",
		"-pix_fmt", "rgba", "-f", "image2pipe", "pipe:1")
	// All shell syntax is fixed; neither the source path nor MIME becomes code.
	wrapper := `ulimit -c 0 && ulimit -t 5 && ulimit -v 786432 && exec ffmpeg "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", wrapper, "thumbnail"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "OMP_NUM_THREADS=1"}
	cmd.WaitDelay = time.Second
	output := limitedBuffer{limit: maxOutput}
	diagnostic := limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Native diagnostics may contain plaintext filenames or metadata.
		return nil, codecFailure(err, diagnostic.String())
	}
	config, err := png.DecodeConfig(bytes.NewReader(output.Bytes()))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxEdge || config.Height > maxEdge {
		return nil, ErrInvalid
	}
	// FFmpeg can propagate EXIF as frame side data despite -map_metadata -1.
	// Re-encode only the already bounded PNG to strip all ancillary input
	// metadata, without exposing the Go decoder to original-size media.
	pixels, err := png.Decode(bytes.NewReader(output.Bytes()))
	if err != nil {
		return nil, ErrInvalid
	}
	clean := limitedBuffer{limit: maxOutput}
	if err := png.Encode(&clean, pixels); err != nil {
		return nil, ErrLimit
	}
	return clean.Bytes(), nil
}

// Return stable categories only; native diagnostics can contain private paths.
func codecFailure(err error, diagnostic string) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && runtime.GOOS == "linux" {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.Signal(24) {
			return ErrLimit // Linux SIGXCPU from the hard CPU budget.
		}
	}
	diagnostic = strings.ToLower(diagnostic)
	if strings.Contains(diagnostic, "invalid nal") || strings.Contains(diagnostic, "partial file") || strings.Contains(diagnostic, "corrupt input packet") {
		return ErrInvalid
	}
	if errors.Is(err, ErrLimit) || strings.Contains(diagnostic, "cannot allocate memory") || strings.Contains(diagnostic, "out of memory") {
		return ErrLimit
	}
	if strings.Contains(diagnostic, "not on whitelist") || strings.Contains(diagnostic, "unknown decoder") || strings.Contains(diagnostic, "decoding requested, but no decoder found") {
		return ErrUnsupported
	}
	return fmt.Errorf("%w: codec failed", ErrInvalid)
}
