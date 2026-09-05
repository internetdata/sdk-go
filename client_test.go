// The Go-specific API surface, as distinct from the shared conformance corpus
// in conformance_test.go.

package internetdata

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The key is an option, not an argument, because what this API serves without a
// licence is a product decision and a New that could not be called without one
// would have to change shape to follow it. What must never go out is
// "Authorization: Bearer " with nothing after it, which reads as a wrong key
// rather than as none.
func TestAClientBuildsWithNoKeyAndSendsNoAuthorizationHeader(t *testing.T) {
	stub := newStub(map[string]stubRoute{pathList: {body: catalog("bogon_ip")}})
	client, err := New(WithHTTPClient(stub.client()))
	if err != nil {
		t.Fatalf("New with no key: %v", err)
	}

	if _, err := client.Database.List(t.Context()); err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := stub.seen()[0].auth; got != "" {
		t.Errorf("a keyless client sent Authorization: %q, want no header at all", got)
	}
}

func TestNewRejectsUnusableOptions(t *testing.T) {
	cases := []struct {
		name   string
		option Option
	}{
		{"negative retries", WithRetries(-1)},
		{"nil http client", WithHTTPClient(nil)},
		{"base url with no scheme", WithBaseURL("internetdata.io")},
		{"base url with no host", WithBaseURL("https://")},
		{"unparseable base url", WithBaseURL("://")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(WithAPIKey("key"), c.option); err == nil {
				t.Error("New should have rejected the option")
			}
		})
	}
}

// The key is the whole identity here, so a client that quietly stopped sending
// it would fail every call with a refusal that reads like a licence problem.
func TestTheKeyIsSentAsABearerTokenOnEveryEndpoint(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathList:      {body: catalog("bogon_ip")},
		pathMetadata:  {body: map[string]any{"id": "bogon_ip_v1", "updated": "2026-09-04", "entries": 42, "schema": map[string]any{}, "size": map[string]int64{"csvgz": 760}}},
		pathChecksum:  {body: checksumBody},
		pathDownloads: {body: map[string]any{"downloads": []any{}}},
	})
	client := newKeyedTestClient(t, stub, "a-real-key")

	if _, err := client.Database.List(t.Context()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := client.Database.Metadata(t.Context(), "bogon_ip_v1"); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if _, err := client.Database.Checksums(t.Context(), "bogon_ip_v1", FormatCSVGZ); err != nil {
		t.Fatalf("Checksums: %v", err)
	}
	if _, err := client.Database.Downloads(t.Context(), 10); err != nil {
		t.Fatalf("Downloads: %v", err)
	}

	calls := stub.seen()
	if len(calls) != 4 {
		t.Fatalf("issued %d request(s), want 4", len(calls))
	}
	for _, call := range calls {
		if call.key != "a-real-key" {
			t.Errorf("%s carried key %q, want the bearer token", call.path, call.key)
		}
	}
}

// The digests hang under a `checksums` key. Reading a top-level sha256 shipped
// broken in one of the vpndetection SDKs, so the depth is pinned here.
func TestChecksumsUnwrapsPastTheEnvelopeAndKeepsAllFour(t *testing.T) {
	stub := newStub(map[string]stubRoute{pathChecksum: {body: checksumBody}})
	client := newTestClient(t, stub)

	sums, err := client.Database.Checksums(t.Context(), "bogon_ip_v1", FormatCSVGZ)
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	if sums.MD5 != "d41d8cd9" || sums.SHA1 != "da39a3ee" ||
		sums.SHA256 != "e3b0c442" || sums.SHA512 != "cf83e135" {
		t.Errorf("Checksums = %+v, and did not unwrap the four digests", sums)
	}
	if query := stub.seen()[0].query; query != "id=bogon_ip_v1&format=csvgz" {
		t.Errorf("asked for %q, want id=bogon_ip_v1&format=csvgz", query)
	}
}

// Zero means "the API's own default", which is what a caller who does not care
// passes. Sending limit=0 would be rejected by the schema's minimum of 1.
func TestDownloadsOmitsALimitItWasNotGiven(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathDownloads: {body: map[string]any{"downloads": []any{}}},
	})
	client := newTestClient(t, stub)

	for _, limit := range []int{0, -1} {
		if _, err := client.Database.Downloads(t.Context(), limit); err != nil {
			t.Fatalf("Downloads(%d): %v", limit, err)
		}
	}
	if _, err := client.Database.Downloads(t.Context(), 25); err != nil {
		t.Fatalf("Downloads(25): %v", err)
	}

	want := []string{"", "", "limit=25"}
	for i, call := range stub.seen() {
		if call.query != want[i] {
			t.Errorf("request %d asked %q, want %q", i, call.query, want[i])
		}
	}
}

func TestDownloadURLReturnsTheRedirectRatherThanFollowingIt(t *testing.T) {
	const location = "https://s3.example.test/bogon_ip_v1.csv.gz?X-Amz-Signature=abc"
	stub := newStub(map[string]stubRoute{
		pathDownload: {status: http.StatusFound, headers: map[string]string{"Location": location}},
	})
	client := newTestClient(t, stub)

	url, err := client.Database.DownloadURL(t.Context(), "bogon_ip_v1", FormatCSVGZ)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}

	if url != location {
		t.Errorf("DownloadURL = %q, want %q", url, location)
	}
	// Following it would have streamed the database itself into memory.
	if stub.count() != 1 {
		t.Errorf("issued %d request(s), want 1", stub.count())
	}
}

// A 302 with nothing to follow is the API failing, not the caller: returning an
// empty string would send a caller off to fetch "".
func TestA302WithNoLocationIsAnError(t *testing.T) {
	stub := newStub(map[string]stubRoute{pathDownload: {status: http.StatusFound}})
	client := newTestClient(t, stub, WithRetries(0))

	_, err := client.Database.DownloadURL(t.Context(), "bogon_ip_v1", FormatCSVGZ)

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != KindServerError {
		t.Fatalf("error was %v, want a server_error *Error", err)
	}
}

func TestRetriesAreConfigurable(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathList: {status: http.StatusInternalServerError, body: map[string]string{"rc": "INTERNAL"}},
	})
	client := newTestClient(t, stub, WithRetries(3))

	if _, err := client.Database.List(t.Context()); err == nil {
		t.Fatal("a 500 should have failed the call")
	}
	// One initial attempt plus three retries.
	if stub.count() != 4 {
		t.Errorf("issued %d request(s), want 4", stub.count())
	}
}

// A 429 with no Retry-After is a spent allowance, and retrying it is hammering
// a quota that will not recover until its window rolls over.
func TestASpentQuotaIsNeverRetried(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathList: {status: http.StatusTooManyRequests, body: map[string]string{"rc": "QUOTA_EXCEEDED"}},
	})
	client := newTestClient(t, stub, WithRetries(5))

	_, err := client.Database.List(t.Context())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != KindQuotaExceeded {
		t.Fatalf("error was %v, want a quota_exceeded *Error", err)
	}
	if stub.count() != 1 {
		t.Errorf("issued %d request(s), want 1", stub.count())
	}
}

func TestARateLimitIsRetriedAfterTheServerSuppliedWait(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathList: {
			status:  http.StatusTooManyRequests,
			body:    map[string]string{"rc": "RATE_LIMITED"},
			headers: map[string]string{"Retry-After": "1"},
		},
	})
	client := newTestClient(t, stub, WithRetries(1))

	start := time.Now()
	if _, err := client.Database.List(t.Context()); err == nil {
		t.Fatal("the call should still have failed after its retry")
	}

	if stub.count() != 2 {
		t.Errorf("issued %d request(s), want 2", stub.count())
	}
	// The header, not the backoff schedule, decides the wait.
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("waited %s before retrying, want at least the 1s Retry-After", waited)
	}
}

// A Retry-After may also be an HTTP date, and reading it as an integer silently
// yields zero, which reclassifies a throttle as a spent quota.
func TestRetryAfterIsReadAsSecondsOrAsAnHTTPDate(t *testing.T) {
	cases := []struct {
		header string
		want   ErrorKind
	}{
		{"2", KindRateLimited},
		{time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat), KindRateLimited},
		{"-5", KindQuotaExceeded},
		{"not-a-delay", KindQuotaExceeded},
		{"", KindQuotaExceeded},
	}
	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			stub := newStub(map[string]stubRoute{
				pathList: {
					status:  http.StatusTooManyRequests,
					body:    map[string]string{"rc": "RATE_LIMITED"},
					headers: map[string]string{"Retry-After": c.header},
				},
			})
			client := newTestClient(t, stub, WithRetries(0))

			_, err := client.Database.List(t.Context())
			var apiErr *Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("error was %v, want an *Error", err)
			}
			if apiErr.Kind != c.want {
				t.Errorf("Retry-After %q classified as %q, want %q", c.header, apiErr.Kind, c.want)
			}
		})
	}
}

// A refusal with no readable body still has to say something useful. The
// fallback is the status, and it must not be an empty message.
func TestARefusalWithNoEnvelopeStillCarriesAMessage(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathList: {status: http.StatusServiceUnavailable},
	})
	client := newTestClient(t, stub, WithRetries(0))

	_, err := client.Database.List(t.Context())
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error was %v, want an *Error", err)
	}
	if apiErr.Message == "" {
		t.Error("Message is empty, so the failure says nothing at all")
	}
	if !strings.Contains(apiErr.Error(), "503") {
		t.Errorf("Error() = %q, and does not name the status it fell back to", apiErr)
	}
}

// The retry loop takes its deadline from ctx, or a canceled call would sleep
// out the whole backoff schedule before noticing.
func TestACanceledContextEndsTheRetryLoop(t *testing.T) {
	stub := newStub(map[string]stubRoute{
		pathList: {
			status:  http.StatusTooManyRequests,
			body:    map[string]string{"rc": "RATE_LIMITED"},
			headers: map[string]string{"Retry-After": "30"},
		},
	})
	client := newTestClient(t, stub, WithRetries(2))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := client.Database.List(ctx); err == nil {
		t.Fatal("the call should have failed")
	}

	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("waited %s, so the 30s Retry-After outlived the context", waited)
	}
}

var checksumBody = map[string]any{
	"id": "bogon_ip_v1", "format": "csvgz",
	"checksums": map[string]string{
		"md5": "d41d8cd9", "sha1": "da39a3ee", "sha256": "e3b0c442", "sha512": "cf83e135",
	},
}
