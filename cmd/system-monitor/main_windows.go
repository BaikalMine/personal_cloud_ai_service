//go:build windows

// system-monitor is intentionally a separate, read-only Windows service. It
// cannot start, stop, update, inspect, or otherwise interact with the miner.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/miningagent"
)

type config struct {
	ListenAddress string `json:"listen_address"`
	Token         string `json:"token"`
	LogFile       string `json:"log_file"`
}

func main() {
	configPath := flag.String("config", "system-monitor.json", "path to the monitor configuration file")
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
	tokenHash := sha256.Sum256([]byte(settings.Token))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/v1/system", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		value := strings.TrimSpace(r.Header.Get("Authorization"))
		token, ok := strings.CutPrefix(value, "Bearer ")
		candidate := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(candidate[:], tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="system-monitor"`)
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		metrics, err := miningagent.ReadWindowsSystemMetrics(ctx)
		if err != nil {
			if metrics.Message == "" {
				metrics.Message = "Не удалось получить метрики Windows."
			}
			writeMetrics(w, http.StatusServiceUnavailable, metrics)
			return
		}
		writeMetrics(w, http.StatusOK, metrics)
	})
	server := &http.Server{Addr: settings.ListenAddress, Handler: secure(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	log.Printf("system monitor listening on %s", settings.ListenAddress)
	return server.ListenAndServe()
}

func loadConfig(path string) (config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	var settings config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return config{}, fmt.Errorf("decode config: %w", err)
	}
	if strings.TrimSpace(settings.ListenAddress) == "" {
		settings.ListenAddress = ":8094"
	}
	if len(strings.TrimSpace(settings.Token)) < 32 {
		return config{}, errors.New("a token of at least 32 characters is required")
	}
	if strings.TrimSpace(settings.LogFile) == "" {
		settings.LogFile = filepath.Join(filepath.Dir(path), "system-monitor.log")
	}
	return settings, nil
}

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func writeMetrics(w http.ResponseWriter, status int, metrics mining.SystemMetrics) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(metrics)
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
