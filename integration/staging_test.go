// The staging fixtures the test files share: the credential gate, the client,
// and a transport that records what was asked for without ever holding the key.

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	internetdata "github.com/internetdata/sdk-go"
)

const (
	staging = "https://staging.internetdata.io"
	// The one credential this suite runs on. Actions interpolates a secret that
	// does not exist to an EMPTY STRING rather than leaving the variable unset,
	// so an empty value means the same thing as an absent one.
	keyEnv = "INTERNETDATA_STAGING_KEY"
	// Bodies above this are never held: a database transfer runs through the
	// same transport as a catalog read, so reading one to its end here would be
	// the multi-gigabyte mistake the SDK exists to avoid.
	maxCapturedBody = 1 << 20
)

func TestMain(m *testing.M) {
	if reason := skipReason(); reason != "" {
		fmt.Printf("==> %s\n", reason)
		notice(reason)
	} else {
		fmt.Println("==> staging key present")
	}
	code := m.Run()
	removeTempDir()
	os.Exit(code)
}

func stagingKey() string {
	return strings.TrimSpace(os.Getenv(keyEnv))
}

// A reason, or the empty string when the suite can run.
//
// Empty counts as absent, and the gate is the ONLY thing standing between an
// unset secret and a green run: the client accepts a keyless build and sends no
// Authorization header, so an ungated suite would collect 401s that every
// assertion below reads as an ordinary refusal.
func skipReason() string {
	if stagingKey() != "" {
		return ""
	}
	return keyEnv + " is not set, so nothing here can be exercised"
}

// A client of its own per test, so one test's request record cannot be read
// through another's.
func clientFor(t *testing.T) (*internetdata.Client, *recorder) {
	t.Helper()
	if reason := skipReason(); reason != "" {
		t.Skip(reason)
	}
	rec := &recorder{key: stagingKey(), bodies: map[string]json.RawMessage{}}
	client, err := internetdata.New(
		internetdata.WithAPIKey(rec.key),
		internetdata.WithBaseURL(staging),
		internetdata.WithHTTPClient(&http.Client{Transport: rec}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, rec
}

// What a test is allowed to remember about a request it made.
//
// Only derived facts leave here. An assertion that fails prints its operands, so
// holding on to the request itself is how a key ends up in a public CI log:
// whether the key was carried is a boolean, and the caller never sees the key.
type fact struct {
	origin     string
	path       string
	carriedKey bool
}

// Records what was asked for and holds on to small JSON answers: the client
// keeps the decoded result, and these tests also need what the wire carried.
type recorder struct {
	key string

	mu     sync.Mutex
	facts  []fact
	bodies map[string]json.RawMessage
}

func (rec *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	rec.note(req)
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || res == nil {
		return res, err
	}
	return rec.capture(req, res)
}

func (rec *recorder) note(req *http.Request) {
	carried := rec.key != "" && strings.Contains(req.URL.String(), rec.key)
	for _, values := range req.Header {
		for _, value := range values {
			carried = carried || (rec.key != "" && strings.Contains(value, rec.key))
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.facts = append(rec.facts, fact{
		origin: originOf(req.URL), path: req.URL.Path, carriedKey: carried,
	})
}

// Only a small JSON answer is held, and only ever the first megabyte of one. The
// bytes read are handed back in front of the rest, so the client still sees the
// whole body whatever the outcome.
func (rec *recorder) capture(req *http.Request, res *http.Response) (*http.Response, error) {
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return res, nil
	}
	head, err := io.ReadAll(io.LimitReader(res.Body, maxCapturedBody+1))
	if err != nil {
		res.Body.Close()
		return nil, err
	}
	rest := res.Body
	res.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), rest), Closer: rest}
	if len(head) > maxCapturedBody {
		return res, nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.bodies[req.URL.Path] = head
	return res, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

func (rec *recorder) rawBody(t *testing.T, path string) json.RawMessage {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	raw, ok := rec.bodies[path]
	if !ok {
		t.Fatalf("no JSON answer was captured for %s", path)
	}
	return raw
}

func (rec *recorder) seen() []fact {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.facts)
}

func originOf(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

// Surfaced on the workflow run itself, so a skip is visible without opening the
// log and reading to the end of it.
func notice(message string) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::notice title=Integration::%s\n", message)
	}
}
