package gateway

import (
	"strings"
	"testing"
)

func TestValidatedLoraCaptionDoesNotTruncateTriggerOrDescription(t *testing.T) {
	caption, err := validatedLoraCaption("wolfgirl", "белые волосы", 100)
	if err != nil || caption != "wolfgirl, белые волосы" {
		t.Fatalf("caption %q: %v", caption, err)
	}
	if _, err := validatedLoraCaption("wolfgirl", strings.Repeat("я", 1000), 1000); err == nil {
		t.Fatal("accepted caption that exceeds limit after adding trigger")
	}
}
