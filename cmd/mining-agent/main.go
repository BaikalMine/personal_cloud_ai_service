package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ai-access-gateway/internal/miningagent"
)

type config struct {
	ListenAddress          string   `json:"listen_address"`
	MiningRoot             string   `json:"mining_root"`
	Token                  string   `json:"token"`
	LogFile                string   `json:"log_file"`
	MinerLogFile           string   `json:"miner_log_file"`
	AllowedArchivePrefixes []string `json:"allowed_archive_prefixes"`
}

func main() {
	configPath := flag.String("config", "mining-agent.json", "path to the agent configuration file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		log.Fatal(err)
	}
}

func run(configPath string) error {
	settings, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	logFile, err := openLog(settings.LogFile)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)

	controller, err := miningagent.NewController(settings.MiningRoot, settings.MinerLogFile, settings.AllowedArchivePrefixes...)
	if err != nil {
		return err
	}
	agent, err := miningagent.NewServer(settings.Token, controller)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              settings.ListenAddress,
		Handler:           agent.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      35 * time.Minute,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Printf("mining agent listening on %s; root=%s", settings.ListenAddress, settings.MiningRoot)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loadConfig(path string) (config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var settings config
	if err := decoder.Decode(&settings); err != nil {
		return config{}, fmt.Errorf("decode config: %w", err)
	}
	if settings.ListenAddress == "" {
		settings.ListenAddress = "0.0.0.0:8092"
	}
	if settings.MiningRoot == "" || len(settings.Token) < 32 {
		return config{}, errors.New("mining_root and a token of at least 32 characters are required")
	}
	base := filepath.Dir(path)
	if settings.LogFile == "" {
		settings.LogFile = filepath.Join(base, "mining-agent.log")
	}
	if settings.MinerLogFile == "" {
		settings.MinerLogFile = filepath.Join(base, "miner-output.log")
	}
	if len(settings.AllowedArchivePrefixes) == 0 {
		settings.AllowedArchivePrefixes = []string{"https://github.com/doktor83/SRBMiner-Multi/releases/download/"}
	}
	return settings, nil
}

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
