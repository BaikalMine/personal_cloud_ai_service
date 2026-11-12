package store

import (
	"ai-access-gateway/internal/domain"
	"testing"
)

func TestCaptionSeriesIdentityBindsAttemptsAndOwner(t *testing.T) {
	a := domain.LoraCaptionJob{ID: "a", RunVersion: 1}
	b := domain.LoraCaptionJob{ID: "b", RunVersion: 1}
	id := captionSeriesID(1, "dataset", []domain.LoraCaptionJob{a, b})
	if id != captionSeriesID(1, "dataset", []domain.LoraCaptionJob{b, a}) {
		t.Fatal("ordering changed idempotency key")
	}
	for _, other := range []string{captionSeriesID(2, "dataset", []domain.LoraCaptionJob{a, b}), captionSeriesID(1, "other", []domain.LoraCaptionJob{a, b}), captionSeriesID(1, "dataset", []domain.LoraCaptionJob{a})} {
		if id == other {
			t.Fatal("series identity collision")
		}
	}
	b.RunVersion++
	if id == captionSeriesID(1, "dataset", []domain.LoraCaptionJob{a, b}) {
		t.Fatal("retry overwrote old attempt set")
	}
}
