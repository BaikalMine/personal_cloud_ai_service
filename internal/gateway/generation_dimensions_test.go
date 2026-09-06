package gateway

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLaunchSummaryDimensionsMatchWorkflow(t *testing.T) {
	body, err := os.ReadFile("testdata/generation_dimensions.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Kind, Aspect                   string
		Megapixels, BaseMP                   float64
		Multiple, MaxLongest                 int
		SourceWidth, SourceHeight, Quality   int
		Width, Height, BaseWidth, BaseHeight int
	}
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var width, height int
			var err error
			if fixture.Kind == "video" {
				width, height, err = miniMaxH3VideoDimensions(fixture.SourceWidth, fixture.SourceHeight, fixture.Quality)
			} else {
				width, height, err = generationDimensions(fixture.Aspect, fixture.Megapixels, fixture.Multiple, fixture.MaxLongest)
			}
			if err != nil || width != fixture.Width || height != fixture.Height {
				t.Fatalf("got %dx%d (%v), expected %dx%d", width, height, err, fixture.Width, fixture.Height)
			}
			if fixture.Kind == "image" {
				bw, bh := baseGenerationDimensions(width, height, fixture.BaseMP)
				if bw != fixture.BaseWidth || bh != fixture.BaseHeight {
					t.Fatalf("base %dx%d, expected %dx%d", bw, bh, fixture.BaseWidth, fixture.BaseHeight)
				}
			}
		})
	}
}
