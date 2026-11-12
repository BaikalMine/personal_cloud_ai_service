// Package mediaprobe reads completed-file metadata and optionally verifies fully
// decoded frames. Probe remains stream-only; Decode is the bounded admission
// path. Callers own authorization, retention and concurrency limits.
package mediaprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const MaxInputBytes = 512 << 20

var (
	ErrUnsupported = errors.New("media probe: unsupported format or platform")
	ErrInvalid     = errors.New("media probe: invalid file")
	ErrLimit       = errors.New("media probe: resource limit")
)

type Measurement struct {
	Version           int       `json:"version"`
	Source            string    `json:"source"`
	SHA256            string    `json:"sha256"`
	SizeBytes         int64     `json:"size_bytes"`
	MeasuredAt        time.Time `json:"measured_at"`
	MediaType         string    `json:"media_type"`
	Codec             string    `json:"codec"`
	Width             int       `json:"width"`
	Height            int       `json:"height"`
	SampleAspectRatio string    `json:"sample_aspect_ratio,omitempty"`
	RotationDegrees   *int      `json:"rotation_degrees,omitempty"`
	AverageFPS        string    `json:"average_fps,omitempty"`
	DurationSeconds   string    `json:"duration_seconds,omitempty"`
	EvidenceGaps      []string  `json:"evidence_gaps"`
}

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

func inputFormat(mime string) (format, codecs, kind string) {
	switch mime {
	case "image/png":
		return "png_pipe", "png", "image"
	case "image/jpeg":
		return "jpeg_pipe", "mjpeg", "image"
	case "image/webp":
		return "webp_pipe", "webp", "image"
	case "video/mp4", "video/quicktime":
		return "mov", "h264,hevc,vp9,av1,libdav1d,mjpeg,png,aac,mp3,alac,pcm_s16le", "video"
	case "video/webm":
		return "matroska", "vp8,vp9,av1,libdav1d,opus,vorbis", "video"
	}
	return "", "", ""
}

func rational(value, separator string) string {
	a, b, ok := strings.Cut(value, separator)
	if !ok || len(a) > 10 || len(b) > 10 {
		return ""
	}
	n, e1 := strconv.ParseInt(a, 10, 64)
	d, e2 := strconv.ParseInt(b, 10, 64)
	if e1 != nil || e2 != nil || n <= 0 || d <= 0 || n > 1e9 || d > 1e9 {
		return ""
	}
	x, y := n, d
	for y != 0 {
		x, y = y, x%y
	}
	return strconv.FormatInt(n/x, 10) + separator + strconv.FormatInt(d/x, 10)
}

func parse(data []byte, kind string) (Measurement, error) {
	var raw struct {
		Streams []struct {
			Codec    string `json:"codec_name"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			SAR      string `json:"sample_aspect_ratio"`
			FPS      string `json:"avg_frame_rate"`
			Duration string `json:"duration"`
			SideData []struct {
				Rotation *float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw.Streams) != 1 {
		return Measurement{}, ErrInvalid
	}
	s := raw.Streams[0]
	if s.Width < 1 || s.Height < 1 || s.Width > 32768 || s.Height > 32768 || int64(s.Width)*int64(s.Height) > 100000000 || len(s.Codec) < 1 || len(s.Codec) > 32 {
		return Measurement{}, ErrInvalid
	}
	for _, c := range s.Codec {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return Measurement{}, ErrInvalid
		}
	}
	m := Measurement{Version: 1, Source: "completed_file_stream_metadata", MediaType: kind, Codec: s.Codec, Width: s.Width, Height: s.Height,
		SampleAspectRatio: rational(s.SAR, ":"), EvidenceGaps: []string{"full_decode_not_performed", "display_geometry_not_measured"}}
	for _, side := range s.SideData {
		if side.Rotation == nil {
			continue
		}
		v := *side.Rotation
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < -360 || v > 360 || m.RotationDegrees != nil {
			return Measurement{}, ErrInvalid
		}
		r := int(v)
		m.RotationDegrees = &r
	}
	if kind == "video" {
		m.AverageFPS = rational(s.FPS, "/")
		if d, err := strconv.ParseFloat(s.Duration, 64); err == nil && !math.IsNaN(d) && !math.IsInf(d, 0) && d > 0 && d <= 86400 {
			m.DurationSeconds = strconv.FormatFloat(d, 'f', -1, 64)
		}
		m.EvidenceGaps = append(m.EvidenceGaps, "constant_frame_rate_not_verified")
		if m.AverageFPS == "" {
			m.EvidenceGaps = append(m.EvidenceGaps, "average_fps_unavailable")
		}
		if m.DurationSeconds == "" {
			m.EvidenceGaps = append(m.EvidenceGaps, "stream_duration_unavailable")
		}
	}
	return m, nil
}

// Probe accepts only a private, materialized regular file. Native parsing uses
// fixed demuxers, no network protocols, disabled MOV references and Linux limits.
// Dimensions are coded pixel dimensions; SAR/rotation are not silently applied.
func Probe(ctx context.Context, path, mime string) (Measurement, error) {
	format, codecs, kind := inputFormat(mime)
	if format == "" || runtime.GOOS != "linux" {
		return Measurement{}, ErrUnsupported
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Measurement{}, ErrInvalid
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return Measurement{}, ErrInvalid
	}
	if before.Size() > MaxInputBytes {
		return Measurement{}, ErrLimit
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-max_alloc", "134217728", "-threads", "1", "-max_pixels", "100000000",
		"-protocol_whitelist", "file", "-format_whitelist", format, "-codec_whitelist", codecs,
		"-probesize", "1048576", "-analyzeduration", "1000000", "-f", format}
	if format == "mov" {
		args = append(args, "-enable_drefs", "0", "-use_absolute_path", "0")
	}
	args = append(args, "-i", path, "-select_streams", "V:0", "-show_entries",
		"stream=codec_name,width,height,sample_aspect_ratio,avg_frame_rate,duration:stream_side_data=rotation", "-of", "json")
	wrapper := `ulimit -c 0 && ulimit -t 5 && ulimit -v 786432 && exec ffprobe "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", wrapper, "media-probe"}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "OMP_NUM_THREADS=1"}
	cmd.WaitDelay = time.Second
	output, diagnostic := limitedBuffer{limit: 16384}, limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Measurement{}, ctx.Err()
		}
		return Measurement{}, ErrInvalid
	}
	m, err := parse(output.Bytes(), kind)
	if err != nil {
		return Measurement{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Measurement{}, ErrInvalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return Measurement{}, ErrInvalid
	}
	digest := sha256.New()
	buf := make([]byte, 128<<10)
	defer clear(buf)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return Measurement{}, err
		}
		n, err := f.Read(buf)
		total += int64(n)
		if total > before.Size() {
			return Measurement{}, ErrInvalid
		}
		_, _ = digest.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Measurement{}, ErrInvalid
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || total != before.Size() {
		return Measurement{}, ErrInvalid
	}
	m.SHA256, m.SizeBytes, m.MeasuredAt = hex.EncodeToString(digest.Sum(nil)), total, time.Now().UTC()
	return m, nil
}
