package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"math"
)

// BootstrapRuntimeRestore installs only a persistent, active runtime fence in
// a newly created empty database. It does not migrate, restore or admit runtime
// traffic. The caller must durably journal creation intent, verify its own new
// creation, and bind the request/token to the fenced source archive first.
// The returned OID identifies the bootstrap table, not a completed restore.
// Close deliberately leaves the persistent fence active.
func BootstrapRuntimeRestore(ctx context.Context, db *sql.DB, request RuntimeReleaseRequest,
	expected, source RuntimeDatabaseIdentity) (*RuntimeRelease, int64, error) {
	if db == nil || !runtimeOperationID.MatchString(request.OperationID) || !runtimeDigest.MatchString(request.Token) ||
		!runtimeDigest.MatchString(request.PreviousManifestHash) || !runtimeDigest.MatchString(request.CandidateManifestHash) ||
		request.ExpectedEpoch < 0 || request.ExpectedEpoch == math.MaxInt64 || source.SystemIdentifier == "" || source.DatabaseOID <= 0 ||
		(source.SystemIdentifier == expected.SystemIdentifier && source.DatabaseOID == expected.DatabaseOID) {
		return nil, 0, ErrRuntimeReleaseConflict
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, 0, err
	}
	keep := false
	defer func() {
		if !keep {
			discardRuntimeController(conn)
		}
	}()
	actual, _, err := readRuntimeDatabaseIdentity(ctx, conn)
	if err != nil || actual != expected {
		return nil, 0, ErrRuntimeReleaseConflict
	}
	var leased bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks
	 WHERE pid=pg_catalog.pg_backend_pid() AND locktype='advisory' AND classid=0
	 AND objid::bigint=$1 AND objsubid=1 AND mode='ShareLock' AND granted)`, runtimeConnectionLockID).Scan(&leased); err != nil {
		return nil, 0, err
	}
	if leased {
		return nil, 0, ErrRuntimeControllerConnection
	}
	for _, lock := range []struct {
		id  int64
		err error
	}{{runtimeControllerLockID, ErrRuntimeControllerBusy}, {runtimeConnectionLockID, ErrRuntimeNotQuiescent}} {
		var acquired bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_catalog.pg_try_advisory_lock($1)`, lock.id).Scan(&acquired); err != nil {
			return nil, 0, err
		}
		if !acquired {
			return nil, 0, lock.err
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var empty bool
	if err := tx.QueryRowContext(ctx, runtimeRestoreEmptySQL, int64(0)).Scan(&empty); err != nil || !empty {
		return nil, 0, ErrRuntimeReleaseConflict
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path = public`); err != nil {
		return nil, 0, err
	}
	// Reuse migration 93 verbatim without adding schema_migrations. pg_restore
	// replaces this table inside its single transaction with the source fence.
	for _, statement := range runtimeFenceMigration.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return nil, 0, err
		}
	}
	digest := sha256.Sum256([]byte(request.Token))
	if _, err := tx.ExecContext(ctx, `UPDATE public.release_runtime_control
	 SET epoch=$1, active=true, operation_id=$2, token_hash=$3,
	 previous_manifest_hash=$4, candidate_manifest_hash=$5, requested_at=now()
	 WHERE singleton=1`, request.ExpectedEpoch+1, request.OperationID, hex.EncodeToString(digest[:]),
		request.PreviousManifestHash, request.CandidateManifestHash); err != nil {
		return nil, 0, err
	}
	record, err := readRuntimeFence(ctx, tx, false)
	if err != nil {
		return nil, 0, err
	}
	var oid int64
	if err := tx.QueryRowContext(ctx, `SELECT 'public.release_runtime_control'::pg_catalog.regclass::oid::bigint`).Scan(&oid); err != nil || oid <= 0 {
		return nil, 0, ErrRuntimeReleaseConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	keep = true
	return &RuntimeRelease{conn: conn, record: record, quiescent: true}, oid, nil
}

// VerifyRuntimeRestoreBootstrap is read-only. The caller must hold the exact
// target controller/exclusion locks and verify its active fence. Permit only
// the receipted bootstrap table and its own primary index and row/array types.
func VerifyRuntimeRestoreBootstrap(ctx context.Context, conn *sql.Conn, expectedOID int64) error {
	if conn == nil || expectedOID <= 0 {
		return ErrRuntimeReleaseConflict
	}
	var valid bool
	if conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class
	 WHERE oid=$1::oid AND oid='public.release_runtime_control'::pg_catalog.regclass
	 AND relkind='r' AND relpersistence='p')
	 AND (SELECT count(*) FROM public.release_runtime_control)=1
	 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=$1::oid AND NOT tgisinternal)
	 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite WHERE ev_class=$1::oid)
	 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_policy WHERE polrelid=$1::oid)`, expectedOID).Scan(&valid) != nil || !valid {
		return ErrRuntimeReleaseConflict
	}
	if conn.QueryRowContext(ctx, runtimeRestoreEmptySQL, expectedOID).Scan(&valid) != nil || !valid {
		return ErrRuntimeReleaseConflict
	}
	return nil
}

// Zero means a completely empty template0 target; otherwise allow only the
// receipted bootstrap objects. Never adopt arbitrary migration-less contents.
const runtimeRestoreEmptySQL = `SELECT
 NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname NOT IN ('public','pg_catalog','information_schema','pg_toast')
   AND nspname !~ '^pg_(temp|toast_temp)_[0-9]+$')
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relnamespace='public'::pg_catalog.regnamespace
   AND oid<>$1::oid AND oid NOT IN (SELECT indexrelid FROM pg_catalog.pg_index WHERE indrelid=$1::oid AND indisprimary))
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_proc WHERE pronamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_type WHERE typnamespace='public'::pg_catalog.regnamespace
   AND oid NOT IN (SELECT reltype FROM pg_catalog.pg_class WHERE oid=$1::oid
     UNION SELECT typarray FROM pg_catalog.pg_type WHERE typrelid=$1::oid AND $1::oid<>0))
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_collation WHERE collnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_conversion WHERE connamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_operator WHERE oprnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_opclass WHERE opcnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_opfamily WHERE opfnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_ts_config WHERE cfgnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_ts_dict WHERE dictnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_ts_parser WHERE prsnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_ts_template WHERE tmplnamespace='public'::pg_catalog.regnamespace)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname<>'plpgsql')
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_event_trigger)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_foreign_server)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_publication)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_subscription WHERE subdbid=(SELECT oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()))
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_largeobject_metadata)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_default_acl)`
