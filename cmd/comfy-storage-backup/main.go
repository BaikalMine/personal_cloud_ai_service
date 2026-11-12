package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-access-gateway/internal/updateagent"
)

func main() {
	mode := flag.String("mode", "", "export or restore")
	configPath := flag.String("config", "", "update-agent config (export only)")
	archive := flag.String("archive", "", "archive path; export never overwrites a file")
	destination := flag.String("destination", "", "new isolated restore directory")
	python := flag.String("python", "", "absolute Python executable (SQLite restore only)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var manifest updateagent.ComfyStorageBackupManifest
	var err error
	switch {
	case *archive == "":
		err = errors.New("archive is required")
	case *mode == "export" && *configPath != "":
		var config updateagent.Config
		var source *os.File
		source, err = os.Open(*configPath)
		if err == nil {
			var payload []byte
			payload, err = io.ReadAll(io.LimitReader(source, (1<<20)+1))
			if err == nil && len(payload) > 1<<20 {
				err = errors.New("invalid update-agent configuration")
			}
			if err == nil {
				decoder := json.NewDecoder(bytes.NewReader(payload))
				decoder.DisallowUnknownFields()
				err = decoder.Decode(&config)
				if err == nil && decoder.Decode(new(any)) != io.EOF {
					err = errors.New("invalid update-agent configuration")
				}
			}
			source.Close()
		}
		if err == nil {
			var file *os.File
			file, err = os.OpenFile(*archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err == nil {
				owned, statErr := file.Stat()
				manifest, err = updateagent.ExportComfyStorageBackup(ctx, config.ComfyUI, file)
				err = errors.Join(err, statErr, file.Sync(), file.Close())
				if err != nil && owned != nil {
					if current, checkErr := os.Lstat(*archive); checkErr == nil && os.SameFile(owned, current) {
						_ = os.Remove(*archive)
					}
				}
			}
		}
	case *mode == "restore" && *destination != "" && *python != "":
		manifest, err = updateagent.RestoreComfyStorageBackup(ctx, *archive, *destination, *python)
	default:
		err = errors.New("choose export with config, or restore with destination and Python")
	}
	report := struct {
		Success  bool                                   `json:"success"`
		Error    string                                 `json:"error,omitempty"`
		Manifest updateagent.ComfyStorageBackupManifest `json:"manifest"`
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
