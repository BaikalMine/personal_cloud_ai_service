package main

import (
	"ai-access-gateway/internal/buildinfo"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMonitorBuildAuthentication(t *testing.T) {
	token := strings.Repeat("t", 32)
	handler := buildHandler(sha256.Sum256([]byte(token)))
	for _, tc := range []struct {
		method, auth string
		status       int
	}{
		{"GET", "", 401}, {"GET", "Bearer wrong", 401}, {"GET", "Bearer " + token, 200}, {"HEAD", "Bearer " + token, 200}, {"POST", "Bearer " + token, 405},
	} {
		r := httptest.NewRequest(tc.method, "/v1/build", nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc.method, w.Code)
		}
		if tc.status == 200 && tc.method == "GET" {
			var report buildinfo.Report
			if json.Unmarshal(w.Body.Bytes(), &report) != nil || buildinfo.Compare(buildinfo.SystemMonitor, report).Status != "compatible" {
				t.Fatal(w.Body.String())
			}
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
}
