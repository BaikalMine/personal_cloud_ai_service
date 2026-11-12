package mining

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFencedClientRequiresMatchingAcknowledgement(t *testing.T) {
	for _, mode := range []string{"okay", "legacy", "wrong-session", "wrong-revision", "missing-state", "lost", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			session := strings.Repeat("a", 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing authentication")
				}
				if r.URL.Path == "/v2/session" {
					json.NewEncoder(w).Encode(CommandSession{Protocol: 2, Session: session, Required: mode != "legacy"})
					return
				}
				posts.Add(1)
				if r.URL.Path != "/v2/command" {
					t.Error("unexpected replay", r.URL.Path)
				}
				var request FencedCommand
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Session != session || request.Revision != 7 {
					t.Error("wrong command")
				}
				result := CommandResult{Session: session, Revision: 7, State: State{Available: true, Running: true}}
				switch mode {
				case "wrong-session":
					result.Session = strings.Repeat("b", 32)
				case "wrong-revision":
					result.Revision = 8
				case "missing-state":
					result.State = State{}
				case "lost":
					w.WriteHeader(504)
				case "redirect":
					w.Header().Set("Location", "/elsewhere")
					w.WriteHeader(307)
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			_, err := NewClient(base, "test-token").Fenced(context.Background(), FencedCommand{Action: "start", Revision: 7})
			if (err == nil) != (mode == "okay") {
				t.Fatal(mode, err)
			}
			want := int32(1)
			if mode == "legacy" {
				want = 0
			}
			if posts.Load() != want {
				t.Fatal("command repeated/fell back", posts.Load())
			}
		})
	}
}

func TestFencedClientReportsRejectedCommandWithoutReplay(t *testing.T) {
	for _, scenario := range []string{"needs-stop", "unknown-text", "wrong-status", "controller-error", "wrong-session"} {
		t.Run(scenario, func(t *testing.T) {
			session := strings.Repeat("a", 32)
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/session" {
					json.NewEncoder(w).Encode(CommandSession{Protocol: 2, Session: session, Required: true})
					return
				}
				posts.Add(1)
				if scenario == "controller-error" || scenario == "wrong-session" {
					ackSession := session
					if scenario == "wrong-session" {
						ackSession = strings.Repeat("b", 32)
					}
					w.WriteHeader(http.StatusConflict)
					json.NewEncoder(w).Encode(CommandResult{Session: ackSession, Revision: 7, State: State{Message: "fixture launch failed"}})
					return
				}
				status := http.StatusConflict
				if scenario == "wrong-status" {
					status = http.StatusBadGateway
				}
				message := ErrCommandNeedsStop.Error()
				if scenario == "unknown-text" {
					message = "superseded command"
				}
				http.Error(w, message, status)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			_, err := NewClient(base, "fixture").Fenced(context.Background(), FencedCommand{Action: "start", Revision: 7})
			if err == nil || posts.Load() != 1 {
				t.Fatal("rejected command accepted or replayed", err, posts.Load())
			}
			if errors.Is(err, ErrCommandNeedsStop) != (scenario == "needs-stop") {
				t.Fatal("incorrect recovery signal", err)
			}
			if strings.Contains(err.Error(), "fixture launch failed") != (scenario == "controller-error") {
				t.Fatal("wrong diagnostic evidence", err)
			}
		})
	}
}
