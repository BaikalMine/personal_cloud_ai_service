package release

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

const dockerObservationAPI = "/v1.47"

var errDockerObservation = errors.New("docker runtime observation is unavailable")

// dockerObserver exposes only fixed read-only Engine API requests. Transport
// selection is explicit and local; Docker environment/context settings are not
// used. Access to a trusted local Engine remains a caller precondition.
type dockerObserver struct {
	client    *http.Client
	transport *http.Transport
}

func newDockerObserver(endpoint string) (*dockerObserver, error) {
	dial, err := localDockerDialer(endpoint)
	if err != nil {
		return nil, errDockerObservation
	}
	transport := &http.Transport{Proxy: nil, DialContext: dial, ResponseHeaderTimeout: 5 * time.Second,
		MaxResponseHeaderBytes: 64 << 10, DisableCompression: true}
	return &dockerObserver{transport: transport, client: &http.Client{
		Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (d *dockerObserver) close() { d.transport.CloseIdleConnections() }

func (d *dockerObserver) get(ctx context.Context, route string, query url.Values) (*http.Response, error) {
	requestURL := url.URL{Scheme: "http", Host: "docker", Path: dockerObservationAPI + route, RawQuery: query.Encode()}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, errDockerObservation
	}
	request.Header.Set("User-Agent", "Gateway-Release-Observer/1")
	response, err := d.client.Do(request)
	if err != nil {
		return nil, errDockerObservation
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, errDockerObservation
	}
	return response, nil
}

func (d *dockerObserver) readJSON(ctx context.Context, route string, query url.Values, target any) error {
	response, err := d.get(ctx, route, query)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if !validationContentType(response, "application/json") {
		return errDockerObservation
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return errDockerObservation
	}
	// Engine API responses are open schemas. Check the selected fields below,
	// but accept additional fields from a newer compatible daemon.
	if json.Unmarshal(body, target) != nil {
		return errDockerObservation
	}
	return nil
}
