package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/release"
)

func TestReleaseToolMetadataDoesNotReadDirectories(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--version-json", "--directory", "/missing-directory"}, &output); err != nil {
		t.Fatal(err)
	}
	var report buildinfo.Report
	if json.Unmarshal(output.Bytes(), &report) != nil || buildinfo.Compare(buildinfo.ReleaseTool, report).Status != "compatible" {
		t.Fatal(output.String())
	}
}

func TestDockerGatewayCLIBoundary(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RELEASE_BINDING_SESSION", "cli-session-must-not-appear")
	for _, port := range []string{"0", "65536", "8090"} {
		var output bytes.Buffer
		err := run([]string{"--mode", "docker-binding", "--directory", t.TempDir(), "--container-port", port}, &output)
		if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "cli-session-must-not-appear") {
			t.Fatal("invalid CLI configuration escaped its early boundary")
		}
	}
	t.Setenv("DATABASE_URL", "postgres://test:db-secret-must-not-appear@127.0.0.1:1/test?sslmode=disable")
	var output bytes.Buffer
	err := run([]string{"--mode", "docker-binding", "--directory", t.TempDir(), "--container-port", "8090", "--public-url", "http://127.0.0.1:18090",
		"--manifest-sha256", strings.Repeat("a", 64), "--engine-id", "fixture-engine-000000000001", "--container-id", strings.Repeat("c", 64),
		"--docker-endpoint", "tcp://remote-must-not-appear:2375"}, &output)
	var report release.DockerGatewayReport
	if err == nil || json.Unmarshal(output.Bytes(), &report) != nil || report.FailureCode != "manifest_open" || report.Success || report.DeploymentAuthorized || report.Identity != nil {
		t.Fatalf("CLI package boundary: %s %v", output.String(), err)
	}
	for _, secret := range []string{"cli-session-must-not-appear", "db-secret-must-not-appear", "remote-must-not-appear"} {
		if strings.Contains(output.String(), secret) || strings.Contains(err.Error(), secret) {
			t.Fatal("CLI exposed a private input")
		}
	}
}

func TestDatabasePreflightRequiresExplicitPreviousPackage(t *testing.T) {
	if err := databasePreflight(&bytes.Buffer{}, release.Manifest{}, ""); err == nil {
		t.Fatal("database preflight accepted no previous package")
	}
	if err := run([]string{"--mode", "database-preflight", "--directory", t.TempDir(), "--previous-directory", t.TempDir()}, &bytes.Buffer{}); err == nil {
		t.Fatal("database preflight accepted missing manifests")
	}
}

func TestReleaseToolRejectsInvalidModesAndMissingInputs(t *testing.T) {
	for _, args := range [][]string{nil, {"--mode", "unknown", "--directory", "/missing"}, {"--mode", "verify", "--directory", "/missing"}, {"--mode", "pack"}, {"--version-json", "unexpected"}} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
	t.Setenv("BUILD_VERSION", "injected -X anything")
	if err := run([]string{"--mode", "validate-stamp"}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid stamp accepted")
	}
}
