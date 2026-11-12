package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

func validHarnessAgentFilePath(value string) bool {
	return value != "" && len(value) <= 4096 && !path.IsAbs(value) && !strings.ContainsAny(value, "\\\x00\r\n") &&
		path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func (c *harnessAgentClient) files(w http.ResponseWriter, r *http.Request, task harnessAgentTask) {
	switch r.Method {
	case "GET":
		filePath := r.URL.Query().Get("path")
		if !validHarnessAgentFilePath(filePath) {
			harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_file_path"}, task.ID)
			return
		}
		request, err := c.request(r.Context(), "GET", "/api/gateway-files/download", url.Values{"sessionId": {task.ID}, "path": {filePath}}, nil)
		if err != nil {
			harnessAgentWriteError(w, err, task.ID)
			return
		}
		client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: rejectUpstreamRedirect}
		response, err := client.Do(request)
		if err != nil {
			harnessAgentWriteError(w, err, task.ID)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			harnessAgentWriteError(w, &harnessAgentFault{404, "file_unavailable"}, task.ID)
			return
		}
		for _, header := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
			if value := response.Header.Get(header); value != "" {
				w.Header().Set(header, value)
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(200)
		_, _ = io.Copy(w, response.Body)
	case "POST":
		var input struct {
			Name   string `json:"name"`
			Base64 string `json:"data_base64"`
		}
		if err := harnessAgentReadJSON(w, r, 29<<20, &input); err != nil {
			harnessAgentWriteError(w, err, task.ID)
			return
		}
		if strings.TrimSpace(input.Name) != input.Name || input.Name == "" || input.Name == "." || input.Name == ".." || len(input.Name) > 220 || strings.ContainsAny(input.Name, "/\\\x00\r\n") || len(input.Base64) > ((20<<20)+2)/3*4 {
			harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_file"}, task.ID)
			return
		}
		data, err := base64.StdEncoding.Strict().DecodeString(input.Base64)
		if err != nil || len(data) > 20<<20 || base64.StdEncoding.EncodeToString(data) != input.Base64 {
			clear(data)
			harnessAgentWriteError(w, &harnessAgentFault{400, "invalid_file_data"}, task.ID)
			return
		}
		clear(data)
		body, _ := json.Marshal(map[string]string{"action": "upload", "sessionId": task.ID, "path": ".", "name": input.Name, "base64": input.Base64})
		var response struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := c.json(r.Context(), "/api/gateway-files/manage", body, &response); err != nil {
			harnessAgentWriteError(w, err, task.ID)
			return
		}
		if !response.OK {
			status, code := 400, "file_upload_failed"
			if response.Error.Code == "exists" {
				status, code = 409, "file_exists"
			}
			harnessAgentWriteError(w, &harnessAgentFault{status, code}, task.ID)
			return
		}
		writeJSON(w, 201, map[string]any{"id": task.ID, "file": response.Value})
	default:
		harnessAgentWriteError(w, &harnessAgentFault{405, "method_not_allowed"}, task.ID)
	}
}
