package release

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgreSQLDumpOptions(t *testing.T) {
	options := DatabaseArchiveOptions{Tool: PostgreSQLDumpTool{Path: filepath.Join(t.TempDir(), "pg_dump"), SHA256: strings.Repeat("a", 64)}, MaxBytes: 1 << 20, TimeoutSeconds: 60}
	if !options.valid() {
		t.Fatal("valid explicit options refused")
	}
	for _, change := range []func(*DatabaseArchiveOptions){
		func(o *DatabaseArchiveOptions) { o.Tool.Path = "pg_dump" },
		func(o *DatabaseArchiveOptions) { o.Tool.SHA256 = "" },
		func(o *DatabaseArchiveOptions) { o.MaxBytes = 1023 },
		func(o *DatabaseArchiveOptions) { o.MaxBytes = 1<<40 + 1 },
		func(o *DatabaseArchiveOptions) { o.TimeoutSeconds = 4 },
		func(o *DatabaseArchiveOptions) { o.TimeoutSeconds = 21601 },
	} {
		bad := options
		change(&bad)
		if bad.valid() {
			t.Fatal("unsafe tool/limits accepted")
		}
	}
	if err := os.WriteFile(options.Tool.Path, []byte("executable fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	options.Tool.SHA256 = hostRecoveryDigest([]byte("executable fixture"))
	id, err := observePostgreSQLDumpTool(options.Tool)
	if err != nil || !validHostHash(id) {
		t.Fatal("tool observation", err)
	}
	if err := os.WriteFile(options.Tool.Path, []byte("changed executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := observePostgreSQLDumpTool(options.Tool); err == nil {
		t.Fatal("changed executable accepted")
	}
}

func TestPostgreSQLDumpPasswordAndEnvironment(t *testing.T) {
	u, _ := url.Parse("postgres://operator:password@127.0.0.1:5432/gateway?sslmode=disable")
	u.User = url.UserPassword(`a:b\c`, `p:a\ss'word`)
	configuration, err := observeDockerDatabaseConfiguration(u.String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := databaseArchivePassword(configuration)
	if err != nil || string(body) != "127.0.0.1:5432:gateway:a\\:b\\\\c:p\\:a\\\\ss'word\n" {
		t.Fatal("pgpass escaping changed")
	}
	for _, value := range []string{"a\nb", "a\rb"} {
		u.User = url.UserPassword("operator", value)
		configuration, err := observeDockerDatabaseConfiguration(u.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := databaseArchivePassword(configuration); err == nil {
			t.Fatal("unrepresentable password accepted")
		}
	}
	t.Setenv("PGPASSWORD", "must-not-be-inherited")
	t.Setenv("PGOPTIONS", "-c search_path=private")
	t.Setenv("LD_PRELOAD", "must-not-be-inherited")
	directory := t.TempDir()
	environment, err := databaseArchiveEnvironment(configuration, directory, filepath.Join(directory, "pgpass"), filepath.Join(directory, "absent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range environment {
		if strings.Contains(entry, "must-not-be-inherited") || strings.HasPrefix(entry, "PGPASSWORD=") || strings.HasPrefix(entry, "LD_") || strings.HasPrefix(entry, "PGOPTIONS=") || strings.HasPrefix(entry, "PATH=") {
			t.Fatal("ambient process configuration inherited")
		}
	}
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if _, exists := values[name]; exists {
			t.Fatal("duplicate child environment option")
		}
		values[name] = value
	}
	for _, name := range []string{"PGSERVICEFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		if values[name] != filepath.Join(directory, "absent") {
			t.Fatal("ambient file lookup allowed", name)
		}
	}
}

func TestPostgreSQLDumpBoundedIO(t *testing.T) {
	var output bytes.Buffer
	writer := &archiveLimitWriter{writer: &output, limit: 5}
	if n, err := writer.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := writer.Write([]byte("def")); n != 0 || err == nil || output.String() != "abc" {
		t.Fatal("archive exceeded its bound")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := io.ReadAll(archiveContextReader{ctx, strings.NewReader("private")}); err == nil {
		t.Fatal("archive hash ignored cancellation")
	}
	for _, id := range []string{"00000003-0000003A-1", "FFFFFFFF-FFFFFFFF-999"} {
		if !postgresSnapshotID.MatchString(id) {
			t.Fatal("snapshot id refused")
		}
	}
	for _, id := range []string{"", "../snapshot", "--flag", "00000003-0000003A-0", "00000003-0000003A-1\n"} {
		if postgresSnapshotID.MatchString(id) {
			t.Fatal("invalid snapshot id accepted")
		}
	}
}
