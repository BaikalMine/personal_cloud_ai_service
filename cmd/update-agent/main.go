package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"ai-access-gateway/internal/updateagent"
)

func main() {
	configPath := flag.String("config", "", "path to the update-agent JSON configuration")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("-config is required")
	}

	file, err := os.Open(*configPath)
	if err != nil {
		log.Fatalf("open config: %v", err)
	}
	defer file.Close()
	var config updateagent.Config
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		log.Fatalf("decode config: %v", err)
	}
	if config.LogFile != "" {
		logFile, err := os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		defer logFile.Close()
		log.SetOutput(logFile)
	}
	controller, err := updateagent.NewController(config)
	if err != nil {
		log.Fatalf("configure update agent: %v", err)
	}
	server, err := updateagent.NewServer(config.Token, controller, config.ComfyUI)
	if err != nil {
		log.Fatalf("create update agent: %v", err)
	}
	httpServer := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Installations may pull images or rebuild dependencies for several minutes.
		WriteTimeout: 25 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}
	log.Printf("update agent listening on %s", config.ListenAddress)
	log.Fatal(httpServer.ListenAndServe())
}
