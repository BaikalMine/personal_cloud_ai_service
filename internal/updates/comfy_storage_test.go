package updates

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func comfyStorageTestRequest() ComfyStorageRequest {
	return ComfyStorageRequest{Protocol: 1, ID: "storage-operation-" + strings.Repeat("a", 32), ExecutorID: strings.Repeat("b", 64), GuardJournalID: strings.Repeat("c", 32), Items: []ComfyStorageSelection{
		{Key: "output:2", Version: strings.Repeat("d", 64), File: ComfyAssetFile{Filename: "output.png", Subfolder: "user-1/job", StorageType: "output", SizeBytes: 10, SHA256: strings.Repeat("e", 64)}},
		{Key: "input:1", Version: strings.Repeat("f", 64), File: ComfyAssetFile{Filename: "input.png", Subfolder: "user-1", StorageType: "input", SizeBytes: 20, SHA256: strings.Repeat("a", 64)}},
	}}
}

func comfyStorageTestResult() ComfyStorageResult {
	request, hash, _ := CanonicalComfyStorageRequest(comfyStorageTestRequest())
	now := time.Now().UTC()
	result := ComfyStorageResult{Protocol: 1, ID: request.ID, ExecutorID: request.ExecutorID, GuardJournalID: request.GuardJournalID, RequestHash: hash, State: "completed", MutationStarted: true, ExecutorFenced: true, GuardClosed: true, StartedAt: now, FinishedAt: &now}
	for _, item := range request.Items {
		result.Items = append(result.Items, ComfyStorageOutcome{Selection: item, Outcome: "deleted", FreedBytes: item.File.SizeBytes})
	}
	return result
}

func TestComfyStorageCanonicalRequest(t *testing.T) {
	request := comfyStorageTestRequest()
	before := append([]ComfyStorageSelection(nil), request.Items...)
	canonical, hash, err := CanonicalComfyStorageRequest(request)
	if err != nil || !ComfyStorageHex(hash, 32) || canonical.Items[0].Key != "input:1" || !reflect.DeepEqual(before, request.Items) {
		t.Fatal(canonical, hash, err)
	}
	again, againHash, err := CanonicalComfyStorageRequest(canonical)
	if err != nil || hash != againHash || !reflect.DeepEqual(canonical, again) {
		t.Fatal(again, againHash, err)
	}
	for _, mutate := range []func(*ComfyStorageRequest){
		func(r *ComfyStorageRequest) { r.ID += "b" },
		func(r *ComfyStorageRequest) { r.ExecutorID = strings.Repeat("1", 64) },
		func(r *ComfyStorageRequest) { r.GuardJournalID = strings.Repeat("2", 32) },
		func(r *ComfyStorageRequest) { r.Items[0].Version = strings.Repeat("3", 64) },
		func(r *ComfyStorageRequest) { r.Items[0].File.SHA256 = strings.Repeat("4", 64) },
	} {
		changed := comfyStorageTestRequest()
		mutate(&changed)
		_, changedHash, err := CanonicalComfyStorageRequest(changed)
		if err != nil || hash == changedHash {
			t.Fatal("identity not bound", changed, err)
		}
	}
}

func TestComfyStorageRejectsInvalidSelection(t *testing.T) {
	tests := map[string]func(*ComfyStorageRequest){
		"protocol":      func(r *ComfyStorageRequest) { r.Protocol = 0 },
		"short id":      func(r *ComfyStorageRequest) { r.ID = "short" },
		"unsafe id":     func(r *ComfyStorageRequest) { r.ID = strings.Repeat("a", 32) + "/b" },
		"executor":      func(r *ComfyStorageRequest) { r.ExecutorID = strings.Repeat("B", 64) },
		"journal":       func(r *ComfyStorageRequest) { r.GuardJournalID = "journal" },
		"empty":         func(r *ComfyStorageRequest) { r.Items = nil },
		"count":         func(r *ComfyStorageRequest) { r.Items = make([]ComfyStorageSelection, 51) },
		"duplicate key": func(r *ComfyStorageRequest) { r.Items[1] = r.Items[0] },
		"duplicate case path": func(r *ComfyStorageRequest) {
			r.Items[1] = r.Items[0]
			r.Items[1].Key = "output:3"
			r.Items[1].File.Filename = "OUTPUT.PNG"
		},
		"wrong kind":         func(r *ComfyStorageRequest) { r.Items[0].Key = "input:2" },
		"negative id":        func(r *ComfyStorageRequest) { r.Items[0].Key = "output:-2" },
		"padded id":          func(r *ComfyStorageRequest) { r.Items[0].Key = "output:02" },
		"version":            func(r *ComfyStorageRequest) { r.Items[0].Version = "v1" },
		"bytes":              func(r *ComfyStorageRequest) { r.Items[0].File.SizeBytes = -1 },
		"large bytes":        func(r *ComfyStorageRequest) { r.Items[0].File.SizeBytes = 1 << 41 },
		"unknown storage":    func(r *ComfyStorageRequest) { r.Items[0].File.StorageType = "temp" },
		"filename":           func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "../file" },
		"absolute":           func(r *ComfyStorageRequest) { r.Items[0].File.Subfolder = "/input" },
		"traversal":          func(r *ComfyStorageRequest) { r.Items[0].File.Subfolder = "user/../input" },
		"alias":              func(r *ComfyStorageRequest) { r.Items[0].File.Subfolder = "user//input" },
		"windows":            func(r *ComfyStorageRequest) { r.Items[0].File.Subfolder = `user\input` },
		"ads":                func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "file:stream" },
		"trailing dot":       func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "file." },
		"trailing space":     func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "file " },
		"device":             func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "con.png" },
		"superscript device": func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "COM\u00b9.png" },
		"control":            func(r *ComfyStorageRequest) { r.Items[0].File.Filename = "file\x01.png" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := comfyStorageTestRequest()
			mutate(&request)
			if _, _, err := CanonicalComfyStorageRequest(request); err == nil {
				t.Fatal("invalid request accepted", request)
			}
		})
	}
}

func TestComfyStorageValidatesResultEvidence(t *testing.T) {
	if err := MatchComfyStorageResult(comfyStorageTestResult(), comfyStorageTestRequest()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ComfyStorageResult){
		"protocol":         func(r *ComfyStorageResult) { r.Protocol = 2 },
		"hash":             func(r *ComfyStorageResult) { r.RequestHash = strings.Repeat("1", 64) },
		"executor":         func(r *ComfyStorageResult) { r.ExecutorID = strings.Repeat("1", 64) },
		"order":            func(r *ComfyStorageResult) { r.Items[0], r.Items[1] = r.Items[1], r.Items[0] },
		"bytes":            func(r *ComfyStorageResult) { r.Items[0].FreedBytes++ },
		"missing bytes":    func(r *ComfyStorageResult) { r.Items[0].Outcome = "missing" },
		"no mutation":      func(r *ComfyStorageResult) { r.MutationStarted = false },
		"no fence":         func(r *ComfyStorageResult) { r.ExecutorFenced = false },
		"no finish":        func(r *ComfyStorageResult) { r.FinishedAt = nil },
		"no start":         func(r *ComfyStorageResult) { r.StartedAt = time.Time{} },
		"unknown complete": func(r *ComfyStorageResult) { r.Items[0].Outcome = "unconfirmed"; r.Items[0].FreedBytes = 0 },
		"rejected delete":  func(r *ComfyStorageResult) { r.State = "rejected" },
		"expired details":  func(r *ComfyStorageResult) { r.State = "expired" },
	} {
		t.Run(name, func(t *testing.T) {
			result := comfyStorageTestResult()
			mutate(&result)
			if err := ValidateComfyStorageResult(result); err == nil {
				t.Fatal("invalid evidence accepted", result)
			}
		})
	}
	result := comfyStorageTestResult()
	result.State, result.Items = "expired", nil
	if err := MatchComfyStorageResult(result, comfyStorageTestRequest()); err != nil {
		t.Fatal("closed ID lost after expiry", err)
	}
}
