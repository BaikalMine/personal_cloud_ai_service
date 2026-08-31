package gateway

import (
	"context"
	"net"
	"net/http"
	"time"

	"ai-access-gateway/internal/security"
)

const websocketAuthorizationRefreshInterval = time.Minute

type trackedWebSocket struct {
	userID      int64
	sessionHash string
	service     string
	connection  net.Conn
}

func sessionHashFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return security.HashToken(cookie.Value)
}

func (a *App) registerWebSocket(userID int64, sessionHash, service string, connection net.Conn) *trackedWebSocket {
	if a == nil || connection == nil || userID <= 0 || sessionHash == "" {
		return nil
	}
	tracked := &trackedWebSocket{userID: userID, sessionHash: sessionHash, service: service, connection: connection}
	a.websocketMu.Lock()
	if a.websocketConnections == nil {
		a.websocketConnections = make(map[*trackedWebSocket]struct{})
	}
	a.websocketConnections[tracked] = struct{}{}
	a.websocketMu.Unlock()
	return tracked
}

func (a *App) unregisterWebSocket(tracked *trackedWebSocket) {
	if a == nil || tracked == nil {
		return
	}
	a.websocketMu.Lock()
	delete(a.websocketConnections, tracked)
	a.websocketMu.Unlock()
}

func (a *App) closeSessionWebSockets(sessionHash string) int {
	return a.closeMatchingWebSockets(func(item *trackedWebSocket) bool { return item.sessionHash == sessionHash })
}

func (a *App) closeTrackedWebSocket(tracked *trackedWebSocket) int {
	return a.closeMatchingWebSockets(func(item *trackedWebSocket) bool { return item == tracked })
}

func (a *App) closeUserWebSockets(userID int64) int {
	return a.closeMatchingWebSockets(func(item *trackedWebSocket) bool { return item.userID == userID })
}

func (a *App) closeOtherUserWebSockets(userID int64, keepSessionHash string) int {
	return a.closeMatchingWebSockets(func(item *trackedWebSocket) bool {
		return item.userID == userID && item.sessionHash != keepSessionHash
	})
}

func (a *App) closeMatchingWebSockets(matches func(*trackedWebSocket) bool) int {
	if a == nil || matches == nil {
		return 0
	}
	a.websocketMu.Lock()
	connections := make([]net.Conn, 0)
	for item := range a.websocketConnections {
		if matches(item) {
			connections = append(connections, item.connection)
			delete(a.websocketConnections, item)
		}
	}
	a.websocketMu.Unlock()
	for _, connection := range connections {
		_ = connection.SetDeadline(time.Now())
		_ = connection.Close()
	}
	return len(connections)
}

func (a *App) pruneUnauthorizedWebSockets(ctx context.Context) (int64, error) {
	if a == nil || a.store == nil {
		return 0, nil
	}
	a.websocketMu.Lock()
	hashes := make([]string, 0, len(a.websocketConnections))
	seen := make(map[string]struct{}, len(a.websocketConnections))
	for item := range a.websocketConnections {
		if _, exists := seen[item.sessionHash]; !exists {
			seen[item.sessionHash] = struct{}{}
			hashes = append(hashes, item.sessionHash)
		}
	}
	a.websocketMu.Unlock()
	if len(hashes) == 0 {
		return 0, nil
	}
	active, err := a.store.ActiveSessionHashes(ctx, hashes, a.cfg.SessionIdleTimeout)
	if err != nil {
		return 0, err
	}
	closed := a.closeMatchingWebSockets(func(item *trackedWebSocket) bool {
		_, ok := active[item.sessionHash]
		return !ok
	})
	return int64(closed), nil
}

func (a *App) authorizeRegisteredWebSocket(ctx context.Context, tracked *trackedWebSocket) bool {
	if a == nil || a.store == nil || tracked == nil || tracked.sessionHash == "" {
		return false
	}
	active, err := a.store.ActiveSessionHashes(ctx, []string{tracked.sessionHash}, a.cfg.SessionIdleTimeout)
	if err != nil {
		return false
	}
	_, ok := active[tracked.sessionHash]
	return ok
}
