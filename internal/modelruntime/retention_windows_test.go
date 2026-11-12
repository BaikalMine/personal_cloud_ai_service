//go:build windows

package modelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestHarnessRuntimeReusesOwnedProcessUntilExplicitStop(t *testing.T) {
	r, trace := newFixture(t, "normal")
	consume := func(_ int, _ http.Header, body io.Reader) error { _, err := io.Copy(io.Discard, body); return err }
	request := HTTPRequest{Method: "POST", Path: "/v1/chat/completions", Body: []byte(modelFixtureRequest), ResponseLimit: 4096, Retain: true}
	first, cancel := context.WithCancel(context.Background())
	result, err := r.ExecuteHTTP(first, request, consume)
	if err != nil || !result.Retained || result.Stopped {
		t.Fatal("first request not retained", result, err)
	}
	before, _ := os.ReadFile(trace)
	var original fixtureProcesses
	if json.Unmarshal(before, &original) != nil {
		t.Fatal("process proof missing")
	}
	cancel()
	if active, err := r.group.Active(); err != nil || active < 2 {
		t.Fatal("request cancellation stopped retained process", active, err)
	}
	result, err = r.ExecuteHTTP(context.Background(), request, consume)
	if err != nil || !result.Retained || result.Stopped {
		t.Fatal("second request not retained", result, err)
	}
	after, _ := os.ReadFile(trace)
	var current fixtureProcesses
	if json.Unmarshal(after, &current) != nil || original.Server != current.Server || original.Child != current.Child {
		t.Fatal("process restarted between steps")
	}
	ordinary := request
	ordinary.Retain = false
	if _, err := r.ExecuteHTTP(context.Background(), ordinary, consume); !errors.Is(err, ErrBusy) {
		t.Fatal("ordinary call stole Harness runtime", err)
	}
	stop, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	stopped, err := r.StopRetained(stop)
	cancelStop()
	if !stopped || err != nil {
		t.Fatal("stop not confirmed", err)
	}
	if active, err := r.group.Active(); err != nil || active != 0 {
		t.Fatal("owned tree survived final stop", active, err)
	}
	result, err = r.ExecuteHTTP(context.Background(), ordinary, consume)
	if err != nil || !result.Stopped || result.Retained {
		t.Fatal("ordinary lifecycle changed", result, err)
	}
}

func TestHarnessRuntimeFailureStopsRetainedTree(t *testing.T) {
	r, _ := newFixture(t, "normal")
	request := HTTPRequest{Method: "POST", Path: "/v1/chat/completions", Body: []byte(modelFixtureRequest), ResponseLimit: 4096, Retain: true}
	_, err := r.ExecuteHTTP(context.Background(), request, func(_ int, _ http.Header, body io.Reader) error { _, err := io.Copy(io.Discard, body); return err })
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("fixture client disconnected")
	result, err := r.ExecuteHTTP(context.Background(), request, func(int, http.Header, io.Reader) error { return failure })
	if !errors.Is(err, failure) || result.Retained || !result.Stopped {
		t.Fatal("failed request retained runtime", result, err)
	}
	if active, err := r.group.Active(); err != nil || active != 0 {
		t.Fatal("failed retained tree survived", active, err)
	}
}
