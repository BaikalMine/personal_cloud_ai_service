package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestTaskNotificationsAndCaptionSeriesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	repo := store.New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	for name, dest := range map[string]*int64{"notice-owner": &owner, "notice-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES($1,'disabled','user') RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES('notice-dataset',$1,'Notice photos','x')`, owner)
	var datasetRevision int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM lora_datasets WHERE id='notice-dataset'`).Scan(&datasetRevision); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height) VALUES('notice-asset',$1,'photo',repeat('a',64),'image/png',1,256,256)`, owner)
	newJobs := func(prefix string, count int) []domain.LoraCaptionJob {
		jobs := []domain.LoraCaptionJob{}
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("%s-%d", prefix, i)
			hash := sha256.Sum256([]byte(id))
			jobs = append(jobs, domain.LoraCaptionJob{ID: id, ImageID: id, AssetID: "notice-asset", RequestKey: hex.EncodeToString(hash[:]), InstructionVersion: "v1", InputCipher: []byte("encrypted-fixture")})
		}
		return jobs
	}
	enqueue := func(jobs []domain.LoraCaptionJob) []domain.LoraCaptionJob {
		t.Helper()
		result, err := repo.EnqueueLoraCaptions(ctx, owner, "notice-dataset", datasetRevision, jobs)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	series := func(id string) domain.LoraCaptionSeries {
		t.Helper()
		value, err := repo.LoraCaptionSeries(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	notices := func() []domain.UserNotification {
		t.Helper()
		items, err := repo.ListUserNotifications(ctx, owner, 100)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	summary := func() domain.UserNotificationSummary {
		t.Helper()
		value, err := repo.UserNotificationSummary(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	jobs := newJobs("notice-first", 3)
	created := enqueue(jobs)
	id := created[0].SeriesID
	if id == "" || summary().ActiveCount != 3 || len(notices()) != 0 {
		t.Fatal("active series announced completion or lost count")
	}
	duplicate := enqueue([]domain.LoraCaptionJob{jobs[2], jobs[0], jobs[1], jobs[0]})
	if duplicate[0].SeriesID != id || series(id).TotalCount != 3 {
		t.Fatal("replayed enqueue duplicated membership")
	}
	if _, err := repo.LoraCaptionSeries(ctx, other, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign owner read series")
	}
	// Concurrent terminal transitions must settle exactly one immutable aggregate.
	results := make(chan error, 3)
	for i, job := range created {
		go func(i int, job domain.LoraCaptionJob) {
			state := "completed"
			if i == 2 {
				state = "failed"
			}
			_, err := db.ExecContext(ctx, `UPDATE lora_caption_jobs SET state=$2 WHERE id=$1`, job.ID, state)
			results <- err
		}(i, job)
	}
	for range created {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	finished := series(id)
	if finished.State != "partial" || finished.CompletedCount != 2 || finished.FailedCount != 1 || finished.FinishedAt == nil {
		t.Fatalf("aggregate: %+v", finished)
	}
	if items := notices(); len(items) != 1 || items[0].SourceKind != "lora_caption_series" || items[0].SourceID != id || items[0].Kind != domain.NotificationCaptionSeriesFailed {
		t.Fatalf("series notification: %+v", items)
	}
	if summary().ActiveCount != 0 || summary().UnreadCount != 1 {
		t.Fatal("incorrect terminal counters")
	}
	if again := enqueue(jobs); again[0].SeriesID != id || len(notices()) != 1 {
		t.Fatal("reconnect created another notification")
	}
	before := summary().Revision
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lora_caption_jobs SET state='queued' WHERE id=$1`, created[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if summary().Revision != before {
		t.Fatal("rolled back transition changed revision")
	}
	retry, err := repo.RetryLoraCaption(ctx, owner, created[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.RunVersion != 2 || retry.SeriesID == id || retry.SeriesID == "" {
		t.Fatal("retry did not create a new attempt series")
	}
	if old := series(id); old.FailedCount != 1 || old.Members[2].Available {
		t.Fatal("retry rewrote old membership")
	}
	exec(`UPDATE lora_caption_jobs SET state='completed' WHERE id=$1`, retry.ID)
	if len(notices()) != 2 || series(retry.SeriesID).State != "completed" {
		t.Fatal("retry result missing")
	}
	exec(`UPDATE lora_caption_jobs SET status='same terminal state' WHERE id=$1`, retry.ID)
	if len(notices()) != 2 {
		t.Fatal("duplicate terminal write notified again")
	}
	if _, err := repo.MarkUserNotificationRead(ctx, other, notices()[0].ID); err != nil {
		t.Fatal(err)
	}
	if summary().UnreadCount != 2 {
		t.Fatal("foreign mark-read changed owner state")
	}
	if _, err := repo.MarkAllUserNotificationsRead(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if summary().UnreadCount != 0 {
		t.Fatal("mark all failed")
	}
	if _, _, err := repo.SetUserNotificationPreferences(ctx, owner, domain.UserNotificationPreferences{InAppEnabled: true, SuccessEnabled: false}); err != nil {
		t.Fatal(err)
	}
	quiet := enqueue(newJobs("notice-success-muted", 1))[0]
	exec(`UPDATE lora_caption_jobs SET state='completed' WHERE id=$1`, quiet.ID)
	if len(notices()) != 2 {
		t.Fatal("success preference ignored")
	}
	makeTraining := func(id string) domain.LoraTrainingJob {
		t.Helper()
		job, err := repo.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: id, RequestID: id, UserID: owner, ProfileID: "profile", Family: "krea2", BaseModel: "base", Name: "Training", OutputName: "notice_lora", TriggerWord: "subject", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100, NetworkDim: 8, NetworkAlpha: 8, LearningRate: 0.0001, SampleCount: 5, DatasetBytes: 1, DatasetPath: "fixture.zip"})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	training := makeTraining("notice-training-failed-1")
	if summary().ActiveCount != 1 {
		t.Fatal("training missing from active count")
	}
	exec(`UPDATE lora_training_jobs SET state='failed' WHERE id=$1`, training.ID)
	items := notices()
	if len(items) != 3 || items[0].Kind != domain.NotificationTrainingFailed || items[0].SourceID != training.PublicID {
		t.Fatalf("training notice: %+v", items)
	}
	exec(`UPDATE lora_training_jobs SET state='failed',message='same failure' WHERE id=$1`, training.ID)
	if len(notices()) != 3 {
		t.Fatal("training replay duplicated notification")
	}
	success := makeTraining("notice-training-success-1")
	exec(`UPDATE lora_training_jobs SET state='completed' WHERE id=$1`, success.ID)
	if len(notices()) != 3 {
		t.Fatal("training success preference ignored")
	}
	if _, _, err := repo.SetUserNotificationPreferences(ctx, owner, domain.UserNotificationPreferences{}); err != nil {
		t.Fatal(err)
	}
	muted := enqueue(newJobs("notice-all-muted", 1))[0]
	exec(`UPDATE lora_caption_jobs SET state='failed' WHERE id=$1`, muted.ID)
	if len(notices()) != 3 || summary().UnreadCount != 0 {
		t.Fatal("disabled in-app preference ignored")
	}
	// Retained membership remains truthful after its encrypted source is deleted.
	exec(`DELETE FROM lora_caption_jobs WHERE id=$1`, created[0].ID)
	if old := series(id); old.CompletedCount != 2 || old.Members[0].Available {
		t.Fatal("cleanup changed recorded result")
	}
	exec(`UPDATE lora_caption_series SET expires_at=now()-interval '1 second' WHERE id=$1`, id)
	if _, err := repo.CleanupLoraCaptionJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoraCaptionSeries(ctx, owner, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired series retained")
	}
	for _, notice := range notices() {
		if !strings.HasPrefix(notice.Href, "/tasks?task=") {
			t.Fatal("new notice has no task destination")
		}
	}
	enqueue(newJobs("notice-active-at-owner-delete", 2))
	exec(`DELETE FROM users WHERE id=$1`, owner)
	var left int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM lora_caption_series WHERE user_id=$1`, owner).Scan(&left); err != nil || left != 0 {
		t.Fatal("deleted owner retained series")
	}
}
