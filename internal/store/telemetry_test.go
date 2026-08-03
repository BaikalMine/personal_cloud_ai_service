package store

import (
	"context"
	"errors"
	"testing"
)

func TestBoundedDays(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "default", in: 0, want: 30},
		{name: "negative", in: -1, want: 30},
		{name: "accepted", in: 90, want: 90},
		{name: "maximum", in: 999, want: 366},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedDays(test.in); got != test.want {
				t.Fatalf("boundedDays(%d) = %d, want %d", test.in, got, test.want)
			}
		})
	}
}

func TestBoundedLimit(t *testing.T) {
	if got := boundedLimit(0, 1, 100); got != 1 {
		t.Fatalf("minimum limit = %d, want 1", got)
	}
	if got := boundedLimit(20, 1, 100); got != 20 {
		t.Fatalf("accepted limit = %d, want 20", got)
	}
	if got := boundedLimit(500, 1, 100); got != 100 {
		t.Fatalf("maximum limit = %d, want 100", got)
	}
}

func TestServiceAnalyticsRejectsUnknownService(t *testing.T) {
	store := &Store{}
	_, err := store.ServiceAnalytics(context.Background(), "internal", 30)
	if !errors.Is(err, ErrUnknownService) {
		t.Fatalf("ServiceAnalytics error = %v, want ErrUnknownService", err)
	}
}
