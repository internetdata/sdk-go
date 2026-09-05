// Package internetdata is the official Go client library for the InternetData
// API: licensed IP and network datasets, downloaded as CSV.GZ or MMDB.
//
// Start with New and Client.Database.List. Every endpoint needs an API key
// carrying the db.download scope, which is why New takes one rather than
// offering it as an option: there is no anonymous tier to fall back to.
package internetdata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/internetdata/sdk-go/internal/api"
)

// DefaultBaseURL is the production API. Override it with WithBaseURL.
const DefaultBaseURL = "https://internetdata.io"

const (
	defaultRetries = 2
	defaultTimeout = 30 * time.Second
	retryBaseDelay = 250 * time.Millisecond
)

// Client is a client for the InternetData API. It is safe for concurrent use.
//
// Nothing it answers is cached. What your organization may see depends on the
// key, so a listing held from one client is not an answer for another, and a
// catalog is small enough that re-reading it costs less than being wrong about
// whose it was.
type Client struct {
	// Database is the licensed database catalog and its downloads. Every call
	// hangs off it rather than off the client, which is how the VPNDetection
	// SDKs read too, so one program holding both clients spells the two the
	// same way.
	Database *DatabaseAPI
}

// New builds a client for one API key.
//
// The key comes from the console and needs the db.download scope. Keys are
// default-deny, so an existing key does not reach these endpoints until the
// scope is added to it.
func New(apiKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("internetdata: an API key is required")
	}
	cfg := config{
		baseURL:    DefaultBaseURL,
		retries:    defaultRetries,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("internetdata: %w", err)
		}
	}

	httpClient := redirectControlled(cfg.httpClient)
	inner, err := api.NewClientWithResponses(cfg.baseURL,
		api.WithHTTPClient(httpClient),
		api.WithRequestEditorFn(bearer(apiKey)))
	if err != nil {
		return nil, fmt.Errorf("internetdata: %w", err)
	}
	return &Client{Database: &DatabaseAPI{
		api:      inner,
		transfer: untimed(httpClient),
		retries:  cfg.retries,
	}}, nil
}

// Option configures a Client.
type Option func(*config) error

// WithBaseURL points the client at a different deployment of the API.
func WithBaseURL(rawURL string) Option {
	return func(c *config) error {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("base url %q: %w", rawURL, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base url %q needs a scheme and a host", rawURL)
		}
		c.baseURL = rawURL
		return nil
	}
}

// WithRetries sets how many further attempts a transient failure gets.
// Default 2.
func WithRetries(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return fmt.Errorf("retries cannot be negative, got %d", n)
		}
		c.retries = n
		return nil
	}
}

// WithHTTPClient supplies the HTTP client to send with, for a custom transport,
// proxy or timeout. Without it the SDK uses a client with a 30 second timeout.
//
// The client is copied rather than mutated, and the copy adds a CheckRedirect
// that defers to yours (see DatabaseAPI.DownloadURL). A dataset transfer runs on a
// second copy with Timeout cleared, and is bounded by its context instead.
func WithHTTPClient(client *http.Client) Option {
	return func(c *config) error {
		if client == nil {
			return errors.New("http client cannot be nil")
		}
		c.httpClient = client
		return nil
	}
}

type config struct {
	baseURL    string
	retries    int
	httpClient *http.Client
}

func bearer(key string) api.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+key)
		return nil
	}
}

// DatabaseAPI.DownloadURL wants the 302 itself rather than what it points at, and
// following that redirect would stream a multi-gigabyte dataset into memory.
// Suppressing it per request through the context keeps a caller's own
// CheckRedirect in force everywhere else.
type noRedirectKey struct{}

func withoutRedirects(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRedirectKey{}, true)
}

func redirectControlled(client *http.Client) *http.Client {
	controlled := *client
	inner := client.CheckRedirect
	controlled.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.Context().Value(noRedirectKey{}) != nil {
			return http.ErrUseLastResponse
		}
		if inner != nil {
			return inner(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &controlled
}

// A dataset transfer runs on the same transport but no whole-request Timeout,
// since one that bounds a catalog read sensibly would kill a gigabyte download
// part way through. Its deadline is the context the caller passed.
func untimed(client *http.Client) *http.Client {
	transfer := *client
	transfer.Timeout = 0
	return &transfer
}

// Backs off exponentially, except that a server-supplied Retry-After wins over
// the schedule. A 429 WITHOUT that header is a spent allowance rather than a
// throttle and is not retried at all, which Error.Retryable decides.
func withRetry[T any](ctx context.Context, retries int, attempt func() (T, error)) (T, error) {
	var zero T
	delay := retryBaseDelay
	for remaining := retries; ; remaining-- {
		value, err := attempt()
		if err == nil {
			return value, nil
		}
		var apiErr *Error
		if remaining <= 0 || !errors.As(err, &apiErr) || !apiErr.Retryable() {
			return zero, err
		}
		wait := delay
		if apiErr.RetryAfter > 0 {
			wait = apiErr.RetryAfter
		}
		if err := sleep(ctx, wait); err != nil {
			return zero, errorFromTransport(err)
		}
		delay *= 2
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
