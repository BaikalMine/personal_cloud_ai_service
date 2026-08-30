package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
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

	eventID, err := repository.InsertContentEvent(ctx, domain.ContentEventRecord{
		UserID: registeredUserID, Service: "comfyui", Kind: "comfyui_prompt",
		ExternalID: "prompt-1", Model: "model", PromptCipher: []byte{1}, ResponseCipher: []byte{2}, MetadataCipher: []byte{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownership := domain.ComfyOutputOwnership{
		PromptID: "prompt-1", Filename: "result.png", Subfolder: "alice", StorageType: "output", MediaType: "image",
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
	assertRetentionWindow(t, db, "content_events", 7*24*time.Hour)
	assertRetentionWindow(t, db, "content_media", 24*time.Hour)
	if used, err := repository.ContentMediaBytesForUser(ctx, registeredUserID); err != nil || used != 3 {
		t.Fatalf("media usage: bytes=%d err=%v", used, err)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	ownership := domain.ComfyOutputOwnership{PromptID: "cleanup-prompt", Filename: "cleanup.png", Subfolder: "tests", StorageType: "output", MediaType: "image"}
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
