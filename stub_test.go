package internetdata

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// An HTTP transport that answers the v2 endpoints from a table and records what
// it was asked for, so "issued exactly one request" and "these two keys were
// answered separately" are asserted rather than assumed.
//
// Routed on the PATH, because every endpoint here is a GET on a fixed path with
// its arguments in the query string.
type stubTransport struct {
	routes map[string]stubRoute
	// Consulted before routes, for the cases that turn on WHO asked: a listing
	// is per organization, so a stub that cannot vary by key cannot express the
	// rule the visibility tests exist to hold.
	byKey map[string]map[string]stubRoute

	mu    sync.Mutex
	calls []stubCall
}

type stubRoute struct {
	status  int
	body    any
	headers map[string]string
}

type stubCall struct {
	path  string
	query string
	key   string
	// The Authorization header verbatim, empty when there was none. `key` alone
	// cannot tell an absent header from "Bearer " with nothing after it, and
	// those are the two outcomes a keyless client has to be held apart.
	auth string
}

func newStub(routes map[string]stubRoute) *stubTransport {
	return &stubTransport{routes: routes}
}

func (s *stubTransport) client() *http.Client {
	return &http.Client{Transport: s}
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	auth := req.Header.Get("Authorization")
	key := strings.TrimPrefix(auth, "Bearer ")
	s.record(stubCall{path: req.URL.Path, query: req.URL.RawQuery, key: key, auth: auth})

	if perKey, ok := s.byKey[key]; ok {
		if route, ok := perKey[req.URL.Path]; ok {
			return s.respond(req, route), nil
		}
	}
	route, ok := s.routes[req.URL.Path]
	if !ok {
		route = stubRoute{status: http.StatusNotFound, body: map[string]string{"rc": "UNKNOWN_DATASET"}}
	}
	return s.respond(req, route), nil
}

func (s *stubTransport) record(call stubCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *stubTransport) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubTransport) seen() []stubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubCall(nil), s.calls...)
}

func (s *stubTransport) respond(req *http.Request, route stubRoute) *http.Response {
	var body []byte
	if route.body != nil {
		encoded, err := json.Marshal(route.body)
		if err != nil {
			encoded = []byte(`{"rc":"STUB_COULD_NOT_ENCODE"}`)
		}
		body = encoded
	}
	header := http.Header{"Content-Type": []string{"application/json"}}
	for name, value := range route.headers {
		header.Set(name, value)
	}
	status := route.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}

// The v2 paths, spelled once so a test never has to.
const (
	pathList      = "/api/v2/database/list"
	pathDownload  = "/api/v2/database/download"
	pathMetadata  = "/api/v2/database/metadata"
	pathChecksum  = "/api/v2/database/checksum"
	pathDownloads = "/api/v2/database/downloads"
)

// A catalog answer built from a set of families, each licensed with one v1.
func catalog(bases ...string) map[string]any {
	databases := make([]map[string]any, 0, len(bases))
	for _, base := range bases {
		databases = append(databases, map[string]any{
			"base": base, "name": base, "summary": "one line",
			"standing": "licensed", "license_type": "standard",
			"starts": nil, "expires": nil,
			"versions": []map[string]any{{
				"id": base + "_v1", "version": 1, "summary": "one line",
				"formats": []string{"csvgz"},
			}},
		})
	}
	return map[string]any{"databases": databases}
}
