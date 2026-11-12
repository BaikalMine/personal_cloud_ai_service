package miningagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/mining"
)

const fenceTestToken = "mining-fence-test-token-not-production"

type delayedController struct {
	fakeController
	entered, release chan struct{}
	starts           atomic.Int32
	running          atomic.Bool
}

func (c *delayedController) Start(context.Context, mining.Request) (mining.State, error) {
	c.starts.Add(1)
	if c.entered != nil {
		close(c.entered)
		<-c.release
	}
	c.running.Store(true)
	return mining.State{Running: true}, nil
}
func (c *delayedController) Stop(context.Context, mining.Request) (mining.State, error) {
	c.running.Store(false)
	return mining.State{}, nil
}
func (c *delayedController) State(context.Context, string) (mining.State, error) {
	return mining.State{Running: c.running.Load()}, nil
}

func (c *delayedController) Update(ctx context.Context, request mining.UpdateRequest) (mining.UpdateResult, error) {
	state, err := c.Start(ctx, mining.Request{ScriptPath: request.ScriptPath, ProcessName: request.ProcessName})
	return mining.UpdateResult{Success: err == nil, Restarted: state.Running}, err
}

type failedStartController struct{ delayedController }

func (*failedStartController) Start(context.Context, mining.Request) (mining.State, error) {
	return mining.State{}, errors.New("fixture launcher completion unknown")
}

func TestMiningFenceDoesNotTurnControllerErrorIntoIdleProof(t *testing.T) {
	s, _ := NewFencedServer(fenceTestToken, &failedStartController{})
	for _, item := range []struct {
		action   string
		revision int64
		code     int
	}{{"start", 1, 409}, {"barrier", 2, 409}, {"start", 3, 409}, {"wrong-stop", 4, 409}, {"stop", 5, 200}, {"barrier", 6, 200}} {
		command := fenceCommand(s, item.revision, item.action)
		if item.action == "wrong-stop" {
			command.Action = "stop"
			command.Miner.ProcessName = "other.exe"
		}
		if w := sendFence(s, context.Background(), command); w.Code != item.code {
			t.Fatal(item, w.Code, w.Body.String())
		}
	}
}

type rejectedStopController struct{ failedStartController }

func (*rejectedStopController) Stop(context.Context, mining.Request) (mining.State, error) {
	return mining.State{}, errCommandRejected
}

func TestMiningFenceRejectedStopPreservesUnknownOutcome(t *testing.T) {
	s, _ := NewFencedServer(fenceTestToken, &rejectedStopController{})
	for i, action := range []string{"start", "stop", "barrier"} {
		response := sendFence(s, context.Background(), fenceCommand(s, int64(i+1), action))
		if response.Code != 409 || !s.commandUnconfirmed {
			t.Fatal(action, response.Code, response.Body.String(), s.commandUnconfirmed)
		}
	}
}

func TestMiningFenceUpdateCannotRestartAfterBarrier(t *testing.T) {
	c := &delayedController{entered: make(chan struct{}), release: make(chan struct{})}
	s, _ := NewFencedServer(fenceTestToken, c)
	command := fenceCommand(s, 1, "update")
	command.Update = &mining.UpdateRequest{ScriptPath: command.Miner.ScriptPath, ProcessName: command.Miner.ProcessName, ArchiveURL: "https://example.test/miner.zip", ArchiveSHA256: strings.Repeat("a", 64)}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- sendFence(s, context.Background(), command) }()
	<-c.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if w := sendFence(s, ctx, fenceCommand(s, 2, "barrier")); w.Body.Len() != 0 {
		t.Fatal("update was not drained")
	}
	close(c.release)
	if w := <-done; w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := sendFence(s, context.Background(), fenceCommand(s, 3, "stop")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := sendFence(s, context.Background(), command); w.Code != 409 {
		t.Fatal("old update replayed")
	}
	if c.running.Load() || c.starts.Load() != 1 {
		t.Fatal("update restarted miner after stop")
	}
}

func fenceCommand(s *Server, revision int64, action string) mining.FencedCommand {
	return mining.FencedCommand{Session: s.session, Revision: revision, Action: action, Miner: mining.Request{ScriptPath: `C:\Mining\test.bat`, ProcessName: "test.exe"}}
}
func sendFence(s *Server, ctx context.Context, command mining.FencedCommand) *httptest.ResponseRecorder {
	body, _ := json.Marshal(command)
	r := httptest.NewRequest(http.MethodPost, "/v2/command", bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+fenceTestToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestMiningFenceRejectsLateStartAndPreviousSession(t *testing.T) {
	c := &delayedController{}
	s, _ := NewFencedServer(fenceTestToken, c)
	old := fenceCommand(s, 1, "start")
	if w := sendFence(s, context.Background(), fenceCommand(s, 2, "barrier")); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, command := range []mining.FencedCommand{old, fenceCommand(s, 2, "start")} {
		if w := sendFence(s, context.Background(), command); w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
	restarted, _ := NewFencedServer(fenceTestToken, c)
	old.Revision = 100
	if w := sendFence(restarted, context.Background(), old); w.Code != 409 {
		t.Fatal("previous agent session accepted")
	}
	if c.starts.Load() != 0 {
		t.Fatal("stale command started miner")
	}
	if w := sendFence(restarted, context.Background(), fenceCommand(restarted, 101, "start")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if c.starts.Load() != 1 {
		t.Fatal("new session did not start")
	}
}

func TestMiningFenceWaitsForActualControllerCompletion(t *testing.T) {
	c := &delayedController{entered: make(chan struct{}), release: make(chan struct{})}
	s, _ := NewFencedServer(fenceTestToken, c)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- sendFence(s, context.Background(), fenceCommand(s, 1, "start")) }()
	<-c.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	w := sendFence(s, ctx, fenceCommand(s, 2, "barrier"))
	if w.Body.Len() != 0 {
		t.Fatal("barrier acknowledged unfinished controller")
	}
	close(c.release)
	if result := <-done; result.Code != 200 {
		t.Fatal(result.Code)
	}
	if w = sendFence(s, context.Background(), fenceCommand(s, 3, "stop")); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = sendFence(s, context.Background(), fenceCommand(s, 2, "start")); w.Code != 409 {
		t.Fatal("older command passed completed stop")
	}
	if c.running.Load() || c.starts.Load() != 1 {
		t.Fatal("miner resumed after stop")
	}
}

func TestMiningFenceDisablesLegacyMutations(t *testing.T) {
	s, _ := NewFencedServer(fenceTestToken, &delayedController{})
	for _, path := range []string{"/v1/start", "/v1/stop", "/v1/update"} {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
		r.Header.Set("Authorization", "Bearer "+fenceTestToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatal(path, w.Code)
		}
	}
}
