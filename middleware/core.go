package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	internetdata "github.com/internetdata/sdk-go/v2"
)

// Defaults set for a request path rather than for a script: failing open
// quickly beats holding a visitor while we try again.
const (
	DefaultTimeout = 2500 * time.Millisecond
	DefaultRetries = 0
)

// RequestView is enough of an incoming request for a selector to work with,
// whatever framework it came from. An adapter supplies one of these per request.
type RequestView struct {
	// Header returns a request header by name, case-insensitively.
	Header func(name string) string
	// FrameworkIP returns the framework's own client-address accessor,
	// whatever that resolves to here.
	FrameworkIP func() string
}

// IPSelector decides the client address.
//
// There is no portable answer: a framework's own accessor may return the socket
// peer, or may already have walked a proxy chain, depending on the framework
// and on how the application configured it. You know your framework and your
// edge, so this is yours to choose.
type IPSelector[Req any] func(request Req) string

// Lookup is what a middleware attached to the request, whether or not it
// succeeded.
type Lookup struct {
	// IP is the address that was classified, as the selector resolved it.
	IP string
	// Result is the answer. Nil when the lookup failed.
	Result *internetdata.Result
	// Err is why the lookup failed. Nil when it succeeded.
	Err error
	// Blocked reports whether the condition matched. Always false when no
	// condition was configured.
	Blocked bool
}

// MissingFieldAction says what to do when a condition names a member the plan
// does not serve.
type MissingFieldAction string

const (
	MissingFieldWarn   MissingFieldAction = "warn"
	MissingFieldError  MissingFieldAction = "error"
	MissingFieldIgnore MissingFieldAction = "ignore"
)

// Options configure a middleware. Everything is optional.
type Options[Req any] struct {
	// Client is an existing client to use. Prefer this if you already hold
	// one: two clients mean two caches, and a cache is per instance because
	// two keys can be on different plans and entitled to different fields.
	Client *internetdata.Client
	// APIKey is ignored when Client is set.
	APIKey string
	// BaseURL is ignored when Client is set. Defaults to
	// internetdata.DefaultBaseURL.
	BaseURL string
	// Timeout is how long a lookup may hold the request. Defaults to 2500ms,
	// a much tighter bound than the client's own 30s.
	Timeout time.Duration
	// Retries for a transient failure. Defaults to 0, unlike the client's 2.
	// New refuses a negative count.
	Retries *int
	// IPSelector decides the client address. Defaults to the framework's own
	// accessor.
	IPSelector IPSelector[Req]
	// BlockCondition is what to block on. Leave it empty to only enrich the
	// request and leave the decision to your own code.
	BlockCondition Conditions
	// FailClosed blocks when the lookup itself fails. Defaults to false, so
	// our outage does not become yours.
	FailClosed bool
	// OnMissingField says what to do when the condition names a member your
	// plan does not serve. Defaults to warning once.
	OnMissingField MissingFieldAction
	// Skip claims a request, so it is never classified.
	Skip func(request Req) bool
	// Logger receives the warnings. Defaults to slog.Default().
	Logger *slog.Logger
}

// Core is the framework-agnostic half of a middleware.
type Core[Req any] struct {
	client         *internetdata.Client
	selector       IPSelector[Req]
	condition      Conditions
	timeout        time.Duration
	retries        int
	failClosed     bool
	onMissingField MissingFieldAction
	skip           func(Req) bool
	warn           func(string)
}

// New builds a Core, or refuses a condition that could never be what anyone
// meant.
//
// defaultIPSelector is the framework's own accessor, supplied by the adapter
// and used when the caller named none.
func New[Req any](options Options[Req], defaultIPSelector IPSelector[Req]) (*Core[Req], error) {
	if err := Validate(options.BlockCondition); err != nil {
		return nil, err
	}
	client := options.Client
	if client == nil {
		// WithBaseURL refuses an empty URL, so an unset BaseURL must leave the
		// client's default alone, or New fails for every caller who sets only an
		// APIKey.
		clientOptions := []internetdata.Option{internetdata.WithAPIKey(options.APIKey)}
		if options.BaseURL != "" {
			clientOptions = append(clientOptions, internetdata.WithBaseURL(options.BaseURL))
		}
		var err error
		client, err = internetdata.New(clientOptions...)
		if err != nil {
			return nil, err
		}
	}
	selector := options.IPSelector
	if selector == nil {
		selector = defaultIPSelector
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	retries := DefaultRetries
	if options.Retries != nil {
		retries = *options.Retries
	}
	// Refused here, as WithRetries refuses one: every lookup would refuse it
	// instead, and the middleware fail open on every request.
	if retries < 0 {
		return nil, fmt.Errorf("internetdata: retries cannot be negative, got %d", retries)
	}
	action := options.OnMissingField
	if action == "" {
		action = MissingFieldWarn
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Core[Req]{
		client:         client,
		selector:       selector,
		condition:      options.BlockCondition,
		timeout:        timeout,
		retries:        retries,
		failClosed:     options.FailClosed,
		onMissingField: action,
		skip:           options.Skip,
		warn:           onceEach(func(message string) { logger.Warn(message) }),
	}, nil
}

// Blocking reports whether a condition was configured at all.
func (c *Core[Req]) Blocking() bool { return len(c.condition) > 0 }

// Evaluate classifies one request. It answers nil when Skip claimed it.
//
// The error return is a MISCONFIGURATION - a condition naming a member the plan
// does not serve, with OnMissingField set to error. A failed LOOKUP is not an
// error here: it lands on Lookup.Err and the request is let through.
func (c *Core[Req]) Evaluate(ctx context.Context, request Req) (*Lookup, error) {
	if c.skip != nil && c.skip(request) {
		return nil, nil
	}
	ip := strings.TrimSpace(c.selector(request))
	if ip == "" {
		c.warn("could not resolve a client address from this request; pass an IPSelector " +
			"that knows where yours comes from")
		return &Lookup{
			Err:     errors.New("internetdata: no client address on the request"),
			Blocked: c.failClosed,
		}, nil
	}
	if internetdata.IsBogon(ip) {
		// Expected in local development. Anywhere else it means a proxy sits
		// in front and its own address is what reached us.
		c.warn(fmt.Sprintf("resolved the client address as %s, which is not a public address. "+
			"If this application runs behind a proxy or load balancer, configure its "+
			"trusted-proxy setting or pass an IPSelector that reads your edge's header.", ip))
	}

	lookupCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.client.Lookup(lookupCtx, ip, internetdata.Retries(c.retries))
	if err != nil {
		return &Lookup{IP: ip, Err: err, Blocked: c.failClosed}, nil
	}
	if len(c.condition) > 0 {
		if err := c.reportMissing(result); err != nil {
			return nil, err
		}
	}
	return &Lookup{
		IP:      ip,
		Result:  result,
		Blocked: len(c.condition) > 0 && Matches(c.condition, result),
	}, nil
}

func (c *Core[Req]) reportMissing(result *internetdata.Result) error {
	if c.onMissingField == MissingFieldIgnore {
		return nil
	}
	missing := MissingMembers(c.condition, result)
	if len(missing) == 0 {
		return nil
	}
	message := fmt.Sprintf("block condition names %s, which your plan does not include, so "+
		"those terms can never match; an absent member means \"not in your plan\", not "+
		"\"checked, and no\"", strings.Join(missing, ", "))
	if c.onMissingField == MissingFieldError {
		return errors.New("internetdata: " + message)
	}
	c.warn(message)
	return nil
}

// Selectors are the shared implementations, bound to one framework's request
// type so a caller writing a custom selector still works with the object they
// know.
type Selectors[Req any] struct {
	// Default is the framework's own client-address accessor. What that
	// resolves to depends on the framework and on how you configured it.
	Default IPSelector[Req]
	// XFF reads X-Forwarded-For.
	//
	// The LEFT-MOST entry is whatever the caller sent, because proxies append,
	// so a visitor who sets the header themselves appears first and this
	// returns their forgery. It is only trustworthy when an edge you control
	// overwrites the header. When you know how many proxies sit in front,
	// count from the right with depth: 1 is the address your nearest proxy saw.
	XFF func(depth int) IPSelector[Req]
	// Header reads a single-value header by name, for an edge that writes one
	// - Header("CF-Connecting-IP") behind Cloudflare. Falls back to the
	// framework's accessor when the header is absent.
	Header func(name string) IPSelector[Req]
}

// BindSelectors gives an adapter the shared selectors under its own request
// type. The adapter passes a function exposing its request once.
func BindSelectors[Req any](view func(Req) RequestView) Selectors[Req] {
	return Selectors[Req]{
		Default: func(request Req) string { return view(request).FrameworkIP() },
		XFF: func(depth int) IPSelector[Req] {
			return func(request Req) string {
				v := view(request)
				var chain []string
				for _, entry := range strings.Split(v.Header("X-Forwarded-For"), ",") {
					if trimmed := strings.TrimSpace(entry); trimmed != "" {
						chain = append(chain, trimmed)
					}
				}
				if len(chain) == 0 {
					return v.FrameworkIP()
				}
				if depth <= 0 {
					return chain[0]
				}
				if depth > len(chain) {
					return chain[0]
				}
				return chain[len(chain)-depth]
			}
		},
		Header: func(name string) IPSelector[Req] {
			return func(request Req) string {
				v := view(request)
				if value := strings.TrimSpace(v.Header(name)); value != "" {
					return value
				}
				return v.FrameworkIP()
			}
		},
	}
}

// A misconfiguration is the same on every request, so saying so once is a
// warning and saying so a million times is an outage of its own.
func onceEach(sink func(string)) func(string) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(message string) {
		mu.Lock()
		defer mu.Unlock()
		if seen[message] {
			return
		}
		seen[message] = true
		sink(message)
	}
}
