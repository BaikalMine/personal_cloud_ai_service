package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
)

func TestStoreIntegrationLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	repository := store.New(db)

	adminHash, err := security.HashPassword("integration-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureBootstrapAdmin(ctx, "admin", adminHash); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	assertMiningProfiles(t, ctx, repository, adminID)
	assertDatabaseRetentionLifecycle(t, ctx, db, repository, adminID)
	assertGenerationJobLifecycle(t, ctx, db, repository, adminID)

	inviteHash := security.HashToken("single-use-integration-invite")
	inviteID, err := repository.CreateInvite(ctx, store.CreateInviteParams{
		TokenHash: inviteHash, CreatedByUserID: adminID, MaxUses: 1,
		ExpiresAt: time.Now().Add(time.Hour), GrantComfyUI: true, GrantOpenWebUI: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := repository.DeleteInvite(ctx, inviteID)
	if err != nil || !deleted {
		t.Fatalf("delete invite = deleted:%v err:%v", deleted, err)
	}
	if _, err := repository.AvailableInvite(ctx, inviteHash); !errors.Is(err, store.ErrInviteUnavailable) {
		t.Fatalf("deleted invite availability error = %v, want ErrInviteUnavailable", err)
	}
	inviteID, err = repository.CreateInvite(ctx, store.CreateInviteParams{
		TokenHash: security.HashToken("single-use-integration-invite"), CreatedByUserID: adminID, MaxUses: 1,
		ExpiresAt: time.Now().Add(time.Hour), GrantComfyUI: true, GrantOpenWebUI: false,
	})
	if err != nil || inviteID <= 0 {
		t.Fatalf("recreate invite = id:%d err:%v", inviteID, err)
	}
	userHash, err := security.HashPassword("integration-user-password")
	if err != nil {
		t.Fatal(err)
	}

	type registrationResult struct {
		userID int64
		err    error
	}
	results := make(chan registrationResult, 2)
	var wg sync.WaitGroup
	for _, username := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			userID, _, registerErr := repository.RegisterFromInvite(context.Background(), store.RegisterFromInviteParams{
				TokenHash: inviteHash, Username: username, Email: username + "@example.test", PasswordHash: userHash, IP: "192.0.2.10",
			})
			results <- registrationResult{userID: userID, err: registerErr}
		}(username)
	}
	wg.Wait()
	close(results)

	var registeredUserID int64
	var successes, unavailable int
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			registeredUserID = result.userID
		case errors.Is(result.err, store.ErrInviteUnavailable):
			unavailable++
		default:
			t.Fatalf("unexpected registration error: %v", result.err)
		}
	}
	if successes != 1 || unavailable != 1 {
		t.Fatalf("concurrent invite results: successes=%d unavailable=%d", successes, unavailable)
	}
	user, err := repository.UserByID(ctx, registeredUserID)
	if err != nil {
		t.Fatal(err)
	}
	if !user.CanUseComfyUI || user.CanUseOpenWebUI {
		t.Fatalf("invite permissions were not preserved: comfy=%v openweb=%v", user.CanUseComfyUI, user.CanUseOpenWebUI)
	}
	byEmail, passwordHash, err := repository.FindUserWithPassword(ctx, strings.ToUpper(user.Email.String))
	if err != nil || byEmail.ID != registeredUserID || passwordHash != userHash {
		t.Fatalf("find user by email: user=%+v err=%v", byEmail, err)
	}
	assertComfyUserStateIsolation(t, ctx, repository, registeredUserID, adminID)
	assertComfyInputLifecycle(t, ctx, db, repository, registeredUserID)
	assertComfyOutputCleanupLifecycle(t, ctx, db, repository, registeredUserID)

	sessionToken := "integration-session-token"
	sessionHash := security.HashToken(sessionToken)
	if err := repository.CreateSession(ctx, registeredUserID, sessionHash, time.Now().Add(time.Hour), "integration-test", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UserBySessionHash(ctx, sessionHash, 30*time.Minute); err != nil {
		t.Fatalf("active session was rejected: %v", err)
	}
	updated, revoked, err := repository.SetDisabled(ctx, registeredUserID, true)
	if err != nil || !updated || revoked != 1 {
		t.Fatalf("disable user: updated=%v revoked=%d err=%v", updated, revoked, err)
	}
	if _, err := repository.UserBySessionHash(ctx, sessionHash, 30*time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled user's session remained usable: %v", err)
	}

	revisionBeforeContent, err := repository.ContentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: registeredUserID, Service: "comfyui", Kind: "comfyui_prompt",
		ExternalID: "prompt-1", Model: "model", PromptCipher: []byte{1}, ResponseCipher: []byte{2}, MetadataCipher: []byte{3},
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	revisionAfterEvent, err := repository.ContentRevision(ctx)
	if err != nil || revisionAfterEvent <= revisionBeforeContent {
		t.Fatalf("content revision after event = %d, before = %d, err=%v", revisionAfterEvent, revisionBeforeContent, err)
	}
	ownership := domain.ComfyOutputOwnership{
		PromptID: "prompt-1", Filename: "result.png", Subfolder: "alice", StorageType: "output", MediaType: "image",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := repository.InsertComfyOutputOwnerships(ctx, registeredUserID, []domain.ComfyOutputOwnership{ownership}); err != nil {
		t.Fatal(err)
	}
	ownerID, known, err := repository.ComfyOutputOwner(ctx, ownership.Filename, ownership.Subfolder, ownership.StorageType)
	if err != nil || !known || ownerID != registeredUserID {
		t.Fatalf("output owner: user=%d known=%v err=%v", ownerID, known, err)
	}
	ownedEventID, err := repository.ComfyOutputEventID(ctx, registeredUserID, ownership.Filename, ownership.Subfolder, ownership.StorageType)
	if err != nil || ownedEventID != eventID {
		t.Fatalf("output event: event=%d err=%v", ownedEventID, err)
	}
	if err := repository.InsertContentMedia(ctx, domain.ContentMediaRecord{
		EventID: eventID, MediaType: "image", MIMEType: "image/png", OriginalName: "result.png",
		Subfolder: "alice", StorageType: "output", PayloadCipher: []byte{4, 5, 6}, SizeBytes: 3, ExpiresAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	revisionAfterMedia, err := repository.ContentRevision(ctx)
	if err != nil || revisionAfterMedia <= revisionAfterEvent {
		t.Fatalf("content revision after media = %d, after event = %d, err=%v", revisionAfterMedia, revisionAfterEvent, err)
	}
	assertRetentionWindow(t, db, "content_events", 7*24*time.Hour)
	assertRetentionWindow(t, db, "content_media", 24*time.Hour)
	if used, err := repository.ContentMediaBytesForUser(ctx, registeredUserID); err != nil || used != 3 {
		t.Fatalf("media usage: bytes=%d err=%v", used, err)
	}
	generatedImages, err := repository.ListUserGenerationImages(ctx, registeredUserID, 10)
	if err != nil || len(generatedImages) != 1 || generatedImages[0].OriginalName != "result.png" || generatedImages[0].ModelName != "model" {
		t.Fatalf("generated image picker media: media=%+v err=%v", generatedImages, err)
	}
	foreignImages, err := repository.ListUserGenerationImages(ctx, adminID, 10)
	if err != nil || len(foreignImages) != 0 {
		t.Fatalf("foreign generated image picker media: media=%+v err=%v", foreignImages, err)
	}
	if _, err := repository.ContentMediaByIDForUser(ctx, generatedImages[0].ID, adminID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign generated image lookup error = %v, want sql.ErrNoRows", err)
	}
	retentionStats, err := repository.ContentRetentionStats(ctx)
	if err != nil || retentionStats.EventCount != 1 || retentionStats.MediaCount != 1 || retentionStats.MediaBytes != 3 || retentionStats.NextEventExpiry == nil || retentionStats.NextMediaExpiry == nil {
		t.Fatalf("content retention stats: stats=%+v err=%v", retentionStats, err)
	}
	media, err := repository.ListContentMediaSummaries(ctx, []int64{eventID})
	if err != nil || len(media[eventID]) != 1 || media[eventID][0].MediaType != "image" {
		t.Fatalf("media summaries: media=%v err=%v", media, err)
	}
	events, err := repository.ListContentEvents(ctx, 10, user.Username, "comfyui")
	if err != nil || len(events) != 1 || events[0].ID != eventID {
		t.Fatalf("filtered content events: events=%v err=%v", events, err)
	}
	deleted, err = repository.DeleteUser(ctx, registeredUserID, user.Username)
	if err != nil || !deleted {
		t.Fatalf("delete user: deleted=%v err=%v", deleted, err)
	}
	if _, err := repository.UserByID(ctx, registeredUserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted user remained readable: %v", err)
	}
	var retainedEvents, anonymizedEvents, retainedMedia int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), count(*) FILTER (WHERE user_id IS NULL)
		FROM content_events WHERE id=$1
	`, eventID).Scan(&retainedEvents, &anonymizedEvents); err != nil || retainedEvents != 1 || anonymizedEvents != 1 {
		t.Fatalf("retained content after user deletion: retained=%d anonymized=%d err=%v", retainedEvents, anonymizedEvents, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM content_media WHERE event_id=$1`, eventID).Scan(&retainedMedia); err != nil || retainedMedia != 1 {
		t.Fatalf("retained media after user deletion: count=%d err=%v", retainedMedia, err)
	}
	retained, err := repository.ListContentEvents(ctx, 10, "Удалённый пользователь", "comfyui")
	if err != nil || len(retained) != 1 || retained[0].ID != eventID || retained[0].Username != "Удалённый пользователь" {
		t.Fatalf("anonymized content listing: events=%v err=%v", retained, err)
	}
	var temporaryID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users(username,password_hash,role,account_expires_at)
		VALUES ('temporary-user','test-hash','user',now() - interval '1 second')
		RETURNING id
	`).Scan(&temporaryID); err != nil {
		t.Fatal(err)
	}
	if deletedCount, err := repository.DeleteExpiredTemporaryUsers(ctx); err != nil || deletedCount != 1 {
		t.Fatalf("delete expired temporary users: count=%d err=%v", deletedCount, err)
	}
	if _, err := repository.UserByID(ctx, temporaryID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired temporary user remained readable: %v", err)
	}
	if deleted, err := repository.DeleteUser(ctx, adminID, "admin"); err != nil || deleted {
		t.Fatalf("admin deletion protection: deleted=%v err=%v", deleted, err)
	}
}

func assertComfyInputLifecycle(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Store, userID int64) {
	t.Helper()
	const (
		reservationID = "0123456789abcdef0123456789abcdef"
		filename      = "gateway-0123456789abcdef0123456789abcdef.png"
		contentHash   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	quota := store.ComfyInputQuota{UserBytes: 1024, GlobalBytes: 2048, UserFiles: 2, GlobalFiles: 4}
	if err := repository.ReserveComfyInputAsset(ctx, userID, reservationID, filename, "gateway/owner", 12, contentHash, quota); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE comfy_input_assets SET expires_at=now() - interval '1 second' WHERE id=$1`, reservationID); err != nil {
		t.Fatal(err)
	}
	items, err := repository.ExpiredComfyInputAssets(ctx, 10)
	if err != nil || len(items) != 1 || items[0].ID != reservationID {
		t.Fatalf("expired reserved ComfyUI input = %#v, err=%v", items, err)
	}
	if deleted, err := repository.DeleteComfyInputAssetsByIDs(ctx, []string{reservationID}); err != nil || deleted != 1 {
		t.Fatalf("delete reserved ComfyUI input = %d, err=%v", deleted, err)
	}
}

func assertComfyOutputCleanupLifecycle(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Store, userID int64) {
	t.Helper()
	const contentHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	scheduled := domain.ComfyOutputCleanupTombstone{
		Filename: "large.mp4", Subfolder: "tests", StorageType: "output",
		SizeBytes: 96 << 20, ContentHash: contentHash,
	}
	if err := repository.ScheduleComfyOutputCleanup(ctx, scheduled, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if due, err := repository.DueComfyOutputCleanup(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("future scheduled cleanup became due: %#v err=%v", due, err)
	}
	if err := repository.ScheduleComfyOutputCleanup(ctx, scheduled, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if due, err := repository.DueComfyOutputCleanup(ctx, 10); err != nil || len(due) != 1 || due[0].Filename != scheduled.Filename {
		t.Fatalf("scheduled cleanup = %#v err=%v", due, err)
	} else if _, err := repository.DeleteComfyOutputCleanupByIDs(ctx, []int64{due[0].ID}); err != nil {
		t.Fatal(err)
	}
	eventID, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: "cleanup-prompt",
		Model: "model", PromptCipher: []byte{1}, ResponseCipher: []byte{2}, MetadataCipher: []byte{3},
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	ownership := domain.ComfyOutputOwnership{PromptID: "cleanup-prompt", Filename: "cleanup.png", Subfolder: "tests", StorageType: "output", MediaType: "image", ExpiresAt: time.Now().Add(24 * time.Hour)}
	if err := repository.InsertComfyOutputOwnerships(ctx, userID, []domain.ComfyOutputOwnership{ownership}); err != nil {
		t.Fatal(err)
	}
	if err := repository.InsertContentMedia(ctx, domain.ContentMediaRecord{
		EventID: eventID, MediaType: "image", MIMEType: "image/png", OriginalName: ownership.Filename,
		Subfolder: ownership.Subfolder, StorageType: ownership.StorageType, PayloadCipher: []byte("encrypted payload"),
		SizeBytes: 12, ContentHash: contentHash, ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	expired, err := repository.ExpiredComfyMedia(ctx, 10)
	if err != nil || len(expired) != 1 || !expired[0].HasOwnership {
		t.Fatalf("expired ComfyUI media = %#v, err=%v", expired, err)
	}
	if deleted, err := repository.QueueExpiredComfyOutputCleanup(ctx, expired); err != nil || deleted != 1 {
		t.Fatalf("queue expired ComfyUI media = %d, err=%v", deleted, err)
	}
	if deleted, err := repository.QueueExpiredComfyOutputCleanup(ctx, expired); err != nil || deleted != 0 {
		t.Fatalf("repeat expired ComfyUI media cleanup = %d, err=%v", deleted, err)
	}
	events, err := repository.ListContentEvents(ctx, 20, "", "comfyui")
	if err != nil {
		t.Fatal(err)
	}
	var archived *domain.ContentEventRow
	for index := range events {
		if events[index].ID == eventID {
			archived = &events[index]
			break
		}
	}
	if archived == nil || archived.GeneratedMediaCount != 1 || archived.MediaCount != 0 || archived.MediaExpiresAt.After(time.Now()) || archived.ExpiresAt.Before(time.Now()) {
		t.Fatalf("archived content event after media expiry = %+v", archived)
	}
	var mediaRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM content_media WHERE event_id=$1`, eventID).Scan(&mediaRows); err != nil || mediaRows != 0 {
		t.Fatalf("expired encrypted payload remains: rows=%d err=%v", mediaRows, err)
	}
	tombstones, err := repository.DueComfyOutputCleanup(ctx, 10)
	if err != nil || len(tombstones) != 1 || tombstones[0].ContentHash != contentHash {
		t.Fatalf("ComfyUI cleanup tombstones = %#v, err=%v", tombstones, err)
	}
	if deferred, err := repository.DeferComfyOutputCleanup(ctx, []int64{tombstones[0].ID}, time.Hour); err != nil || deferred != 1 {
		t.Fatalf("defer ComfyUI output cleanup = %d, err=%v", deferred, err)
	}
	if due, err := repository.DueComfyOutputCleanup(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("deferred tombstone remained due: %#v err=%v", due, err)
	}
	if deleted, err := repository.DeleteComfyOutputCleanupByIDs(ctx, []int64{tombstones[0].ID}); err != nil || deleted != 1 {
		t.Fatalf("delete ComfyUI cleanup tombstone = %d, err=%v", deleted, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM content_events WHERE id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
}

func assertGenerationJobLifecycle(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Store, userID int64) {
	t.Helper()
	const (
		requestID = "integration-job-primary-request"
		publicID  = "job_integration_primary_0001"
		promptID  = "integration-job-prompt-0001"
	)
	var committedUsageDate time.Time
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM content_events WHERE external_id=$1`, promptID); err != nil {
			t.Errorf("clean generation job content event: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM generation_jobs WHERE public_id LIKE 'job_integration_%'`); err != nil {
			t.Errorf("clean generation jobs: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM generation_requests WHERE request_id LIKE 'integration-job-%'`); err != nil {
			t.Errorf("clean generation job requests: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM quick_generation_mining_leases WHERE id='integration-job-lease'`); err != nil {
			t.Errorf("clean generation job mining lease: %v", err)
		}
		if !committedUsageDate.IsZero() {
			if _, err := db.ExecContext(cleanupCtx, `UPDATE quick_generation_daily_usage
				SET used_count=GREATEST(used_count-1,0) WHERE user_id=$1 AND usage_date=$2`, userID, committedUsageDate); err != nil {
				t.Errorf("clean generation job daily usage: %v", err)
			}
			if _, err := db.ExecContext(cleanupCtx, `DELETE FROM quick_generation_daily_usage WHERE user_id=$1 AND used_count=0`, userID); err != nil {
				t.Errorf("clean zero generation job daily usage: %v", err)
			}
			if _, err := db.ExecContext(cleanupCtx, `UPDATE users SET generation_total_used=GREATEST(generation_total_used-1,0) WHERE id=$1`, userID); err != nil {
				t.Errorf("clean generation job total usage: %v", err)
			}
		}
	}()

	revisionBefore, err := repository.GenerationJobRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := repository.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{
		PublicID: publicID, UserID: userID, UsernameSnapshot: "admin", RequestID: requestID,
	})
	if err != nil || !claimed || job.State != domain.GenerationJobDraft {
		t.Fatalf("claim generation job: job=%+v claimed=%v err=%v", job, claimed, err)
	}
	revisionAfterCreate, err := repository.GenerationJobRevision(ctx)
	if err != nil || revisionAfterCreate <= revisionBefore {
		t.Fatalf("generation job revision after create=%d before=%d err=%v", revisionAfterCreate, revisionBefore, err)
	}
	recovered, claimed, err := repository.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{
		PublicID: "job_integration_duplicate_0001", UserID: userID, UsernameSnapshot: "admin", RequestID: requestID,
	})
	if err != nil || claimed || recovered.ID != job.ID || recovered.PublicID != publicID {
		t.Fatalf("recover generation job: job=%+v claimed=%v err=%v", recovered, claimed, err)
	}
	foreignUserID := userID + 1000000
	if _, _, err := repository.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{
		PublicID: "job_integration_foreign_0001", UserID: foreignUserID, UsernameSnapshot: "other",
		RequestID: "integration-job-foreign-request", ParentJobID: &job.ID,
	}); !errors.Is(err, store.ErrGenerationJobParentConflict) {
		t.Fatalf("foreign generation job parent error=%v, want ErrGenerationJobParentConflict", err)
	}

	job, changed, err := repository.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobPreparing, Message: "Проверяем параметры",
	})
	if err != nil || !changed || job.State != domain.GenerationJobPreparing {
		t.Fatalf("prepare transition: job=%+v changed=%v err=%v", job, changed, err)
	}
	job, err = repository.PrepareGenerationJob(ctx, job.ID, domain.PreparedGenerationJob{
		TemplateID: "video", WorkflowID: "minimax-h3-v4", ModelName: "MiniMax H3", Seed: 42,
		PayloadCipher: []byte{1, 2, 3}, Dependencies: []string{" comfyui ", "rife", "comfyui"}, InputCount: 2,
	})
	if err != nil || job.WorkflowID != "minimax-h3-v4" || job.Seed != 42 || len(job.Dependencies) != 2 || job.InputCount != 2 {
		t.Fatalf("prepare generation job payload: job=%+v err=%v", job, err)
	}
	job, changed, err = repository.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobWaitingForResources, Message: "Ожидаем ресурсы",
	})
	if err != nil || !changed || job.State != domain.GenerationJobWaitingForResources {
		t.Fatalf("resource transition: job=%+v changed=%v err=%v", job, changed, err)
	}
	quotaBefore, err := repository.QuickGenerationQuota(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	reservation, reserved, err := repository.ReserveQuickGenerationForJob(ctx, job.ID, userID)
	if err != nil || !reserved || reservation.UsageDate.IsZero() {
		t.Fatalf("reserve generation job quota: reservation=%+v reserved=%v err=%v", reservation, reserved, err)
	}
	repeatedReservation, reserved, err := repository.ReserveQuickGenerationForJob(ctx, job.ID, userID)
	if err != nil || reserved || !repeatedReservation.UsageDate.Equal(reservation.UsageDate) {
		t.Fatalf("repeat generation job quota: reservation=%+v reserved=%v err=%v", repeatedReservation, reserved, err)
	}
	quotaAfterReservation, err := repository.QuickGenerationQuota(ctx, userID)
	if err != nil || quotaAfterReservation.TotalUsed != quotaBefore.TotalUsed+1 || quotaAfterReservation.DailyUsed != quotaBefore.DailyUsed+1 {
		t.Fatalf("generation quota after reservation=%+v before=%+v err=%v", quotaAfterReservation, quotaBefore, err)
	}
	job, requested, err := repository.RequestGenerationJobCancellation(ctx, job.ID, userID)
	if err != nil || !requested || job.CancellationRequestedAt == nil {
		t.Fatalf("request generation job cancellation: job=%+v requested=%v err=%v", job, requested, err)
	}
	if _, requested, err := repository.RequestGenerationJobCancellation(ctx, job.ID, userID); err != nil || requested {
		t.Fatalf("repeat generation job cancellation requested=%v err=%v", requested, err)
	}
	job, cleared, err := repository.ClearGenerationJobCancellation(ctx, job.ID, userID, "Ожидаем ресурсы")
	if err != nil || !cleared || job.CancellationRequestedAt != nil {
		t.Fatalf("clear generation job cancellation: job=%+v cleared=%v err=%v", job, cleared, err)
	}
	var minerID int64
	var minerScript, minerProcess string
	if err := db.QueryRowContext(ctx, `SELECT id,script_path,process_name FROM miners WHERE is_default`).Scan(&minerID, &minerScript, &minerProcess); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateQuickGenerationMiningLease(ctx, domain.QuickGenerationMiningLease{
		ID: "integration-job-lease", GenerationJobID: job.ID, UserID: userID, MinerID: minerID,
		ScriptPath: minerScript, ProcessName: minerProcess, ResumeMining: false,
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := repository.QuickGenerationMiningLeaseByJobID(ctx, job.ID)
	if err != nil || lease.GenerationJobID != job.ID || lease.PromptID != "" {
		t.Fatalf("generation job mining lease=%+v err=%v", lease, err)
	}
	if attached, err := repository.AttachQuickGenerationMiningLease(ctx, lease.ID, promptID); err != nil || !attached {
		t.Fatalf("attach generation job mining lease=%v err=%v", attached, err)
	}
	job, err = repository.BindGenerationJobPrompt(ctx, job.ID, promptID)
	if err != nil || job.PromptID != promptID {
		t.Fatalf("bind generation job prompt: job=%+v err=%v", job, err)
	}
	revisionAfterBind, err := repository.GenerationJobRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BindGenerationJobPrompt(ctx, job.ID, promptID); err != nil {
		t.Fatalf("idempotent generation job prompt bind: %v", err)
	}
	revisionAfterRepeatedBind, err := repository.GenerationJobRevision(ctx)
	if err != nil || revisionAfterRepeatedBind != revisionAfterBind {
		t.Fatalf("idempotent prompt bind revision=%d want=%d err=%v", revisionAfterRepeatedBind, revisionAfterBind, err)
	}
	committed, err := repository.CommitQuickGenerationForJob(ctx, job.ID)
	if err != nil || !committed {
		t.Fatalf("commit generation job quota=%v err=%v", committed, err)
	}
	committedUsageDate = reservation.UsageDate
	if committed, err := repository.CommitQuickGenerationForJob(ctx, job.ID); err != nil || committed {
		t.Fatalf("repeat generation job quota commit=%v err=%v", committed, err)
	}
	if releasedQuota, err := repository.ReleaseQuickGenerationForJob(ctx, job.ID); err != nil || releasedQuota {
		t.Fatalf("committed generation job quota released=%v err=%v", releasedQuota, err)
	}
	var linkedJobID int64
	if err := db.QueryRowContext(ctx, `SELECT job_id FROM generation_requests WHERE user_id=$1 AND request_id=$2`, userID, requestID).Scan(&linkedJobID); err != nil || linkedJobID != job.ID {
		t.Fatalf("generation request job link=%d want=%d err=%v", linkedJobID, job.ID, err)
	}
	if err := repository.InsertGenerationVariant(ctx, userID, promptID, "video", "minimax-h3-v4", "MiniMax H3", 42, []byte{4}); err != nil {
		t.Fatal(err)
	}
	if err := repository.LinkGenerationJobVariant(ctx, job.ID, promptID); err != nil {
		t.Fatal(err)
	}
	eventID, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: promptID,
		Model: "MiniMax H3", GenerationState: "queued", PromptCipher: []byte{5}, ResponseCipher: []byte{6},
		MetadataCipher: []byte{7}, ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.LinkGenerationJobContentEvent(ctx, job.ID, userID, promptID); err != nil {
		t.Fatal(err)
	}
	duplicateEventID, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: userID, GenerationJobID: &job.ID, Service: "comfyui", Kind: "comfyui_prompt", ExternalID: promptID,
		Model: "MiniMax H3", GenerationState: "queued", PromptCipher: []byte{5}, ResponseCipher: []byte{6},
		MetadataCipher: []byte{7}, ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil || duplicateEventID != eventID {
		t.Fatalf("idempotent generation content projection id=%d want=%d err=%v", duplicateEventID, eventID, err)
	}
	var variantJobID, contentJobID int64
	if err := db.QueryRowContext(ctx, `SELECT job_id FROM quick_generation_variants WHERE prompt_id=$1`, promptID).Scan(&variantJobID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT generation_job_id FROM content_events WHERE id=$1`, eventID).Scan(&contentJobID); err != nil {
		t.Fatal(err)
	}
	if variantJobID != job.ID || contentJobID != job.ID {
		t.Fatalf("generation job projections: variant=%d content=%d want=%d", variantJobID, contentJobID, job.ID)
	}

	for _, transition := range []domain.GenerationJobTransitionParams{
		{State: domain.GenerationJobQueued, Message: "В очереди ComfyUI"},
		{State: domain.GenerationJobRunning, Message: "Выполняем workflow"},
		{State: domain.GenerationJobPostprocessing, Message: "Обрабатываем результат"},
		{State: domain.GenerationJobArchiving, Message: "Сохраняем результат"},
	} {
		job, changed, err = repository.TransitionGenerationJob(ctx, job.ID, transition)
		if err != nil || !changed || job.State != transition.State {
			t.Fatalf("generation job transition to %s: job=%+v changed=%v err=%v", transition.State, job, changed, err)
		}
	}
	if _, _, err := repository.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobCompleted, Message: "Готово",
	}); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatalf("completion before resource release error=%v", err)
	}
	if _, _, err := repository.RequestGenerationJobCancellation(ctx, job.ID, userID); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatalf("terminal generation job cancellation error=%v", err)
	}
	if _, err := repository.MarkGenerationJobResourcesReleased(ctx, job.ID); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatalf("resource release with active mining lease error=%v", err)
	}
	if _, _, err := repository.DeleteQuickGenerationMiningLease(ctx, "integration-job-lease"); err != nil {
		t.Fatalf("delete generation job mining lease: %v", err)
	}
	revisionBeforeRelease, err := repository.GenerationJobRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released, err := repository.MarkGenerationJobResourcesReleased(ctx, job.ID)
	if err != nil || released.ResourcesReleasedAt == nil {
		t.Fatalf("release generation job resources: job=%+v err=%v", released, err)
	}
	revisionAfterRelease, err := repository.GenerationJobRevision(ctx)
	if err != nil || revisionAfterRelease <= revisionBeforeRelease {
		t.Fatalf("resource release revision=%d before=%d err=%v", revisionAfterRelease, revisionBeforeRelease, err)
	}
	releasedAgain, err := repository.MarkGenerationJobResourcesReleased(ctx, job.ID)
	if err != nil || releasedAgain.ResourcesReleasedAt == nil {
		t.Fatalf("repeat generation job resource release: job=%+v err=%v", releasedAgain, err)
	}
	revisionAfterRepeatedRelease, err := repository.GenerationJobRevision(ctx)
	if err != nil || revisionAfterRepeatedRelease != revisionAfterRelease {
		t.Fatalf("idempotent resource release revision=%d want=%d err=%v", revisionAfterRepeatedRelease, revisionAfterRelease, err)
	}
	job, changed, err = repository.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobCompleted, Message: "Готово",
	})
	if err != nil || !changed || job.State != domain.GenerationJobCompleted || job.FinishedAt == nil {
		t.Fatalf("complete generation job: job=%+v changed=%v err=%v", job, changed, err)
	}
	if _, changed, err := repository.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobCompleted, Message: "Готово",
	}); err != nil || changed {
		t.Fatalf("idempotent terminal observation changed=%v err=%v", changed, err)
	}
	if _, _, err := repository.TransitionGenerationJob(ctx, job.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobCompleted, Message: "Перезаписанное состояние",
	}); !errors.Is(err, store.ErrGenerationJobStateConflict) {
		t.Fatalf("terminal generation job mutation error=%v", err)
	}

	transitions, err := repository.GenerationJobTransitions(ctx, job.ID, userID)
	if err != nil || len(transitions) != 8 || transitions[0].ToState != domain.GenerationJobDraft || transitions[len(transitions)-1].ToState != domain.GenerationJobCompleted {
		t.Fatalf("generation job transitions=%+v err=%v", transitions, err)
	}
	if _, err := repository.GenerationJobByPublicID(ctx, userID+1, publicID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign generation job lookup error=%v, want sql.ErrNoRows", err)
	}
	listed, err := repository.ListGenerationJobs(ctx, userID, 20, time.Now().Add(-time.Hour))
	if err != nil || len(listed) == 0 || listed[0].ID != job.ID {
		t.Fatalf("list generation jobs=%+v err=%v", listed, err)
	}
	active, err := repository.ListActiveGenerationJobs(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, activeJob := range active {
		if activeJob.ID == job.ID {
			t.Fatalf("completed job remained active: %+v", activeJob)
		}
	}

	retry, created, err := repository.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{
		PublicID: "job_integration_retry_0001", UserID: userID, UsernameSnapshot: "admin",
		RequestID: "integration-job-retry-request", ParentJobID: &job.ID,
	})
	if err != nil || !created || retry.ParentJobID == nil || *retry.ParentJobID != job.ID {
		t.Fatalf("create retry generation job: job=%+v created=%v err=%v", retry, created, err)
	}
	for _, transition := range []domain.GenerationJobTransitionParams{
		{State: domain.GenerationJobPreparing, Message: "Проверяем повтор"},
		{State: domain.GenerationJobWaitingForResources, Message: "Ожидаем ресурсы"},
	} {
		retry, changed, err = repository.TransitionGenerationJob(ctx, retry.ID, transition)
		if err != nil || !changed {
			t.Fatalf("prepare retry generation job: job=%+v changed=%v err=%v", retry, changed, err)
		}
	}
	if _, reserved, err := repository.ReserveQuickGenerationForJob(ctx, retry.ID, userID); err != nil || !reserved {
		t.Fatalf("reserve retry generation quota=%v err=%v", reserved, err)
	}
	if releasedQuota, err := repository.ReleaseQuickGenerationForJob(ctx, retry.ID); err != nil || !releasedQuota {
		t.Fatalf("release uncommitted retry quota=%v err=%v", releasedQuota, err)
	}
	if _, err := repository.MarkGenerationJobResourcesReleased(ctx, retry.ID); err != nil {
		t.Fatal(err)
	}
	retry, changed, err = repository.TransitionGenerationJob(ctx, retry.ID, domain.GenerationJobTransitionParams{
		State: domain.GenerationJobFailed, Message: "Повтор не запущен", ErrorCode: "integration_retry_failed", ErrorMessage: "test failure",
	})
	if err != nil || !changed || retry.State != domain.GenerationJobFailed || retry.ErrorCode != "integration_retry_failed" {
		t.Fatalf("fail retry generation job: job=%+v changed=%v err=%v", retry, changed, err)
	}
}

func resetIntegrationDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		TRUNCATE miners, comfy_output_cleanup_tombstones, comfy_input_assets, comfy_userdata, comfy_settings, comfy_output_ownership, content_media, content_events, websocket_sessions, proxy_requests,
			audit_log, invite_uses, invites, sessions, users RESTART IDENTITY CASCADE
		;
		INSERT INTO miners (name, script_path, process_name, enabled, is_default)
		VALUES ('Example miner', 'mining-root/example/start-mining.bat', 'miner.exe', true, true)
	`); err != nil {
		t.Fatal(err)
	}
}

func assertDatabaseRetentionLifecycle(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Store, userID int64) {
	t.Helper()
	now := time.Now().UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms,created_at)
		 SELECT $1,'comfyui','GET','/retention/old/' || value,200,1,now() - interval '72 hours'
		 FROM generate_series(1,101) value`,
		`INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms,created_at)
		 VALUES ($1,'comfyui','GET','/retention/current',200,1,now())`,
		`INSERT INTO websocket_sessions(user_id,service,opened_at,closed_at,duration_ms,client_ip)
		 VALUES ($1,'comfyui',now() - interval '72 hours',now() - interval '71 hours',3600000,'retention-test'),
		        ($1,'comfyui',now(),NULL,NULL,'retention-test')`,
		`INSERT INTO generation_requests(user_id,request_id,prompt_id,created_at,updated_at)
		 VALUES ($1,'retention-old-request','retention-old-prompt',now() - interval '72 hours',now() - interval '72 hours'),
		        ($1,'retention-current-request','retention-current-prompt',now(),now())`,
		`INSERT INTO quick_generation_daily_usage(user_id,usage_date,used_count)
		 VALUES ($1,current_date - 3,2),($1,current_date,1)`,
		`INSERT INTO invites(token_hash,created_by_user_id,max_uses,expires_at,created_at)
		 VALUES ('retention-old-invite',$1,1,now() - interval '72 hours',now() - interval '96 hours'),
		        ('retention-current-invite',$1,1,now() + interval '24 hours',now())`,
		`INSERT INTO audit_log(actor_user_id,action,target_type,created_at)
		 VALUES ($1,'retention_old','system',now() - interval '72 hours'),
		        ($1,'retention_current','system',now())`,
		`INSERT INTO host_metrics(recorded_at,cpu_percent,memory_used_bytes,memory_total_bytes)
		 VALUES (now() - interval '72 hours',91,1,2),(now(),92,1,2)`,
		`INSERT INTO quick_generation_variants(user_id,prompt_id,seed,payload_cipher,state,created_at,finished_at,state_changed_at)
		 VALUES ($1,'retention-old-variant',1,decode('00','hex'),'completed',now() - interval '72 hours',now() - interval '71 hours',now() - interval '71 hours'),
		        ($1,'retention-current-variant',2,decode('00','hex'),'completed',now(),now(),now()),
		        ($1,'retention-active-variant',3,decode('00','hex'),'running',now() - interval '72 hours',NULL,now())`,
		`INSERT INTO generation_jobs(public_id,user_id,username_snapshot,request_id,state,status_message,state_changed_at,finished_at,resources_released_at,created_at,updated_at)
		 VALUES ('job_retention_old_0001',$1,'admin','retention-job-old','completed','Готово',now() - interval '71 hours',now() - interval '71 hours',now() - interval '71 hours',now() - interval '72 hours',now() - interval '71 hours'),
		        ('job_retention_current_0001',$1,'admin','retention-job-current','completed','Готово',now(),now(),now(),now(),now()),
		        ('job_retention_active_0001',$1,'admin','retention-job-active','running','Выполняем workflow',now(),NULL,NULL,now() - interval '72 hours',now())`,
		`WITH event AS (
			INSERT INTO content_events(user_id,service,kind,external_id,prompt_cipher,response_cipher,metadata_cipher,expires_at)
			VALUES ($1,'comfyui','comfyui_prompt','retention-ownership-event',decode('00','hex'),decode('00','hex'),decode('00','hex'),now() + interval '7 days')
			RETURNING id
		 )
		 INSERT INTO comfy_output_ownership(event_id,user_id,prompt_id,filename,subfolder,storage_type,media_type,created_at,expires_at)
		 SELECT id,$1,'retention-ownership-event','old.png','retention','output','image',now() - interval '72 hours',now() - interval '48 hours' FROM event`,
		`INSERT INTO comfy_output_ownership(event_id,user_id,prompt_id,filename,subfolder,storage_type,media_type,created_at,expires_at)
		 SELECT id,$1,'retention-ownership-event','current.png','retention','output','image',now(),now() + interval '24 hours'
		 FROM content_events WHERE external_id='retention-ownership-event'`,
	}
	for _, statement := range statements {
		args := []any(nil)
		if strings.Contains(statement, "$1") {
			args = append(args, userID)
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	defer cleanupDatabaseRetentionFixtures(t, db, userID)

	cutoff := now.Add(-24 * time.Hour)
	cutoffs := domain.DatabaseRetentionCutoffs{
		ProxyRequests: cutoff, WebSocketSessions: cutoff, GenerationRequests: cutoff,
		GenerationJobs: cutoff,
		DailyUsage:     cutoff, InviteHistory: cutoff, AuditLog: cutoff, HostMetrics: cutoff,
		GenerationVariants: cutoff, OutputOwnerships: now,
	}
	first, err := repository.CleanupDatabaseRetention(ctx, cutoffs, 100, 1)
	if err != nil || first.Status != "ok" {
		t.Fatalf("first database retention cleanup: report=%+v err=%v", first, err)
	}
	for table, expected := range map[string]int64{
		"proxy_requests": 100, "websocket_sessions": 1, "generation_requests": 1,
		"quick_generation_daily_usage": 1, "invites": 1, "audit_log": 1,
		"host_metrics": 1, "quick_generation_variants": 1, "comfy_output_ownership": 1,
		"generation_jobs": 1,
	} {
		if first.DeletedRows[table] != expected {
			t.Fatalf("first cleanup deleted %s=%d, want %d", table, first.DeletedRows[table], expected)
		}
	}
	var remainingOldProxy int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM proxy_requests WHERE path LIKE '/retention/old/%'`).Scan(&remainingOldProxy); err != nil || remainingOldProxy != 1 {
		t.Fatalf("bounded proxy cleanup left %d old rows, err=%v", remainingOldProxy, err)
	}

	second, err := repository.CleanupDatabaseRetention(ctx, cutoffs, 100, 2)
	if err != nil || second.DeletedRows["proxy_requests"] != 1 {
		t.Fatalf("second database retention cleanup: report=%+v err=%v", second, err)
	}
	if err := repository.SaveDatabaseCleanupState(ctx, second); err != nil {
		t.Fatal(err)
	}
	state, err := repository.DatabaseCleanupState(ctx)
	if err != nil || state.Status != "ok" || state.DeletedRows["proxy_requests"] != 1 || state.LastSuccessAt == nil {
		t.Fatalf("database cleanup state=%+v err=%v", state, err)
	}

	third, err := repository.CleanupDatabaseRetention(ctx, cutoffs, 100, 2)
	if err != nil || third.TotalDeleted() != 0 {
		t.Fatalf("idempotent database retention cleanup: report=%+v err=%v", third, err)
	}
	for query, expected := range map[string]int{
		`SELECT count(*) FROM proxy_requests WHERE path='/retention/current'`:                                                               1,
		`SELECT count(*) FROM websocket_sessions WHERE closed_at IS NULL`:                                                                   1,
		`SELECT count(*) FROM generation_requests WHERE request_id='retention-current-request'`:                                             1,
		`SELECT count(*) FROM quick_generation_daily_usage WHERE user_id=` + strconv.FormatInt(userID, 10) + ` AND usage_date=current_date`: 1,
		`SELECT count(*) FROM invites WHERE token_hash='retention-current-invite'`:                                                          1,
		`SELECT count(*) FROM audit_log WHERE action='retention_current'`:                                                                   1,
		`SELECT count(*) FROM host_metrics WHERE recorded_at >= now() - interval '1 hour'`:                                                  1,
		`SELECT count(*) FROM quick_generation_variants WHERE prompt_id IN ('retention-current-variant','retention-active-variant')`:        2,
		`SELECT count(*) FROM generation_jobs WHERE request_id IN ('retention-job-current','retention-job-active')`:                         2,
		`SELECT count(*) FROM comfy_output_ownership WHERE filename='current.png'`:                                                          1,
	} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != expected {
			t.Fatalf("live retention query %q count=%d want=%d err=%v", query, count, expected, err)
		}
	}
	stats, err := repository.DatabaseTableStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundProxy, foundState := false, false
	for _, item := range stats {
		foundProxy = foundProxy || item.Name == "proxy_requests" && item.TotalBytes > 0 && item.OldestAt != nil
		foundState = foundState || item.Name == "database_cleanup_state" && item.TotalBytes > 0
	}
	if !foundProxy || !foundState {
		t.Fatalf("database table stats missing expected rows: proxy=%v state=%v", foundProxy, foundState)
	}
	visited := 0
	if err := repository.VisitAuditBefore(ctx, now.Add(time.Hour), func(row domain.AuditRow) error {
		if row.Action == "retention_current" {
			visited++
		}
		return nil
	}); err != nil || visited != 1 {
		t.Fatalf("visit retained audit rows: visited=%d err=%v", visited, err)
	}
}

func cleanupDatabaseRetentionFixtures(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM proxy_requests WHERE path LIKE '/retention/%'`, nil},
		{`DELETE FROM websocket_sessions WHERE client_ip='retention-test'`, nil},
		{`DELETE FROM generation_requests WHERE request_id IN ('retention-old-request','retention-current-request')`, nil},
		{`DELETE FROM quick_generation_daily_usage WHERE user_id=$1 AND usage_date IN (current_date - 3,current_date)`, []any{userID}},
		{`DELETE FROM invites WHERE token_hash IN ('retention-old-invite','retention-current-invite')`, nil},
		{`DELETE FROM audit_log WHERE action IN ('retention_old','retention_current')`, nil},
		{`DELETE FROM host_metrics WHERE cpu_percent IN (91,92) AND memory_total_bytes=2`, nil},
		{`DELETE FROM quick_generation_variants WHERE prompt_id IN ('retention-old-variant','retention-current-variant','retention-active-variant')`, nil},
		{`DELETE FROM generation_jobs WHERE request_id IN ('retention-job-old','retention-job-current','retention-job-active')`, nil},
		{`DELETE FROM comfy_output_ownership WHERE prompt_id='retention-ownership-event'`, nil},
		{`DELETE FROM content_events WHERE external_id='retention-ownership-event'`, nil},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Errorf("clean retention fixture: %v", err)
			return
		}
	}
}

func assertComfyUserStateIsolation(t *testing.T, ctx context.Context, repository *store.Store, userID, otherUserID int64) {
	t.Helper()
	const dataPath = "workflows/shared-name.json"
	firstPayload := []byte(`{"workflow":"alice"}`)
	secondPayload := []byte(`{"workflow":"admin"}`)
	if _, err := repository.PutComfyUserData(ctx, userID, dataPath, firstPayload, false, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PutComfyUserData(ctx, otherUserID, dataPath, secondPayload, false, 1024); err != nil {
		t.Fatal(err)
	}
	got, _, err := repository.ComfyUserData(ctx, userID, dataPath)
	if err != nil || string(got) != string(firstPayload) {
		t.Fatalf("first user's data = %q, err=%v", got, err)
	}
	got, _, err = repository.ComfyUserData(ctx, otherUserID, dataPath)
	if err != nil || string(got) != string(secondPayload) {
		t.Fatalf("second user's data = %q, err=%v", got, err)
	}
	if _, err := repository.PutComfyUserData(ctx, userID, dataPath, []byte("duplicate"), false, 1024); !errors.Is(err, store.ErrComfyDataExists) {
		t.Fatalf("overwrite=false error = %v", err)
	}
	if _, err := repository.PutComfyUserData(ctx, userID, "workflows/too-large.json", make([]byte, 100), true, int64(len(firstPayload))); !errors.Is(err, store.ErrComfyDataQuota) {
		t.Fatalf("quota error = %v", err)
	}
	movedPath := "workflows/moved.json"
	if _, err := repository.MoveComfyUserData(ctx, userID, dataPath, movedPath, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ComfyUserData(ctx, userID, dataPath); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("source remained after move: %v", err)
	}
	if err := repository.DeleteComfyUserData(ctx, userID, movedPath); err != nil {
		t.Fatal(err)
	}

	if err := repository.MergeComfySettings(ctx, userID, json.RawMessage(`{"theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MergeComfySettings(ctx, otherUserID, json.RawMessage(`{"theme":"light"}`)); err != nil {
		t.Fatal(err)
	}
	firstSettings, err := repository.ComfySettings(ctx, userID)
	if err != nil || !strings.Contains(string(firstSettings), `"dark"`) {
		t.Fatalf("first settings = %s, err=%v", firstSettings, err)
	}
	secondSettings, err := repository.ComfySettings(ctx, otherUserID)
	if err != nil || !strings.Contains(string(secondSettings), `"light"`) {
		t.Fatalf("second settings = %s, err=%v", secondSettings, err)
	}
}

func assertMiningProfiles(t *testing.T, ctx context.Context, repository *store.Store, adminID int64) {
	t.Helper()
	initial, err := repository.DefaultMiner(ctx)
	if err != nil || initial.ProcessName != "miner.exe" || initial.ScriptPath != `mining-root/example/start-mining.bat` {
		t.Fatalf("initial mining profile = %+v, err=%v", initial, err)
	}
	icon := []byte{0x89, 'P', 'N', 'G'}
	id, err := repository.CreateMiner(ctx, store.CreateMinerParams{
		Name: "Integration miner", ScriptPath: `mining-root/integration/start.bat`, ProcessName: "integration-miner.exe",
		IconMIME: "image/png", IconData: icon, Enabled: true, CreatedByUserID: adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := repository.SetDefaultMiner(ctx, id); err != nil || !changed {
		t.Fatalf("set default miner: changed=%v err=%v", changed, err)
	}
	selected, err := repository.DefaultMiner(ctx)
	if err != nil || selected.ID != id {
		t.Fatalf("selected miner = %+v, err=%v", selected, err)
	}
	storedIcon, err := repository.MinerIcon(ctx, id)
	if err != nil || storedIcon.MIME != "image/png" || string(storedIcon.Data) != string(icon) {
		t.Fatalf("stored icon = %+v, err=%v", storedIcon, err)
	}
	if changed, err := repository.SetMinerEnabled(ctx, initial.ID, false); err != nil || !changed {
		t.Fatalf("disable non-default miner: changed=%v err=%v", changed, err)
	}
	if _, err := repository.SetMinerEnabled(ctx, id, false); !errors.Is(err, store.ErrDefaultMinerRequired) {
		t.Fatalf("disable default miner error = %v", err)
	}
}

func assertRetentionWindow(t *testing.T, db *sql.DB, table string, expected time.Duration) {
	t.Helper()
	var seconds float64
	query := `SELECT EXTRACT(EPOCH FROM (expires_at-created_at)) FROM ` + table + ` ORDER BY id DESC LIMIT 1`
	if err := db.QueryRow(query).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if delta := time.Duration(seconds*float64(time.Second)) - expected; delta < -time.Second || delta > time.Second {
		t.Fatalf("%s retention = %s, want %s", table, time.Duration(seconds*float64(time.Second)), expected)
	}
}
