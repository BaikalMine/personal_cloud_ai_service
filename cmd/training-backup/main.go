package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"ai-access-gateway/internal/trainingagent"
)

func main() {
	mode := flag.String("mode", "", "export or restore")
	configPath := flag.String("config", "", "training-agent config (export only)")
	archive := flag.String("archive", "", "archive path; export never overwrites an existing archive")
	destination := flag.String("destination", "", "new isolated restore directory")
	datasetPolicy := flag.String("dataset-policy", "", "database: saved datasets remain in the companion database dump")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var manifest trainingagent.TrainingBackupManifest
	var err error
	switch {
	case *archive == "":
		err = errors.New("archive is required")
	case *mode == "export":
		var config trainingagent.Config
		config, err = trainingagent.LoadConfigFile(*configPath)
		if err == nil {
			var file *os.File
			file, err = os.OpenFile(*archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err == nil {
				manifest, err = trainingagent.ExportTrainingBackup(ctx, config, file, *datasetPolicy)
				err = errors.Join(err, file.Sync(), file.Close())
				if err != nil {
					_ = os.Remove(*archive)
				}
			}
		}
	case *mode == "restore" && *destination != "":
		manifest, err = trainingagent.RestoreTrainingBackup(ctx, *archive, *destination)
	default:
		err = errors.New("choose export with config/dataset-policy or restore with destination")
	}
	report := struct {
		Success  bool                                 `json:"success"`
		Error    string                               `json:"error,omitempty"`
		Manifest trainingagent.TrainingBackupManifest `json:"manifest"`
	}{Success: err == nil, Manifest: manifest}
	if err != nil {
		report.Error = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		fmt.Fprintln(os.Stderr, "cannot write backup report")
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}
