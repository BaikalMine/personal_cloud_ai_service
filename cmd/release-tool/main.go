package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("release-tool", flag.ContinueOnError)
	mode := flags.String("mode", "verify", "validate-stamp, pack, verify, compare, database-preflight, validation-smoke or docker-binding")
	version := flags.Bool("version-json", false, "print artifact tool build without reading files")
	directory := flags.String("directory", "", "release directory")
	previous := flags.String("previous", "", "previous release manifest for schema comparison")
	previousDirectory := flags.String("previous-directory", "", "previous verified package for database-preflight")
	gatewayImage := flags.String("gateway-image", "", "immutable gateway image ID")
	inferenceImage := flags.String("inference-image", "", "immutable inference image ID")
	publicURL := flags.String("public-url", "", "literal-loopback Gateway origin for validation-smoke or docker-binding")
	adminURL := flags.String("admin-url", "", "literal-loopback candidate admin origin for validation-smoke")
	dockerEndpoint := flags.String("docker-endpoint", "", "explicit local Docker Unix socket or Windows named pipe for docker-binding")
	engineID := flags.String("engine-id", "", "trusted Docker Engine ID for docker-binding")
	containerID := flags.String("container-id", "", "full immutable container ID for docker-binding")
	containerPort := flags.Uint("container-port", 0, "container TCP port mapped to the Gateway origin")
	manifestHash := flags.String("manifest-sha256", "", "independently trusted installed manifest hash for docker-binding")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *version {
		return buildinfo.Write(output, buildinfo.ReleaseTool)
	}
	if *mode == "validate-stamp" {
		return release.ValidateBuildEnvironment(os.Getenv)
	}
	if *directory == "" {
		return fmt.Errorf("release directory is required")
	}
	if *mode == "pack" {
		manifest, err := release.Pack(*directory, *gatewayImage, *inferenceImage)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(manifest)
	}
	if *mode == "docker-binding" {
		if *containerPort < 1 || *containerPort > 65535 {
			return fmt.Errorf("docker-binding requires a valid container port")
		}
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return fmt.Errorf("DATABASE_URL is required for docker-binding")
		}
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return fmt.Errorf("database connection configuration is invalid")
		}
		defer db.Close()
		report, bindingErr := release.ObserveDockerGateway(context.Background(), db, *directory, release.DockerGatewayOptions{
			EngineEndpoint: *dockerEndpoint, EngineID: *engineID, ContainerID: *containerID, GatewayPort: uint16(*containerPort),
			RuntimeBindingOptions: release.RuntimeBindingOptions{Origin: *publicURL, ManifestHash: *manifestHash, SessionToken: os.Getenv("RELEASE_BINDING_SESSION")},
		})
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
		return bindingErr
	}
	if *mode == "validation-smoke" {
		epoch, _ := strconv.ParseInt(os.Getenv("RELEASE_OPERATION_EPOCH"), 10, 64)
		report, smokeErr := release.SmokeValidation(context.Background(), *directory, release.ValidationSmokeOptions{
			PublicURL: *publicURL, AdminURL: *adminURL,
			Operation: database.RuntimeValidationRequest{
				OperationID: os.Getenv("RELEASE_OPERATION_ID"), Epoch: epoch,
				CandidateManifestHash: os.Getenv("RELEASE_CANDIDATE_MANIFEST_SHA256"), Token: os.Getenv("RELEASE_OPERATION_TOKEN"),
			},
			Username: os.Getenv("RELEASE_SMOKE_USERNAME"), Password: os.Getenv("RELEASE_SMOKE_PASSWORD"),
		})
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
		return smokeErr
	}
	if *mode != "verify" && *mode != "compare" && *mode != "database-preflight" {
		return fmt.Errorf("unknown release mode")
	}
	file, err := os.Open(filepath.Join(*directory, "manifest.json"))
	if err != nil {
		return err
	}
	manifest, err := release.Read(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	if err := manifest.VerifyFiles(*directory); err != nil {
		return err
	}
	if *mode == "verify" {
		return json.NewEncoder(output).Encode(map[string]any{"verified": true, "build": manifest.Build, "images": manifest.Images})
	}
	if *mode == "database-preflight" {
		return databasePreflight(output, manifest, *previousDirectory)
	}
	file, err = os.Open(*previous)
	if err != nil {
		return err
	}
	old, err := release.Read(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(release.CompareSchema(old.Migrations, manifest.Migrations))
}

func databasePreflight(output io.Writer, candidate release.Manifest, previousDirectory string) error {
	if previousDirectory == "" {
		return fmt.Errorf("database-preflight requires a previous release directory")
	}
	file, err := os.Open(filepath.Join(previousDirectory, "manifest.json"))
	if err != nil {
		return err
	}
	previous, err := release.Read(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	if err = previous.VerifyFiles(previousDirectory); err != nil {
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required for database-preflight")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("database connection configuration is invalid")
	}
	defer db.Close()
	report, err := release.InspectDatabase(context.Background(), db, previous, candidate)
	if encodeErr := json.NewEncoder(output).Encode(report); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		return err
	}
	if !report.DatabaseChecksPassed {
		return fmt.Errorf("database preflight has blocking observations")
	}
	return nil
}
