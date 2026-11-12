package mediaprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
)

type decodeContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r decodeContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(data)
}

// Native decoders can accept a playable prefix or ignore animation chunks when
// forced to a static image demuxer. Validate framing without decoding metadata.
func checkDecodeContainer(ctx context.Context, path, format string, size int64) error {
	if format != "png_pipe" && format != "webp_pipe" && format != "mov" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return ErrInvalid
	}
	defer f.Close()
	r := decodeContextReader{ctx: ctx, r: f}
	switch format {
	case "png_pipe":
		return checkDecodePNG(r, size)
	case "webp_pipe":
		return checkDecodeWebP(r, size)
	default:
		return checkDecodeMOV(r, size)
	}
}

func checkDecodePNG(r io.Reader, size int64) error {
	var signature [8]byte
	if _, err := io.ReadFull(r, signature[:]); err != nil || !bytes.Equal(signature[:], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return ErrInvalid
	}
	remaining := size - 8
	for chunks := 0; remaining > 0; chunks++ {
		if chunks >= 10000 {
			return ErrLimit
		}
		var header [8]byte
		if remaining < 12 {
			return ErrInvalid
		}
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return err
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		if length > remaining-12 {
			return ErrInvalid
		}
		kind := string(header[4:])
		if chunks == 0 && (kind != "IHDR" || length != 13) {
			return ErrInvalid
		}
		if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
			return ErrUnsupported
		}
		digest := crc32.NewIEEE()
		_, _ = digest.Write(header[4:])
		if _, err := io.CopyN(digest, r, length); err != nil {
			return err
		}
		var checksum uint32
		if err := binary.Read(r, binary.BigEndian, &checksum); err != nil {
			return err
		}
		if checksum != digest.Sum32() {
			return ErrInvalid
		}
		remaining -= length + 12
		if kind == "IEND" {
			if length != 0 || remaining != 0 {
				return ErrInvalid
			}
			return nil
		}
	}
	return ErrInvalid
}

func checkDecodeWebP(r io.Reader, size int64) error {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WEBP" || int64(binary.LittleEndian.Uint32(header[4:8]))+8 != size {
		return ErrInvalid
	}
	remaining := size - 12
	for chunks := 0; remaining > 0; chunks++ {
		if chunks >= 10000 {
			return ErrLimit
		}
		var chunk [8]byte
		if remaining < 8 {
			return ErrInvalid
		}
		if _, err := io.ReadFull(r, chunk[:]); err != nil {
			return err
		}
		length := int64(binary.LittleEndian.Uint32(chunk[4:]))
		padded := length + length%2
		if padded > remaining-8 {
			return ErrInvalid
		}
		kind := string(chunk[:4])
		if kind == "ANIM" || kind == "ANMF" {
			return ErrUnsupported
		}
		if kind == "VP8X" {
			if length != 10 {
				return ErrInvalid
			}
			var flags [1]byte
			if _, err := io.ReadFull(r, flags[:]); err != nil {
				return err
			}
			if flags[0]&2 != 0 {
				return ErrUnsupported
			}
			if _, err := io.CopyN(io.Discard, r, padded-1); err != nil {
				return err
			}
		} else if _, err := io.CopyN(io.Discard, r, padded); err != nil {
			return err
		}
		remaining -= padded + 8
	}
	return nil
}

func checkDecodeMOV(r io.Reader, size int64) error {
	remaining := size
	for boxes := 0; remaining > 0; boxes++ {
		if boxes >= 10000 {
			return ErrLimit
		}
		var header [8]byte
		if remaining < 8 {
			return ErrInvalid
		}
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return err
		}
		length, headerLength := uint64(binary.BigEndian.Uint32(header[:4])), uint64(8)
		if length == 1 {
			if remaining < 16 {
				return ErrInvalid
			}
			if err := binary.Read(r, binary.BigEndian, &length); err != nil {
				return err
			}
			headerLength = 16
		} else if length == 0 {
			length = uint64(remaining)
		}
		if length < headerLength || length > uint64(remaining) {
			return ErrInvalid
		}
		if _, err := io.CopyN(io.Discard, r, int64(length-headerLength)); err != nil {
			return err
		}
		remaining -= int64(length)
	}
	return nil
}
