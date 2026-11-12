//go:build !windows

package release

import (
	"context"
	"net"
	"path"
	"strings"
	"time"
)

func localDockerDialer(endpoint string) (func(context.Context, string, string) (net.Conn, error), error) {
	socket, ok := strings.CutPrefix(endpoint, "unix://")
	if !ok || !strings.HasPrefix(socket, "/") || socket == "/" || len(socket) > 4096 || path.Clean(socket) != socket || strings.ContainsAny(socket, "\x00\r\n?#") {
		return nil, errDockerObservation
	}
	return func(ctx context.Context, _, address string) (net.Conn, error) {
		if address != "docker:80" {
			return nil, errDockerObservation
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}, nil
}
