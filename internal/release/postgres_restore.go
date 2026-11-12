package release

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrDatabaseRestore = errors.New("release database restore is unavailable or unresolved")

type DatabaseRestoreOptions struct {
	Tool           PostgreSQLClientTool `json:"tool"`
	TimeoutSeconds int                  `json:"timeout_seconds"`
}

func (o DatabaseRestoreOptions) valid() bool {
	return filepath.IsAbs(o.Tool.Path) && filepath.Clean(o.Tool.Path) == o.Tool.Path &&
		validHostDirectory(filepath.Dir(o.Tool.Path)) && validHostHash(o.Tool.SHA256) &&
		o.TimeoutSeconds >= 5 && o.TimeoutSeconds <= 21600
}

var postgresRestoreVersion = regexp.MustCompile(`^pg_restore \(PostgreSQL\) ([1-9][0-9]*)\.[^\r\n]+\r?\n?$`)
var postgresRestoreDatabaseName = regexp.MustCompile(`^gateway_restore_[0-9a-f]{32}$`)

func checkPostgreSQLRestoreVersion(ctx context.Context, tool PostgreSQLClientTool, environment []string, directory string, major int) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var output strings.Builder
	command := exec.CommandContext(ctx, tool.Path, "--version")
	command.Env, command.Dir = environment, directory
	command.Stdout = &archiveLimitWriter{writer: &output, limit: 4096}
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	configureReleaseCommand(command)
	if command.Run() != nil {
		return ErrDatabaseRestore
	}
	match := postgresRestoreVersion.FindStringSubmatch(output.String())
	if len(match) != 2 || match[1] != strconv.Itoa(major) {
		return ErrDatabaseRestore
	}
	return nil
}

// The archive was produced without --create. All commands, including removal
// of the bootstrap control table, must be one transaction. No parallel workers,
// shell, archive pathname or credential-bearing database URL are allowed here.
func runPostgreSQLRestore(ctx context.Context, options DatabaseRestoreOptions, environment []string,
	directory, databaseName string, input *os.File) error {
	return runPreparedPostgreSQLRestore(ctx, options, environment, directory, databaseName, input, nil)
}

func runPreparedPostgreSQLRestore(ctx context.Context, options DatabaseRestoreOptions, environment []string,
	directory, databaseName string, input *os.File, prepare func(context.Context, databaseProcessIdentity) error) error {
	if !options.valid() || !postgresRestoreDatabaseName.MatchString(databaseName) {
		return ErrDatabaseRestore
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	identity, err := observePostgreSQLClientTool(options.Tool)
	if err != nil || ctx.Err() != nil {
		return ErrDatabaseRestore
	}
	args := []string{"--clean", "--if-exists", "--single-transaction", "--no-password", "--dbname=" + databaseName}
	if runPreparedDatabaseWriter(ctx, options.Tool.Path, args, environment, directory, input, prepare) != nil {
		return ErrDatabaseRestore
	}
	after, err := observePostgreSQLClientTool(options.Tool)
	if err != nil || after != identity {
		return ErrDatabaseRestore
	}
	return nil
}

func databaseRestoreEnvironment(configuration dockerDatabaseConfiguration, directory, password, absent string) ([]string, error) {
	environment, err := databaseArchiveEnvironment(configuration, directory, password, absent)
	if err != nil {
		return nil, ErrDatabaseRestore
	}
	for i, value := range environment {
		if strings.HasPrefix(value, "PGAPPNAME=") {
			environment[i] = "PGAPPNAME=gateway-release-restore"
		}
	}
	return environment, nil
}
