package release

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

const databaseObservationTimeout = 15 * time.Second

type WorkloadObservation struct {
	Kind       string `json:"kind"`
	Applicable bool   `json:"applicable"`
	Count      int64  `json:"count"`
}

// DatabaseObservation is a point-in-time report, never an admission fence or an
// installation permit. Even an empty database cannot prove a remote GPU idle.
type DatabaseObservation struct {
	SchemaVersion        int                        `json:"schema_version"`
	Scope                string                     `json:"scope"`
	DeploymentAuthorized bool                       `json:"deployment_authorized"`
	DatabaseChecksPassed bool                       `json:"database_checks_passed"`
	SnapshotAt           time.Time                  `json:"snapshot_at"`
	FinishedAt           time.Time                  `json:"finished_at"`
	DeclaredPrevious     buildinfo.Info             `json:"declared_previous"`
	Candidate            buildinfo.Info             `json:"candidate"`
	Migrations           []database.MigrationRecord `json:"migrations"`
	MatchesPrevious      bool                       `json:"matches_previous"`
	ReaderSupported      bool                       `json:"reader_supported"`
	Schema               SchemaDecision             `json:"schema"`
	WorkloadChecked      bool                       `json:"workload_checked"`
	WorkloadIdle         bool                       `json:"workload_idle"`
	Workload             []WorkloadObservation      `json:"workload"`
	Blockers             []string                   `json:"blockers"`
	FailureCode          string                     `json:"failure_code,omitempty"`
	SQLState             string                     `json:"sql_state,omitempty"`
}

type workloadQuery struct {
	kind       string
	introduced int64
	query      string
}

// NOT IN terminal states makes an unrecognized state fail closed. Counts from
// related tables can refer to the same job and must not be summed as job totals.
var releaseWorkloadQueries = []workloadQuery{
	{"mining_leases", 16, `SELECT count(*) FROM public.quick_generation_mining_leases`},
	{"legacy_generation", 28, `SELECT count(*) FROM public.quick_generation_variants WHERE state NOT IN ('completed','error','cancelled')`},
	{"suggestion_scans", 32, `SELECT count(*) FROM public.feature_suggestion_scans WHERE status NOT IN ('completed','error')`},
	{"generation", 40, `SELECT count(*) FROM public.generation_jobs WHERE state NOT IN ('completed','failed','cancelled','expired')`},
	{"training", 51, `SELECT count(*) FROM public.lora_training_jobs WHERE state NOT IN ('completed','failed','cancelled')`},
	{"caption", 55, `SELECT count(*) FROM public.lora_caption_jobs WHERE state NOT IN ('completed','failed','cancelled')`},
	{"gpu_work", 56, `SELECT count(*) FROM public.gpu_work WHERE state NOT IN ('released','cancelled')`},
	{"inference", 63, `SELECT count(*) FROM public.gpu_inference_requests WHERE state NOT IN ('completed','not_dispatched','stopped')`},
	{"assistant", 65, `SELECT count(*) FROM public.prompt_assistant_jobs WHERE state NOT IN ('completed','failed','cancelled')`},
	{"executor_permits", 69, `SELECT count(*) FROM public.gpu_executor_permits WHERE state<>'closed'`},
	{"caption_series", 72, `SELECT count(*) FROM public.lora_caption_series WHERE state NOT IN ('completed','failed','cancelled','partial')`},
	{"output_cleanup_unconfirmed", 84, `SELECT count(*) FROM public.comfy_output_cleanup_tombstones WHERE last_outcome IN ('legacy_retry','unconfirmed')`},
	{"input_cleanup_unconfirmed", 85, `SELECT count(*) FROM public.comfy_input_assets WHERE cleanup_last_outcome IN ('legacy_retry','unconfirmed')`},
	{"training_storage", 86, `SELECT count(*) FROM public.training_storage_operations WHERE state NOT IN ('preview','completed','partial','rejected','cancelled','expired')`},
	{"comfy_storage", 90, `SELECT count(*) FROM public.comfy_storage_operations WHERE state NOT IN ('preview','completed','partial','rejected','cancelled','expired')`},
}

// InspectDatabase reads only the explicitly supplied database. The caller must
// separately authenticate and verify both packages and the actual running build.
func InspectDatabase(parent context.Context, db *sql.DB, previous, candidate Manifest) (DatabaseObservation, error) {
	if db == nil {
		return inspectDatabase(parent, nil, previous, candidate)
	}
	return inspectDatabase(parent, db, previous, candidate)
}

// InspectDatabaseOnConnection preserves the caller's physical session. A release
// coordinator must call it inside RuntimeRelease.WithQuiescentConnection so its
// final workload observation cannot escape to another, unguarded pool session.
// This function alone neither verifies the session's locks nor authorizes work.
func InspectDatabaseOnConnection(parent context.Context, conn *sql.Conn, previous, candidate Manifest) (DatabaseObservation, error) {
	if conn == nil {
		return inspectDatabase(parent, nil, previous, candidate)
	}
	return inspectDatabase(parent, conn, previous, candidate)
}

type observationTransactionSource interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func inspectDatabase(parent context.Context, db observationTransactionSource, previous, candidate Manifest) (DatabaseObservation, error) {
	report := DatabaseObservation{
		SchemaVersion: 1, Scope: "database_observation", DeclaredPrevious: previous.Build,
		Candidate: candidate.Build, Migrations: []database.MigrationRecord{},
		Workload: []WorkloadObservation{}, Blockers: []string{},
	}
	fail := func(code string, err error) (DatabaseObservation, error) {
		report.FailureCode = code
		report.DatabaseChecksPassed = false
		report.FinishedAt = time.Now().UTC()
		var pg *pq.Error
		if errors.As(err, &pg) {
			report.SQLState = string(pg.Code)
		}
		// Connection errors may contain credentials, paths or database names.
		return report, fmt.Errorf("database observation failed: %s", code)
	}
	if previous.Validate() != nil || candidate.Validate() != nil {
		return fail("invalid_release_manifest", nil)
	}
	if db == nil {
		return fail("database_not_configured", nil)
	}
	ctx, cancel := context.WithTimeout(parent, databaseObservationTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return fail("database_unavailable", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='15s'`); err != nil {
		return fail("observation_timeout_setup", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&report.SnapshotAt); err != nil {
		return fail("snapshot_time_unavailable", err)
	}
	report.SnapshotAt = report.SnapshotAt.UTC()
	rows, err := tx.QueryContext(ctx, `SELECT version,left(name,101),left(checksum,65) FROM public.schema_migrations ORDER BY version LIMIT 10001`)
	if err != nil {
		return fail("migration_catalog_unavailable", err)
	}
	for rows.Next() {
		var item database.MigrationRecord
		if err = rows.Scan(&item.Version, &item.Name, &item.Checksum); err != nil {
			_ = rows.Close()
			return fail("migration_catalog_unreadable", err)
		}
		report.Migrations = append(report.Migrations, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fail("migration_catalog_unreadable", err)
	}
	report.MatchesPrevious = slices.Equal(report.Migrations, previous.Migrations)
	if !report.MatchesPrevious {
		report.Blockers = append(report.Blockers, "database_differs_from_previous_release")
	}
	report.ReaderSupported = CompareSchema(report.Migrations, database.ReleaseMigrations()).CanUpgrade
	if !report.ReaderSupported {
		report.Blockers = append(report.Blockers, "unsupported_database_catalog")
	}
	report.Schema = CompareSchema(report.Migrations, candidate.Migrations)
	if !report.Schema.CanUpgrade {
		report.Blockers = append(report.Blockers, "candidate_schema_incompatible")
	}
	if report.ReaderSupported {
		report.WorkloadIdle = true
		version := report.Migrations[len(report.Migrations)-1].Version
		for _, check := range releaseWorkloadQueries {
			item := WorkloadObservation{Kind: check.kind, Applicable: version >= check.introduced}
			if item.Applicable {
				if err = tx.QueryRowContext(ctx, check.query).Scan(&item.Count); err != nil {
					report.WorkloadIdle = false
					return fail("workload_unavailable_"+check.kind, err)
				}
				if item.Count > 0 {
					report.WorkloadIdle = false
					report.Blockers = append(report.Blockers, "workload_"+check.kind)
				}
			}
			report.Workload = append(report.Workload, item)
		}
		report.WorkloadChecked = true
	}
	if err = tx.Commit(); err != nil {
		return fail("snapshot_incomplete", err)
	}
	report.FinishedAt = time.Now().UTC()
	report.DatabaseChecksPassed = len(report.Blockers) == 0 && report.WorkloadChecked && report.WorkloadIdle
	return report, nil
}
