package inference

import (
	"encoding/json"
	"mime"
	"net/http"
	"sync"
	"time"
)

const ollamaCatalogLimit = 1 << 20
const ollamaCatalogMaxAge = time.Hour

type ollamaCatalogEntry struct {
	body        []byte
	contentType string
	at          time.Time
}

// Only last-confirmed catalogs are reusable during contention. They are never
// evidence that a process is stopped or a GPU is idle, and never survive restart.
type ollamaCatalogCache struct {
	mu      sync.RWMutex
	entries map[string]ollamaCatalogEntry
}

func ollamaCatalogRoute(path string) bool {
	return path == "/api/tags" || path == "/v1/models"
}

func (c *ollamaCatalogCache) put(path, contentType string, body []byte, now time.Time) {
	if !ollamaCatalogRoute(path) || len(body) > ollamaCatalogLimit {
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return
	}
	key := "models"
	if path == "/v1/models" {
		key = "data"
	}
	var entries []json.RawMessage
	if json.Unmarshal(object[key], &entries) != nil || entries == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]ollamaCatalogEntry, 2)
	}
	c.entries[path] = ollamaCatalogEntry{body: append([]byte(nil), body...), contentType: contentType, at: now}
}

func (c *ollamaCatalogCache) serve(w http.ResponseWriter, r *http.Request, now time.Time) bool {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !ollamaCatalogRoute(r.URL.Path) {
		return false
	}
	c.mu.RLock()
	entry, ok := c.entries[r.URL.Path]
	c.mu.RUnlock()
	if !ok || now.Before(entry.at) || now.Sub(entry.at) >= ollamaCatalogMaxAge {
		return false
	}
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("X-AI-Gateway-Catalog", "cached")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(entry.body)
	}
	return true
}

type ollamaCatalogCapture struct {
	body     []byte
	overflow bool
}

func (c *ollamaCatalogCapture) Write(data []byte) (int, error) {
	if !c.overflow {
		if len(c.body)+len(data) > ollamaCatalogLimit {
			c.body = nil
			c.overflow = true
		} else {
			c.body = append(c.body, data...)
		}
	}
	return len(data), nil
}
