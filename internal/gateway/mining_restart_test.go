package gateway

import (
	"ai-access-gateway/internal/mining"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestMiningRestartConfirmsStopBeforeStart(t *testing.T) {
	for _, stopRunning := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "stop_unconfirmed"}[stopRunning], func(t *testing.T) {
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				running := stopRunning
				if r.URL.Path == "/v1/start" {
					running = true
				}
				_ = json.NewEncoder(w).Encode(mining.State{Available: true, Running: running})
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			app := &App{mining: mining.NewClient(base, "fixture")}
			result, err := app.miningCommand(context.Background(), "restart", mining.Request{ProcessName: "miner.exe", ScriptPath: `C:\Mining\start.bat`}, nil)
			expected := []string{"/v1/stop"}
			if !stopRunning {
				expected = append(expected, "/v1/start")
				if err != nil || !result.State.Running {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("restart proceeded without stop")
			}
			if !reflect.DeepEqual(calls, expected) {
				t.Fatal(calls)
			}
		})
	}
}

func TestMiningRestartFailureDoesNotRetryCommands(t *testing.T) {
	for _, scenario := range []string{"stop_http_error", "stop_unavailable", "stop_cancelled", "start_http_error", "start_not_running", "start_unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			calls := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.URL.Path
				if r.URL.Path == "/v1/stop" && scenario == "stop_cancelled" {
					time.Sleep(1500 * time.Millisecond)
					return
				}
				if (r.URL.Path == "/v1/stop" && scenario == "stop_http_error") || (r.URL.Path == "/v1/start" && scenario == "start_http_error") {
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(mining.State{})
					return
				}
				state := mining.State{Available: true, Running: r.URL.Path == "/v1/start"}
				if scenario == "stop_unavailable" || (r.URL.Path == "/v1/start" && scenario == "start_unavailable") {
					state.Available = false
				}
				if scenario == "start_not_running" {
					state.Running = false
				}
				_ = json.NewEncoder(w).Encode(state)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			app := &App{mining: mining.NewClient(base, "fixture")}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := app.miningCommand(ctx, "restart", mining.Request{ProcessName: "miner.exe", ScriptPath: `C:\Mining\go.bat`}, nil)
			if err == nil {
				t.Fatal("failure reported as success")
			}
			close(calls)
			var got []string
			for path := range calls {
				got = append(got, path)
			}
			want := []string{"/v1/stop"}
			if scenario[:5] == "start" {
				want = append(want, "/v1/start")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("commands=%v want=%v", got, want)
			}
		})
	}
}
