package release

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/database"

	"github.com/lib/pq"
)

var ErrDockerDatabaseConfiguration = errors.New("release database configuration is unavailable or changed")

const maxDockerDatabaseConfigBytes = 128 << 10
const maxDockerDatabaseURLBytes = 32 << 10
const maxDockerDatabaseTLSBytes = 1 << 20

type dockerDatabaseTLSFile struct {
	Option   string `json:"option"`
	Path     string `json:"path"`
	Absent   bool   `json:"absent,omitempty"`
	Identity string `json:"identity,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type dockerDatabaseConfiguration struct {
	URL      string                  `json:"url"`
	TLSFiles []dockerDatabaseTLSFile `json:"tls_files"`
}

func canonicalDockerDatabaseURL(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > maxDockerDatabaseURLBytes || strings.ContainsRune(raw, 0) {
		return "", ErrDockerDatabaseConfiguration
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" || u.User == nil ||
		u.User.Username() == "" || u.Path == "" || u.Path == "/" || u.ForceQuery {
		return "", ErrDockerDatabaseConfiguration
	}
	password, present := u.User.Password()
	if !present || password == "" || strings.ContainsRune(password, 0) || strings.ContainsRune(u.User.Username(), 0) || strings.ContainsRune(u.Path, 0) {
		return "", ErrDockerDatabaseConfiguration
	}
	host, portText, err := net.SplitHostPort(u.Host)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 || strings.ContainsAny(host, "\\/\x00\r\n\t ") {
		return "", ErrDockerDatabaseConfiguration
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrDockerDatabaseConfiguration
	}
	for name, values := range values {
		if len(values) != 1 || values[0] == "" || strings.ContainsRune(values[0], 0) {
			return "", ErrDockerDatabaseConfiguration
		}
		switch name {
		case "sslmode", "sslrootcert", "sslcert", "sslkey", "connect_timeout":
		default:
			return "", ErrDockerDatabaseConfiguration
		}
	}
	mode := values.Get("sslmode")
	switch mode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return "", ErrDockerDatabaseConfiguration
	}
	if (values.Get("sslcert") == "") != (values.Get("sslkey") == "") ||
		((mode == "verify-ca" || mode == "verify-full") && values.Get("sslrootcert") == "") {
		return "", ErrDockerDatabaseConfiguration
	}
	for _, option := range []string{"sslrootcert", "sslcert", "sslkey"} {
		path := values.Get(option)
		if path != "" && (mode == "disable" || !validHostDirectory(filepath.Dir(path)) || !filepath.IsAbs(path) || filepath.Clean(path) != path) {
			return "", ErrDockerDatabaseConfiguration
		}
	}
	seconds := 5
	if value := values.Get("connect_timeout"); value != "" {
		seconds, err = strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 30 {
			return "", ErrDockerDatabaseConfiguration
		}
	}
	values.Set("connect_timeout", strconv.Itoa(seconds))
	u.Scheme, u.Host, u.RawPath, u.RawQuery = "postgres", net.JoinHostPort(host, strconv.Itoa(port)), "", values.Encode()
	canonical := u.String()
	if len(canonical) > maxDockerDatabaseURLBytes {
		return "", ErrDockerDatabaseConfiguration
	}
	return canonical, nil
}

func dockerDatabaseTLSTargets(raw string) ([]dockerDatabaseTLSFile, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrDockerDatabaseConfiguration
	}
	values := u.Query()
	var targets []dockerDatabaseTLSFile
	for _, option := range []string{"sslrootcert", "sslcert", "sslkey"} {
		if path := values.Get(option); path != "" {
			targets = append(targets, dockerDatabaseTLSFile{Option: option, Path: path})
		}
	}
	// lib/pq otherwise discovers a client certificate in the account's home
	// directory. Bind its absence instead of silently adopting ambient credentials.
	if values.Get("sslmode") != "disable" && values.Get("sslcert") == "" {
		account, err := user.Current()
		if err != nil || account.HomeDir == "" {
			return nil, ErrDockerDatabaseConfiguration
		}
		targets = append(targets, dockerDatabaseTLSFile{Option: "default_sslcert_absent", Path: filepath.Join(account.HomeDir, ".postgresql", "postgresql.crt"), Absent: true})
	}
	return targets, nil
}

func observeDockerDatabaseTLS(target dockerDatabaseTLSFile) (dockerDatabaseTLSFile, error) {
	if !filepath.IsAbs(target.Path) || filepath.Clean(target.Path) != target.Path {
		return target, ErrDockerDatabaseConfiguration
	}
	info, err := os.Lstat(target.Path)
	if target.Absent {
		if !errors.Is(err, os.ErrNotExist) {
			return target, ErrDockerDatabaseConfiguration
		}
		return target, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDockerDatabaseTLSBytes {
		return target, ErrDockerDatabaseConfiguration
	}
	resolved, err := filepath.EvalSymlinks(target.Path)
	if err != nil || resolved != target.Path {
		return target, ErrDockerDatabaseConfiguration
	}
	file, err := os.Open(target.Path)
	if err != nil {
		return target, ErrDockerDatabaseConfiguration
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return target, ErrDockerDatabaseConfiguration
	}
	id, err := hostFileIdentity(file)
	if err != nil {
		return target, ErrDockerDatabaseConfiguration
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxDockerDatabaseTLSBytes+1))
	after, statErr := os.Lstat(target.Path)
	if err != nil || statErr != nil || size != info.Size() || !os.SameFile(info, after) || !after.Mode().IsRegular() {
		return target, ErrDockerDatabaseConfiguration
	}
	target.Identity, target.Bytes, target.SHA256 = id, size, hex.EncodeToString(hash.Sum(nil))
	return target, nil
}

func observeDockerDatabaseConfiguration(raw string) (dockerDatabaseConfiguration, error) {
	canonical, err := canonicalDockerDatabaseURL(raw)
	if err != nil {
		return dockerDatabaseConfiguration{}, err
	}
	targets, err := dockerDatabaseTLSTargets(canonical)
	if err != nil {
		return dockerDatabaseConfiguration{}, err
	}
	for i := range targets {
		targets[i], err = observeDockerDatabaseTLS(targets[i])
		if err != nil {
			return dockerDatabaseConfiguration{}, err
		}
	}
	return dockerDatabaseConfiguration{URL: canonical, TLSFiles: targets}, nil
}

func (configuration dockerDatabaseConfiguration) validate() error {
	canonical, err := canonicalDockerDatabaseURL(configuration.URL)
	if err != nil || canonical != configuration.URL {
		return ErrDockerDatabaseConfiguration
	}
	targets, err := dockerDatabaseTLSTargets(canonical)
	if err != nil || len(targets) != len(configuration.TLSFiles) {
		return ErrDockerDatabaseConfiguration
	}
	for i, target := range targets {
		observed, err := observeDockerDatabaseTLS(target)
		if err != nil || observed != configuration.TLSFiles[i] {
			return ErrDockerDatabaseConfiguration
		}
	}
	return nil
}

func openDockerDatabase(ctx context.Context, configuration dockerDatabaseConfiguration, expected database.RuntimeDatabaseIdentity) (*sql.DB, error) {
	if configuration.validate() != nil {
		return nil, ErrDockerDatabaseConfiguration
	}
	// No process environment edits: lib/pq's ambient options are refused before
	// opening a connection, including service/password/certificate overrides.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") {
			return nil, ErrDockerDatabaseConfiguration
		}
	}
	connector, err := pq.NewConnector(configuration.URL)
	if err != nil {
		return nil, ErrDockerDatabaseConfiguration
	}
	// NewConnector captures parsed options once. A later pool connection must
	// not re-read process environment through Driver.Open.
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	probeCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	probe, err := database.BeginRuntimeBinding(probeCtx, db)
	if err != nil {
		db.Close()
		return nil, ErrDockerDatabaseConfiguration
	}
	identity, err := probe.DatabaseIdentity(probeCtx)
	probe.Close()
	if err != nil || identity != expected || configuration.validate() != nil {
		db.Close()
		return nil, ErrDockerDatabaseConfiguration
	}
	return db, nil
}
