package gateway

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

func TestLoraDatasetQualityVisualCues(t *testing.T) {
	flat := image.NewRGBA(image.Rect(0, 0, 512, 512))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.RGBA{R: 80, G: 140, B: 200, A: 255}), image.Point{}, draw.Src)
	q := MeasureLoraDatasetQuality(flat)
	if q.Version != 1 || !q.LowDetail || q.DetailScore != 0 || q.Colors[0] != 80 || q.Colors[1] != 140 || q.Colors[2] != 200 {
		t.Fatalf("flat surface metrics: %+v", q)
	}
	sharp := image.NewRGBA(image.Rect(10, 20, 522, 532))
	soft := image.NewRGBA(sharp.Bounds())
	for y := 20; y < 532; y++ {
		for x := 10; x < 522; x++ {
			v := uint8(0)
			if ((x-10)/24+(y-20)/24)%2 == 0 {
				v = 255
			}
			sharp.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
			v = uint8(120 + 30*math.Sin(float64(x-10)/100) + 30*math.Sin(float64(y-20)/100))
			soft.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	a, b := MeasureLoraDatasetQuality(sharp), MeasureLoraDatasetQuality(soft)
	if a.LowDetail || !b.LowDetail || a.DetailScore < b.DetailScore*100 {
		t.Fatalf("sharp versus smooth metrics: %v / %v", a.DetailScore, b.DetailScore)
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	q = MeasureLoraDatasetQuality(transparent)
	for _, value := range q.Colors {
		if value != 255 {
			t.Fatal("transparent pixels not composited on white")
		}
	}
}

func TestLoraDatasetQualityExtremeAspect(t *testing.T) {
	for _, size := range []image.Point{{256, 16384}, {16384, 256}} {
		q := MeasureLoraDatasetQuality(image.NewRGBA(image.Rect(0, 0, size.X, size.Y)))
		if math.IsNaN(q.DetailScore) || math.IsInf(q.DetailScore, 0) {
			t.Fatal("invalid metric for thin image")
		}
	}
}
