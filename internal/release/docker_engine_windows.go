package release

import (
	"context"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

var localPipeName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func localDockerDialer(endpoint string) (func(context.Context, string, string) (net.Conn, error), error) {
	name, ok := strings.CutPrefix(endpoint, "npipe:////./pipe/")
	if !ok || !localPipeName.MatchString(name) || name == "." || name == ".." {
		return nil, errDockerObservation
	}
	return func(ctx context.Context, _, address string) (net.Conn, error) {
		if address != "docker:80" {
			return nil, errDockerObservation
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return winio.DialPipeContext(ctx, `\\.\pipe\`+name)
	}, nil
}
