package gateway

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/updates"
)

func TestComfyOutputCleanupResultsRequireExactPerFileEvidence(t *testing.T) {
	a := domain.ComfyOutputCleanupTombstone{ID: 1, Filename: "a.png", Subfolder: "owner", StorageType: "output", SizeBytes: 10, ContentHash: strings.Repeat("a", 64)}
	b := a
	b.ID, b.Filename, b.SizeBytes = 2, "b.png", 20
	ack := func(item domain.ComfyOutputCleanupTombstone, status string) updates.ComfyAssetDeleteOutcome {
		return updates.ComfyAssetDeleteOutcome{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: item.ContentHash, Status: status}
	}
	wrong := a
	wrong.SizeBytes++
	for _, tc := range []struct {
		name   string
		result updates.ComfyAssetDeleteResult
		err    error
		want   []string
	}{
		{"exact mixed", updates.ComfyAssetDeleteResult{Items: []updates.ComfyAssetDeleteOutcome{ack(b, "missing"), ack(a, "deleted")}}, nil, []string{"deleted", "missing"}},
		{"legacy aggregate", updates.ComfyAssetDeleteResult{Deleted: 1, Missing: 1}, nil, []string{"legacy_confirmed", "legacy_confirmed"}},
		{"invalid aggregate", updates.ComfyAssetDeleteResult{Deleted: -1, Missing: 3}, nil, []string{"unconfirmed", "unconfirmed"}},
		{"legacy error", updates.ComfyAssetDeleteResult{Deleted: 2}, errors.New("HTTP error"), []string{"unconfirmed", "unconfirmed"}},
		{"unavailable", updates.ComfyAssetDeleteResult{}, updates.ErrUnavailable, []string{"host_unavailable", "host_unavailable"}},
		{"partial failure", updates.ComfyAssetDeleteResult{Items: []updates.ComfyAssetDeleteOutcome{ack(a, "deleted")}}, errors.New("HTTP 500"), []string{"deleted", "unconfirmed"}},
		{"mismatch rejected", updates.ComfyAssetDeleteResult{Items: []updates.ComfyAssetDeleteOutcome{ack(a, "mismatched"), ack(b, "rejected")}}, nil, []string{"mismatched", "rejected"}},
		{"wrong identity", updates.ComfyAssetDeleteResult{Deleted: 2, Items: []updates.ComfyAssetDeleteOutcome{ack(wrong, "deleted")}}, nil, []string{"unconfirmed", "unconfirmed"}},
		{"dry run is not deletion", updates.ComfyAssetDeleteResult{Items: []updates.ComfyAssetDeleteOutcome{ack(a, "matched")}}, nil, []string{"unconfirmed", "unconfirmed"}},
		{"conflicting duplicates", updates.ComfyAssetDeleteResult{Items: []updates.ComfyAssetDeleteOutcome{ack(a, "deleted"), ack(a, "missing"), ack(a, "deleted")}}, nil, []string{"unconfirmed", "unconfirmed"}},
		{"identical duplicates", updates.ComfyAssetDeleteResult{Items: []updates.ComfyAssetDeleteOutcome{ack(a, "deleted"), ack(a, "deleted")}}, nil, []string{"deleted", "unconfirmed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := comfyOutputCleanupResults([]domain.ComfyOutputCleanupTombstone{a, b}, tc.result, tc.err)
			var outcomes []string
			for i, result := range results {
				if result.ComfyOutputCleanupTombstone != []domain.ComfyOutputCleanupTombstone{a, b}[i] {
					t.Fatal("source identity changed", result)
				}
				outcomes = append(outcomes, result.Outcome)
			}
			if !reflect.DeepEqual(outcomes, tc.want) {
				t.Fatal(outcomes, tc.want)
			}
		})
	}
}

func TestComfyCleanupOutcomesKeepMismatchesForRetry(t *testing.T) {
	deleted := updates.ComfyAssetFile{Filename: "deleted.png", Subfolder: "a", StorageType: "output", SizeBytes: 10, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	mismatched := updates.ComfyAssetFile{Filename: "mismatch.png", Subfolder: "b", StorageType: "output", SizeBytes: 20, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	result := updates.ComfyAssetDeleteResult{Deleted: 1, Mismatched: 1, Items: []updates.ComfyAssetDeleteOutcome{
		{Filename: deleted.Filename, Subfolder: deleted.Subfolder, StorageType: deleted.StorageType, SizeBytes: deleted.SizeBytes, SHA256: deleted.SHA256, Status: "deleted"},
		{Filename: mismatched.Filename, Subfolder: mismatched.Subfolder, StorageType: mismatched.StorageType, SizeBytes: mismatched.SizeBytes, SHA256: mismatched.SHA256, Status: "mismatched"},
	}}
	outcomes := comfyCleanupOutcomes([]updates.ComfyAssetFile{deleted, mismatched}, result, nil)
	if !reflect.DeepEqual(outcomes, []string{"deleted", "mismatched"}) {
		t.Fatal(outcomes)
	}
}

func TestComfyCleanupOutcomesSupportsLegacyAggregateResponse(t *testing.T) {
	result := updates.ComfyAssetDeleteResult{Deleted: 1, Missing: 1}
	outcomes := comfyCleanupOutcomes(make([]updates.ComfyAssetFile, 2), result, nil)
	if !reflect.DeepEqual(outcomes, []string{"legacy_confirmed", "legacy_confirmed"}) {
		t.Fatal(outcomes)
	}
}
