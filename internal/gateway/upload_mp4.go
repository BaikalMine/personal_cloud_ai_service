package gateway

import (
	"encoding/binary"
)

// Only these explicitly safe validation messages may be returned by the proxy.
type uploadValidationError struct{ message string }

func (e *uploadValidationError) Error() string { return e.message }

// Inspect top-level ISO BMFF boxes while streaming; never buffer video payloads.
type uploadMP4Check struct {
	header    [16]byte
	n         int
	remaining uint64
	toEOF     bool
	moov      bool
	boxMoov   bool
	invalid   bool
}

func (v *uploadMP4Check) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 && !v.invalid && !v.toEOF {
		if v.remaining > 0 {
			count := uint64(len(p))
			if count > v.remaining {
				count = v.remaining
			}
			p = p[int(count):]
			v.remaining -= count
			if v.remaining == 0 && v.boxMoov {
				v.moov = true
			}
			continue
		}
		need := 8
		if v.n >= 8 && binary.BigEndian.Uint32(v.header[:4]) == 1 {
			need = 16
		}
		count := need - v.n
		if count > len(p) {
			count = len(p)
		}
		copy(v.header[v.n:], p[:count])
		v.n += count
		p = p[count:]
		if v.n < need {
			continue
		}
		size := uint64(binary.BigEndian.Uint32(v.header[:4]))
		if size == 1 {
			if v.n < 16 {
				continue
			}
			size = binary.BigEndian.Uint64(v.header[8:])
		}
		v.boxMoov = string(v.header[4:8]) == "moov"
		if size == 0 {
			v.toEOF = true
			// A size-zero moov is permitted to extend to the end of the file.
			v.moov = v.moov || v.boxMoov
		} else if size < uint64(need) {
			v.invalid = true
		} else {
			v.remaining = size - uint64(need)
			if v.remaining == 0 && v.boxMoov {
				v.moov = true
			}
		}
		v.n = 0
	}
	return written, nil
}

func (v *uploadMP4Check) validate() error {
	if v.invalid || v.n != 0 || v.remaining != 0 || !v.moov {
		return &uploadValidationError{message: "Видео обрезано или повреждено: структура MP4/MOV неполная. Выберите полностью скачанный оригинал. Этот файл не сохранён."}
	}
	return nil
}
