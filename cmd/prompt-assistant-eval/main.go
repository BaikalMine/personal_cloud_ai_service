package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ai-access-gateway/internal/promptassistant"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "prompt-assistant-eval:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultURL := strings.TrimSpace(os.Getenv("OLLAMA_UPSTREAM"))
	if defaultURL == "" {
		defaultURL = "http://127.0.0.1:11434"
	}
	defaultModel := strings.TrimSpace(os.Getenv("PROMPT_ASSISTANT_MODEL"))
	ollamaURL := flag.String("ollama-url", defaultURL, "Ollama base URL")
	model := flag.String("model", defaultModel, "local model name")
	label := flag.String("label", "", "human-readable model/version label")
	output := flag.String("output", filepath.Join("artifacts", "prompt-assistant-eval", "report.json"), "JSON report path")
	baselinePath := flag.String("baseline", "", "optional baseline JSON report")
	think := flag.Bool("think", false, "enable the model reasoning mode")
	failOnRegression := flag.Bool("fail-on-regression", false, "exit unsuccessfully when pass count or mean score regresses")
	imageTokens := flag.Int("image-tokens", promptassistant.DefaultImageNumPredict, "image prompt token budget")
	imageThinkTokens := flag.Int("image-think-tokens", promptassistant.DefaultImageThinkNumPredict, "image reasoning token budget")
	videoTokens := flag.Int("video-tokens", promptassistant.DefaultVideoNumPredict, "video prompt token budget")
	videoThinkTokens := flag.Int("video-think-tokens", promptassistant.DefaultVideoThinkNumPredict, "video reasoning token budget")
	imageTimeout := flag.Duration("image-timeout", promptassistant.DefaultImageTimeout, "timeout for each image case")
	videoTimeout := flag.Duration("video-timeout", promptassistant.DefaultVideoTimeout, "timeout for each video case")
	keepAlive := flag.String("keep-alive", promptassistant.DefaultKeepAlive, "Ollama keep_alive duration")
	flag.Parse()

	*model = strings.TrimSpace(*model)
	if *model == "" {
		return errors.New("set -model or PROMPT_ASSISTANT_MODEL")
	}
	baseURL, err := url.Parse(strings.TrimSpace(*ollamaURL))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" || baseURL.User != nil {
		return errors.New("-ollama-url must be an http(s) base URL without credentials")
	}
	if *imageTokens <= 0 || *imageThinkTokens <= 0 || *videoTokens <= 0 || *videoThinkTokens <= 0 {
		return errors.New("token budgets must be positive")
	}
	if *imageTimeout <= 0 || *videoTimeout <= 0 {
		return errors.New("timeouts must be positive")
	}
	keepAliveDuration, err := time.ParseDuration(strings.TrimSpace(*keepAlive))
	if err != nil || keepAliveDuration < 0 {
		return errors.New("-keep-alive must be a non-negative Go duration")
	}

	client := promptassistant.NewClientWithPolicy(baseURL, *model, promptassistant.ModelPolicy{
		ImageNumPredict: *imageTokens, ImageThinkNumPredict: *imageThinkTokens,
		VideoNumPredict: *videoTokens, VideoThinkNumPredict: *videoThinkTokens,
		ImageTimeout: *imageTimeout, VideoTimeout: *videoTimeout, KeepAlive: strings.TrimSpace(*keepAlive),
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report := promptassistant.RunFixedEvaluation(ctx, client, *model, *label, *think, func(index, total int, caseID string) {
		fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", index, total, caseID)
	})

	regressed := false
	if strings.TrimSpace(*baselinePath) != "" {
		baselineData, readErr := os.ReadFile(*baselinePath)
		if readErr != nil {
			return fmt.Errorf("read baseline: %w", readErr)
		}
		var baseline promptassistant.EvaluationReport
		if err := json.Unmarshal(baselineData, &baseline); err != nil {
			return fmt.Errorf("decode baseline: %w", err)
		}
		comparison, compareErr := promptassistant.CompareEvaluationReports(baseline, report)
		if compareErr != nil {
			return compareErr
		}
		report.Comparison = comparison
		regressed = comparison.PassedDelta < 0 || comparison.ScoreDelta < 0
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*output, append(payload, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("suite=%s model=%s passed=%d/%d mean_score=%.2f mean_latency_ms=%d\n", report.SuiteID, report.Model, report.Summary.PassedCount, report.Summary.CaseCount, report.Summary.MeanScore, report.Summary.MeanLatencyMS)
	if report.Comparison != nil {
		fmt.Printf("baseline=%s score_delta=%+.2f passed_delta=%+d latency_delta_ms=%+d\n", report.Comparison.BaselineLabel, report.Comparison.ScoreDelta, report.Comparison.PassedDelta, report.Comparison.LatencyDeltaMS)
	}
	fmt.Println("report=" + *output)
	if *failOnRegression && regressed {
		return errors.New("candidate regressed against the fixed baseline")
	}
	return nil
}
