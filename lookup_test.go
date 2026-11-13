// The lookups' Go-specific surface, as distinct from the shared conformance
// corpus in conformance_test.go.

package internetdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Enough addresses for seven chunks of the batch endpoint's 1000, so a
// concurrency bound has something to bound: one request per chunk, and only
// the chunks overlap.
var manyAddrs = func() []string {
	addrs := make([]string, 6001)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("9.%d.%d.%d", 1+i/65536, (i/256)%256, i%256)
	}
	return addrs
}()

// Peak in-flight is the only measurement that tells a real limit from an option
// that was accepted and ignored.
func TestBatchConcurrencyIsConfigurablePerCall(t *testing.T) {
	stub := newStub(okRoutes(manyAddrs...))
	stub.delay = 20 * time.Millisecond
	client := newTestClient(t, stub, WithoutCache())

	if _, err := client.LookupBatch(t.Context(), manyAddrs, Concurrency(3)); err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}

	if stub.count() != 7 {
		t.Errorf("issued %d request(s), want 7 chunks for %d addresses", stub.count(), len(manyAddrs))
	}
	if peak := stub.peakInFlight(); peak > 3 {
		t.Errorf("peak in flight was %d, want at most 3", peak)
	}
	if peak := stub.peakInFlight(); peak <= 1 {
		t.Errorf("peak in flight was %d, so requests never overlapped", peak)
	}
}

func TestAPerCallConcurrencyOverridesTheClientDefault(t *testing.T) {
	stub := newStub(okRoutes(manyAddrs...))
	stub.delay = 20 * time.Millisecond
	client := newTestClient(t, stub, WithoutCache(), WithConcurrency(2))

	if _, err := client.LookupBatch(t.Context(), manyAddrs, Concurrency(6)); err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}

	peak := stub.peakInFlight()
	if peak <= 2 {
		t.Errorf("override ignored: peak in flight was %d, want above 2", peak)
	}
	if peak > 6 {
		t.Errorf("peak in flight was %d, want at most 6", peak)
	}
}

func TestWithoutAnOverrideTheClientConcurrencyStillApplies(t *testing.T) {
	stub := newStub(okRoutes(manyAddrs...))
	stub.delay = 20 * time.Millisecond
	client := newTestClient(t, stub, WithoutCache(), WithConcurrency(2))

	if _, err := client.LookupBatch(t.Context(), manyAddrs); err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}

	if peak := stub.peakInFlight(); peak > 2 {
		t.Errorf("peak in flight was %d, want at most 2", peak)
	}
}

// errgroup reads a limit of 0 as admitting nothing, so an unrefused
// Concurrency(0) would wait forever, deaf even to its context: the call runs
// apart from the test, which fails rather than hangs.
func TestABatchRefusesAConcurrencyBelowOneBeforeAnyRequest(t *testing.T) {
	for _, n := range []int{0, -1} {
		stub := newStub(okRoutes("9.9.9.9"))
		client := newTestClient(t, stub, WithoutCache())

		var got map[string]BatchResult
		var err error
		done := make(chan struct{})
		go func() {
			defer close(done)
			got, err = client.LookupBatch(t.Context(), []string{"9.9.9.9"}, Concurrency(n))
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Concurrency(%d): the batch never returned", n)
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Kind != KindBadRequest || got != nil {
			t.Fatalf("Concurrency(%d): got %v, %v; want a bad_request *Error", n, got, err)
		}
		if stub.count() != 0 {
			t.Errorf("Concurrency(%d): issued %d request(s), want none", n, stub.count())
		}
	}
}

// Every call taking per-call options, each reduced to its error. A batch's is
// the error its address carries.
var optionedCalls = map[string]func(context.Context, *Client, ...LookupOption) error{
	"Lookup": func(ctx context.Context, c *Client, opts ...LookupOption) error {
		_, err := c.Lookup(ctx, "9.9.9.9", opts...)
		return err
	},
	"LookupASN": func(ctx context.Context, c *Client, opts ...LookupOption) error {
		_, err := c.LookupASN(ctx, 9009, opts...)
		return err
	},
	"LookupDomain": func(ctx context.Context, c *Client, opts ...LookupOption) error {
		_, err := c.LookupDomain(ctx, "example.com", opts...)
		return err
	},
	"MyIP": func(ctx context.Context, c *Client, opts ...LookupOption) error {
		_, err := c.MyIP(ctx, opts...)
		return err
	},
	"MyEntitlement": func(ctx context.Context, c *Client, opts ...LookupOption) error {
		_, err := c.MyEntitlement(ctx, opts...)
		return err
	},
	"LookupBatch": func(ctx context.Context, c *Client, opts ...LookupOption) error {
		batchOpts := make([]BatchOption, len(opts))
		for i, opt := range opts {
			batchOpts[i] = opt
		}
		got, err := c.LookupBatch(ctx, []string{"9.9.9.9"}, batchOpts...)
		if err != nil {
			return err
		}
		return got["9.9.9.9"].Err
	},
}

// The per-call Timeout has to be the bound that fires, not the client's, and it
// has to fail as every transport failure does. Retries(1) makes it per ATTEMPT:
// a deadline over the whole call would expire during the backoff and never send
// the second request.
func TestAPerCallTimeoutBoundsEachAttemptBelowTheClients(t *testing.T) {
	for name, call := range optionedCalls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			api := newSlowAPI(t, 0)
			client, err := New(WithBaseURL(api.URL), WithoutCache(),
				WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			start := time.Now()
			err = call(t.Context(), client, Timeout(100*time.Millisecond), Retries(1))
			elapsed := time.Since(start)

			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Kind != KindNetwork || !apiErr.Retryable() {
				t.Fatalf("error was %v, want a retryable network *Error", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("error was %v, want one that reports a deadline", err)
			}
			if n := api.requests.Load(); n != 2 {
				t.Errorf("sent %d request(s), want 2: each attempt gets the whole timeout", n)
			}
			if elapsed > 2500*time.Millisecond {
				t.Errorf("gave up after %s, so the client's 5s bound fired rather than the call's", elapsed)
			}
		})
	}
}

// The override replaces the client's bound rather than racing it, so a call
// that is expected to be slow can be given longer than the client allows.
func TestAPerCallTimeoutCanLoosenTheClients(t *testing.T) {
	api := newSlowAPI(t, 300*time.Millisecond)
	client, err := New(WithBaseURL(api.URL), WithoutCache(), WithRetries(0),
		WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.Lookup(t.Context(), "9.9.9.9"); err == nil {
		t.Fatal("a 300ms answer should have outlasted the client's 50ms")
	}
	result, err := client.Lookup(t.Context(), "9.9.9.9", Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("Lookup with a 5s timeout: %v", err)
	}
	if result.IP != "9.9.9.9" {
		t.Errorf("IP = %q, want 9.9.9.9", result.IP)
	}
}

// An API that answers every request after a delay, or with a zero delay never:
// it holds each request until the client abandons it. A real server rather
// than the stub transport, because a timeout is only honest against a transport
// that honors cancellation.
type slowAPI struct {
	*httptest.Server
	requests atomic.Int32
}

func newSlowAPI(t *testing.T, delay time.Duration) *slowAPI {
	t.Helper()
	answer, err := json.Marshal(answerAbout("9.9.9.9", nil))
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	api := &slowAPI{}
	release := make(chan struct{})
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.requests.Add(1)
		// Draining the body is what lets the server notice the client leave.
		_, _ = io.Copy(io.Discard, r.Body)
		var answered <-chan time.Time
		if delay > 0 {
			answered = time.After(delay)
		}
		select {
		case <-answered:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(answer)
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanups run last-in first-out, so the held handlers return before Close
	// waits on them.
	t.Cleanup(api.Close)
	t.Cleanup(func() { close(release) })
	return api
}

func TestRetriesAreConfigurablePerCall(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"9.9.9.9": {status: http.StatusInternalServerError, body: map[string]string{"error": "lookup failed"}},
	})
	client := newTestClient(t, stub, WithoutCache(), WithRetries(0))

	if _, err := client.Lookup(t.Context(), "9.9.9.9", Retries(2)); err == nil {
		t.Fatal("a 500 should have failed the lookup")
	}
	// One initial attempt plus two retries, rather than the client's zero.
	if stub.count() != 3 {
		t.Errorf("issued %d request(s), want 3", stub.count())
	}
}

// A negative per-call count is refused where it is set, as WithRetries refuses
// one, before a bogon, a cached address or a bogon-only batch can answer it.
func TestANegativePerCallRetryCountIsRefusedBeforeAnythingAnswers(t *testing.T) {
	stub := newStub(okRoutes("9.9.9.9"))
	client := newTestClient(t, stub)
	if _, err := client.Lookup(t.Context(), "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func(ctx context.Context) error{
		"a bogon": func(ctx context.Context) error {
			_, err := client.Lookup(ctx, "10.0.0.1", Retries(-1))
			return err
		},
		"a cached address": func(ctx context.Context) error {
			_, err := client.Lookup(ctx, "9.9.9.9", Retries(-1))
			return err
		},
		"an address not yet asked": func(ctx context.Context) error {
			_, err := client.Lookup(ctx, "45.83.91.1", Retries(-1))
			return err
		},
		"LookupASN": func(ctx context.Context) error {
			_, err := client.LookupASN(ctx, 9009, Retries(-1))
			return err
		},
		"LookupDomain": func(ctx context.Context) error {
			_, err := client.LookupDomain(ctx, "example.com", Retries(-1))
			return err
		},
		"MyIP": func(ctx context.Context) error {
			_, err := client.MyIP(ctx, Retries(-1))
			return err
		},
		"MyEntitlement": func(ctx context.Context) error {
			_, err := client.MyEntitlement(ctx, Retries(-1))
			return err
		},
		"a bogon-only batch": func(ctx context.Context) error {
			_, err := client.LookupBatch(ctx, []string{"10.0.0.1"}, Retries(-1))
			return err
		},
		"a batch": func(ctx context.Context) error {
			_, err := client.LookupBatch(ctx, []string{"9.9.9.9", "45.83.91.1"}, Retries(-1))
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			sent := stub.count()
			assertKind(t, call(t.Context()), KindBadRequest, 0)
			if stub.count() != sent {
				t.Errorf("sent %d request(s), want none", stub.count()-sent)
			}
		})
	}
	if _, err := client.Lookup(t.Context(), "10.0.0.1", Retries(0)); err != nil {
		t.Errorf("Retries(0): %v", err)
	}
}

// A 429 with no Retry-After is a spent allowance, and retrying it is hammering
// a quota that will not recover until its window rolls over.
func TestALookupsSpentQuotaIsNeverRetried(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"9.9.9.9": {
			status: http.StatusTooManyRequests,
			body:   map[string]string{"error": "overage cap reached; raise or remove it in Billing"},
		},
	})
	client := newTestClient(t, stub, WithoutCache(), WithRetries(5))

	_, err := client.Lookup(t.Context(), "9.9.9.9")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != KindQuotaExceeded {
		t.Fatalf("error was %v, want a quota_exceeded *Error", err)
	}
	if stub.count() != 1 {
		t.Errorf("issued %d request(s), want 1", stub.count())
	}
}

func TestALookupsRateLimitIsRetriedAfterTheServerSuppliedWait(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"9.9.9.9": {
			status:  http.StatusTooManyRequests,
			body:    map[string]string{"error": "per-minute limit for Unlimited lookups exceeded"},
			headers: map[string]string{"Retry-After": "1"},
		},
	})
	client := newTestClient(t, stub, WithoutCache(), WithRetries(1))

	start := time.Now()
	if _, err := client.Lookup(t.Context(), "9.9.9.9"); err == nil {
		t.Fatal("the lookup should still have failed after its retry")
	}
	if stub.count() != 2 {
		t.Errorf("issued %d request(s), want 2", stub.count())
	}
	// The header, not the backoff schedule, decides the wait.
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("waited %s before retrying, want at least the 1s Retry-After", waited)
	}
}

// A Retry-After past 2^31 - 1 ms is still a throttle, but the server's word is
// not held to: the call waits the client's own backoff and stays rate_limited.
func TestALookupsRetryAfterTooLongToHoldWaitsTheBackoff(t *testing.T) {
	for _, header := range []string{
		"2147484", "9223372036", "9223372036854775807", "18446744074", "Fri, 31 Dec 9999 23:59:59 GMT",
	} {
		t.Run(header, func(t *testing.T) {
			stub := newStub(map[string]stubRoute{
				"9.9.9.9": {
					status:  http.StatusTooManyRequests,
					body:    map[string]string{"error": "rate limit exceeded"},
					headers: map[string]string{"Retry-After": header},
				},
			})
			client := newTestClient(t, stub, WithoutCache(), WithRetries(1))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			_, err := client.Lookup(ctx, "9.9.9.9")

			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Kind != KindRateLimited {
				t.Fatalf("error was %v, want rate_limited after one retry", err)
			}
			if stub.count() != 2 {
				t.Errorf("issued %d request(s), want 2", stub.count())
			}
		})
	}
}

// A batch chunk's 429 is read from the response as a lookup's is, never through
// a generated parser binding the header as the spec's integer, so a date, and
// anything past 2^31 - 1 where int is 32 bits, is still a throttle.
func TestABatchChunksRetryAfterIsReadFromTheResponse(t *testing.T) {
	for _, header := range []string{"9223372036", "Fri, 31 Dec 9999 23:59:59 GMT"} {
		t.Run(header, func(t *testing.T) {
			var requests atomic.Int32
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {header}},
					Body:       io.NopCloser(strings.NewReader(`{"error":"rate limit exceeded"}`)),
					Request:    req,
				}, nil
			})
			client, err := New(WithHTTPClient(&http.Client{Transport: transport}), WithoutCache(), WithRetries(1))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			results, err := client.LookupBatch(ctx, []string{"9.9.9.9"})
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}
			var apiErr *Error
			if got := results["9.9.9.9"].Err; !errors.As(got, &apiErr) || apiErr.Kind != KindRateLimited {
				t.Fatalf("error was %v, want rate_limited after one retry", got)
			}
			if requests.Load() != 2 {
				t.Errorf("issued %d request(s), want 2", requests.Load())
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestIsBogonIsOnTheClientAndAgreesWithTheStandaloneFunction(t *testing.T) {
	client := newTestClient(t, newStub(nil))
	for _, c := range corpus(t).IsBogon {
		if got := client.IsBogon(c.IP); got != c.Expect {
			t.Errorf("client.IsBogon(%q) = %v, want %v (%s)", c.IP, got, c.Expect, c.Why)
		}
		if client.IsBogon(c.IP) != IsBogon(c.IP) {
			t.Errorf("%s: the client and the standalone function disagree", c.IP)
		}
	}
}

// Go has no `??`, so this reader is the only ergonomic way to ask "flagged or
// not" without losing the absent-versus-false distinction elsewhere.
func TestBoolValueCoalescesAnAbsentFlag(t *testing.T) {
	absent := &Result{}
	if BoolValue(absent.IsHosting) || BoolValue(absent.IsVpn) {
		t.Error("an absent flag should read as false")
	}
	if absent.IsHosting != nil {
		t.Error("the reader must not populate the field it reads")
	}

	present := &Result{LookupResponse: LookupResponse{IsHosting: ptr(true), IsVpn: ptr(false)}}
	if !BoolValue(present.IsHosting) {
		t.Error("a present true flag should read as true")
	}
	if BoolValue(present.IsVpn) {
		t.Error("a present false flag should read as false")
	}
}

func TestMyIPAsksTheServerWhichAddressYouAre(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"myip": {body: answerAbout("203.0.113.9", map[string]any{"is_vpn": true})},
	})
	client := newTestClient(t, stub)

	result, err := client.MyIP(t.Context())
	if err != nil {
		t.Fatalf("MyIP: %v", err)
	}
	if result.IP != "203.0.113.9" {
		t.Errorf("got %q, want 203.0.113.9", result.IP)
	}
	if !BoolValue(result.IsVpn) {
		t.Error("is_vpn did not survive")
	}
}

// The cache is keyed by ADDRESS, and which address this is IS the question, so
// a second call has to ask again. A laptop that moved networks would otherwise
// be told where it used to be.
func TestMyIPIsNotCached(t *testing.T) {
	stub := newStub(map[string]stubRoute{"myip": {body: answerAbout("203.0.113.9", nil)}})
	client := newTestClient(t, stub)

	for range 3 {
		if _, err := client.MyIP(t.Context()); err != nil {
			t.Fatalf("MyIP: %v", err)
		}
	}
	if stub.count() != 3 {
		t.Errorf("issued %d request(s), want 3 - the answer was cached", stub.count())
	}
}

func TestMyIPSurfacesAnError(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"myip": {status: 401, body: map[string]string{"error": "invalid API key"}},
	})
	client := newTestClient(t, stub)

	_, err := client.MyIP(t.Context())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != KindUnauthorized || apiErr.Message != "invalid API key" {
		t.Fatalf("got %v, want an unauthorized *Error carrying the API's message", err)
	}
}

var entitlementAnswer = map[string]any{
	"org_id": "85bb51e4-2eb6-4a31-8e4d-02ba8b98fe61",
	"apikey": map[string]any{
		"id": "0ab424cc-7619-4dad-b027-afacdc2cedb0", "expires": nil, "allowed_cidrs": []string{},
	},
	"plan": map[string]any{"key": "max", "tier": "max"},
	"usage": map[string]any{
		"requests": 580, "quota": 5000000, "hard_limit": nil,
		"window_start": "2026-09-04T07:00:00Z", "window_end": "2026-10-04T07:00:00Z",
	},
}

func TestMyEntitlementReportsThePlanAndTheUsage(t *testing.T) {
	stub := newStub(map[string]stubRoute{"/api/v1/entitlement": {body: entitlementAnswer}})
	client := newTestClient(t, stub)

	acct, err := client.MyEntitlement(t.Context())
	if err != nil {
		t.Fatalf("MyEntitlement: %v", err)
	}
	if acct.Plan.Key != "max" {
		t.Errorf("plan = %q, want max", acct.Plan.Key)
	}
	if acct.Usage.Requests != 580 {
		t.Errorf("requests = %d, want 580", acct.Usage.Requests)
	}
	// An uncapped plan reports NULL, which is not zero: zero would read as
	// "stop serving immediately".
	if acct.Usage.HardLimit != nil {
		t.Errorf("hard_limit = %v, want nil for an uncapped plan", *acct.Usage.HardLimit)
	}
}

// Usage is the whole point, so a cached answer is a wrong one within seconds.
func TestMyEntitlementIsNotCached(t *testing.T) {
	stub := newStub(map[string]stubRoute{"/api/v1/entitlement": {body: entitlementAnswer}})
	client := newTestClient(t, stub)
	for range 3 {
		if _, err := client.MyEntitlement(t.Context()); err != nil {
			t.Fatalf("MyEntitlement: %v", err)
		}
	}
	if stub.count() != 3 {
		t.Errorf("issued %d request(s), want 3 - the answer was cached", stub.count())
	}
}

// The API answers an AS or a domain at an address's path, in a shape Lookup
// cannot read, so anything but an address is refused before it is sent, naming
// the calls that ask for those. A batch's addresses are refused entry by entry
// by the API instead.
func TestLookupRefusesATargetThatIsNotAnAddress(t *testing.T) {
	for _, target := range []string{"AS9009", "9009", "example.com", "notanip", "", " 8.8.8.8", "8.8.8.8/32"} {
		t.Run(target, func(t *testing.T) {
			stub := newStub(okRoutes("8.8.8.8"))
			client := newTestClient(t, stub)

			_, err := client.Lookup(t.Context(), target)
			assertKind(t, err, KindBadRequest, 0)
			var apiErr *Error
			if errors.As(err, &apiErr) && (!strings.Contains(apiErr.Message, "LookupASN") ||
				!strings.Contains(apiErr.Message, "LookupDomain")) {
				t.Errorf("Message = %q, want it to name LookupASN and LookupDomain", apiErr.Message)
			}
			if stub.count() != 0 {
				t.Errorf("sent %d request(s), want none", stub.count())
			}
		})
	}
}

// Every call goes to the client's base URL: the spec gives each lookup path a
// server of its own, production, which a generated client honoring it would
// send every lookup to whatever the client was built with.
func TestEveryLookupIsServedFromTheBaseURL(t *testing.T) {
	answers := map[string]any{
		"/8.8.8.8":            answerAbout("8.8.8.8", nil),
		"/myip":               answerAbout("203.0.113.9", nil),
		"/asn/9009":           corpusBody("asn-unlimited-answer"),
		"/domain/example.com": corpusBody("domain-unlimited-answer"),
		"/api/v1/entitlement": entitlementAnswer,
		"/batch": map[string]any{
			"results": map[string]any{"9.9.9.9": answerAbout("9.9.9.9", nil)}, "errors": map[string]any{},
		},
	}
	var served atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(server.Close)
	client, err := New(WithBaseURL(server.URL), WithRetries(0),
		WithHTTPClient(&http.Client{Transport: refuseElsewhere(server.URL)}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for name, call := range optionedCalls {
		if name == "Lookup" {
			call = func(ctx context.Context, c *Client, opts ...LookupOption) error {
				_, err := c.Lookup(ctx, "8.8.8.8", opts...)
				return err
			}
		}
		if err := call(t.Context(), client); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := int(served.Load()); n != len(optionedCalls) {
		t.Errorf("the local API served %d call(s), want %d", n, len(optionedCalls))
	}
}

// A transport that sends to one origin alone, and fails a request to any other.
func refuseElsewhere(origin string) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.String(), origin+"/") {
			return nil, fmt.Errorf("sent to %s, not the base URL %s", req.URL, origin)
		}
		return http.DefaultTransport.RoundTrip(req)
	}
}

// The AS number goes out as the bare number, and the domain as given,
// path-escaped; the API normalizes both.
func TestAnASAndADomainAreSentAsGiven(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"asn/4294967295":     {body: corpusBody("asn-unlimited-answer")},
		"domain/bücher.de":   {body: corpusBody("domain-unlimited-answer")},
		"domain/EXAMPLE.com": {body: corpusBody("domain-unlimited-answer")},
	})
	client := newTestClient(t, stub)

	if _, err := client.LookupASN(t.Context(), 4294967295); err != nil {
		t.Fatalf("LookupASN: %v", err)
	}
	for _, domain := range []string{"bücher.de", "EXAMPLE.com"} {
		if _, err := client.LookupDomain(t.Context(), domain); err != nil {
			t.Fatalf("LookupDomain(%q): %v", domain, err)
		}
	}
	var sent []string
	for _, call := range stub.seen() {
		sent = append(sent, call.url)
	}
	want := []string{
		"https://api.internetdata.io/asn/4294967295",
		"https://api.internetdata.io/domain/b%C3%BCcher.de",
		"https://api.internetdata.io/domain/EXAMPLE.com",
	}
	if strings.Join(sent, " ") != strings.Join(want, " ") {
		t.Errorf("sent %v, want %v", sent, want)
	}
}

// The cache and its shared requests are for addresses: an AS or a domain asked
// twice is asked twice.
func TestAnASOrADomainIsNeverCached(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		"asn/9009":           {body: corpusBody("asn-unlimited-answer")},
		"domain/example.com": {body: corpusBody("domain-unlimited-answer")},
	})
	client := newTestClient(t, stub)

	for range 2 {
		if _, err := client.LookupASN(t.Context(), 9009); err != nil {
			t.Fatalf("LookupASN: %v", err)
		}
		if _, err := client.LookupDomain(t.Context(), "example.com"); err != nil {
			t.Fatalf("LookupDomain: %v", err)
		}
	}
	if stub.count() != 4 {
		t.Errorf("issued %d request(s), want 4", stub.count())
	}
}

// The lookups and the database answer on the API's host, and OAuth at the apex,
// whose issuer it is: api. dropped, api-<env>. turned into <env>., anything
// else as given.
func TestOauthSignsInAtTheApexAndEverythingElseAtTheBaseURL(t *testing.T) {
	cases := []struct{ base, api, oauth string }{
		{"", "https://api.internetdata.io", "https://internetdata.io"},
		{"https://api.internetdata.io/", "https://api.internetdata.io", "https://internetdata.io"},
		{"https://api-dev.example.test", "https://api-dev.example.test", "https://dev.example.test"},
		{"https://internetdata.io", "https://internetdata.io", "https://internetdata.io"},
		{"https://dev.example.test", "https://dev.example.test", "https://dev.example.test"},
		{"https://API.example.test:8443", "https://API.example.test:8443", "https://example.test:8443"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://api-local:8080", "http://api-local:8080", "http://api-local:8080"},
		{"https://apis.example.test", "https://apis.example.test", "https://apis.example.test"},
		{"https://api-.example.test", "https://api-.example.test", "https://api-.example.test"},
	}
	for _, c := range cases {
		t.Run(c.base, func(t *testing.T) {
			routes := okRoutes("8.8.8.8")
			routes[pathList] = stubRoute{body: catalog("bogon_ip")}
			routes[".well-known/oauth-authorization-server"] = stubRoute{body: json.RawMessage(everyRequiredMember)}
			stub := newStub(routes)
			opts := []Option{WithHTTPClient(stub.client()), WithRetries(0)}
			if c.base != "" {
				opts = append(opts, WithBaseURL(c.base))
			}
			client, err := New(opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if _, err := client.Lookup(t.Context(), "8.8.8.8"); err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if _, err := client.Database.List(t.Context()); err != nil {
				t.Fatalf("List: %v", err)
			}
			if _, err := client.Oauth.Metadata(t.Context()); err != nil {
				t.Fatalf("Oauth.Metadata: %v", err)
			}
			link, err := client.Oauth.AuthorizationURL("cid", "http://127.0.0.1:8765/cb", "challenge",
				AuthorizationURLOptions{})
			if err != nil {
				t.Fatalf("AuthorizationURL: %v", err)
			}

			want := []string{
				c.api + "/8.8.8.8",
				c.api + pathList,
				c.oauth + "/.well-known/oauth-authorization-server",
			}
			for i, call := range stub.seen() {
				if i >= len(want) || call.url != want[i] {
					t.Errorf("request %d went to %s, want %v", i, call.url, want)
				}
			}
			if !strings.HasPrefix(link, c.oauth+"/oauth/authorize?") {
				t.Errorf("AuthorizationURL = %s, want it at %s", link, c.oauth)
			}
		})
	}
}
