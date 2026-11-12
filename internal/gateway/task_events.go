package gateway

import (
	"fmt"
	"io"
	"net/http"
)

func (a *App) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	a.serveLiveEvents(w, r, "tasks", func(w io.Writer, event string, revision int64) error {
		_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %d\n\n", revision, event, revision)
		return err
	})
}
