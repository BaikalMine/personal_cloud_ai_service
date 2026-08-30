package gateway

import (
	"net"
	"testing"
	"time"
)

func TestWebSocketRegistryClosesOnlyRevokedSession(t *testing.T) {
	app := &App{websocketConnections: make(map[*trackedWebSocket]struct{})}
	serverA, clientA := net.Pipe()
	serverB, clientB := net.Pipe()
	defer clientA.Close()
	defer clientB.Close()
	defer serverB.Close()
	app.registerWebSocket(7, "session-a", "comfyui", serverA)
	trackedB := app.registerWebSocket(7, "session-b", "openwebui", serverB)

	if closed := app.closeSessionWebSockets("session-a"); closed != 1 {
		t.Fatalf("closed WebSockets = %d, want 1", closed)
	}
	_ = clientA.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientA.Read(make([]byte, 1)); err == nil {
		t.Fatal("revoked session connection remained open")
	}
	app.websocketMu.Lock()
	_, stillTracked := app.websocketConnections[trackedB]
	app.websocketMu.Unlock()
	if !stillTracked {
		t.Fatal("unrelated session was removed")
	}
}

func TestWebSocketRegistryClosesOtherUserSessions(t *testing.T) {
	app := &App{websocketConnections: make(map[*trackedWebSocket]struct{})}
	serverA, clientA := net.Pipe()
	serverB, clientB := net.Pipe()
	defer clientA.Close()
	defer clientB.Close()
	current := app.registerWebSocket(9, "current", "comfyui", serverA)
	app.registerWebSocket(9, "other", "comfyui", serverB)

	if closed := app.closeOtherUserWebSockets(9, "current"); closed != 1 {
		t.Fatalf("closed WebSockets = %d, want 1", closed)
	}
	app.websocketMu.Lock()
	_, currentTracked := app.websocketConnections[current]
	app.websocketMu.Unlock()
	if !currentTracked {
		t.Fatal("current session was removed")
	}
	_ = clientB.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientB.Read(make([]byte, 1)); err == nil {
		t.Fatal("other session connection remained open")
	}
	_ = serverA.Close()
}
