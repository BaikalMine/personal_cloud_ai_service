package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const adminUltraPath = "/admin/harness-ultra"

var ultraHistoryClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Transport: transport, CheckRedirect: rejectUpstreamRedirect}
}()

type ultraHistoryMedia struct {
	Index         int   `json:"index"`
	Bytes         int64 `json:"bytes"`
	Width, Height int
	Missing       bool
}

type ultraHistoryJob struct {
	ID             string              `json:"id"`
	State          string              `json:"state"`
	Stage          string              `json:"stage"`
	Created        float64             `json:"created"`
	Updated        float64             `json:"updated"`
	ElapsedSeconds float64             `json:"elapsed_seconds"`
	Error          string              `json:"error"`
	PromptExcerpt  string              `json:"prompt_excerpt"`
	Mode           string              `json:"mode"`
	Model          string              `json:"model"`
	Width          int                 `json:"width"`
	Height         int                 `json:"height"`
	Outputs        []ultraHistoryMedia `json:"outputs"`
	Inputs         []ultraHistoryMedia `json:"inputs"`
	Request        json.RawMessage     `json:"request"`
	Parameters     json.RawMessage     `json:"parameters"`
}

func (j ultraHistoryJob) Active() bool {
	return j.State != "completed" && j.State != "failed" && j.State != "cancelled"
}
func (j ultraHistoryJob) OutputCount() int64 { return int64(len(j.Outputs)) }
func (j ultraHistoryJob) StateLabel() string {
	if j.State == "completed" {
		return "Готово"
	}
	if j.State == "failed" {
		return "Ошибка"
	}
	if j.State == "cancelled" {
		return "Отменено"
	}
	if j.State == "waiting_gpu" {
		return "Ожидает GPU"
	}
	if j.State == "queued" {
		return "В очереди"
	}
	if j.State == "finalizing" {
		return "Сохранение"
	}
	if j.State == "running" {
		return "Генерация"
	}
	if j.State == "submission_unknown" {
		return "Проверка запуска"
	}
	return "Подготовка"
}
func (j ultraHistoryJob) Tone() string {
	if j.State == "completed" {
		return "ok"
	}
	if j.State == "failed" {
		return "danger"
	}
	return ""
}
func (j ultraHistoryJob) ModeLabel() string {
	return map[string]string{"text": "По тексту", "references": "По референсам", "identity": "Персонаж", "control": "Контроль глубины", "inpaint": "Правка по маске", "outpaint": "Расширение кадра"}[j.Mode]
}
func (j ultraHistoryJob) CreatedAt() time.Time { return time.UnixMilli(int64(j.Created * 1000)) }
func (j ultraHistoryJob) UpdatedAt() time.Time { return time.UnixMilli(int64(j.Updated * 1000)) }
func (j ultraHistoryJob) Duration() string {
	return formatDurationLong(time.Duration(j.ElapsedSeconds * float64(time.Second)))
}
func (j ultraHistoryJob) Prompt() string {
	var request struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(j.Request, &request)
	return request.Prompt
}
func (j ultraHistoryJob) Negative() string {
	var request struct {
		Negative string `json:"negative_prompt"`
	}
	_ = json.Unmarshal(j.Request, &request)
	return request.Negative
}
func (j ultraHistoryJob) RequestJSON() string    { return ultraPrettyJSON(j.Request) }
func (j ultraHistoryJob) ParametersJSON() string { return ultraPrettyJSON(j.Parameters) }
func ultraPrettyJSON(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "{}"
	}
	encoded, _ := json.MarshalIndent(value, "", "  ")
	return string(encoded)
}

type ultraHistoryList struct {
	Items      []ultraHistoryJob `json:"items"`
	Counts     map[string]int    `json:"counts"`
	NextCursor string            `json:"next_cursor"`
}

func (a *App) ultraHistoryRead(ctx context.Context, path, query string) (*http.Response, error) {
	h := a.cfg.Harness
	if h.UltraHistoryURL == nil || h.UltraHistoryKey == "" {
		return nil, errors.New("history not configured")
	}
	u := *h.UltraHistoryURL
	u.Path, u.RawPath, u.RawQuery = "/v1/history"+path, "", query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.UltraHistoryKey)
	return ultraHistoryClient.Do(req)
}

func (a *App) handleAdminUltra(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, adminUltraPath)
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if path != "" && (len(parts) != 1 && len(parts) != 3 || !harnessUltraID.MatchString(parts[0])) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 {
		if (parts[1] != "inputs" && parts[1] != "outputs") || len(parts[2]) > 2 {
			http.NotFound(w, r)
			return
		}
		index, err := strconv.Atoi(parts[2])
		if err != nil || index < 0 || strconv.Itoa(index) != parts[2] {
			http.NotFound(w, r)
			return
		}
		a.handleAdminUltraMedia(w, r, path, parts[0], index)
		return
	}
	query := r.URL.Query()
	for name, values := range query {
		if len(values) != 1 || name != "q" && name != "state" && name != "cursor" && name != "partial" {
			http.Error(w, "неверный фильтр", 400)
			return
		}
	}
	if len([]rune(query.Get("q"))) > 200 || len(query.Get("cursor")) > 160 || !ultraHistoryState(query.Get("state")) || query.Get("partial") != "" && query.Get("partial") != "1" {
		http.Error(w, "неверный фильтр", 400)
		return
	}
	if path != "" && (query.Get("q") != "" || query.Get("state") != "" || query.Get("cursor") != "") {
		http.Error(w, "неверный фильтр", 400)
		return
	}
	partial := query.Get("partial") == "1"
	query.Del("partial")
	data := map[string]any{"Title": "История Harness Ultra", "Query": query.Get("q"), "State": query.Get("state"), "Filtered": len(query) > 0, "List": ultraHistoryList{Counts: map[string]int{}}, "RefreshURL": r.URL.Path}
	if query.Encode() != "" {
		data["RefreshURL"] = r.URL.Path + "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	response, err := a.ultraHistoryRead(ctx, path, query.Encode())
	status := http.StatusOK
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode == 404 && path != "" {
			http.NotFound(w, r)
			return
		}
		if response.StatusCode == 400 {
			http.Error(w, "неверный фильтр истории", 400)
			return
		}
		if response.StatusCode != 200 {
			err = errors.New("history read failed")
		} else {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
			data["SnapshotVersion"] = fmt.Sprintf("%x", sha256.Sum256(body))
			if readErr != nil || len(body) > 2<<20 {
				err = errors.New("invalid history response")
			} else if path == "" {
				var list ultraHistoryList
				err = json.Unmarshal(body, &list)
				if err == nil && (list.Counts == nil || len(list.Items) > 50) {
					err = errors.New("invalid history list")
				}
				for _, job := range list.Items {
					if !harnessUltraID.MatchString(job.ID) || job.State == "" {
						err = errors.New("invalid history item")
					}
				}
				data["List"] = list
				if list.NextCursor != "" {
					query.Set("cursor", list.NextCursor)
					data["NextURL"] = adminUltraPath + "?" + query.Encode()
				}
			} else {
				var job ultraHistoryJob
				err = json.Unmarshal(body, &job)
				if err == nil && (job.ID != parts[0] || job.State == "") {
					err = errors.New("history identity mismatch")
				}
				data["Job"] = job
			}
		}
	}
	if err != nil {
		status = http.StatusServiceUnavailable
		data["LoadError"] = "История Harness Ultra временно недоступна. Сохранённые задания не удалены."
		delete(data, "Job")
	}
	if partial {
		if err != nil {
			http.Error(w, "история временно недоступна", status)
			return
		}
		a.renderStatus(w, r, status, "admin_ultra_snapshot", data)
		return
	}
	a.renderStatus(w, r, status, "admin_ultra", data)
}

func ultraHistoryState(state string) bool {
	return state == "" || state == "active" || state == "completed" || state == "failed" || state == "cancelled"
}

func (a *App) handleAdminUltraMedia(w http.ResponseWriter, r *http.Request, path, id string, index int) {
	if len(r.URL.Query()) > 1 || (len(r.URL.Query()) == 1 && r.URL.Query().Get("download") != "1") {
		http.Error(w, "неверный запрос", 400)
		return
	}
	release := a.acquireAdminMediaSlot(r.Context())
	if release == nil {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	response, err := a.ultraHistoryRead(ctx, path, "")
	if err != nil {
		http.Error(w, "файл временно недоступен", 503)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		http.NotFound(w, r)
		return
	}
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != 200 || (kind != "image/png" && kind != "image/jpeg" && kind != "image/webp") || response.ContentLength <= 0 || response.ContentLength > 512<<20 {
		http.Error(w, "файл временно недоступен", 503)
		return
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[kind]
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": fmt.Sprintf("harness-ultra-%s-%d.%s", id, index+1, ext)}))
	w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, response.Body, response.ContentLength)
}
