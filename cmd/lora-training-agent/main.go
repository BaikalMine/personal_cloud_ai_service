package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"ai-access-gateway/internal/trainingagent"
)

func main() {
	configPath := flag.String("config", "", "path to the LoRA training agent JSON config")
	flag.Parse()
	var config trainingagent.Config
	var err error
	if *configPath != "" {
		config, err = trainingagent.LoadConfigFile(*configPath)
	} else {
		config, err = trainingagent.LoadConfig()
	}
	if err != nil {
		log.Fatal(err)
	}
	if config.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(config.LogFile), 0o750); err != nil {
			log.Fatal(err)
		}
		logFile, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			log.Fatal(err)
		}
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}
	controller, err := trainingagent.NewController(config)
	if err != nil {
		log.Fatal(err)
	}
	defer controller.Close()
	serverHandler, err := trainingagent.NewServer(config.Token, controller)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr: config.Addr, Handler: serverHandler.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 20 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("LoRA training agent listening on %s", config.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
