package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"ai-access-gateway/internal/database"
)

func TestDatabasePreflightQueryInventory(t *testing.T) {
	want := []string{"mining_leases", "legacy_generation", "suggestion_scans", "generation", "training", "caption", "gpu_work", "inference", "assistant", "executor_permits", "caption_series", "output_cleanup_unconfirmed", "input_cleanup_unconfirmed", "training_storage", "comfy_storage"}
	var got []string
	catalog := database.ReleaseMigrations()
	for _, item := range releaseWorkloadQueries {
		got = append(got, item.kind)
		if item.introduced < 1 || item.introduced > int64(len(catalog)) || !strings.HasPrefix(item.query, "SELECT count(*) FROM public.") || strings.Contains(item.query, ";") {
			t.Fatalf("invalid read-only query: %+v", item)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("workload inventory changed: %v", got)
	}
}

func TestDatabasePreflightFailureDoesNotLeakConnectionData(t *testing.T) {
	_, manifest := fixtureManifest(t)
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=privateuser password=privatepassword dbname=privatedb sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	report, err := InspectDatabase(context.Background(), db, manifest, manifest)
	if err == nil || report.DatabaseChecksPassed || report.WorkloadChecked || report.DeploymentAuthorized || report.FailureCode == "" {
		t.Fatalf("connection failure accepted: %+v %v", report, err)
	}
	data, _ := json.Marshal(report)
	for _, secret := range []string{"privateuser", "privatepassword", "privatedb", "127.0.0.1"} {
		if strings.Contains(string(data), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("connection data leaked: %s", secret)
		}
	}
	report, err = InspectDatabase(context.Background(), nil, manifest, manifest)
	if err == nil || report.FailureCode != "database_not_configured" {
		t.Fatalf("nil database: %+v %v", report, err)
	}
	report, err = InspectDatabase(context.Background(), nil, Manifest{}, manifest)
	if err == nil || report.FailureCode != "invalid_release_manifest" {
		t.Fatalf("invalid manifest: %+v %v", report, err)
	}
	report, err = InspectDatabaseOnConnection(context.Background(), nil, manifest, manifest)
	if err == nil || report.FailureCode != "database_not_configured" {
		t.Fatalf("nil connection: %+v %v", report, err)
	}
}
