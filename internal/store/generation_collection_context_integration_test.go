package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestGenerationCollectionContextIntegration(t *testing.T) {
	db, repository, owner := comfyStorageDBFixture(t)
	ctx := context.Background()
	collection, err := repository.CreateGenerationMediaCollection(ctx, owner, "Series")
	if err != nil || collection.Revision <= 0 {
		t.Fatal(collection, err)
	}
	draft, err := repository.SaveGenerationDraft(ctx, owner, 0, []byte("source"))
	if err != nil {
		t.Fatal(err)
	}
	params := domain.SaveGenerationCollectionContextParams{UserID: owner, CollectionID: collection.ID, ExpectedRevision: collection.Revision,
		ExpectedDraftRevision: draft.Revision, Name: collection.Name, ReplaceContext: true, PayloadCipher: []byte("encrypted-context")}
	row, err := repository.SaveGenerationCollectionContext(ctx, params)
	if err != nil || row.ContextSavedAt == nil || row.Collection.Revision <= collection.Revision {
		t.Fatal(row, err)
	}
	if _, err := repository.SaveGenerationCollectionContext(ctx, params); !errors.Is(err, store.ErrGenerationCollectionConflict) {
		t.Fatal("stale collection saved", err)
	}
	params.ExpectedRevision = row.Collection.Revision
	draft, err = repository.SaveGenerationDraft(ctx, owner, draft.Revision, []byte("new draft"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveGenerationCollectionContext(ctx, params); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatal("stale source draft saved", err)
	}
	params.ExpectedDraftRevision = draft.Revision
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repository.SaveGenerationCollectionContext(ctx, params)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, store.ErrGenerationCollectionConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("concurrent save winners", winners)
	}
	current, err := repository.GenerationCollectionContext(ctx, owner, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.OpenGenerationCollectionDraft(ctx, owner, collection.ID, row.Collection.Revision, draft.Revision, []byte("open")); !errors.Is(err, store.ErrGenerationCollectionConflict) {
		t.Fatal("stale collection opened", err)
	}
	if _, err := repository.OpenGenerationCollectionDraft(ctx, owner, collection.ID, current.Collection.Revision, 0, []byte("open")); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatal("current draft overwritten", err)
	}
	opened, err := repository.OpenGenerationCollectionDraft(ctx, owner, collection.ID, current.Collection.Revision, draft.Revision, []byte("open"))
	if err != nil || string(opened.PayloadCipher) != "open" || opened.Revision <= draft.Revision {
		t.Fatal(opened, err)
	}
	var foreign int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('collection-other','hash','user') RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	params.UserID, params.ExpectedRevision = foreign, current.Collection.Revision
	if _, err := repository.GenerationCollectionContext(ctx, foreign, collection.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign context read", err)
	}
	if _, err := repository.SaveGenerationCollectionContext(ctx, params); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign context saved", err)
	}
	if _, err := repository.OpenGenerationCollectionDraft(ctx, foreign, collection.ID, current.Collection.Revision, 0, []byte("foreign")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign context opened", err)
	}
	other, err := repository.CreateGenerationMediaCollection(ctx, owner, "Other")
	if err != nil {
		t.Fatal(err)
	}
	params.UserID, params.ExpectedDraftRevision, params.ReplaceContext = owner, 0, false
	params.Name = other.Name
	if _, err := repository.SaveGenerationCollectionContext(ctx, params); !errors.Is(err, store.ErrGenerationCollectionName) {
		t.Fatal("duplicate collection renamed", err)
	}
	params.Name = "Renamed"
	renamed, err := repository.SaveGenerationCollectionContext(ctx, params)
	if err != nil || string(renamed.PayloadCipher) != string(current.PayloadCipher) || !renamed.ContextSavedAt.Equal(*current.ContextSavedAt) {
		t.Fatal("rename changed context", renamed, err)
	}
	same, err := repository.CreateGenerationMediaCollection(ctx, owner, "Renamed")
	if err != nil || same.Revision != renamed.Collection.Revision {
		t.Fatal("idempotent create changed context revision", same, err)
	}
	cased, err := repository.CreateGenerationMediaCollection(ctx, owner, "RENAMED")
	if err != nil || cased.Revision <= same.Revision {
		t.Fatal("display name did not invalidate revision", cased, err)
	}
	if _, err := db.Exec(`UPDATE generation_drafts SET expires_at=now()-interval '1 second' WHERE user_id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.OpenGenerationCollectionDraft(ctx, owner, collection.ID, cased.Revision, opened.Revision, []byte("expired")); !errors.Is(err, store.ErrGenerationDraftConflict) {
		t.Fatal("expired revision opened", err)
	}
	if _, err := repository.OpenGenerationCollectionDraft(ctx, owner, collection.ID, cased.Revision, 0, []byte("recreated")); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationCollectionJobBindingIntegration(t *testing.T) {
	db, repository, owner := comfyStorageDBFixture(t)
	ctx := context.Background()
	collection, err := repository.CreateGenerationMediaCollection(ctx, owner, "Results")
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			create := repository.ClaimGenerationJob
			if legacy {
				create = repository.CreateGenerationJob
			}
			params := domain.CreateGenerationJobParams{UserID: owner, CollectionID: collection.ID, PublicID: fmt.Sprint("collection-job-", legacy), RequestID: fmt.Sprint("collection-request-", legacy)}
			job, created, err := create(ctx, params)
			if err != nil || !created {
				t.Fatal(job, created, err)
			}
			params.CollectionID = collection.ID + 9999
			again, created, err := create(ctx, params)
			if err != nil || created || again.ID != job.ID {
				t.Fatal("replay changed identity", again, created, err)
			}
			var bound int64
			if err := db.QueryRow(`SELECT collection_id FROM generation_collection_jobs WHERE job_id=$1`, job.ID).Scan(&bound); err != nil || bound != collection.ID {
				t.Fatal("replay rebound job", bound, err)
			}
			params.PublicID += "-rejected"
			params.RequestID += "-rejected"
			if _, created, err := create(ctx, params); !errors.Is(err, store.ErrGenerationCollectionMissing) || created {
				t.Fatal("missing collection accepted", created, err)
			}
			var count int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_jobs WHERE request_id=$1)+
			 (SELECT count(*) FROM generation_requests WHERE request_id=$1)+(SELECT count(*) FROM generation_submissions WHERE request_id=$1)`, params.RequestID).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed claim left partial rows", count, err)
			}
		})
	}
	batchParams := submissionBatch(owner, "collection")
	batchParams.CollectionID = collection.ID
	batch, created, err := repository.CreateGenerationBatch(ctx, batchParams)
	if err != nil || !created {
		t.Fatal(batch, created, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM generation_collection_jobs binding JOIN generation_jobs job ON job.id=binding.job_id WHERE job.batch_id=$1 AND binding.collection_id=$2`, batch.ID, collection.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("batch binding", count, err)
	}
	rejected := submissionBatch(owner, "collection-rejected")
	rejected.CollectionID = collection.ID + 9999
	var quotaBefore, quotaAfter int64
	if err := db.QueryRow(`SELECT generation_total_used FROM users WHERE id=$1`, owner).Scan(&quotaBefore); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repository.CreateGenerationBatch(ctx, rejected); !errors.Is(err, store.ErrGenerationCollectionMissing) || created {
		t.Fatal("bad batch collection accepted", created, err)
	}
	if err := db.QueryRow(`SELECT generation_total_used FROM users WHERE id=$1`, owner).Scan(&quotaAfter); err != nil || quotaAfter != quotaBefore {
		t.Fatal("failed batch consumed quota", quotaBefore, quotaAfter, err)
	}
	if ok, err := repository.DeleteGenerationMediaCollection(ctx, owner, collection.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	again, created, err := repository.CreateGenerationBatch(ctx, batchParams)
	if err != nil || created || again.ID != batch.ID {
		t.Fatal("deleted collection broke batch replay", again, created, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM generation_jobs WHERE user_id=$1`, owner).Scan(&count); err != nil || count != 4 {
		t.Fatal("collection delete removed jobs or rejected batch leaked", count, err)
	}
}

func TestGenerationCollectionOutputArchiveIntegration(t *testing.T) {
	db, repository, owner := comfyStorageDBFixture(t)
	ctx := context.Background()
	collection, err := repository.CreateGenerationMediaCollection(ctx, owner, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	var foreign int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('foreign-output-owner','hash','user') RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"valid", "unbound", "foreign-event", "expired-event", "expired-media", "hidden"} {
		job, _, err := repository.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{UserID: owner, CollectionID: collection.ID, PublicID: "archive-job-" + kind, RequestID: "archive-request-" + kind})
		if err != nil {
			t.Fatal(err)
		}
		if kind == "unbound" {
			if _, err := db.Exec(`DELETE FROM generation_collection_jobs WHERE job_id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
		}
		eventOwner, expiry := owner, time.Now().Add(time.Hour)
		if kind == "foreign-event" {
			eventOwner = foreign
		}
		if kind == "expired-event" {
			expiry = time.Now().Add(-time.Hour)
		}
		event, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{UserID: eventOwner, GenerationJobID: &job.ID,
			Service: "comfyui", Kind: "comfyui_prompt", ExternalID: kind, ExpiresAt: expiry})
		if err != nil {
			t.Fatal(err)
		}
		mediaExpiry := time.Now().Add(time.Hour)
		if kind == "expired-media" {
			mediaExpiry = time.Now().Add(-time.Hour)
		}
		var media int64
		if err := db.QueryRow(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,storage_type,payload_cipher,size_bytes,expires_at,profile_hidden_at)
		 VALUES($1,'image','image/png',$2,'output',$3,1,$4,CASE WHEN $5 THEN now() ELSE NULL END) RETURNING id`, event, kind+".png", []byte("encrypted"), mediaExpiry, kind == "hidden").Scan(&media); err != nil {
			t.Fatal(err)
		}
		var count int
		want := 0
		if kind == "valid" {
			want = 1
		}
		if err := db.QueryRow(`SELECT count(*) FROM generation_media_collection_items WHERE collection_id=$1 AND media_id=$2`, collection.ID, media).Scan(&count); err != nil || count != want {
			t.Fatalf("archive %s: %d want %d: %v", kind, count, want, err)
		}
	}
	list, err := repository.ListGenerationMediaCollections(ctx, owner)
	if err != nil || len(list) != 1 || list[0].ItemCount != 1 || list[0].Revision != collection.Revision {
		t.Fatal("archive counts/revision", list, err)
	}
	if _, err := repository.DeleteGenerationMediaCollection(ctx, owner, collection.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM content_media`).Scan(&count); err != nil || count != 6 {
		t.Fatal("collection deleted archive files", count, err)
	}
}
