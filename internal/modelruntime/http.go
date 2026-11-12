package modelruntime

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-access-gateway/internal/domain"
)

const MaxHTTPResponseBytes int64 = 64 << 20

var (
	ErrHTTPInput              = errors.New("invalid owned model HTTP request")
	ErrHTTPResponseLimit      = errors.New("owned model HTTP response exceeds limit")
	ErrHTTPResponseIncomplete = errors.New("owned model HTTP response was not consumed to EOF")
)

// HTTPRequest is internal transport input, not a public admission API. Its caller
// must authorize the route and own the GPU resource until ExecuteHTTP returns.
// Paths are origin-relative; clients cannot choose another upstream or headers.
type HTTPRequest struct {
	Method        string
	Path          string
	Body          []byte
	ResponseLimit int64
	// Internal Harness task admission only; never inferred from a model payload.
	Retain bool
}

// HTTPConsumer runs synchronously while the process tree is still owned. It must
// read to EOF or return an error. Stream bytes or HTTP success never prove that
// GPU work stopped; only the returned Result.Stopped provides that evidence.
type HTTPConsumer func(status int, headers http.Header, body io.Reader) error

func validateHTTPRequest(request HTTPRequest, consume HTTPConsumer) error {
	if consume == nil || len(request.Body) > domain.MaxGPUInferenceRequestBytes || request.ResponseLimit < 1 || request.ResponseLimit > MaxHTTPResponseBytes {
		return ErrHTTPInput
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		if len(request.Body) != 0 {
			return ErrHTTPInput
		}
	case http.MethodPost, http.MethodDelete:
	default:
		return ErrHTTPInput
	}
	u, err := url.ParseRequestURI(request.Path)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(request.Path, "/") || strings.HasPrefix(request.Path, "//") || strings.ContainsAny(request.Path, "\\#\r\n") {
		return ErrHTTPInput
	}
	return nil
}

type responseReader struct {
	source    io.Reader
	remaining int64
	complete  bool
	err       error
}

func (r *responseReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.complete {
		return 0, io.EOF
	}
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.source.Read(extra[:])
		if n > 0 {
			r.err = ErrHTTPResponseLimit
			return 0, r.err
		}
		if errors.Is(err, io.EOF) {
			r.complete = true
		} else if err != nil {
			r.err = err
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	r.remaining -= int64(n)
	if errors.Is(err, io.EOF) {
		r.complete = true
	} else if err != nil {
		r.err = err
	}
	return n, err
}

func consumeHTTPResponse(response *http.Response, limit int64, consume HTTPConsumer) error {
	reader := &responseReader{source: response.Body, remaining: limit}
	err := consume(response.StatusCode, response.Header.Clone(), reader)
	if err != nil {
		return err
	}
	if reader.err != nil {
		return reader.err
	}
	if !reader.complete {
		return ErrHTTPResponseIncomplete
	}
	return nil
}
