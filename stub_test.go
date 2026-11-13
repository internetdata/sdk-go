package internetdata

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// An HTTP transport that answers from a table and records what it was asked
// for, so "issued exactly one request", "never touched the network", "kept at
// most N in flight" and "these two keys were answered separately" are asserted
// rather than assumed.
//
// A lookup is routed on the address its path names, anything under /api/ on
// its path, and POST /batch is answered from the same table.
type stubTransport struct {
	routes map[string]stubRoute
	// Consulted before routes, for the cases that turn on WHO asked: a listing
	// is per organization, so a stub that cannot vary by key cannot express the
	// rule the visibility tests exist to hold.
	byKey map[string]map[string]stubRoute
	// How long each answer is held. A real transport gives up when the
	// request's context does, and a test of who runs a request under whose
	// context needs that.
	delay time.Duration

	mu       sync.Mutex
	calls    []stubCall
	batched  [][]string
	inFlight int
	peak     int
}

type stubRoute struct {
	status  int
	body    any
	headers map[string]string
}

type stubCall struct {
	url   string
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
	s.enter(stubCall{url: req.URL.String(), path: req.URL.Path, query: req.URL.RawQuery, key: key, auth: auth})
	defer s.leave()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	if req.Method == http.MethodPost && req.URL.Path == "/batch" {
		return s.respond(req, s.batch(req)), nil
	}
	return s.respond(req, s.route(key, stubKey(req))), nil
}

func (s *stubTransport) route(key, at string) stubRoute {
	s.mu.Lock()
	defer s.mu.Unlock()
	if route, ok := s.byKey[key][at]; ok {
		return route
	}
	if route, ok := s.routes[at]; ok {
		return route
	}
	if strings.HasPrefix(at, "/api/") {
		return stubRoute{status: http.StatusNotFound, body: map[string]string{"rc": "UNKNOWN_DATASET"}}
	}
	return stubRoute{status: http.StatusBadRequest, body: map[string]string{"error": invalidTarget}}
}

const invalidTarget = "not a valid IP address, AS number or domain"

// A POST /batch is answered the way the API answers one: every address the
// table knows is a result if its route is a 200 and an entry error otherwise,
// and an unknown address is the 400 the API gives a string that is not one.
// One call however many addresses, which is what the request counts measure.
func (s *stubTransport) batch(req *http.Request) stubRoute {
	var in struct {
		IPs []string `json:"ips"`
	}
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &in)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batched = append(s.batched, in.IPs)
	results := map[string]any{}
	failures := map[string]any{}
	for _, ip := range in.IPs {
		route, ok := s.routes[ip]
		if !ok {
			failures[ip] = map[string]any{"status": http.StatusBadRequest, "error": invalidTarget}
			continue
		}
		if route.status == 0 || route.status == http.StatusOK {
			results[ip] = route.body
			continue
		}
		encoded, _ := json.Marshal(route.body)
		failures[ip] = map[string]any{"status": route.status, "error": messageOf(encoded)}
	}
	return stubRoute{body: map[string]any{"results": results, "errors": failures}}
}

func (s *stubTransport) enter(call stubCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	s.inFlight++
	s.peak = max(s.peak, s.inFlight)
}

func (s *stubTransport) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
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

func (s *stubTransport) peakInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *stubTransport) respond(req *http.Request, route stubRoute) *http.Response {
	var body []byte
	if raw, ok := route.body.([]byte); ok {
		body = raw
	} else if route.body != nil {
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

// The lookup path is the target itself (an address, asn/9009, domain/x, myip);
// anything under /api/ is keyed by its whole path.
func stubKey(req *http.Request) string {
	path := strings.TrimPrefix(req.URL.Path, "/")
	if strings.HasPrefix(path, "api/") {
		return "/" + path
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		return unescaped
	}
	return path
}

// Successful lookups for a set of addresses, which is what most cases want.
func okRoutes(ips ...string) map[string]stubRoute {
	routes := make(map[string]stubRoute, len(ips))
	for _, ip := range ips {
		routes[ip] = stubRoute{body: answerAbout(ip, nil)}
	}
	return routes
}

// The corpus's Unlimited answer, about ip and with members replaced, so the
// stub serves what the API serves: an answer short of a required member would
// be refused as unreadable.
func answerAbout(ip string, members map[string]any) map[string]any {
	answer := corpusBody("unlimited-answer")
	answer["ip"] = ip
	maps.Copy(answer, members)
	return answer
}

// A corpus lookup case's served body, by the case's name.
func corpusBody(name string) map[string]any {
	body, ok := corpusBodies()[name]
	if !ok {
		panic("the corpus has no lookup case named " + name)
	}
	return maps.Clone(body)
}

var corpusBodies = sync.OnceValue(func() map[string]map[string]any {
	raw, err := os.ReadFile("testdata/testdata.json")
	if err != nil {
		panic(err)
	}
	var corpus struct {
		Lookup []struct {
			Name string         `json:"name"`
			Body map[string]any `json:"body"`
		} `json:"lookup"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		panic(err)
	}
	bodies := make(map[string]map[string]any, len(corpus.Lookup))
	for _, c := range corpus.Lookup {
		bodies[c.Name] = c.Body
	}
	return bodies
})

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
			"standing": "licensed", "open": false, "license_type": "standard",
			"starts": nil, "expires": nil,
			"versions": []map[string]any{{
				"id": base + "_v1", "version": 1, "summary": "one line",
				"formats": []string{"csvgz"},
			}},
		})
	}
	return map[string]any{"databases": databases}
}
