package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/gateway"
)

func main() {
	verifyDatasets := flag.Bool("verify-training-datasets", false, "verify decryption and integrity of saved datasets in an isolated restored database, without starting Gateway")
	version := flag.Bool("version-json", false, "print the running build and API contracts without starting services")
	flag.Parse()
	if *version {
		if err := buildinfo.Write(os.Stdout, buildinfo.Gateway); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *verifyDatasets {
		os.Exit(verifyTrainingDatasets())
	}
	if err := gateway.Run(); err != nil {
		log.Fatal(err)
	}
}

func verifyTrainingDatasets() int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var inventory gateway.TrainingDatasetRestoreReport
	db, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err == nil {
		defer db.Close()
		inventory, err = gateway.VerifyTrainingDatasetRestore(ctx, db, os.Getenv("SESSION_SECRET"))
	}
	report := struct {
		Success   bool                                 `json:"success"`
		Error     string                               `json:"error,omitempty"`
		Inventory gateway.TrainingDatasetRestoreReport `json:"inventory"`
	}{Success: err == nil, Inventory: inventory}
	if err != nil {
		report.Error = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil || err != nil {
		return 1
	}
	return 0
}
