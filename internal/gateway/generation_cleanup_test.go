package gateway

import (
	"testing"

	"ai-access-gateway/internal/updates"
)

func TestConfirmedComfyCleanupIDsKeepMismatchesForRetry(t *testing.T) {
	deleted := updates.ComfyAssetFile{Filename: "deleted.png", Subfolder: "a", StorageType: "output", SizeBytes: 10, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	mismatched := updates.ComfyAssetFile{Filename: "mismatch.png", Subfolder: "b", StorageType: "output", SizeBytes: 20, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	idsByKey := map[string][]int64{
		comfyAssetCleanupKey(deleted.Filename, deleted.Subfolder, deleted.StorageType, deleted.SizeBytes, deleted.SHA256):                {1},
		comfyAssetCleanupKey(mismatched.Filename, mismatched.Subfolder, mismatched.StorageType, mismatched.SizeBytes, mismatched.SHA256): {2},
	}
	result := updates.ComfyAssetDeleteResult{Deleted: 1, Mismatched: 1, Items: []updates.ComfyAssetDeleteOutcome{
		{Filename: deleted.Filename, Subfolder: deleted.Subfolder, StorageType: deleted.StorageType, SizeBytes: deleted.SizeBytes, SHA256: deleted.SHA256, Status: "deleted"},
		{Filename: mismatched.Filename, Subfolder: mismatched.Subfolder, StorageType: mismatched.StorageType, SizeBytes: mismatched.SizeBytes, SHA256: mismatched.SHA256, Status: "mismatched"},
	}}
	confirmed, deferred := confirmedComfyCleanupIDs(result, idsByKey, []int64{1, 2}, 2)
	if len(confirmed) != 1 || confirmed[0] != 1 || len(deferred) != 1 || deferred[0] != 2 {
		t.Fatalf("confirmed=%v deferred=%v", confirmed, deferred)
	}
}

func TestConfirmedComfyCleanupIDsSupportsLegacyAggregateResponse(t *testing.T) {
	result := updates.ComfyAssetDeleteResult{Deleted: 1, Missing: 1}
	confirmed, deferred := confirmedComfyCleanupIDs(result, map[string][]string{}, []string{"one", "two"}, 2)
	if len(confirmed) != 2 || len(deferred) != 0 {
		t.Fatalf("confirmed=%v deferred=%v", confirmed, deferred)
	}
}
