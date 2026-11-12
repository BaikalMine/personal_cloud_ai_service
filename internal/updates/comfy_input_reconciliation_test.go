package updates

import (
	"strings"
	"testing"
	"time"
)

func TestComfyInputReconciliationReceiptBinding(t *testing.T) {
	request, hash, err := CanonicalComfyInputReconciliationRequest(reconciliationFixture())
	if err != nil {
		t.Fatal(err)
	}
	makeReceipt := func() ComfyInputReconciliationReceipt {
		now := time.Now().UTC()
		receipt := ComfyInputReconciliationReceipt{Protocol: 1, ID: request.ID, RequestHash: hash, ExecutorID: request.ExecutorID, GuardJournalID: request.GuardJournalID, GuardInstanceID: strings.Repeat("1", 32), HeldAt: now, ObservedAt: now}
		for _, selection := range request.Items {
			size := selection.File.SizeBytes
			receipt.Items = append(receipt.Items, ComfyInputObservation{Selection: selection, Outcome: "matched", SizeBytes: &size, SHA256: selection.File.SHA256})
		}
		return receipt
	}
	if err := ValidateComfyInputReconciliationReceipt(request, makeReceipt()); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"missing", "unavailable"} {
		r := makeReceipt()
		r.Items[0].Outcome = outcome
		r.Items[0].SizeBytes = nil
		r.Items[0].SHA256 = ""
		if err := ValidateComfyInputReconciliationReceipt(request, r); err != nil {
			t.Fatal(outcome, err)
		}
	}
	changed := makeReceipt()
	changed.Items[0].Outcome = "changed"
	changed.Items[0].SHA256 = strings.Repeat("2", 64)
	if err := ValidateComfyInputReconciliationReceipt(request, changed); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ComfyInputReconciliationReceipt){
		func(r *ComfyInputReconciliationReceipt) { r.RequestHash = strings.Repeat("0", 64) },
		func(r *ComfyInputReconciliationReceipt) { r.ExecutorID = strings.Repeat("0", 64) },
		func(r *ComfyInputReconciliationReceipt) { r.GuardInstanceID = "" },
		func(r *ComfyInputReconciliationReceipt) { r.ObservedAt = r.HeldAt.Add(-time.Second) },
		func(r *ComfyInputReconciliationReceipt) { r.Items = r.Items[:1] },
		func(r *ComfyInputReconciliationReceipt) { r.Items[1] = r.Items[0] },
		func(r *ComfyInputReconciliationReceipt) { r.Items[0].Outcome = "deleted" },
		func(r *ComfyInputReconciliationReceipt) { r.Items[0].Outcome = "missing" },
		func(r *ComfyInputReconciliationReceipt) { r.Items[0].Outcome = "changed" },
		func(r *ComfyInputReconciliationReceipt) { r.Items[0].SHA256 = strings.Repeat("0", 64) },
	} {
		r := makeReceipt()
		mutate(&r)
		if ValidateComfyInputReconciliationReceipt(request, r) == nil {
			t.Fatal("invalid receipt accepted", r)
		}
	}
}

func reconciliationFixture() ComfyInputReconciliationRequest {
	return ComfyInputReconciliationRequest{Protocol: 1, Purpose: "reconcile_input", ID: strings.Repeat("x", 32), ExecutorID: strings.Repeat("a", 64), GuardJournalID: strings.Repeat("b", 32), Items: []ComfyInputReconciliationSelection{
		{InputID: "postprocess-input-" + strings.Repeat("d", 64), Version: strings.Repeat("e", 64), File: ComfyAssetFile{StorageType: "input", Filename: "second.png", Subfolder: "private", SizeBytes: 42, SHA256: strings.Repeat("f", 64)}},
		{InputID: "postprocess-input-" + strings.Repeat("c", 64), Version: strings.Repeat("e", 64), File: ComfyAssetFile{StorageType: "input", Filename: "first.png", Subfolder: "private", SizeBytes: 42, SHA256: strings.Repeat("f", 64)}},
	}}
}
func TestComfyInputReconciliationCanonical(t *testing.T) {
	original := reconciliationFixture()
	firstID := original.Items[0].InputID
	sorted, hash, err := CanonicalComfyInputReconciliationRequest(original)
	if err != nil || len(hash) != 64 || original.Items[0].InputID != firstID || sorted.Items[0].InputID == firstID {
		t.Fatal(sorted, hash, err)
	}
	_, second, err := CanonicalComfyInputReconciliationRequest(sorted)
	if err != nil || hash != second {
		t.Fatal(second, err)
	}
	sorted.Items[0].Version = strings.Repeat("9", 64)
	_, changed, err := CanonicalComfyInputReconciliationRequest(sorted)
	if err != nil || changed == hash {
		t.Fatal("identity change not bound", err)
	}
}
func TestComfyInputReconciliationRejectsUnsafeSelections(t *testing.T) {
	for _, change := range []func(*ComfyInputReconciliationRequest){
		func(r *ComfyInputReconciliationRequest) { r.Purpose = "delete" },
		func(r *ComfyInputReconciliationRequest) { r.Items = nil },
		func(r *ComfyInputReconciliationRequest) { r.Items[0].File.StorageType = "output" },
		func(r *ComfyInputReconciliationRequest) { r.Items[0].File.Subfolder = "../private" },
		func(r *ComfyInputReconciliationRequest) { r.Items[0].File.SizeBytes = 536870913 },
		func(r *ComfyInputReconciliationRequest) { r.Items[0].InputID = "input:1" },
		func(r *ComfyInputReconciliationRequest) { r.Items[0].Version = "" },
		func(r *ComfyInputReconciliationRequest) { r.Items[1].InputID = r.Items[0].InputID },
		func(r *ComfyInputReconciliationRequest) { r.Items[1].File.Filename = "SECOND.PNG" },
	} {
		r := reconciliationFixture()
		change(&r)
		if _, _, err := CanonicalComfyInputReconciliationRequest(r); err == nil {
			t.Fatal("unsafe request accepted", r)
		}
	}
}
