//go:build windows

package modelruntime

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOwnedHTTPStreamingLifecycle(t *testing.T) {
	for _, path := range []string{"/api/chat", "/api/generate", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			r, trace := newFixture(t, "stream")
			body := []byte(`{"model":"fixture","stream":true,"tools":[],"think":true}`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var received string
			result, err := r.ExecuteHTTP(ctx, HTTPRequest{Method: "POST", Path: path, Body: body, ResponseLimit: 4096}, func(status int, header http.Header, reader io.Reader) error {
				if status != 200 || header.Get("Content-Type") != "application/x-ndjson" {
					t.Fatal("stream envelope lost")
				}
				buffered := bufio.NewReader(reader)
				first, err := buffered.ReadString('\n')
				if err != nil {
					return err
				}
				if !strings.Contains(first, "first") {
					t.Fatal("first frame not delivered")
				}
				if active, err := r.group.Active(); err != nil || active < 2 {
					t.Fatal("processes stopped before stream consumption", active, err)
				}
				busy, busyErr := r.ExecuteHTTP(ctx, HTTPRequest{Method: "GET", Path: "/api/tags", ResponseLimit: 4096}, func(int, http.Header, io.Reader) error { return nil })
				if !errors.Is(busyErr, ErrBusy) || busy.Dispatched || busy.Stopped {
					t.Fatal("overlapping runtime admitted", busy, busyErr)
				}
				if err = os.WriteFile(trace+".continue", []byte("continue"), 0600); err != nil {
					return err
				}
				rest, err := io.ReadAll(buffered)
				received = first + string(rest)
				return err
			})
			if err != nil || !result.Stopped || !result.Dispatched || result.Status != 200 || len(result.Body) != 0 {
				t.Fatal(result, err)
			}
			if received != "{\"done\":false,\"response\":\"first\"}\n{\"done\":true,\"response\":\"last\"}\n" {
				t.Fatal("stream bytes changed", received)
			}
			actual, err := os.ReadFile(trace + ".request")
			if err != nil || string(actual) != string(body) {
				t.Fatal("request changed", err)
			}
			route, err := os.ReadFile(trace + ".route")
			if err != nil || string(route) != "POST "+path {
				t.Fatal("route changed", string(route), err)
			}
			if active, err := r.group.Active(); err != nil || active != 0 {
				t.Fatal("stream tree survived", active, err)
			}
		})
	}
}

func TestOwnedHTTPStreamAbortAndProof(t *testing.T) {
	for _, mode := range []string{"consumer error", "cancel", "disconnect", "missing proof", "panic"} {
		t.Run(mode, func(t *testing.T) {
			serverMode := "stream"
			if mode == "disconnect" {
				serverMode = "stream-disconnect"
			}
			r, _ := newFixture(t, serverMode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			clientGone := errors.New("fixture client disconnected")
			var result Result
			var err error
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				result, err = r.ExecuteHTTP(ctx, HTTPRequest{Method: "POST", Path: "/api/chat", Body: []byte(modelFixtureRequest), ResponseLimit: 4096}, func(_ int, _ http.Header, reader io.Reader) error {
					buffered := bufio.NewReader(reader)
					if _, err := buffered.ReadString('\n'); err != nil {
						return err
					}
					switch mode {
					case "consumer error":
						return clientGone
					case "cancel":
						cancel()
					case "missing proof":
						if err := r.group.Close(); err != nil {
							return err
						}
					case "panic":
						panic("fixture consumer panic")
					}
					_, err := io.Copy(io.Discard, buffered)
					return err
				})
			}()
			if mode == "panic" {
				if !panicked {
					t.Fatal("panic disappeared")
				}
			} else {
				if !result.Dispatched || err == nil || result.Stopped != (mode != "missing proof") {
					t.Fatal(result, err)
				}
				if mode == "consumer error" && !errors.Is(err, clientGone) {
					t.Fatal("client error lost", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
			}
			if mode != "missing proof" {
				if active, err := r.group.Active(); err != nil || active != 0 {
					t.Fatal("aborted stream survived", active, err)
				}
			}
		})
	}
}

func TestOwnedHTTPReadOnlyAndLimits(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			r, trace := newFixture(t, "normal")
			result, err := r.ExecuteHTTP(context.Background(), HTTPRequest{Method: method, Path: "/api/tags?fixture=1", ResponseLimit: 4096}, func(_ int, _ http.Header, reader io.Reader) error { _, err := io.Copy(io.Discard, reader); return err })
			if err != nil || !result.Stopped || result.Status != 200 {
				t.Fatal(result, err)
			}
			route, err := os.ReadFile(trace + ".route")
			if err != nil || string(route) != method+" /api/tags?fixture=1" {
				t.Fatal("method/query lost", string(route), err)
			}
		})
	}
	t.Run("limit", func(t *testing.T) {
		r, _ := newFixture(t, "oversized")
		result, err := r.ExecuteHTTP(context.Background(), HTTPRequest{Method: "POST", Path: "/api/embed", Body: []byte(modelFixtureRequest), ResponseLimit: 32}, func(_ int, _ http.Header, reader io.Reader) error { _, err := io.Copy(io.Discard, reader); return err })
		if !errors.Is(err, ErrHTTPResponseLimit) || !result.Stopped {
			t.Fatal(result, err)
		}
	})
	t.Run("early consumer", func(t *testing.T) {
		r, _ := newFixture(t, "normal")
		result, err := r.ExecuteHTTP(context.Background(), HTTPRequest{Method: "GET", Path: "/api/tags", ResponseLimit: 1024}, func(int, http.Header, io.Reader) error { return nil })
		if !errors.Is(err, ErrHTTPResponseIncomplete) || !result.Stopped {
			t.Fatal(result, err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		r, _ := newFixture(t, "redirect")
		result, err := r.ExecuteHTTP(context.Background(), HTTPRequest{Method: "GET", Path: "/api/tags", ResponseLimit: 1024}, func(status int, h http.Header, reader io.Reader) error {
			if status != 307 || h.Get("Location") != "http://127.0.0.1:1/foreign" {
				t.Fatal("redirect was followed")
			}
			_, err := io.Copy(io.Discard, reader)
			return err
		})
		if err != nil || !result.Stopped || result.Status != 307 {
			t.Fatal(result, err)
		}
	})
}

func TestOwnedHTTPRejectsInputBeforeLaunch(t *testing.T) {
	r, trace := newFixture(t, "normal")
	result, err := r.ExecuteHTTP(context.Background(), HTTPRequest{Method: http.MethodPost, Path: "http://127.0.0.1:1/api/chat", ResponseLimit: 1024}, func(int, http.Header, io.Reader) error { return nil })
	if !errors.Is(err, ErrHTTPInput) || result.Dispatched || result.Stopped {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid input launched a process", err)
	}
}
