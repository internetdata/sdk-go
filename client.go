// Package internetdata is the official Go client library for the InternetData
// API: what every InternetData database says about an IP address, an AS or a
// domain, and the licensed databases themselves, downloaded as CSV.GZ or MMDB.
//
// Start with New and Client.Lookup. No API key is needed: without one a lookup
// is on Unlimited, the free plan, limited per address per minute. A batch needs
// a paid plan, and Client.Database a key carrying the db.download scope.
package internetdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/errgroup"

	"github.com/internetdata/sdk-go/v2/internal/api"
)

// DefaultBaseURL is the production API. Override it with WithBaseURL.
const DefaultBaseURL = "https://api.internetdata.io"

const (
	defaultCacheSize   = 10_000
	defaultCacheTTL    = time.Hour
	defaultConcurrency = 8
	defaultRetries     = 2
	defaultTimeout     = 30 * time.Second
	retryBaseDelay     = 250 * time.Millisecond
	// The longest Retry-After waited out as given, 2^31 - 1 ms (about 24.8
	// days). A longer one is still a throttle, but it is the server's word, so
	// the wait falls back to the client's own backoff rather than holding the
	// call for years.
	maxRetryAfter = (1<<31 - 1) * time.Millisecond
	// batchMax is the most addresses POST /batch takes in one call; a larger
	// batch is sent in chunks of this size.
	batchMax = 1000
)

// Client is a client for the InternetData API. It is safe for concurrent use.
//
// The address lookups are cached per instance and never globally: two clients
// holding different API keys are on different plans and entitled to different
// fields, so a shared cache would serve one of them the other's shape. Nothing
// else is cached: what your organization may see depends on the key, so a
// listing held from one client is not an answer for another.
type Client struct {
	// Database is the licensed database catalog and its downloads, for keys
	// that carry the db.download scope. Every database call hangs off it, which
	// is how the VPNDetection SDKs read too, so one program holding both
	// clients spells the two the same way.
	Database *DatabaseAPI
	// Oauth is the device sign-in, which hands a program one of a person's API
	// keys. Its requests never carry this client's key.
	Oauth *OauthAPI

	api   *api.ClientWithResponses
	cache *expirable.LRU[string, Result]
	// The addresses with a request in flight, shared by Lookup and LookupBatch.
	// Only a client that caches shares them: without a cache every lookup is
	// served, as WithoutCache promises.
	flights     flights
	concurrency int
	retries     int
}

// New builds a client. With no options it queries the production API without
// a key.
func New(opts ...Option) (*Client, error) {
	cfg := config{
		baseURL:     DefaultBaseURL,
		cacheSize:   defaultCacheSize,
		cacheTTL:    defaultCacheTTL,
		concurrency: defaultConcurrency,
		retries:     defaultRetries,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("internetdata: %w", err)
		}
	}

	httpClient := redirectControlled(cfg.httpClient)
	apiOpts := []api.ClientOption{api.WithHTTPClient(timedDoer{client: httpClient})}
	if cfg.apiKey != "" {
		apiOpts = append(apiOpts, api.WithRequestEditorFn(bearer(cfg.apiKey)))
	}
	inner, err := api.NewClientWithResponses(cfg.baseURL, apiOpts...)
	if err != nil {
		return nil, fmt.Errorf("internetdata: %w", err)
	}
	// The same transport without the key's request editor, which is client-wide,
	// sent to the authorization server.
	issuer := apexOf(cfg.baseURL)
	keyless, err := api.NewClientWithResponses(issuer, apiOpts[0])
	if err != nil {
		return nil, fmt.Errorf("internetdata: %w", err)
	}

	client := &Client{
		Database:    &DatabaseAPI{api: inner, transfer: untimed(httpClient), retries: cfg.retries},
		Oauth:       &OauthAPI{api: keyless, baseURL: issuer, retries: cfg.retries, sleep: sleep, now: time.Now},
		api:         inner,
		concurrency: cfg.concurrency,
		retries:     cfg.retries,
	}
	if !cfg.cacheOff {
		client.cache = expirable.NewLRU[string, Result](cfg.cacheSize, nil, cfg.cacheTTL)
	}
	return client, nil
}

// Lookup answers what InternetData knows about one address.
//
// A bogon is answered locally and never reaches the network. Everything else is
// served, then cached for this client. Treat the answer as read only: repeat
// lookups of one address hand back the same cached pointers.
//
// Calls that miss the cache while a request for their address is in flight, a
// batch's included, await that request rather than sending their own, and take
// its answer, sent under the options of the call that led it. The request runs
// detached from any one caller's context, so a caller giving up fails nobody
// else: it returns at once, and the answer is cached for the next.
//
// An IPv4-mapped address (::ffff:8.8.8.8) is the IPv4 address it carries:
// judged, sent and cached as that, so the answer names 8.8.8.8.
//
// Anything but an IP address is refused before sending, as a bad_request: the
// API answers an AS or a domain at the same path, in a shape this call cannot
// read, so LookupASN and LookupDomain are how to ask for those.
func (c *Client) Lookup(ctx context.Context, ip string, opts ...LookupOption) (*Result, error) {
	call := c.callConfig()
	for _, opt := range opts {
		opt.applyLookup(&call)
	}
	if err := call.refused(); err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(ip); err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: fmt.Sprintf(
			"%q is not an IP address; look an AS up with LookupASN and a domain with LookupDomain", ip)}
	}
	ip = unmapped(ip)
	if IsBogon(ip) {
		return bogonResult(ip), nil
	}
	ctx = call.carry(ctx)
	if c.cache == nil {
		return c.serve(ctx, ip, call.retries)
	}
	if hit, ok := c.cache.Get(ip); ok {
		return &hit, nil
	}

	led, joined := c.flights.board([]string{ip})
	fl, leads := led[ip]
	if leads {
		go c.fly(context.WithoutCancel(ctx), ip, fl, call.retries)
	} else {
		fl = joined[ip]
	}
	select {
	case <-fl.done:
		return fl.answer()
	case <-ctx.Done():
		return nil, errorFromTransport(ctx.Err())
	}
}

// fly sends one address's request and lands it. A request that landed between
// the caller's miss and its boarding cached its answer first, so it is found
// here rather than asked for again.
func (c *Client) fly(ctx context.Context, ip string, fl *flight, retries int) {
	if hit, ok := c.cache.Get(ip); ok {
		c.flights.land(ip, fl, &hit, nil)
		return
	}
	result, err := c.serve(ctx, ip, retries)
	if err == nil {
		c.cache.Add(ip, *result)
	}
	c.flights.land(ip, fl, result, err)
}

func (c *Client) serve(ctx context.Context, ip string, retries int) (*Result, error) {
	return withRetry(ctx, retries, func() (*Result, error) {
		answer, err := decodeAnswer[api.LookupResponse](c.api.LookupIP(ctx, ip))
		if err != nil {
			return nil, err
		}
		return &Result{LookupResponse: *answer}, nil
	})
}

// MyIP looks up the address this client is calling from.
//
// The answer is the one Lookup would give for that address, and it costs the
// same against your allowance. The address is the one our edge observed, so a
// call made through a proxy or a VPN reports the exit it left through rather
// than the machine that made it - usually the point of asking.
//
// Deliberately NOT cached. The cache is keyed by address, and the whole question
// here is which address that is: a laptop that moves between networks would
// otherwise be told where it used to be.
func (c *Client) MyIP(ctx context.Context, opts ...LookupOption) (*Result, error) {
	call := c.callConfig()
	for _, opt := range opts {
		opt.applyLookup(&call)
	}
	if err := call.refused(); err != nil {
		return nil, err
	}
	ctx = call.carry(ctx)
	return withRetry(ctx, call.retries, func() (*Result, error) {
		answer, err := decodeAnswer[api.LookupResponse](c.api.LookupMyIP(ctx))
		if err != nil {
			return nil, err
		}
		return &Result{LookupResponse: *answer}, nil
	})
}

// LookupASN answers what InternetData knows about one autonomous system: its
// registration on every plan, and on a paid one its ASN Whois record and the
// prefixes it announces. The number is sent as given, and an AS no database
// knows is answered with empty values.
//
// Not cached, and not batched: the cache and the batch are for addresses.
func (c *Client) LookupASN(
	ctx context.Context, asn uint32, opts ...LookupOption,
) (*AsnLookupResponse, error) {
	call := c.callConfig()
	for _, opt := range opts {
		opt.applyLookup(&call)
	}
	if err := call.refused(); err != nil {
		return nil, err
	}
	ctx = call.carry(ctx)
	return withRetry(ctx, call.retries, func() (*AsnLookupResponse, error) {
		return decodeAnswer[AsnLookupResponse](c.api.LookupAsn(ctx, strconv.FormatUint(uint64(asn), 10)))
	})
}

// LookupDomain answers what InternetData knows about one domain: on every plan
// whether it is registered and which DNS record types it has, and on a paid one
// the records themselves, its organization's IP ranges and whether it is a
// disposable email domain. The domain is sent as given, path-escaped; the API
// normalizes it and refuses one it cannot read as a bad_request.
//
// Not cached, and not batched: the cache and the batch are for addresses.
func (c *Client) LookupDomain(
	ctx context.Context, domain string, opts ...LookupOption,
) (*DomainLookupResponse, error) {
	call := c.callConfig()
	for _, opt := range opts {
		opt.applyLookup(&call)
	}
	if err := call.refused(); err != nil {
		return nil, err
	}
	ctx = call.carry(ctx)
	return withRetry(ctx, call.retries, func() (*DomainLookupResponse, error) {
		return decodeAnswer[DomainLookupResponse](c.api.LookupDomain(ctx, domain))
	})
}

// LookupBatch looks many addresses up in as few requests as possible.
//
// Bogons are answered locally and cached answers are reused, and an address
// with a request already in flight, a Lookup's or another batch's, awaits that
// request; whatever is left goes to POST /batch in chunks of up to 1000, with at
// most `concurrency` chunks in flight, and a Lookup arriving meanwhile awaits
// this batch's answer for its address. The answers are keyed by address rather than positional,
// so duplicates in the input collapse to a single entry and the caller never
// has to line two lists up. An address that fails carries its error as its
// value, so one bad entry cannot lose the rest of the answers: the API reports
// a per-entry failure with the status the single lookup would have answered,
// and a chunk that fails as a whole marks every address in it. POST /batch
// needs a paid plan: without a key every address in a request of two or more
// carries a KindUnauthorized error, and a free-plan key's request of two or
// more a KindBadRequest (HTTP 402). A request of one address is answered as
// its lookup would be. The returned error is reserved for the batch as a
// whole failing: the context canceled, or a Concurrency below 1 or negative
// Retries, refused before any request.
func (c *Client) LookupBatch(
	ctx context.Context, ips []string, opts ...BatchOption,
) (map[string]BatchResult, error) {
	call := c.callConfig()
	for _, opt := range opts {
		opt.applyBatch(&call)
	}
	// errgroup reads a limit of 0 as "admit nothing", which would wait forever.
	if call.concurrency < 1 {
		return nil, &Error{
			Kind: KindBadRequest, Message: fmt.Sprintf("concurrency must be at least 1, got %d", call.concurrency),
		}
	}
	if err := call.refused(); err != nil {
		return nil, err
	}
	ctx = call.carry(ctx)

	// An IPv4-mapped address is looked up as the IPv4 address it carries, once,
	// and its answer keyed as the caller passed it.
	asked := dedupe(ips)
	wire := make([]string, len(asked))
	mapped := false
	for i, ip := range asked {
		wire[i] = unmapped(ip)
		mapped = mapped || wire[i] != ip
	}
	unique := asked
	if mapped {
		unique = dedupe(wire)
	}
	results, err := c.lookupBatch(ctx, call, unique)
	if !mapped {
		return results, err
	}
	out := make(map[string]BatchResult, len(asked))
	for i, ip := range asked {
		if answer, ok := results[wire[i]]; ok {
			out[ip] = answer
		}
	}
	return out, err
}

// lookupBatch answers LookupBatch's addresses, each already the form it is sent in.
func (c *Client) lookupBatch(ctx context.Context, call callConfig, unique []string) (map[string]BatchResult, error) {
	results := make(map[string]BatchResult, len(unique))
	var pending []string
	for _, ip := range unique {
		if IsBogon(ip) {
			results[ip] = BatchResult{Result: bogonResult(ip)}
			continue
		}
		if c.cache != nil {
			if hit, ok := c.cache.Get(ip); ok {
				results[ip] = BatchResult{Result: &hit}
				continue
			}
		}
		pending = append(pending, ip)
	}
	if c.cache != nil {
		c.shareBatch(ctx, call, pending, results)
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("internetdata: batch: %w", err)
		}
		return results, nil
	}

	// One map of answers per chunk, written by index so the workers need no
	// lock, and merged afterwards.
	chunks := chunk(pending, batchMax)
	answers := make([]map[string]BatchResult, len(chunks))
	group := new(errgroup.Group)
	group.SetLimit(call.concurrency)
	for i, addrs := range chunks {
		group.Go(func() error {
			answers[i] = c.lookupChunk(ctx, addrs, call.retries)
			return nil
		})
	}
	// Every unit records its own outcome and returns nil, so this only waits.
	_ = group.Wait()
	for _, chunkAnswers := range answers {
		for ip, answer := range chunkAnswers {
			results[ip] = answer
		}
	}

	if err := ctx.Err(); err != nil {
		return results, fmt.Errorf("internetdata: batch: %w", err)
	}
	return results, nil
}

// shareBatch answers a caching client's pending addresses through the board:
// each joins the request in flight for it, or this batch leads one. The chunks
// run detached from ctx, as a Lookup's request does, so a batch given up part
// way fails no Lookup that joined it; ctx ends only this call's wait, and an
// address still unanswered then carries that error.
func (c *Client) shareBatch(ctx context.Context, call callConfig, pending []string, results map[string]BatchResult) {
	led, joined := c.flights.board(pending)
	var send []string
	for _, ip := range pending {
		fl, ok := led[ip]
		if !ok {
			continue
		}
		// Landed since the cache was read, and cached before it landed.
		if hit, ok := c.cache.Get(ip); ok {
			c.flights.land(ip, fl, &hit, nil)
			continue
		}
		send = append(send, ip)
	}

	detached := context.WithoutCancel(ctx)
	go func() {
		group := new(errgroup.Group)
		group.SetLimit(call.concurrency)
		for _, addrs := range chunk(send, batchMax) {
			group.Go(func() error {
				for ip, answer := range c.lookupChunk(detached, addrs, call.retries) {
					c.flights.land(ip, led[ip], answer.Result, answer.Err)
				}
				return nil
			})
		}
		_ = group.Wait()
	}()

	for _, ip := range pending {
		fl, ok := led[ip]
		if !ok {
			fl = joined[ip]
		}
		select {
		case <-fl.done:
			result, err := fl.answer()
			results[ip] = BatchResult{Result: result, Err: err}
		case <-ctx.Done():
			results[ip] = BatchResult{Err: errorFromTransport(ctx.Err())}
		}
	}
}

// lookupChunk sends one POST /batch and maps its answer back onto the
// addresses it was asked about. A chunk-level failure - the call refused, the
// transport failing, the retries exhausted - becomes every address's error,
// exactly as it would have been had each been looked up alone.
func (c *Client) lookupChunk(ctx context.Context, addrs []string, retries int) map[string]BatchResult {
	out := make(map[string]BatchResult, len(addrs))
	body, err := withRetry(ctx, retries, func() (*batchAnswer, error) {
		return decodeBatch(c.api.LookupBatch(ctx, api.BatchLookupRequest{Ips: &addrs}))
	})
	if err != nil {
		for _, ip := range addrs {
			out[ip] = BatchResult{Err: err}
		}
		return out
	}
	for _, ip := range addrs {
		if raw, ok := body.Results[ip]; ok {
			served, ok := decodeComplete[api.LookupResponse](raw)
			if !ok {
				out[ip] = BatchResult{Err: unreadable(body.status, "the batch answer for "+ip+" was not a lookup answer")}
				continue
			}
			result := &Result{LookupResponse: *served}
			if c.cache != nil {
				c.cache.Add(ip, *result)
			}
			out[ip] = BatchResult{Result: result}
			continue
		}
		if raw, ok := body.Errors[ip]; ok {
			failed, ok := decodeComplete[api.BatchLookupError](raw)
			if !ok {
				out[ip] = BatchResult{Err: unreadable(body.status, "the batch error for "+ip+" was not one")}
				continue
			}
			out[ip] = BatchResult{Err: errorFromEntry(failed.Status, failed.Error)}
			continue
		}
		out[ip] = BatchResult{Err: unreadable(body.status, "the batch answer did not include "+ip)}
	}
	return out
}

// A batch answer, its entries kept raw so each is read on its own: one entry
// the client cannot read is that address's error alone.
type batchAnswer struct {
	Results map[string]json.RawMessage `json:"results"`
	Errors  map[string]json.RawMessage `json:"errors"`
	status  int
}

// Reads a chunk's answer as decodeAnswer reads any other, except that only its
// envelope must be whole: an answer holding neither map, or one that is not a
// map, is unreadable, retried like a 5xx and carrying its status.
func decodeBatch(res *http.Response, err error) (*batchAnswer, error) {
	if err != nil {
		return nil, errorFromTransport(err)
	}
	body, err := readBody(res)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, errorFromResponse(res.StatusCode, res.Header, body)
	}
	answer := batchAnswer{status: res.StatusCode}
	if json.Unmarshal(body, &answer) != nil || (answer.Results == nil && answer.Errors == nil) {
		return nil, unreadable(res.StatusCode, "the response was not a batch answer")
	}
	return &answer, nil
}

// IsBogon reports whether an address is answered locally rather than served.
// Exposed here so the check is reachable from the client you already hold; the
// package-level IsBogon is the same function.
func (c *Client) IsBogon(ip string) bool {
	return IsBogon(ip)
}

// BatchResult is one address's outcome within a batch. Exactly one of Result
// and Err is set.
type BatchResult struct {
	Result *Result
	Err    error
}

// Option configures a Client.
type Option func(*config) error

// WithAPIKey authenticates as the key's organization. Which fields a lookup
// answers with, and how many lookups are allowed, follow the key's plan; which
// databases are listed, and which of them may be downloaded, follow its
// licenses.
//
// The database calls need a key carrying the db.download scope. Keys are
// default-deny, so an existing key does not reach them until the scope is added
// to it.
//
// The key is trimmed, and blanks alone are no key. A control character left in
// it is refused, by New.
func WithAPIKey(key string) Option {
	return func(c *config) error {
		// Untrimmed, a key of blanks went out as "Bearer" and a padded one
		// padded, and net/http refuses a CR, LF, NUL or DEL in a header, so
		// every call failed as a retried network error with nothing sent, while
		// a tab inside the key went out on the wire (2.6.2, measured 2026-10-12).
		key = strings.TrimSpace(key)
		if strings.ContainsFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return errors.New("the API key holds a control character")
		}
		c.apiKey = key
		return nil
	}
}

// WithBaseURL points the client at a different deployment of the API.
//
// Client.Oauth signs in at the apex rather than the API's host, since that is
// whose issuer it is: api. is dropped from the host and api-<env>. becomes
// <env>.; any other base URL is used as given.
func WithBaseURL(rawURL string) Option {
	return func(c *config) error {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("base url %q: %w", rawURL, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base url %q needs a scheme and a host", rawURL)
		}
		// Every path appended starts with a slash, and a second one is another
		// path, which the API answers with a redirect this client never follows.
		c.baseURL = strings.TrimRight(rawURL, "/")
		return nil
	}
}

// WithCache sizes the per-client answer cache. Defaults are 10000 addresses and
// a one hour TTL.
func WithCache(size int, ttl time.Duration) Option {
	return func(c *config) error {
		if size < 1 {
			return fmt.Errorf("cache size must be at least 1, got %d", size)
		}
		if ttl <= 0 {
			return fmt.Errorf("cache ttl must be positive, got %s", ttl)
		}
		c.cacheSize, c.cacheTTL, c.cacheOff = size, ttl, false
		return nil
	}
}

// WithoutCache turns caching off, so every lookup of a non-bogon address is
// served.
func WithoutCache() Option {
	return func(c *config) error {
		c.cacheOff = true
		return nil
	}
}

// WithConcurrency sets how many batch requests - chunks of up to 1000
// addresses - a batch keeps in flight. Default 8.
func WithConcurrency(n int) Option {
	return func(c *config) error {
		if n < 1 {
			return fmt.Errorf("concurrency must be at least 1, got %d", n)
		}
		c.concurrency = n
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
// that defers to yours (see DatabaseAPI.DownloadURL). A database transfer runs
// on a second copy with Timeout cleared, and is bounded by its context instead.
func WithHTTPClient(client *http.Client) Option {
	return func(c *config) error {
		if client == nil {
			return errors.New("http client cannot be nil")
		}
		c.httpClient = client
		return nil
	}
}

// LookupOption overrides a client default for one call: Lookup, LookupASN,
// LookupDomain, MyIP, MyEntitlement or a batch, since every LookupOption works
// on a batch too.
type LookupOption interface {
	BatchOption
	applyLookup(*callConfig)
}

// BatchOption overrides a client default for one batch. Options that only mean
// something for a batch are BatchOption alone, so passing one to Lookup does
// not compile rather than being accepted and ignored.
type BatchOption interface {
	applyBatch(*callConfig)
}

// Retries overrides the client's retry count for this call. A negative count is
// refused with a bad_request *Error before anything answers the call, a bogon
// or a cached address included.
func Retries(n int) LookupOption {
	return retriesOption(n)
}

// Concurrency overrides the client's in-flight limit for this batch - how many
// chunks of up to 1000 addresses are sent at once - so one large batch does not
// need a second client to widen it.
func Concurrency(n int) BatchOption {
	return concurrencyOption(n)
}

// Timeout overrides the HTTP client's Timeout for this call: 30 seconds unless
// WithHTTPClient supplied another. It replaces that bound rather than capping
// it, and like it bounds each ATTEMPT, so a retried call can take longer in
// total and ctx is what bounds the whole call. On a batch it bounds each
// chunk's request. Zero or less leaves an attempt unbounded, as on http.Client.
func Timeout(d time.Duration) LookupOption {
	return timeoutOption(d)
}

type config struct {
	apiKey      string
	baseURL     string
	cacheSize   int
	cacheTTL    time.Duration
	cacheOff    bool
	concurrency int
	retries     int
	httpClient  *http.Client
}

type callConfig struct {
	retries     int
	concurrency int
	// timeout is meaningful only when timed is set; otherwise the HTTP client's
	// own Timeout applies.
	timeout time.Duration
	timed   bool
}

// refused is the bad_request a per-call option earns, as WithRetries refuses a
// negative count where it is set, before a bogon, a cached address or a
// bogon-only batch can answer it.
func (c callConfig) refused() error {
	if c.retries < 0 {
		return &Error{Kind: KindBadRequest, Message: fmt.Sprintf("retries cannot be negative, got %d", c.retries)}
	}
	return nil
}

func (c *Client) callConfig() callConfig {
	return callConfig{retries: c.retries, concurrency: c.concurrency}
}

// carry puts a per-call timeout on the context, which is the only thing that
// reaches timedDoer through the generated client.
func (c callConfig) carry(ctx context.Context) context.Context {
	if !c.timed {
		return ctx
	}
	return context.WithValue(ctx, timeoutKey{}, c.timeout)
}

type retriesOption int

func (o retriesOption) applyLookup(c *callConfig) { c.retries = int(o) }
func (o retriesOption) applyBatch(c *callConfig)  { c.retries = int(o) }

type concurrencyOption int

func (o concurrencyOption) applyBatch(c *callConfig) { c.concurrency = int(o) }

type timeoutOption time.Duration

func (o timeoutOption) applyLookup(c *callConfig) { c.timeout, c.timed = time.Duration(o), true }
func (o timeoutOption) applyBatch(c *callConfig)  { c.timeout, c.timed = time.Duration(o), true }

type timeoutKey struct{}

// timedDoer sends every API request, and swaps a per-call Timeout in for the
// client's own. The client-level bound IS http.Client.Timeout, so the override
// sets that field on a copy: the same per-attempt meaning, the same coverage of
// the body, the same error.
type timedDoer struct {
	client *http.Client
}

func (t timedDoer) Do(req *http.Request) (*http.Response, error) {
	timeout, ok := req.Context().Value(timeoutKey{}).(time.Duration)
	if !ok {
		return t.client.Do(req)
	}
	timed := *t.client
	timed.Timeout = timeout
	return timed.Do(req)
}

func bearer(key string) api.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+key)
		return nil
	}
}

// DatabaseAPI.DownloadURL wants the 302 itself rather than what it points at, and
// following that redirect would stream a multi-gigabyte database into memory.
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

// A database transfer runs on the same transport but no whole-request Timeout,
// since one that bounds a lookup sensibly would kill a gigabyte download part
// way through. Its deadline is the context the caller passed.
func untimed(client *http.Client) *http.Client {
	transfer := *client
	transfer.Timeout = 0
	return &transfer
}

// apexOf is the authorization server's base URL for an API base URL: its host
// with api. dropped, or api-<env>. turned into <env>., and any other as given.
// The API host answers no OAuth path at all, so the issuer is reached there.
func apexOf(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	label, rest, ok := strings.Cut(parsed.Host, ".")
	if !ok || rest == "" {
		return baseURL
	}
	switch {
	case strings.EqualFold(label, "api"):
		parsed.Host = rest
	case len(label) > len("api-") && strings.EqualFold(label[:len("api-")], "api-"):
		parsed.Host = label[len("api-"):] + "." + rest
	default:
		return baseURL
	}
	return parsed.String()
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
		if apiErr.RetryAfter > 0 && apiErr.RetryAfter <= maxRetryAfter {
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

func dedupe(ips []string) []string {
	seen := make(map[string]struct{}, len(ips))
	unique := make([]string, 0, len(ips))
	for _, ip := range ips {
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		unique = append(unique, ip)
	}
	return unique
}

// chunk splits addresses into consecutive slices of at most size each.
func chunk(ips []string, size int) [][]string {
	var out [][]string
	for len(ips) > size {
		out = append(out, ips[:size])
		ips = ips[size:]
	}
	if len(ips) > 0 {
		out = append(out, ips)
	}
	return out
}
