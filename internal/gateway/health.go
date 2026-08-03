package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

func (a *App) serviceStatuses(ctx context.Context) []ServiceStatus {
	statuses := make([]ServiceStatus, 2)
	checks := []struct {
		name string
		url  *url.URL
		auth string
	}{
		{name: "ComfyUI", url: a.cfg.ComfyUIUpstream, auth: a.cfg.ComfyUIUpstreamAuthHeader},
		{name: "OpenWebUI", url: a.cfg.OpenWebUIUpstream, auth: a.cfg.OpenWebUIUpstreamAuth},
	}
	var wg sync.WaitGroup
	for index := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			check := checks[index]
			statuses[index] = a.checkService(ctx, check.name, check.url, check.auth)
		}()
	}
	wg.Wait()
	return statuses
}

func (a *App) checkService(parent context.Context, name string, upstream *url.URL, authHeader string) ServiceStatus {
	status := ServiceStatus{Name: name}
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.String(), http.NoBody)
	if err != nil {
		status.Detail = "неверный адрес сервиса"
		return status
	}
	req.Header.Set("User-Agent", "ai-access-gateway/health")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := (&http.Client{}).Do(req)
	status.Latency = time.Since(started)
	if err != nil {
		status.Detail = "недоступен"
		return status
	}
	defer resp.Body.Close()
	status.Status = resp.StatusCode
	status.Online = resp.StatusCode < http.StatusInternalServerError
	if !status.Online {
		status.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	} else {
		status.Detail = "готов"
	}
	return status
}
