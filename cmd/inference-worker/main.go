package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ai-access-gateway/internal/buildinfo"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/store"
)

func main() {
	version := flag.Bool("version-json", false, "print the running build and API contracts without starting services")
	configPath := flag.String("config", "", "protected JSON environment configuration for the native owned worker")
	flag.Parse()
	if *version {
		if err := buildinfo.Write(os.Stdout, buildinfo.InferenceWorker); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *configPath != "" {
		journal, err := os.OpenFile(filepath.Join(filepath.Dir(*configPath), "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatal("cannot open protected worker log")
		}
		defer journal.Close()
		log.SetOutput(journal)
		values, err := readNativeConfiguration(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		for key, value := range values {
			if err = os.Setenv(key, value); err != nil {
				log.Fatal("cannot configure native executor")
			}
		}
	}
	dsn, secret, endpoint := os.Getenv("DATABASE_URL"), os.Getenv("SESSION_SECRET"), os.Getenv("OLLAMA_UPSTREAM")
	if dsn == "" || len(secret) < 32 {
		log.Fatal("DATABASE_URL and SESSION_SECRET (32+ characters) are required")
	}
	mode, err := executionMode(os.Getenv("INFERENCE_RUNTIME"))
	if err != nil {
		log.Fatal(err)
	}
	ollamaAddr, ollamaConfig, err := ollamaAPISettings(mode, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 5*time.Minute)
	db, err := database.OpenRuntime(startup, dsn)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	// Migrations belong to the coordinated Gateway release, not this executor.
	schemaVersion, schemaName := 97, "encrypted_assistant_inference_progress"
	var schemaReady bool
	if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1 AND name=$2)`, schemaVersion, schemaName).Scan(&schemaReady); err != nil || !schemaReady {
		log.Fatal("inference executor schema is not installed; run the coordinated Gateway migration first")
	}
	cipher, err := contentcrypto.NewCipher(secret)
	if err != nil {
		log.Fatal(err)
	}
	outcomeDirectory := strings.TrimSpace(os.Getenv("INFERENCE_OUTCOME_DIR"))
	if outcomeDirectory == "" {
		log.Fatal("INFERENCE_OUTCOME_DIR must name a persistent dedicated directory")
	}
	var worker *inference.Worker
	if mode == "windows-owned" {
		var runtime *modelruntime.Runtime
		var modelConfig modelruntime.Config
		modelConfig, err = ownedModelConfig(os.Getenv)
		if err == nil {
			runtime, err = modelruntime.New(modelConfig)
		}
		if err == nil {
			worker, err = inference.NewOwnedWorker(store.New(db), cipher, runtime, outcomeDirectory)
			if err != nil {
				_ = runtime.Close()
			}
		}
	} else {
		var upstream *url.URL
		upstream, err = url.Parse(endpoint)
		if err == nil {
			worker, err = inference.NewDurableWorker(store.New(db), cipher, upstream, outcomeDirectory)
		}
	}
	if err != nil {
		log.Fatal(err)
	}
	defer worker.Close()
	addr := strings.TrimSpace(os.Getenv("INFERENCE_WORKER_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8096"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal("cannot bind inference readiness listener: ", err)
	}
	health := &http.Server{Handler: worker.ReadinessHandler(db.PingContext), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
	defer health.Close()
	go func() {
		if err := health.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print("inference readiness listener failed; stopping executor")
			stop()
		}
	}()
	if ollamaAddr != "" {
		api, err := inference.NewOllamaAPI(worker, ollamaConfig)
		if err != nil {
			log.Fatal(err)
		}
		defer api.Close()
		apiListener, err := net.Listen("tcp", ollamaAddr)
		if err != nil {
			log.Fatal("cannot bind guarded Ollama API listener")
		}
		apiServer := &http.Server{Handler: api, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 30 * time.Second,
			IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
		defer apiServer.Close()
		go func() {
			if err := apiServer.Serve(apiListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Print("guarded Ollama API listener failed; stopping executor")
				stop()
			}
		}()
		log.Print("guarded Ollama API listener started")
	}
	log.Print("durable inference worker started")
	for ctx.Err() == nil {
		if err = worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("inference worker recovery/storage error: %v; retrying without resubmitting running requests", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func executionMode(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "direct":
		return "direct", nil
	case "windows-owned":
		return "windows-owned", nil
	default:
		return "", errors.New("INFERENCE_RUNTIME must be direct or windows-owned")
	}
}
