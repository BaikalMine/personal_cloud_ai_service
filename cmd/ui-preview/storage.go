package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

type storagePreviewSession struct {
	records    []domain.ComfyStorageItem
	training   []loratraining.StorageItem
	operations map[string]map[string]any
	freed      int64
}

// This preview owns synthetic records in memory. It has no filesystem cleanup,
// database, agent client, GPU admission or connection to an installed service.
func registerStoragePreview(mux *http.ServeMux, now time.Time) {
	mux.HandleFunc("/admin/storage/comfy/unresolved-inputs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
			return
		}
		items := []map[string]any{}
		if r.URL.Query().Get("after") == "" {
			for i, name := range []string{"reference-window-light.png", "source-after-deleted-job.png"} {
				var owner any
				if i == 0 {
					owner = "9007199254740993"
				}
				items = append(items, map[string]any{"id": fmt.Sprintf("preview-unresolved-%d", i), "user_id": owner, "job_id": nil,
					"filename": name, "subfolder": "preview/references", "size_bytes": 4194304, "sha256": strings.Repeat("a", 64),
					"state": "reserved", "created_at": now.Add(-time.Hour), "can_cleanup": false})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"scope": "unresolved_input_registry", "items": items, "next_after": ""})
	})
	var mu sync.Mutex
	sessions := map[string]*storagePreviewSession{}
	version := strings.Repeat("a", 64)
	write := func(w http.ResponseWriter, code int, value any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(value)
	}
	newSession := func() *storagePreviewSession {
		s := &storagePreviewSession{operations: map[string]map[string]any{}}
		for i := 0; i < 67; i++ {
			eligible := now.Add(-time.Duration(2+i%8) * time.Hour)
			item := domain.ComfyStorageItem{Key: fmt.Sprintf("cleanup:%d", i+1), Kind: "output", Filename: fmt.Sprintf("portrait-series_%03d.png", i+1), StorageType: "output", Subfolder: "rayka/portrait-study", SizeKnown: true, SizeBytes: int64(7+i) << 20, OwnerName: "rayka", OwnerCount: 1, State: "cleanup_pending", Pending: true, Reason: "retention", CreatedAt: now.Add(-5 * 24 * time.Hour), EligibleAt: &eligible, Attempts: 1, LastOutcome: "host_unavailable", CanInspect: true, Protection: []string{"executor_not_fenced"}, Version: version}
			switch i {
			case 0:
				item.Filename = "portrait_soft-window-light_0042.png"
				item.LastOutcome = "not_attempted"
			case 1:
				item.Filename = "minimax_first-last-frame_studio-study_1080p.mp4"
				item.Subfolder = "demo4518/video-tests"
				item.OwnerName = "demo4518"
				item.SizeBytes = 812 << 20
				item.LastOutcome = "mismatched"
			case 2:
				item.Key = "input:" + strings.Repeat("c", 32)
				item.Kind, item.StorageType = "input", "input"
				item.Filename = "reference-for-active-generation.png"
				item.State = "stored"
				item.ReferenceProtected = true
				item.Protection = []string{"generation_input_reference", "executor_not_fenced"}
			case 3:
				item.Filename = "hidden-from-results_original-still-pending.png"
				item.ProfileHidden = true
				item.Reason = "profile_hidden"
				item.LastOutcome = "unconfirmed"
			case 4:
				item.Key = "output:9007199254740993"
				item.Filename = "saved-library-result_with-long-descriptive-filename-and-original-colour-profile.png"
				item.Pending = false
				item.State = "tracked"
				item.Reason = "retained"
				item.Protection = []string{"retained", "executor_not_fenced"}
				item.ExpiresAt = timePointer(now.Add(3 * 24 * time.Hour))
				item.EligibleAt = nil
			case 5:
				item.Key = "input:" + strings.Repeat("d", 32)
				item.Kind, item.StorageType = "input", "input"
				item.Filename = "upload-not-confirmed.png"
				item.State = "reserved"
				item.CanInspect = false
				item.Protection = []string{"upload_unconfirmed", "executor_not_fenced"}
			case 6:
				item.Filename = "already-missing-on-host.png"
				item.LastOutcome = "not_attempted"
				item.OwnerName = ""
				item.OwnerCount = 0
			case 7:
				item.Filename = "legacy-result-without-fingerprint.png"
				item.SizeKnown = false
				item.CanInspect = false
				item.Protection = []string{"fingerprint_unavailable", "executor_not_fenced"}
				item.LastOutcome = "legacy_retry"
			}
			s.records = append(s.records, item)
		}
		kinds := []string{"training_cache", "normalized_model", "original_model", "trained_artifact", "dataset_archive", "dataset_files", "training_output", "unmanaged_file"}
		for i, kind := range kinds {
			item := loratraining.StorageItem{ID: kind + ":" + strings.Repeat(fmt.Sprintf("%x", i+1), 32), Kind: kind, Label: []string{"portrait-window-light/cache", "krea2-raw-official-format-v2.safetensors", "krea2Raw_v10.safetensors", "portrait_studio_style_v3.safetensors", "dataset.zip", "dataset", "output", "unrecognised-model-copy.bin"}[i], State: "present", SizeBytes: int64(i+1) * 512 << 20, Files: int64(i+1) * 25, ProfileID: "krea2-raw-official-bf16", GatewayJobID: "a-training-job-portrait-study", Version: version, CanCleanup: loratraining.RebuildableStorageKind(kind)}
			if kind == "original_model" {
				item.ProtectedReason = "original_model"
			}
			if kind == "trained_artifact" {
				item.ProtectedReason = "artifact_lifecycle"
			}
			if kind == "unmanaged_file" {
				item.ProtectedReason = "unmanaged_path"
			}
			if kind == "dataset_files" {
				item.CanCleanup = false
				item.ProtectedReason = "active_job"
			}
			s.training = append(s.training, item)
		}
		return s
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		mu.Lock()
		defer mu.Unlock()
		id := ""
		if cookie, err := r.Cookie("storage-preview"); err == nil && updates.ValidComfyStorageID(cookie.Value) {
			id = cookie.Value
		}
		if id == "" {
			var raw [16]byte
			if _, err := rand.Read(raw[:]); err != nil {
				write(w, 500, nil)
				return
			}
			id = hex.EncodeToString(raw[:])
			http.SetCookie(w, &http.Cookie{Name: "storage-preview", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		if sessions[id] == nil {
			sessions[id] = newSession()
		}
		s := sessions[id]
		kind := "comfy"
		if strings.Contains(r.URL.Path, "/training") {
			kind = "training"
		}
		if strings.Contains(r.URL.Path, "/operations") {
			root := "/admin/storage/" + kind + "/operations"
			opID, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, root+"/"), "/")
			if r.URL.Path == root {
				opID = ""
			}
			if r.Method == "GET" && opID == "" {
				ops := []map[string]any{}
				for _, envelope := range s.operations {
					if envelope["kind"] == kind {
						ops = append(ops, envelope["operation"].(map[string]any))
					}
				}
				if len(ops) == 0 {
					ops = append(ops, map[string]any{"id": strings.Repeat("f", 32), "origin": "retention", "state": "completed", "actor_name": "", "item_count": 18, "expected_bytes": int64(2150 << 20), "freed_bytes": int64(2022 << 20), "deleted_files": 17, "created_at": now.Add(-2 * time.Hour), "finished_at": now.Add(-2*time.Hour + time.Minute), "request_hash": version})
				}
				write(w, 200, map[string]any{"items": ops, "total": len(ops), "freed_bytes": int64(2022<<20) + s.freed, "since": now.Add(-90 * 24 * time.Hour), "checked_at": now})
				return
			}
			if r.Method == "GET" && opID != "" {
				if envelope := s.operations[opID]; envelope != nil {
					write(w, 200, envelope)
					return
				}
				if opID == strings.Repeat("f", 32) {
					write(w, 200, map[string]any{"operation": map[string]any{"id": opID, "origin": "retention", "state": "completed", "actor_name": "", "item_count": 18, "expected_bytes": int64(2150 << 20), "freed_bytes": int64(2022 << 20), "deleted_files": 17, "created_at": now.Add(-2 * time.Hour), "finished_at": now.Add(-2*time.Hour + time.Minute), "request_hash": version}, "can_confirm": false, "can_cancel": false, "can_reconcile": false})
					return
				}
				write(w, 404, map[string]string{"error": "storage_operation_missing"})
				return
			}
			if r.Method != "POST" || r.Header.Get("X-CSRF-Token") != "preview" {
				write(w, 403, map[string]string{"error": "csrf_failed"})
				return
			}
			if opID == "" {
				var input struct {
					ID    string                                `json:"id"`
					Items []store.ComfyStorageRegistrySelection `json:"items"`
					IDs   []string                              `json:"ids"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input) != nil || !updates.ValidComfyStorageID(input.ID) {
					write(w, 400, map[string]string{"error": "invalid_storage_request"})
					return
				}
				if existing := s.operations[input.ID]; existing != nil {
					write(w, 200, existing)
					return
				}
				files := []updates.ComfyStorageSelection{}
				selected := []store.ComfyStorageTarget{}
				training := []loratraining.StorageItem{}
				expected := int64(0)
				if kind == "comfy" {
					for _, selection := range input.Items {
						for _, item := range s.records {
							if selection.Key == item.Key && selection.Version == item.Version && item.Pending && item.CanInspect && !item.ReferenceProtected {
								key := fmt.Sprintf("%s:%d", item.Kind, len(files)+1)
								selected = append(selected, store.ComfyStorageTarget{ComfyStorageRegistrySelection: selection, HostKey: key})
								files = append(files, updates.ComfyStorageSelection{Key: key, Version: version, File: updates.ComfyAssetFile{Filename: item.Filename, Subfolder: item.Subfolder, StorageType: item.StorageType, SizeBytes: item.SizeBytes, SHA256: version}})
								expected += item.SizeBytes
							}
						}
					}
					if len(files) == 0 || len(files) != len(input.Items) {
						write(w, 409, map[string]string{"error": "storage_operation_changed"})
						return
					}
				} else {
					for _, id := range input.IDs {
						for _, item := range s.training {
							if id == item.ID && item.CanCleanup {
								training = append(training, item)
								expected += item.SizeBytes
							}
						}
					}
					if len(training) == 0 || len(training) != len(input.IDs) {
						write(w, 409, map[string]string{"error": "storage_operation_changed"})
						return
					}
				}
				count := len(files) + len(training)
				op := map[string]any{"id": input.ID, "origin": "manual", "state": "preview", "actor_id": 1, "actor_name": "admin", "request_hash": version, "item_count": count, "expected_bytes": expected, "freed_bytes": int64(0), "deleted_files": 0, "created_at": time.Now().UTC(), "expires_at": time.Now().UTC().Add(30 * time.Minute), "selection": selected}
				if kind == "training" {
					op["preview"] = loratraining.StorageInventory{ProtocolVersion: 1, CheckedAt: now, Complete: true, TotalItems: len(training), Items: training}
				}
				envelope := map[string]any{"kind": kind, "operation": op, "files": files, "can_confirm": true, "can_cancel": true, "can_reconcile": false}
				s.operations[input.ID] = envelope
				write(w, 200, envelope)
				return
			}
			envelope := s.operations[opID]
			if envelope == nil {
				write(w, 404, map[string]string{"error": "storage_operation_missing"})
				return
			}
			op := envelope["operation"].(map[string]any)
			if action == "cancel" && op["state"] == "preview" {
				op["state"] = "cancelled"
				op["finished_at"] = time.Now().UTC()
			}
			if action == "confirm" && op["state"] == "preview" {
				var input struct {
					Hash    string `json:"request_hash"`
					Confirm bool   `json:"confirm"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || !input.Confirm || input.Hash != version {
					write(w, 400, map[string]string{"error": "invalid_storage_request"})
					return
				}
				outcomes := []map[string]any{}
				freed := int64(0)
				removed := map[string]bool{}
				if kind == "comfy" {
					for _, file := range envelope["files"].([]updates.ComfyStorageSelection) {
						outcome := "deleted"
						size := file.File.SizeBytes
						if strings.Contains(file.File.Filename, "already-missing") {
							outcome = "missing"
							size = 0
						}
						outcomes = append(outcomes, map[string]any{"selection": file, "outcome": outcome, "freed_bytes": size})
						freed += size
					}
					for _, selection := range op["selection"].([]store.ComfyStorageTarget) {
						removed[selection.Key] = true
					}
					kept := []domain.ComfyStorageItem{}
					for _, item := range s.records {
						if !removed[item.Key] {
							kept = append(kept, item)
						}
					}
					s.records = kept
				} else {
					for _, item := range op["preview"].(loratraining.StorageInventory).Items {
						outcomes = append(outcomes, map[string]any{"id": item.ID, "label": item.Label, "kind": item.Kind, "outcome": "deleted", "freed_bytes": item.SizeBytes})
						freed += item.SizeBytes
						removed[item.ID] = true
					}
					for i := range s.training {
						if removed[s.training[i].ID] {
							s.training[i].State = "missing"
							s.training[i].CanCleanup = false
							s.training[i].SizeBytes = 0
							s.training[i].Files = 0
						}
					}
				}
				op["state"] = "completed"
				op["freed_bytes"] = freed
				deleted := 0
				for _, outcome := range outcomes {
					if outcome["outcome"] == "deleted" {
						deleted++
					}
				}
				op["deleted_files"] = deleted
				op["finished_at"] = time.Now().UTC()
				op["result"] = map[string]any{"items": outcomes}
				s.freed += freed
			}
			envelope["can_confirm"] = op["state"] == "preview"
			envelope["can_cancel"] = op["state"] == "preview"
			write(w, 200, envelope)
			return
		}
		if r.URL.Path == "/admin/storage/comfy/inspect" {
			var input struct {
				Items []store.ComfyStorageRegistrySelection `json:"items"`
			}
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&input) != nil {
				write(w, 400, nil)
				return
			}
			items := []map[string]any{}
			for _, selection := range input.Items {
				for _, item := range s.records {
					if item.Key == selection.Key && item.Version == selection.Version {
						state := "matched"
						if item.LastOutcome == "mismatched" {
							state = "mismatched"
						}
						if strings.Contains(item.Filename, "already-missing") {
							state = "missing"
						}
						items = append(items, map[string]any{"key": item.Key, "version": item.Version, "status": state, "protection": item.Protection})
					}
				}
			}
			if len(items) != len(input.Items) {
				write(w, 409, map[string]string{"error": "storage_selection_changed"})
				return
			}
			write(w, 200, map[string]any{"inspection": map[string]any{"checked_at": now, "host_checked_at": now, "complete": true, "host_state": "available", "items": items}})
			return
		}
		if r.Method != "GET" {
			write(w, 405, nil)
			return
		}
		if kind == "training" {
			write(w, 200, map[string]any{"inventory": loratraining.StorageInventory{ProtocolVersion: 1, CheckedAt: now, Complete: true, TotalItems: len(s.training), Items: s.training}, "manual_cleanup_available": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			query, err := updates.ParseComfyFileInventoryQuery(r.URL.RawQuery)
			if err != nil {
				write(w, 400, nil)
				return
			}
			all := []map[string]any{}
			for i, name := range []string{"portrait_soft-window-light_0042.png", "outside-gateway_manual-comfyui-session.png", "junction-to-other-volume", "old-unregistered-render-with-a-very-long-filename-that-must-remain-readable-at-mobile-width.png"} {
				entry := "file"
				known := true
				size := int64(i+4) << 20
				registration := "unregistered_path"
				if i == 0 {
					registration = "registered_path"
				}
				if i == 2 {
					entry = "link"
					known = false
					size = 0
				}
				if query.Search != "" && !strings.Contains(name, query.Search) {
					continue
				}
				all = append(all, map[string]any{"id": updates.ComfyFileInventoryID(query.Kind, "manual-session", name), "filename": name, "subfolder": "manual-session", "entry_type": entry, "size_known": known, "size_bytes": size, "modified_at": now.Add(-time.Hour), "registration": registration, "can_cleanup": false})
			}
			write(w, 200, map[string]any{"scope": "host_metadata", "registry_checked_at": now, "inventory": map[string]any{"protocol": 1, "kind": query.Kind, "checked_at": now, "complete": true, "issue": "", "snapshot": version, "total_items": 4, "total_files": 3, "logical_bytes": 16 << 20, "matched_items": len(all), "next_cursor": "", "items": all}})
			return
		}
		query := r.URL.Query()
		offset, _ := strconv.Atoi(query.Get("offset"))
		if offset < 0 {
			offset = 0
		}
		filtered := []domain.ComfyStorageItem{}
		var pending, expected, protected int64
		for _, item := range s.records {
			if item.Pending {
				pending++
				expected += item.SizeBytes
			}
			if item.ReferenceProtected {
				protected++
			}
			state := query.Get("state")
			if query.Get("kind") != "" && item.Kind != query.Get("kind") || query.Get("q") != "" && !strings.Contains(strings.ToLower(item.Filename+" "+item.Subfolder+" "+item.OwnerName), strings.ToLower(query.Get("q"))) {
				continue
			}
			if state == "pending" && !item.Pending || state == "retained" && item.Pending || state == "mismatched" && item.LastOutcome != "mismatched" || state == "referenced" && !item.ReferenceProtected || state == "retry" && item.Attempts == 0 || state == "unconfirmed" && item.State != "reserved" && item.LastOutcome != "unconfirmed" {
				continue
			}
			filtered = append(filtered, item)
		}
		total := len(filtered)
		offset = min(offset, total)
		filtered = filtered[offset:min(offset+50, total)]
		write(w, 200, map[string]any{"scope": "tracked_records", "manual_cleanup_available": true, "manual_cleanup_reason": "confirmation_required", "host_configured": true, "inventory": domain.ComfyStorageInventory{CheckedAt: now, Items: filtered, Total: int64(total), Limit: 50, Offset: offset, Summary: domain.ComfyStorageSummary{PendingRecords: pending, PendingExpectedBytes: expected, ConfirmedFreedBytes: int64(2022<<20) + s.freed, ProtectedReferences: protected, UnconfirmedUploads: 1, MissingReceipts: 1, OldestEligibleAt: timePointer(now.Add(-5 * 24 * time.Hour)), ReportingSince: now.Add(-90 * 24 * time.Hour)}}})
	}
	for _, path := range []string{"/admin/storage/comfy", "/admin/storage/comfy/", "/admin/storage/training", "/admin/storage/training/"} {
		mux.HandleFunc(path, handler)
	}
}
