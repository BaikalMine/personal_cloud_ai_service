package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const harnessNativeBusy = `{"error":{"code":"gpu_busy","type":"gateway_error","message":"gpu_busy"}}`

func TestHarnessWaitsOnlyForConfirmedPreDispatchBusy(t *testing.T) {
	for _, scenario := range []string{"busy then admitted", "permanent busy", "malformed busy", "other conflict", "server failure", "cancel", "stream error"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"model":"fixture"}` || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("replayed request changed")
				}
				if scenario == "busy then admitted" && count == 3 || scenario == "stream error" {
					w.WriteHeader(200)
					io.WriteString(w, "data: partial\n\n")
					return
				}
				status, reply := 409, harnessNativeBusy
				switch scenario {
				case "malformed busy":
					reply = "not JSON"
				case "other conflict":
					reply = `{"error":{"code":"another_error"}}`
				case "server failure":
					status = 503
				}
				w.WriteHeader(status)
				io.WriteString(w, reply)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				time.AfterFunc(10*time.Millisecond, cancel)
			}
			r, _ := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"model":"fixture"}`))
			r.Header.Set("Authorization", "Bearer fixture")
			transport := harnessAdmissionTransport{base: http.DefaultTransport, wait: 40 * time.Millisecond, interval: 5 * time.Millisecond}
			response, err := transport.RoundTrip(r)
			if response != nil {
				defer response.Body.Close()
			}
			if scenario == "cancel" {
				if !errors.Is(err, context.Canceled) || calls.Load() < 1 {
					t.Fatal("cancelled request replayed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "busy then admitted" {
				if calls.Load() != 3 || response.StatusCode != 200 {
					t.Fatal("admission wait failed")
				}
			} else if scenario == "permanent busy" {
				if calls.Load() < 2 || calls.Load() > 10 || response.StatusCode != 409 {
					t.Fatal("unbounded admission retries")
				}
			} else if calls.Load() != 1 {
				t.Fatal("unsafe request was retried")
			}
		})
	}
}

type harnessTransportFunc func(*http.Request) (*http.Response, error)

func (f harnessTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHarnessDoesNotReplayLostResponses(t *testing.T) {
	var calls int
	transport := harnessAdmissionTransport{base: harnessTransportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, io.ErrUnexpectedEOF
	}), wait: time.Second, interval: time.Millisecond}
	r, _ := http.NewRequest("POST", "http://fixture.test", strings.NewReader(`{}`))
	if _, err := transport.RoundTrip(r); err == nil || calls != 1 {
		t.Fatal("ambiguous dispatch replayed")
	}
}
