package gateway

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/jpeg"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	visionReferenceMaxSide      = 1536
	visionReferencePassthrough  = 2 << 20
	visionReferenceJPEGQuality  = 84
	visionReferenceDecodePixels = 36_000_000
)

func prepareVisionReference(payload []byte, mimeType string) ([]byte, string, bool, error) {
	if len(payload) == 0 {
		return nil, "", false, errors.New("vision reference is empty")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, "", false, errors.New("vision reference format is unsupported")
	}
	pixels := int64(config.Width) * int64(config.Height)
	if pixels <= 0 || pixels > visionReferenceDecodePixels {
		return nil, "", false, errors.New("vision reference dimensions are too large")
	}
	normalizedMIME := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if max(config.Width, config.Height) <= visionReferenceMaxSide && len(payload) <= visionReferencePassthrough {
		switch normalizedMIME {
		case "image/jpeg", "image/png", "image/webp":
			return payload, normalizedMIME, false, nil
		}
	}

	source, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		return nil, "", false, errors.New("decode vision reference")
	}
	width, height := config.Width, config.Height
	if longest := max(width, height); longest > visionReferenceMaxSide {
		scale := float64(visionReferenceMaxSide) / float64(longest)
		width = max(1, int(float64(width)*scale+0.5))
		height = max(1, int(float64(height)*scale+0.5))
	}
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	stddraw.Draw(destination, destination.Bounds(), &image.Uniform{C: color.White}, image.Point{}, stddraw.Src)
	xdraw.ApproxBiLinear.Scale(destination, destination.Bounds(), source, source.Bounds(), stddraw.Over, nil)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, destination, &jpeg.Options{Quality: visionReferenceJPEGQuality}); err != nil {
		return nil, "", false, errors.New("encode vision reference")
	}
	return encoded.Bytes(), "image/jpeg", true, nil
}
