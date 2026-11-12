package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrDatabaseArchive = errors.New("release database archive is unavailable or unresolved")

// PostgreSQLClientTool is an explicitly trusted executable, never a PATH lookup.
// Its containing installation and dynamic libraries must be operator-controlled.
type PostgreSQLClientTool struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type PostgreSQLDumpTool = PostgreSQLClientTool

type DatabaseArchiveOptions struct {
	Tool           PostgreSQLDumpTool `json:"tool"`
	MaxBytes       int64              `json:"max_bytes"`
	TimeoutSeconds int                `json:"timeout_seconds"`
}

func (o DatabaseArchiveOptions) valid() bool {
	return filepath.IsAbs(o.Tool.Path) && filepath.Clean(o.Tool.Path) == o.Tool.Path &&
		validHostDirectory(filepath.Dir(o.Tool.Path)) && validHostHash(o.Tool.SHA256) &&
		o.MaxBytes >= 1024 && o.MaxBytes <= 1<<40 && o.TimeoutSeconds >= 5 && o.TimeoutSeconds <= 21600
}

func observePostgreSQLDumpTool(tool PostgreSQLDumpTool) (string, error) {
	return observePostgreSQLClientTool(tool)
}

func observePostgreSQLClientTool(tool PostgreSQLClientTool) (string, error) {
	if !filepath.IsAbs(tool.Path) || filepath.Clean(tool.Path) != tool.Path || !validHostHash(tool.SHA256) {
		return "", ErrDatabaseArchive
	}
	resolved, err := filepath.EvalSymlinks(tool.Path)
	if err != nil || resolved != tool.Path {
		return "", ErrDatabaseArchive
	}
	info, err := os.Lstat(tool.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128<<20 {
		return "", ErrDatabaseArchive
	}
	file, err := os.Open(tool.Path)
	if err != nil {
		return "", ErrDatabaseArchive
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", ErrDatabaseArchive
	}
	id, err := hostFileIdentity(file)
	if err != nil {
		return "", ErrDatabaseArchive
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, 128<<20+1))
	after, statErr := os.Lstat(tool.Path)
	if err != nil || statErr != nil || !os.SameFile(info, after) || size != info.Size() ||
		hex.EncodeToString(hash.Sum(nil)) != tool.SHA256 {
		return "", ErrDatabaseArchive
	}
	return id, nil
}

// pgpass is never exposed in command arguments or inherited by other children.
// Newlines cannot be represented by the password-file format.
func databaseArchivePassword(configuration dockerDatabaseConfiguration) ([]byte, error) {
	u, err := url.Parse(configuration.URL)
	if err != nil || u.User == nil {
		return nil, ErrDatabaseArchive
	}
	password, _ := u.User.Password()
	parts := []string{u.Hostname(), u.Port(), strings.TrimPrefix(u.Path, "/"), u.User.Username(), password}
	for i, value := range parts {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") || (i < 4 && strings.Contains(value, "*")) {
			return nil, ErrDatabaseArchive
		}
		parts[i] = strings.NewReplacer(`\`, `\\`, ":", `\:`).Replace(value)
	}
	return []byte(strings.Join(parts, ":") + "\n"), nil
}

func databaseArchiveEnvironment(configuration dockerDatabaseConfiguration, directory, passwordFile, absentFile string) ([]string, error) {
	if configuration.validate() != nil {
		return nil, ErrDatabaseArchive
	}
	u, err := url.Parse(configuration.URL)
	if err != nil || u.User == nil {
		return nil, ErrDatabaseArchive
	}
	q := u.Query()
	environment := []string{
		"PGHOST=" + u.Hostname(), "PGPORT=" + u.Port(), "PGDATABASE=" + strings.TrimPrefix(u.Path, "/"),
		"PGUSER=" + u.User.Username(), "PGPASSFILE=" + passwordFile,
		"PGSSLMODE=" + q.Get("sslmode"), "PGCONNECT_TIMEOUT=" + q.Get("connect_timeout"),
		"PGGSSENCMODE=disable", "PGTARGETSESSIONATTRS=read-write", "PGAPPNAME=gateway-release-dump",
		"PGSERVICEFILE=" + absentFile, "PGSSLCRL=" + absentFile, "PGSSLCRLDIR=" + absentFile,
		"HOME=" + directory, "APPDATA=" + directory, "TMP=" + directory, "TEMP=" + directory,
		"LANG=C", "LC_ALL=C",
	}
	for _, option := range []string{"sslrootcert", "sslcert", "sslkey"} {
		value := q.Get(option)
		if value == "" {
			value = absentFile
		}
		environment = append(environment, "PG"+strings.ToUpper(option)+"="+value)
	}
	// Windows needs its system directory to load the executable's dependencies.
	// No PATH, PG*, loader hook or other application environment is inherited.
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		environment = append(environment, "SystemRoot="+systemRoot)
	}
	return environment, nil
}

type archiveLimitWriter struct {
	writer io.Writer
	limit  int64
	bytes  int64
}

func (w *archiveLimitWriter) Write(body []byte) (int, error) {
	if int64(len(body)) > w.limit-w.bytes {
		return 0, ErrDatabaseArchive
	}
	n, err := w.writer.Write(body)
	w.bytes += int64(n)
	return n, err
}

var postgresDumpVersion = regexp.MustCompile(`^pg_dump \(PostgreSQL\) ([1-9][0-9]*)\.[^\r\n]+\r?\n?$`)

func checkPostgreSQLDumpVersion(ctx context.Context, tool PostgreSQLDumpTool, environment []string, directory string, major int) error {
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
		return ErrDatabaseArchive
	}
	match := postgresDumpVersion.FindStringSubmatch(output.String())
	if len(match) != 2 || match[1] != strconv.Itoa(major) {
		return ErrDatabaseArchive
	}
	return nil
}

func runPostgreSQLDump(ctx context.Context, options DatabaseArchiveOptions, environment []string, directory, snapshot string, output io.Writer) error {
	command := exec.CommandContext(ctx, options.Tool.Path, "--format=custom", "--no-password", "--snapshot="+snapshot, "--lock-wait-timeout=5000")
	command.Env, command.Dir = environment, directory
	// A wrapper forces an os/exec pipe: the child never receives the archive file
	// handle and cannot continue writing that file after the coordinator dies.
	command.Stdout = &archiveLimitWriter{writer: output, limit: options.MaxBytes}
	command.Stderr = io.Discard
	command.WaitDelay = 5 * time.Second
	configureReleaseCommand(command)
	if command.Run() != nil {
		return ErrDatabaseArchive
	}
	return nil
}
