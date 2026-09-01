package gateway

import (
	"io"
	"testing"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/store"
)

type zeroMediaReader struct{}

func (zeroMediaReader) Read(payload []byte) (int, error) {
	clear(payload)
	return len(payload), nil
}

func BenchmarkEncryptMediaChunks(b *testing.B) {
	cipher, err := contentcrypto.NewCipher("01234567890123456789012345678901")
	if err != nil {
		b.Fatal(err)
	}
	for _, sizeBytes := range []int64{8 << 20, 32 << 20, 64 << 20} {
		b.Run(mediaBenchmarkLabel(sizeBytes), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(sizeBytes)
			for range b.N {
				reader := io.LimitReader(zeroMediaReader{}, sizeBytes)
				plain := make([]byte, store.ContentMediaChunkSize)
				var encrypted []byte
				remaining := sizeBytes
				for chunkIndex := 0; remaining > 0; chunkIndex++ {
					chunkSize := min(remaining, int64(len(plain)))
					chunk := plain[:int(chunkSize)]
					if _, err := io.ReadFull(reader, chunk); err != nil {
						b.Fatal(err)
					}
					encrypted, err = cipher.EncryptBytesWithAADInto(encrypted[:0], chunk, contentMediaChunkAAD(1, chunkIndex))
					if err != nil {
						b.Fatal(err)
					}
					clear(encrypted)
					remaining -= chunkSize
				}
				clear(plain)
			}
		})
	}
}

func BenchmarkSpoolGenerationOutputArchive(b *testing.B) {
	for _, sizeBytes := range []int64{8 << 20, 32 << 20, 64 << 20} {
		b.Run(mediaBenchmarkLabel(sizeBytes), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(sizeBytes)
			directory := b.TempDir()
			for range b.N {
				reader := io.LimitReader(zeroMediaReader{}, sizeBytes)
				file, spoolPath, actualSize, _, err := spoolGenerationOutputArchive(reader, directory, sizeBytes, sizeBytes)
				if err != nil {
					b.Fatal(err)
				}
				if file == nil || spoolPath == "" || actualSize != sizeBytes {
					b.Fatalf("spool file=%v path=%q size=%d", file, spoolPath, actualSize)
				}
				archive := generationOutputArchive{File: file, path: spoolPath}
				if err := archive.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func mediaBenchmarkLabel(sizeBytes int64) string {
	switch sizeBytes {
	case 8 << 20:
		return "8MiB"
	case 32 << 20:
		return "32MiB"
	case 64 << 20:
		return "64MiB"
	default:
		return "custom"
	}
}
