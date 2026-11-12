package release

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"github.com/lib/pq"
)

func TestPostgreSQLDatabasePropertySettings(t *testing.T) {
	for _, test := range []struct{ name, value, want string }{
		{"work_mem", "16MB", "'16MB'"},
		{"gateway.restore_note", "literal=value", "'literal=value'"},
		{"search_path", `"$user", public`, `'$user', 'public'`},
		{"search_path", `"comma,name", "quote""name", pg_catalog`, `'comma,name', 'quote"name', 'pg_catalog'`},
		{"search_path", `""`, `''`},
		{"search_path", "\"carriage\r\nnewline\", public", "'carriage\r\nnewline', 'public'"},
		{"temp_tablespaces", `one, two`, `'one', 'two'`},
	} {
		value, err := postgresSettingSQLValue(postgresDatabaseSetting{Name: test.name, Value: test.value})
		if err != nil || value != test.want {
			t.Fatalf("%s=%q: got %q, %v", test.name, test.value, value, err)
		}
	}
	for _, value := range []string{`"unterminated`, "one\ntwo", `one,"x"tail`} {
		if _, err := postgresSettingSQLValue(postgresDatabaseSetting{Name: "search_path", Value: value}); err == nil {
			t.Fatal("invalid list was accepted", value)
		}
	}
}

func TestPostgreSQLDatabasePropertiesRoundTrip(t *testing.T) {
	for _, mode := range []string{"default", "configured", "icu"} {
		t.Run(mode, func(t *testing.T) {
			db := releaseTestDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var sourceOID int64
			var sourceName, currentRole string
			if err := db.QueryRowContext(ctx, `SELECT oid::bigint,datname,current_user FROM pg_catalog.pg_database WHERE datname=current_database()`).
				Scan(&sourceOID, &sourceName, &currentRole); err != nil {
				t.Fatal(err)
			}
			if mode == "configured" {
				name := pq.QuoteIdentifier(sourceName)
				preflightExec(t, db, `ALTER DATABASE `+name+` CONNECTION LIMIT 17`)
				preflightExec(t, db, `COMMENT ON DATABASE `+name+` IS `+pq.QuoteLiteral("owner's \\ private comment"))
				preflightExec(t, db, `ALTER DATABASE `+name+` SET search_path TO "space,quote""path", pg_catalog, "$user", `+pq.QuoteIdentifier("carriage\r\nnewline"))
				preflightExec(t, db, `ALTER DATABASE `+name+` SET "gateway.restore_note" TO `+pq.QuoteLiteral("literal=x'y\\z"))
				preflightExec(t, db, `ALTER ROLE `+pq.QuoteIdentifier(currentRole)+` IN DATABASE `+name+` SET statement_timeout TO '23s'`)
				preflightExec(t, db, `ALTER ROLE `+pq.QuoteIdentifier(currentRole)+` IN DATABASE `+name+` SET search_path TO pg_catalog, public`)
				preflightExec(t, db, `REVOKE ALL ON DATABASE `+name+` FROM PUBLIC`)
				preflightExec(t, db, `GRANT CONNECT ON DATABASE `+name+` TO pg_monitor WITH GRANT OPTION`)
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE pg_monitor`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `GRANT CONNECT ON DATABASE `+name+` TO pg_read_all_stats`); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else if mode == "icu" {
				icuName := sourceName + "_icu"
				preflightExec(t, db, `CREATE DATABASE `+pq.QuoteIdentifier(icuName)+` TEMPLATE template0 LOCALE_PROVIDER icu ICU_LOCALE 'en' ICU_RULES '&a < b < c'`)
				t.Cleanup(func() {
					if _, err := db.Exec(`DROP DATABASE ` + pq.QuoteIdentifier(icuName) + ` WITH (FORCE)`); err != nil {
						t.Error(err)
					}
				})
				if err := db.QueryRowContext(ctx, `SELECT oid::bigint FROM pg_catalog.pg_database WHERE datname=$1`, icuName).Scan(&sourceOID); err != nil {
					t.Fatal(err)
				}
			}
			properties, err := readPostgresDatabaseProperties(ctx, db, sourceOID)
			if err != nil {
				var raw []byte
				err = db.QueryRowContext(ctx, postgresDatabasePropertiesSQL, sourceOID).Scan(&raw)
				t.Fatalf("properties: SQL=%v raw=%s", err, raw)
			}
			if err := properties.checkCreationPrivileges(ctx, db, sourceOID); err != nil {
				t.Fatal("creation privilege check", err)
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				t.Fatal(err)
			}
			name := "gateway_restore_" + hex.EncodeToString(nonce[:])
			query, err := properties.createSQL(name)
			if err != nil {
				t.Fatal(err)
			}
			preflightExec(t, db, query)
			t.Cleanup(func() {
				if _, err := db.Exec(`DROP DATABASE ` + pq.QuoteIdentifier(name) + ` WITH (FORCE)`); err != nil {
					t.Error(err)
				}
			})
			target, err := database.Open(ctx, releaseTestDSN(t, os.Getenv("TEST_DATABASE_URL"), name))
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			conn, err := target.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := properties.apply(ctx, conn, name); err != nil {
				t.Fatal("apply properties", err)
			}
			var targetOID int64
			if err := conn.QueryRowContext(ctx, `SELECT oid::bigint FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()`).Scan(&targetOID); err != nil {
				t.Fatal(err)
			}
			after, err := readPostgresDatabaseProperties(ctx, conn, targetOID)
			if err != nil || !properties.same(after) {
				t.Fatalf("properties differ: %v\nbefore=%+v\nafter=%+v", err, properties, after)
			}
			original, err := readPostgresDatabaseProperties(ctx, db, sourceOID)
			if err != nil || !properties.same(original) {
				t.Fatal("original properties changed", err)
			}
			wrong := properties
			wrong.OwnerName = strings.Repeat("x", 20)
			if wrong.checkCreationPrivileges(ctx, db, sourceOID) == nil {
				t.Fatal("renamed owner accepted")
			}
		})
	}
}
