package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAdminTrainingHistoryAndGPUScopeIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	r := store.New(db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`TRUNCATE gpu_resources CASCADE`)
	var owner, other int64
	for name, id := range map[string]*int64{"training-owner": &owner, "training-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES($1,'disabled','user') RETURNING id`, name).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO lora_training_jobs(public_id,user_id,username_snapshot,request_id,profile_id,family,base_model,name,output_name,trigger_word,concept_type,preset,resolution,max_train_steps,network_dim,network_alpha,learning_rate,seed,sample_count,dataset_bytes,dataset_path,state,artifact_name,artifact_bytes,created_at)
 SELECT 'admin-training-'||lpad(n::text,4,'0'),$1,'training-owner','admin-request-'||lpad(n::text,4,'0'),'profile-'||n%2,CASE WHEN n%2=0 THEN 'krea2' ELSE 'flux2-klein' END,'base.safetensors','Training '||n,'training_'||n,'subject','object','quick',768,800,16,16,0.0001,1,10,1024,'private-dataset-path','completed','training_'||n||'.safetensors',n*1024,'2026-01-01'::timestamptz+n*interval '1 minute' FROM generate_series(1,213) n`, owner)
	exec(`UPDATE lora_training_jobs SET name='Literal %_ needle',user_id=NULL,username_snapshot='deleted-owner' WHERE public_id='admin-training-0001'`)
	exec(`UPDATE lora_training_jobs SET state='queued',artifact_name='',artifact_bytes=0 WHERE public_id='admin-training-0212'`)
	exec(`UPDATE lora_training_jobs SET state='running',artifact_name='',artifact_bytes=0,user_id=$1,username_snapshot='training-other' WHERE public_id='admin-training-0213'`, other)
	exec(`UPDATE lora_training_jobs SET state='failed',artifact_name='',artifact_bytes=0 WHERE public_id='admin-training-0211'`)
	read := func(f store.AdminLoraTrainingFilter) store.AdminLoraTrainingPage {
		t.Helper()
		p, err := r.ListAdminLoraTrainingPage(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := read(store.AdminLoraTrainingFilter{})
	if p.Total != 213 || p.Active != 2 || p.Failed != 1 || p.Completed != 210 || p.ArtifactBytes != 210*211/2*1024 || p.Now.IsZero() {
		t.Fatalf("summary: %+v", p)
	}
	if p.Jobs[0].PublicID != "admin-training-0213" || p.Jobs[1].PublicID != "admin-training-0212" || p.Jobs[2].State != domain.LoraTrainingFailed {
		t.Fatal("attention order")
	}
	seen := map[string]bool{}
	for offset := 0; offset < 213; offset += store.AdminLoraTrainingPageSize {
		page := read(store.AdminLoraTrainingFilter{Offset: offset, Sort: "oldest"})
		for _, job := range page.Jobs {
			if seen[job.PublicID] {
				t.Fatal("duplicate page row", job.PublicID)
			}
			seen[job.PublicID] = true
		}
	}
	if len(seen) != 213 {
		t.Fatal("history truncated", len(seen))
	}
	if p := read(store.AdminLoraTrainingFilter{Offset: 9999}); p.Filter.Offset != 200 || len(p.Jobs) != 13 {
		t.Fatal("page clamping", p.Filter, len(p.Jobs))
	}
	for _, search := range []string{"%_", "Literal %_ needle", "admin-training-0001", "deleted-owner"} {
		p := read(store.AdminLoraTrainingFilter{Search: search})
		if p.Total != 1 || p.Jobs[0].UserID != nil {
			t.Fatal("literal or retained owner search", search, p.Total)
		}
	}
	if p := read(store.AdminLoraTrainingFilter{State: "active", Family: "krea2"}); p.Total != 1 || p.Active != 1 || p.Completed != 0 || p.ArtifactBytes != 0 {
		t.Fatal("filtered aggregate", p)
	}
	if p := read(store.AdminLoraTrainingFilter{Search: "not-found", Offset: 500}); p.Total != 0 || p.Filter.Offset != 0 || len(p.Jobs) != 0 {
		t.Fatal("empty", p)
	}
	if p := read(store.AdminLoraTrainingFilter{Sort: "artifact"}); p.Jobs[0].PublicID != "admin-training-0210" {
		t.Fatal("artifact order")
	}
	if p := read(store.AdminLoraTrainingFilter{Sort: "newest"}); p.Jobs[0].PublicID != "admin-training-0213" {
		t.Fatal("newest order")
	}
	for _, f := range []store.AdminLoraTrainingFilter{{State: "unknown"}, {Family: "minimax"}, {Sort: "id;DROP"}, {Offset: -1}, {Search: strings.Repeat("я", 121)}, {Search: "a\x00b"}} {
		if _, err := r.ListAdminLoraTrainingPage(ctx, f); !errors.Is(err, store.ErrAdminLoraTrainingFilter) {
			t.Fatalf("filter accepted: %+v %v", f, err)
		}
	}
	for _, v := range []struct {
		id   string
		user int64
		job  string
	}{{"training-gpu-own", owner, "admin-training-0212"}, {"training-gpu-other", other, "admin-training-0213"}} {
		if _, err := r.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: v.id, UserID: v.user, Kind: domain.GPUWorkTraining, JobKey: v.job, ResourceID: domain.GPUPrimaryResource}); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE gpu_work SET state='held',lease_token='private-lease-token',lease_until=now()-interval '1 second' WHERE id='training-gpu-other'`)
	ids := []string{"admin-training-0212", "admin-training-0213", "admin-training-0001", "missing-training-run"}
	admin, err := r.AdminTrainingGPUSnapshots(ctx, ids)
	if err != nil || len(admin.Tasks) != 4 {
		t.Fatal("admin gpu", admin, err)
	}
	if !admin.Tasks[0].Available || !admin.Tasks[1].Available || admin.Tasks[0].GPU.Code != "executor_unconfirmed" || admin.Tasks[1].GPU.Code != "executor_unconfirmed" {
		t.Fatal("expired holder was lost", admin)
	}
	if admin.Tasks[2].Available || admin.Tasks[3].Available {
		t.Fatal("deleted/absent source acquired an owner", admin)
	}
	keys := []domain.TaskGPUKey{{Kind: "lora_training", ID: ids[0]}, {Kind: "lora_training", ID: ids[1]}}
	public, err := r.TaskGPUSnapshots(ctx, domain.TaskScope{UserID: owner, Training: true}, keys)
	if err != nil || !public.Tasks[0].Available || public.Tasks[1].Available || public.Tasks[1].GPU != nil {
		t.Fatal("public owner isolation", public, err)
	}
	denied, err := r.TaskGPUSnapshots(ctx, domain.TaskScope{UserID: owner}, keys)
	if err != nil || denied.Tasks[0].Available || denied.Tasks[1].Available {
		t.Fatal("public permission isolation", denied, err)
	}
	if _, err := r.TaskGPUSnapshots(ctx, domain.TaskScope{Training: true}, keys); !errors.Is(err, store.ErrTaskQuery) {
		t.Fatal("zero owner accepted", err)
	}
	before, err := r.GPUWork(ctx, "training-gpu-other")
	if err != nil {
		t.Fatal(err)
	}
	if before.State != domain.GPUWorkHeld || before.LeaseToken != "private-lease-token" {
		t.Fatal("read changed lease", before)
	}
}
