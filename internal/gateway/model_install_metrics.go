package gateway

import (
	"context"
	"math"
	"time"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/store"
)

type modelInstallView struct {
	store.ModelInstall
	BytesDownloaded  *int64   `json:"bytes_downloaded,omitempty"`
	TotalBytes       *int64   `json:"total_bytes,omitempty"`
	BytesPerSecond   *float64 `json:"bytes_per_second,omitempty"`
	MetricsAvailable bool     `json:"metrics_available"`
}

// Telemetry is read-only and best effort; an unavailable manager never hides durable jobs.
func modelInstallViews(ctx context.Context, client *modelmanager.Client, jobs []store.ModelInstall) []modelInstallView {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	views := make([]modelInstallView, 0, len(jobs))
	for _, job := range jobs {
		view := modelInstallView{ModelInstall: job}
		if (job.State == "running" || job.State == "reconciling") && ctx.Err() == nil {
			if result, err := client.Progress(ctx, job.ID); err == nil && result.Success {
				applyModelInstallMetrics(&view, result)
			}
		}
		views = append(views, view)
	}
	return views
}

func applyModelInstallMetrics(view *modelInstallView, result modelmanager.Result) {
	if !math.IsNaN(result.Progress) && !math.IsInf(result.Progress, 0) {
		view.Progress = math.Max(view.Progress, math.Max(0, math.Min(100, result.Progress)))
		view.MetricsAvailable = true
	}
	if result.TotalBytes != nil && *result.TotalBytes > 0 {
		view.TotalBytes = result.TotalBytes
	}
	if result.BytesDownloaded != nil && *result.BytesDownloaded >= 0 && (view.TotalBytes == nil || *result.BytesDownloaded <= *view.TotalBytes) {
		view.BytesDownloaded = result.BytesDownloaded
	}
	if view.State == "running" && view.Progress < 100 && result.Status != "completed" && result.Status != "error" && result.Status != "failed" && result.Status != "cancelled" &&
		result.BytesPerSecond != nil && *result.BytesPerSecond >= 0 && !math.IsNaN(*result.BytesPerSecond) && !math.IsInf(*result.BytesPerSecond, 0) {
		view.BytesPerSecond = result.BytesPerSecond
	}
}
