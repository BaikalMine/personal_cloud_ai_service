package mediaprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	FullDecodeSource  = "completed_file_full_frame_and_pixel_decode"
	FullDecodeVersion = 1
	MaxDecodeFrames   = 450
	MaxDecodePixels   = int64(200000000)
	decodeTimeout     = 45 * time.Second
)

// DecodedMeasurement describes admitted, fully decoded source video frames or
// a single image. It is separate from Probe's inexpensive stream-only evidence.
type DecodedMeasurement struct {
	Measurement
	DecodeVersion     int   `json:"decode_version"`
	Frames            int   `json:"frames"`
	FPSNumerator      int64 `json:"fps_numerator,omitempty"`
	FPSDenominator    int64 `json:"fps_denominator,omitempty"`
	DecodedPixels     int64 `json:"decoded_pixels"`
	Opaque            bool  `json:"opaque"`
	NormalOrientation bool  `json:"normal_orientation"`
	SquarePixels      bool  `json:"square_pixels"`
	UniformCadence    bool  `json:"uniform_cadence"`
}

type decodedSideData struct {
	Rotation *float64 `json:"rotation"`
	Matrix   string   `json:"displaymatrix"`
}

type decodedFrame struct {
	PTS         json.Number       `json:"best_effort_timestamp"`
	Duration    json.Number       `json:"duration"`
	OldDuration json.Number       `json:"pkt_duration"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	SAR         string            `json:"sample_aspect_ratio"`
	Tags        map[string]string `json:"tags"`
	SideData    []decodedSideData `json:"side_data_list"`
}

func normalDecodeMetadata(sar string, tags map[string]string, sideData []decodedSideData) bool {
	// Unspecified SAR is the decoder's default square-pixel display geometry.
	if sar != "" && sar != "N/A" && rational(sar, ":") != "1:1" {
		return false
	}
	for key, value := range tags {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "orientation":
			if value != "1" {
				return false
			}
		case "rotate":
			if value != "0" {
				return false
			}
		case "alpha_mode":
			// Some native VP9 decoders omit alpha even when the container has it.
			if value != "0" {
				return false
			}
		}
	}
	for _, side := range sideData {
		if side.Rotation != nil && (*side.Rotation != 0 || math.IsNaN(*side.Rotation)) {
			return false
		}
		if side.Matrix == "" {
			continue
		}
		var values []int64
		for _, line := range strings.Split(strings.TrimSpace(side.Matrix), "\n") {
			_, row, ok := strings.Cut(line, ":")
			if !ok {
				return false
			}
			fields := strings.Fields(row)
			if len(fields) != 3 {
				return false
			}
			for _, field := range fields {
				value, err := strconv.ParseInt(field, 10, 64)
				if err != nil {
					return false
				}
				values = append(values, value)
			}
		}
		identity := []int64{65536, 0, 0, 0, 65536, 0, 0, 0, 1073741824}
		if len(values) != len(identity) {
			return false
		}
		for i := range values {
			if values[i] != identity[i] {
				return false
			}
		}
	}
	return true
}

func parseDecodedFrames(data []byte, source Measurement) (DecodedMeasurement, error) {
	if source.Width < 64 || source.Height < 64 || source.Width > 4096 || source.Height > 4096 || int64(source.Width)*int64(source.Height) > 16000000 {
		return DecodedMeasurement{}, ErrLimit
	}
	var raw struct {
		Frames  []decodedFrame `json:"frames"`
		Streams []struct {
			TimeBase string            `json:"time_base"`
			SAR      string            `json:"sample_aspect_ratio"`
			Tags     map[string]string `json:"tags"`
			SideData []decodedSideData `json:"side_data_list"`
		} `json:"streams"`
	}
	if json.Unmarshal(data, &raw) != nil || len(raw.Streams) != 1 || len(raw.Frames) == 0 {
		return DecodedMeasurement{}, ErrInvalid
	}
	if len(raw.Frames) > MaxDecodeFrames || int64(len(raw.Frames))*int64(source.Width)*int64(source.Height) > MaxDecodePixels {
		return DecodedMeasurement{}, ErrLimit
	}
	stream := raw.Streams[0]
	if !normalDecodeMetadata(stream.SAR, stream.Tags, stream.SideData) {
		return DecodedMeasurement{}, ErrUnsupported
	}
	result := DecodedMeasurement{Measurement: source, DecodeVersion: FullDecodeVersion, Frames: len(raw.Frames), NormalOrientation: true, SquarePixels: true, DecodedPixels: int64(len(raw.Frames)) * int64(source.Width) * int64(source.Height)}
	for _, frame := range raw.Frames {
		if frame.Width != source.Width || frame.Height != source.Height {
			return DecodedMeasurement{}, ErrUnsupported
		}
		if !normalDecodeMetadata(frame.SAR, frame.Tags, frame.SideData) {
			return DecodedMeasurement{}, ErrUnsupported
		}
	}
	if source.MediaType == "image" {
		if len(raw.Frames) != 1 {
			return DecodedMeasurement{}, ErrUnsupported
		}
		return result, nil
	}
	if source.MediaType != "video" || len(raw.Frames) < 2 {
		return DecodedMeasurement{}, ErrUnsupported
	}
	timeBase := rational(stream.TimeBase, "/")
	n, d, ok := strings.Cut(timeBase, "/")
	if !ok {
		return DecodedMeasurement{}, ErrUnsupported
	}
	baseNum, _ := strconv.ParseInt(n, 10, 64)
	baseDen, _ := strconv.ParseInt(d, 10, 64)
	var previous, period int64
	for index, frame := range raw.Frames {
		pts, err := frame.PTS.Int64()
		if err != nil || pts < 0 || pts > 9000000000000000 || (index == 0 && pts != 0) {
			return DecodedMeasurement{}, ErrUnsupported
		}
		if index > 0 {
			delta := pts - previous
			if delta < 1 || delta > 1000000000 || (index > 1 && delta != period) {
				return DecodedMeasurement{}, ErrUnsupported
			}
			period = delta
		}
		previous = pts
	}
	for _, frame := range raw.Frames {
		duration := frame.Duration
		if duration == "" {
			duration = frame.OldDuration
		}
		value, err := duration.Int64()
		if err != nil || value != period {
			return DecodedMeasurement{}, ErrUnsupported
		}
	}
	// Bounded factors are at most 1e9 each; simplify before exposing the ratio.
	result.FPSNumerator, result.FPSDenominator = baseDen, baseNum*period
	a, b := result.FPSNumerator, result.FPSDenominator
	for b != 0 {
		a, b = b, a%b
	}
	result.FPSNumerator /= a
	result.FPSDenominator /= a
	fps := float64(result.FPSNumerator) / float64(result.FPSDenominator)
	if fps < 1 || fps > 60 || float64(result.Frames)/fps > 30 {
		return DecodedMeasurement{}, ErrLimit
	}
	actualFPS := fmt.Sprintf("%d/%d", result.FPSNumerator, result.FPSDenominator)
	if source.AverageFPS != "" && source.AverageFPS != actualFPS {
		return DecodedMeasurement{}, ErrUnsupported
	}
	result.UniformCadence = true
	result.AverageFPS = actualFPS
	return result, nil
}

type rgbaDecodeProof struct {
	count   int64
	limit   int64
	failure error
	cancel  context.CancelFunc
}

func (p *rgbaDecodeProof) Write(data []byte) (int, error) {
	fail := func(err error) (int, error) {
		p.failure = err
		if p.cancel != nil {
			p.cancel()
		}
		return 0, err
	}
	if p.failure != nil {
		return 0, p.failure
	}
	if int64(len(data)) > p.limit-p.count {
		return fail(ErrLimit)
	}
	// FFmpeg emits RGBA64LE. Inspect both alpha bytes without retaining pixels,
	// accounting for arbitrary pipe-write boundaries within a pixel.
	index := 0
	for index < len(data) && (p.count+int64(index))%8 != 0 {
		if (p.count+int64(index))%8 >= 6 && data[index] != 255 {
			return fail(ErrUnsupported)
		}
		index++
	}
	for index+7 < len(data) {
		if data[index+6] != 255 || data[index+7] != 255 {
			return fail(ErrUnsupported)
		}
		index += 8
	}
	for ; index < len(data); index++ {
		if (p.count+int64(index))%8 >= 6 && data[index] != 255 {
			return fail(ErrUnsupported)
		}
	}
	p.count += int64(len(data))
	return len(data), nil
}

func decodeCommand(ctx context.Context, executable string, args []string) *exec.Cmd {
	// Fixed shell syntax and positional arguments only. No path becomes code.
	wrapper := `ulimit -c 0 && ulimit -t 20 && ulimit -v 1048576 && exec "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", wrapper, "media-decode", executable}, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/nonexistent", "OMP_NUM_THREADS=1"}
	cmd.WaitDelay = time.Second
	return cmd
}

func decodeInputArgs(format, codecs string) []string {
	args := []string{"-v", "error", "-max_alloc", "134217728", "-threads", "1", "-max_pixels", "16000000", "-err_detect", "explode",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", format, "-codec_whitelist", codecs, "-probesize", "1048576", "-analyzeduration", "1000000", "-f", format}
	if format == "mov" {
		args = append(args, "-enable_drefs", "0", "-use_absolute_path", "0")
	}
	return args
}

// Decode validates a private immutable source through bounded CPU-only complete
// frame and pixel decoding. Callers still own authorization, retention, private
// materialization and concurrency admission. It never rewrites the source.
func Decode(ctx context.Context, path, mime string) (DecodedMeasurement, error) {
	format, codecs, _ := inputFormat(mime)
	if runtime.GOOS != "linux" || format == "" {
		return DecodedMeasurement{}, ErrUnsupported
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return DecodedMeasurement{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, decodeTimeout)
	defer cancel()
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return DecodedMeasurement{}, ErrInvalid
	}
	if before.Size() > MaxInputBytes {
		return DecodedMeasurement{}, ErrLimit
	}
	if err := checkDecodeContainer(ctx, path, format, before.Size()); err != nil {
		if ctx.Err() != nil {
			return DecodedMeasurement{}, ctx.Err()
		}
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrLimit) {
			return DecodedMeasurement{}, err
		}
		return DecodedMeasurement{}, ErrInvalid
	}
	source, err := Probe(ctx, path, mime)
	if err != nil {
		return DecodedMeasurement{}, err
	}
	if source.Width < 64 || source.Height < 64 || source.Width > 4096 || source.Height > 4096 || int64(source.Width)*int64(source.Height) > 16000000 {
		return DecodedMeasurement{}, ErrLimit
	}
	if source.RotationDegrees != nil && *source.RotationDegrees != 0 {
		return DecodedMeasurement{}, ErrUnsupported
	}
	if !normalDecodeMetadata(source.SampleAspectRatio, nil, nil) {
		return DecodedMeasurement{}, ErrUnsupported
	}
	if before.Size() != source.SizeBytes {
		return DecodedMeasurement{}, ErrInvalid
	}
	args := append(decodeInputArgs(format, codecs), "-i", path, "-select_streams", "V:0", "-show_frames", "-show_entries",
		"frame=best_effort_timestamp,duration,pkt_duration,width,height,sample_aspect_ratio:frame_tags=Orientation,orientation,rotate,alpha_mode:frame_side_data=rotation,displaymatrix:stream=time_base,sample_aspect_ratio:stream_tags=Orientation,orientation,rotate,alpha_mode:stream_side_data=rotation,displaymatrix", "-of", "json")
	cmd := decodeCommand(ctx, "ffprobe", args)
	output, diagnostic := limitedBuffer{limit: 1 << 20}, limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil || diagnostic.Len() != 0 {
		if ctx.Err() != nil {
			return DecodedMeasurement{}, ctx.Err()
		}
		if errors.Is(err, ErrLimit) {
			return DecodedMeasurement{}, ErrLimit
		}
		return DecodedMeasurement{}, ErrInvalid
	}
	result, err := parseDecodedFrames(output.Bytes(), source)
	if err != nil {
		return DecodedMeasurement{}, err
	}
	pixelCtx, pixelCancel := context.WithCancel(ctx)
	defer pixelCancel()
	args = append([]string{"-nostdin", "-hide_banner", "-xerror", "-filter_threads", "1", "-filter_complex_threads", "1", "-hwaccel", "none", "-noautorotate"}, decodeInputArgs(format, codecs)...)
	args = append(args, "-i", path, "-map", "0:V:0", "-an", "-sn", "-dn", "-fps_mode", "passthrough", "-map_metadata", "-1", "-map_chapters", "-1", "-threads", "1", "-c:v", "rawvideo", "-pix_fmt", "rgba64le", "-f", "rawvideo", "pipe:1")
	cmd = decodeCommand(pixelCtx, "ffmpeg", args)
	proof := rgbaDecodeProof{limit: result.DecodedPixels * 8, cancel: pixelCancel}
	diagnostic = limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = &proof, &diagnostic
	if err := cmd.Run(); err != nil || diagnostic.Len() != 0 || proof.count != proof.limit {
		if ctx.Err() != nil {
			return DecodedMeasurement{}, ctx.Err()
		}
		if proof.failure != nil {
			return DecodedMeasurement{}, proof.failure
		}
		return DecodedMeasurement{}, ErrInvalid
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return DecodedMeasurement{}, ErrInvalid
	}
	verified, err := Probe(ctx, path, mime)
	if err != nil {
		return DecodedMeasurement{}, err
	}
	if verified.SHA256 != source.SHA256 || verified.SizeBytes != source.SizeBytes {
		return DecodedMeasurement{}, ErrInvalid
	}
	result.Opaque = true
	result.Source = FullDecodeSource
	result.MeasuredAt = time.Now().UTC()
	result.EvidenceGaps = []string{"audio_not_decoded", "comfy_runtime_input_not_verified"}
	return result, nil
}
