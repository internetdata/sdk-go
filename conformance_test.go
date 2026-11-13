// Asserts the shared conformance corpus that every InternetData SDK asserts.
//
// The corpus is generated into testdata/ and is identical across languages, so
// a behavior that drifts here fails here rather than surfacing as two client
// libraries quietly disagreeing about the same refusal.

package internetdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The error map is the load-bearing half of this corpus. Three of the four
// vpndetection SDKs turned a 404 into a retryable server_error, which no
// language's own tests caught and this fixture does. Every case is served to a
// lookup and to a database call, since the two read different envelopes.
func TestARefusalIsClassifiedByStatusAndRetryAfterNotByItsCode(t *testing.T) {
	for _, c := range corpus(t).Errors {
		for _, call := range refusableCalls {
			t.Run(c.Name+"/"+call.name, func(t *testing.T) {
				stub := newStub(map[string]stubRoute{
					call.route: {status: c.Status, body: json.RawMessage(c.Body), headers: c.Headers},
				})
				// No retries, so a retryable failure surfaces rather than looping.
				client := newTestClient(t, stub, WithRetries(0))

				err := call.do(t.Context(), client)
				var apiErr *Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("error was %v, want an *internetdata.Error", err)
				}
				if string(apiErr.Kind) != c.Expect.Kind {
					t.Errorf("Kind = %q, want %q", apiErr.Kind, c.Expect.Kind)
				}
				if apiErr.Retryable() != c.Expect.Retryable {
					t.Errorf("Retryable() = %v, want %v", apiErr.Retryable(), c.Expect.Retryable)
				}
				if apiErr.StatusCode != c.Status {
					t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, c.Status)
				}
				// The API's own words, not a status-derived sentence: NOT_LICENSED
				// and LICENSE_EXPIRED are both 403 and mean different things to do
				// next.
				if c.Expect.Message != "" && apiErr.Message != c.Expect.Message {
					t.Errorf("Message = %q, want %q", apiErr.Message, c.Expect.Message)
				}
				if c.Expect.RetryAfterSeconds != nil {
					want := *c.Expect.RetryAfterSeconds
					if got := int(apiErr.RetryAfter.Seconds()); got != want {
						t.Errorf("RetryAfter = %ds, want %ds", got, want)
					}
				}
			})
		}
	}
}

// A retryable classification is only worth anything if the retry actually
// happens, and a non-retryable one only if it does not.
func TestOnlyTheRetryableRefusalsAreRetried(t *testing.T) {
	for _, c := range corpus(t).Errors {
		for _, call := range refusableCalls {
			t.Run(c.Name+"/"+call.name, func(t *testing.T) {
				stub := newStub(map[string]stubRoute{
					call.route: {status: c.Status, body: json.RawMessage(c.Body), headers: c.Headers},
				})
				client := newTestClient(t, stub, WithRetries(1))

				if err := call.do(t.Context(), client); err == nil {
					t.Fatal("the call should have failed")
				}

				want := 1
				if c.Expect.Retryable {
					want = 2
				}
				if got := stub.count(); got != want {
					t.Errorf("issued %d request(s), want %d for retryable=%v",
						got, want, c.Expect.Retryable)
				}
			})
		}
	}
}

// A lookup and a database call, each with the route its refusal is served on.
var refusableCalls = []struct {
	name, route string
	do          func(context.Context, *Client) error
}{
	{"Lookup", "1.1.1.1", func(ctx context.Context, c *Client) error {
		_, err := c.Lookup(ctx, "1.1.1.1")
		return err
	}},
	{"Database.Metadata", pathMetadata, func(ctx context.Context, c *Client) error {
		_, err := c.Database.Metadata(ctx, "bogon_ip_v1")
		return err
	}},
}

func TestIsBogonMatchesTheCanonicalRanges(t *testing.T) {
	for _, c := range corpus(t).IsBogon {
		if got := IsBogon(c.IP); got != c.Expect {
			t.Errorf("IsBogon(%q) = %v, want %v (%s)", c.IP, got, c.Expect, c.Why)
		}
	}
}

// A server listening on :: can see an IPv4 visitor as ::ffff:a.b.c.d, which
// read whole is inside ::ffff:0:0/96 and so would be answered as a bogon with
// no request made.
func TestAnIPv4MappedAddressIsTheIPv4AddressItCarries(t *testing.T) {
	for _, c := range corpus(t).IPv4Mapped {
		t.Run(c.IP, func(t *testing.T) {
			if got := IsBogon(c.IP); got != c.Expect {
				t.Fatalf("IsBogon(%q) = %v, want %v (%s)", c.IP, got, c.Expect, c.Why)
			}
			wantSent := []string{c.Carries}
			if c.Expect {
				wantSent = nil
			}

			stub := newStub(okRoutes(c.Carries))
			client := newTestClient(t, stub)
			r, err := client.Lookup(t.Context(), c.IP)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", c.IP, err)
			}
			if r.IsBogon != c.Expect || r.IP != c.Carries {
				t.Errorf("Lookup(%q) answered %s, bogon %v; want %s, bogon %v", c.IP, r.IP, r.IsBogon, c.Carries, c.Expect)
			}
			if _, err := client.Lookup(t.Context(), c.Carries); err != nil {
				t.Fatalf("Lookup(%q): %v", c.Carries, err)
			}
			if stub.count() != len(wantSent) {
				t.Errorf("issued %d request(s), want %d: one, cached under the carried address", stub.count(), len(wantSent))
			}

			batchStub := newStub(okRoutes(c.Carries))
			uncached := newTestClient(t, batchStub, WithoutCache())
			asked := dedupe([]string{c.IP, c.Carries})
			results, err := uncached.LookupBatch(t.Context(), asked)
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}
			if len(results) != len(asked) {
				t.Errorf("batch answered %d address(es), want %d, keyed as asked", len(results), len(asked))
			}
			if got := results[c.IP]; got.Err != nil || got.Result == nil || got.Result.IP != c.Carries {
				t.Errorf("batch answered %q with %+v, want %s", c.IP, got, c.Carries)
			}
			var sent []string
			for _, addrs := range batchStub.batched {
				sent = append(sent, addrs...)
			}
			if !slices.Equal(sent, wantSent) {
				t.Errorf("batch sent %v, want %v", sent, wantSent)
			}
		})
	}
}

// The answer is the corpus's, member for member, with the address filled in:
// nothing missing, nothing more, and never a request.
func TestBogonIsAnsweredLocallyInTheFullPaidShape(t *testing.T) {
	bogon := corpus(t).BogonResponse
	stub := newStub(nil)
	client := newTestClient(t, stub)

	result, err := client.Lookup(t.Context(), "10.0.0.1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	answer := reflect.ValueOf(result.LookupResponse)
	if result.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want 10.0.0.1", result.IP)
	}
	for _, name := range bogon.FlagsFalse {
		assertMember(t, answer, name, json.RawMessage("false"))
	}
	for _, name := range slices.Concat(bogon.EmptyObjects, bogon.EmptyLists) {
		assertEmpty(t, answer, name)
	}
	for name, want := range bogon.Answer {
		if !slices.Contains(bogon.EmptyObjects, name) && !slices.Contains(bogon.EmptyLists, name) {
			assertMember(t, answer, name, want)
		}
	}
	for name := range wireNames(answer.Type()) {
		if _, ok := bogon.Answer[name]; !ok && name != "ip" {
			assertAbsent(t, answer, name)
		}
	}
	if stub.count() != 0 {
		t.Errorf("a bogon must not reach the network, got %d request(s)", stub.count())
	}
}

// Every lookup case, each served once: a present member reads as served, an
// absent one is nil and never false or zero, a paid member inside a shared
// object included, and a detail object served as `{}` is present and empty.
func TestALookupKeepsAbsentApartFromFalseOnEveryPlan(t *testing.T) {
	for _, c := range corpus(t).Lookup {
		t.Run(c.Name, func(t *testing.T) {
			var route string
			var do func(*Client) (any, error)
			switch c.Call {
			case "lookup":
				var ip string
				if err := json.Unmarshal(c.Target, &ip); err != nil {
					t.Fatalf("target: %v", err)
				}
				route = ip
				do = func(client *Client) (any, error) {
					r, err := client.Lookup(t.Context(), ip)
					if err != nil {
						return nil, err
					}
					return r.LookupResponse, nil
				}
			case "lookupAsn":
				var asn uint32
				if err := json.Unmarshal(c.Target, &asn); err != nil {
					t.Fatalf("target: %v", err)
				}
				route = fmt.Sprintf("asn/%d", asn)
				do = func(client *Client) (any, error) {
					return client.LookupASN(t.Context(), asn)
				}
			case "lookupDomain":
				var domain string
				if err := json.Unmarshal(c.Target, &domain); err != nil {
					t.Fatalf("target: %v", err)
				}
				route = "domain/" + domain
				do = func(client *Client) (any, error) {
					return client.LookupDomain(t.Context(), domain)
				}
			default:
				t.Fatalf("the corpus names a call %q nothing here makes", c.Call)
			}
			stub := newStub(map[string]stubRoute{route: {status: c.Status, body: c.Body}})

			got, err := do(newTestClient(t, stub))
			if err != nil {
				t.Fatalf("%s: %v", c.Call, err)
			}
			answer := reflect.Indirect(reflect.ValueOf(got))
			for name, want := range c.Expect.Present {
				assertMember(t, answer, name, want)
			}
			for _, name := range c.Expect.Objects {
				if !present(wireField(t, answer, name)) {
					t.Errorf("%s must be present", name)
				}
			}
			for _, name := range c.Expect.Empty {
				assertEmpty(t, answer, name)
			}
			for _, name := range c.Expect.Absent {
				assertAbsent(t, answer, name)
			}
			for object, names := range c.Expect.AbsentIn {
				inner := wireField(t, answer, object)
				if !present(inner) {
					t.Errorf("%s must be present", object)
					continue
				}
				for _, name := range names {
					assertAbsent(t, reflect.Indirect(inner), object+"."+name)
				}
			}
			if stub.count() != 1 {
				t.Errorf("issued %d request(s), want 1", stub.count())
			}
		})
	}
}

func TestTheStandingsAreExactlyWhatTheCorpusPins(t *testing.T) {
	got := []string{string(StandingLicensed), string(StandingExpired), string(StandingUnlicensed)}
	assertSameSet(t, "standings", got, corpus(t).Standings)
}

func TestTheLicenseTypesAreExactlyWhatTheCorpusPins(t *testing.T) {
	got := []string{
		string(LicenseTypeEvaluation),
		string(LicenseTypeStandard),
		string(LicenseTypeRedistribute),
	}
	assertSameSet(t, "license_type", got, corpus(t).LicenseType)
}

func TestTheFormatsAreExactlyWhatTheCorpusPins(t *testing.T) {
	got := []string{string(FormatCSVGZ), string(FormatMMDB)}
	assertSameSet(t, "formats", got, corpus(t).Formats)
}

// The visibility contract, rule by rule.
//
// A private family is one built for a single customer. It is ABSENT from
// another organization's listing rather than present with standing
// `unlicensed`, and the SERVER is what enforces that. All this library has to
// do is not undermine it, which is what the corpus's three client rules spell
// out.
//
// Driven off the corpus rather than written out, so a rule added there fails
// here instead of going quietly unasserted. The private IDs are deliberately
// not in the corpus - it is committed into public repositories - so the stub
// names a placeholder of its own.
func TestTheVisibilityContractIsHeldRuleByRule(t *testing.T) {
	held := map[string]func(*testing.T){
		"listing-is-returned-as-served":            assertListingIsReturnedAsServed,
		"no-catalog-is-compiled-into-the-client":   assertNoCatalogIsCompiledIn,
		"a-listing-is-never-reused-across-clients": assertAListingIsNeverReused,
	}
	data := corpus(t)

	for _, rule := range data.Visibility.ClientRules {
		assert, ok := held[rule]
		if !ok {
			t.Errorf("the corpus names the rule %q and nothing here asserts it (%s)",
				rule, data.Visibility.Why)
			continue
		}
		t.Run(rule, assert)
	}
	if len(held) != len(data.Visibility.ClientRules) {
		t.Errorf("%d rule(s) asserted here, and the corpus names %d",
			len(held), len(data.Visibility.ClientRules))
	}
}

// What comes back is what the server sent: same families, same order, same
// standings. A client that added to the catalog from any other source would be
// advertising one customer's commission to every other.
func assertListingIsReturnedAsServed(t *testing.T) {
	served := catalog("bogon_asn", "bogon_ip")
	served["databases"].([]map[string]any)[1]["standing"] = "unlicensed"
	stub := newStub(map[string]stubRoute{pathList: {body: served}})
	client := newTestClient(t, stub)

	databases, err := client.Database.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := basesOf(databases); !slices.Equal(got, []string{"bogon_asn", "bogon_ip"}) {
		t.Errorf("List = %v, want the served catalog unchanged", got)
	}
	if databases[0].Standing != StandingLicensed || databases[1].Standing != StandingUnlicensed {
		t.Errorf("standings came back as %q and %q, want them verbatim",
			databases[0].Standing, databases[1].Standing)
	}
}

// Nothing about which databases exist is baked in, so an empty listing stays
// empty and a family the library has never heard of comes through untouched. A
// compiled-in catalog would answer for an organization that cannot see it, and
// would filter out one it can.
func assertNoCatalogIsCompiledIn(t *testing.T) {
	empty := newStub(map[string]stubRoute{pathList: {body: map[string]any{"databases": []any{}}}})
	if databases, err := newTestClient(t, empty).Database.List(t.Context()); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(databases) != 0 {
		t.Errorf("an empty listing came back as %v", basesOf(databases))
	}

	unknown := newStub(map[string]stubRoute{
		pathList: {body: catalog("a_family_this_library_has_never_heard_of")},
	})
	databases, err := newTestClient(t, unknown).Database.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := basesOf(databases); !slices.Equal(got, []string{"a_family_this_library_has_never_heard_of"}) {
		t.Errorf("List = %v, so the answer was filtered against something compiled in", got)
	}
}

// Two keys, two answers, and a repeat is a new request. A listing cached
// against one key and handed to another is exactly how a private family leaks
// without anyone editing a listing at all, and licenses start and end, so a
// catalog has no shelf life this library could assume.
func assertAListingIsNeverReused(t *testing.T) {
	const private = "a_family_built_for_one_customer"
	stub := newStub(nil)
	stub.byKey = map[string]map[string]stubRoute{
		"key-commissioner": {pathList: {body: catalog("bogon_ip", private)}},
		"key-stranger":     {pathList: {body: catalog("bogon_ip")}},
	}

	theirs, err := newKeyedTestClient(t, stub, "key-commissioner").Database.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	stranger := newKeyedTestClient(t, stub, "key-stranger")
	others, err := stranger.Database.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := stranger.Database.List(t.Context()); err != nil {
		t.Fatalf("List: %v", err)
	}

	if !slices.Contains(basesOf(theirs), private) {
		t.Errorf("the commissioning organization cannot see %s", private)
	}
	if slices.Contains(basesOf(others), private) {
		t.Errorf("%s reached an organization that does not license it", private)
	}
	if stub.count() != 3 {
		t.Errorf("issued %d request(s), want 3: nothing here may be answered from memory",
			stub.count())
	}
}

// Without a cache nothing boards the addresses in flight, which collapses a
// repeated one too, so only that client shows whether the input was deduped.
func TestBatchDedupesShortCircuitsBogonsAndKeysByAddress(t *testing.T) {
	c := batchCase(t, "dedup-bogon-and-order-free-keying")
	for name, opts := range map[string][]Option{"cached": nil, "uncached": {WithoutCache()}} {
		t.Run(name, func(t *testing.T) {
			stub := newStub(okRoutes("1.1.1.1", "8.8.8.8"))
			client := newTestClient(t, stub, opts...)

			got, err := client.LookupBatch(t.Context(), c.Input)
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}

			// Keyed by address, not positional. A Go map has no order of its own,
			// so the corpus's key list is asserted as a set.
			assertKeys(t, got, c.Expect.Keys)
			if stub.count() != *c.Expect.HTTPRequests {
				t.Errorf("issued %d request(s), want %d", stub.count(), *c.Expect.HTTPRequests)
			}
			for _, ip := range c.Expect.BogonKeys {
				if !got[ip].Result.IsBogon {
					t.Errorf("%s should be a local answer", ip)
				}
			}
			var sent []string
			for _, addrs := range stub.batched {
				sent = append(sent, addrs...)
			}
			var want []string
			for _, ip := range c.Expect.Keys {
				if !slices.Contains(c.Expect.BogonKeys, ip) {
					want = append(want, ip)
				}
			}
			if !slices.Equal(slices.Sorted(slices.Values(sent)), slices.Sorted(slices.Values(want))) {
				t.Errorf("sent %v, want each address but the bogons once: %v", sent, want)
			}
		})
	}
}

func TestOneBadAddressDoesNotLoseTheRestOfTheBatch(t *testing.T) {
	c := batchCase(t, "partial-failure-does-not-fail-the-batch")
	stub := newStub(okRoutes("1.1.1.1"))
	client := newTestClient(t, stub, WithRetries(0))

	got, err := client.LookupBatch(t.Context(), c.Input)
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}

	assertKeys(t, got, c.Expect.Keys)
	for _, ip := range c.Expect.ErrorKeys {
		var apiErr *Error
		if !errors.As(got[ip].Err, &apiErr) {
			t.Errorf("%s should carry its own error, got %v", ip, got[ip].Err)
			continue
		}
		if want := c.Expect.ErrorKinds[ip]; string(apiErr.Kind) != want {
			t.Errorf("%s: Kind = %q, want %q", ip, apiErr.Kind, want)
		}
	}
	if good := got["1.1.1.1"]; good.Err != nil || good.Result.IP != "1.1.1.1" {
		t.Errorf("the good address should still have answered, got %+v", good)
	}
}

func TestACacheHitIssuesNoSecondRequest(t *testing.T) {
	c := batchCase(t, "cache-hit-issues-no-second-request")
	stub := newStub(okRoutes("1.1.1.1"))
	client := newTestClient(t, stub)

	for range c.Repeat {
		if _, err := client.LookupBatch(t.Context(), c.Input); err != nil {
			t.Fatalf("LookupBatch: %v", err)
		}
	}
	if stub.count() != *c.Expect.HTTPRequests {
		t.Errorf("issued %d request(s), want %d", stub.count(), *c.Expect.HTTPRequests)
	}
}

// The boundary, and the absence of any cap: 2,500 addresses are three requests.
func TestALargeBatchIsSentInChunksOfAThousand(t *testing.T) {
	for _, name := range []string{"chunks-of-one-thousand", "uncapped-input-is-chunked"} {
		t.Run(name, func(t *testing.T) {
			c := batchCase(t, name)
			stub := newStub(okRoutes(c.Input...))
			client := newTestClient(t, stub, WithoutCache())

			got, err := client.LookupBatch(t.Context(), c.Input)
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}
			if len(got) != c.Expect.KeyCount {
				t.Errorf("batch has %d key(s), want %d", len(got), c.Expect.KeyCount)
			}
			if stub.count() != *c.Expect.HTTPRequests {
				t.Errorf("issued %d request(s), want %d", stub.count(), *c.Expect.HTTPRequests)
			}
			for _, call := range stub.seen() {
				if call.path != "/batch" {
					t.Errorf("requested %s, want only POST /batch", call.url)
				}
			}
			for _, ip := range c.Input {
				if answer := got[ip]; answer.Err != nil || answer.Result == nil || answer.Result.IP != ip {
					t.Fatalf("%s: %+v, want a served answer for itself", ip, answer)
				}
			}
		})
	}
}

// A per-entry failure carries no headers, so its 429 can only be a spent
// allowance, and a 500 is the server's; neither is retried per entry, because
// retries belong to the call and the call succeeded.
func TestAnEntryErrorIsClassifiedByItsStatus(t *testing.T) {
	c := batchCase(t, "an-entry-error-is-classified-by-its-status")
	routes := okRoutes("1.1.1.1")
	routes["8.8.8.8"] = stubRoute{
		status: http.StatusTooManyRequests,
		body:   map[string]string{"error": "overage cap reached; raise or remove it in Billing"},
	}
	routes["9.9.9.9"] = stubRoute{
		status: http.StatusInternalServerError,
		body:   map[string]string{"error": "lookup failed"},
	}
	stub := newStub(routes)
	client := newTestClient(t, stub, WithRetries(3))

	got, err := client.LookupBatch(t.Context(), c.Input)
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	assertKeys(t, got, c.Expect.Keys)
	for ip, kind := range c.Expect.ErrorKinds {
		var apiErr *Error
		if !errors.As(got[ip].Err, &apiErr) || string(apiErr.Kind) != kind {
			t.Errorf("%s: error = %v, want kind %q", ip, got[ip].Err, kind)
		}
	}
	if stub.count() != *c.Expect.HTTPRequests {
		t.Errorf("issued %d request(s), want %d", stub.count(), *c.Expect.HTTPRequests)
	}
}

func TestTwoClientsNeverShareACachedAnswer(t *testing.T) {
	stub := newStub(okRoutes("1.1.1.1"))
	a := newKeyedTestClient(t, stub, "key-a")
	b := newKeyedTestClient(t, stub, "key-b")

	if _, err := a.Lookup(t.Context(), "1.1.1.1"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := b.Lookup(t.Context(), "1.1.1.1"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// Two keys can be on different plans and so entitled to different fields; a
	// shared cache would serve one of them the other's shape.
	if stub.count() != 2 {
		t.Errorf("issued %d request(s), want 2", stub.count())
	}
}

func TestCachingCanBeTurnedOff(t *testing.T) {
	stub := newStub(okRoutes("1.1.1.1"))
	client := newTestClient(t, stub, WithoutCache())

	for range 2 {
		if _, err := client.Lookup(t.Context(), "1.1.1.1"); err != nil {
			t.Fatalf("Lookup: %v", err)
		}
	}
	if stub.count() != 2 {
		t.Errorf("issued %d request(s), want 2", stub.count())
	}
}

// The compiled-in table and the corpus are emitted by the same generator run,
// so a mismatch means one of the two was regenerated without the other.
func TestBogonTableMatchesTheCorpus(t *testing.T) {
	data := corpus(t)
	if !slices.Equal(bogonV4, data.Bogons.V4) {
		t.Errorf("bogonV4 differs from the corpus:\n got %v\nwant %v", bogonV4, data.Bogons.V4)
	}
	if !slices.Equal(bogonV6, data.Bogons.V6) {
		t.Errorf("bogonV6 differs from the corpus:\n got %v\nwant %v", bogonV6, data.Bogons.V6)
	}
}

// Guards the panic in parsePrefixes: it can only ever fire on a broken build.
func TestBogonTableParses(t *testing.T) {
	for _, cidr := range slices.Concat(bogonV4, bogonV6) {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			t.Errorf("bogon range %q does not parse: %v", cidr, err)
		}
	}
	if len(bogonPrefixesV4()) != len(bogonV4) || len(bogonPrefixesV6()) != len(bogonV6) {
		t.Error("the parsed tables do not cover every generated range")
	}
}

func newTestClient(t *testing.T, stub *stubTransport, opts ...Option) *Client {
	t.Helper()
	return newKeyedTestClient(t, stub, "test-key", opts...)
}

func newKeyedTestClient(
	t *testing.T, stub *stubTransport, key string, opts ...Option,
) *Client {
	t.Helper()
	client, err := New(
		append([]Option{WithHTTPClient(stub.client()), WithAPIKey(key)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// A member is present when it is not a nil pointer: a member the spec requires
// is never a pointer, and is always there.
func present(member reflect.Value) bool {
	return member.Kind() != reflect.Pointer || !member.IsNil()
}

// assertMember holds a present member to the JSON value the corpus gives it.
func assertMember(t *testing.T, answer reflect.Value, wire string, want json.RawMessage) {
	t.Helper()
	member := wireField(t, answer, wire)
	if !present(member) {
		t.Errorf("%s must be present and %s, but is absent", wire, want)
		return
	}
	got, err := json.Marshal(member.Interface())
	if err != nil {
		t.Fatalf("%s: %v", wire, err)
	}
	var gotAny, wantAny any
	if json.Unmarshal(got, &gotAny) != nil || json.Unmarshal(want, &wantAny) != nil {
		t.Fatalf("%s: comparing %s with %s", wire, got, want)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("%s = %s, want %s", wire, got, want)
	}
}

// assertEmpty holds a detail object present and empty, or a list present and
// holding nothing.
func assertEmpty(t *testing.T, answer reflect.Value, wire string) {
	t.Helper()
	member := wireField(t, answer, wire)
	if !present(member) {
		t.Errorf("%s must be present and empty, but is absent", wire)
		return
	}
	member = reflect.Indirect(member)
	if member.Kind() == reflect.Slice || member.Kind() == reflect.Map {
		if member.IsNil() || member.Len() != 0 {
			t.Errorf("%s must be present and EMPTY, got %+v", wire, member.Interface())
		}
		return
	}
	if !member.IsZero() {
		t.Errorf("%s must be present and EMPTY, got %+v", wire, member.Interface())
	}
}

// assertAbsent holds a member to nil: not in this plan, never false or zero.
func assertAbsent(t *testing.T, answer reflect.Value, wire string) {
	t.Helper()
	_, name, nested := strings.Cut(wire, ".")
	if !nested {
		name = wire
	}
	member := wireField(t, answer, name)
	if member.Kind() != reflect.Pointer {
		t.Errorf("%s is not optional, so it cannot express absent-versus-false", wire)
		return
	}
	if !member.IsNil() {
		t.Errorf("%s must be ABSENT, not %+v", wire, member.Elem().Interface())
	}
}

// Looks a member up by its WIRE name, so the corpus asserts the names the API
// actually serves rather than whatever the generator called them.
func wireField(t *testing.T, answer reflect.Value, wire string) reflect.Value {
	t.Helper()
	field, ok := fieldByWire(answer.Type(), wire)
	if !ok {
		t.Fatalf("%s has no member tagged %q", answer.Type().Name(), wire)
	}
	return answer.FieldByIndex(field.Index)
}

func fieldByWire(answer reflect.Type, wire string) (reflect.StructField, bool) {
	for i := range answer.NumField() {
		field := answer.Field(i)
		if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag == wire {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

func wireNames(answer reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := range answer.NumField() {
		if tag, _, _ := strings.Cut(answer.Field(i).Tag.Get("json"), ","); tag != "" {
			names[tag] = true
		}
	}
	return names
}

func assertKeys(t *testing.T, got map[string]BatchResult, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("batch has %d key(s), want %d: %v", len(got), len(want), got)
	}
	for _, ip := range want {
		if _, ok := got[ip]; !ok {
			t.Errorf("batch is missing the key %q", ip)
		}
	}
}

func batchCase(t *testing.T, name string) corpusBatch {
	t.Helper()
	for _, c := range corpus(t).Batch {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("the corpus has no batch case named %q", name)
	return corpusBatch{}
}

func basesOf(databases []Database) []string {
	bases := make([]string, len(databases))
	for i, d := range databases {
		bases[i] = d.Base
	}
	return bases
}

func assertSameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotSorted, wantSorted := slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))
	if !slices.Equal(gotSorted, wantSorted) {
		t.Errorf("%s = %v, want %v", what, gotSorted, wantSorted)
	}
}

func corpus(t *testing.T) corpusData {
	t.Helper()
	raw, err := os.ReadFile("testdata/testdata.json")
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	var data corpusData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parsing the corpus: %v", err)
	}
	return data
}

type corpusData struct {
	Errors []struct {
		Name    string            `json:"name"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
		Expect  struct {
			Kind              string `json:"kind"`
			Retryable         bool   `json:"retryable"`
			Message           string `json:"message"`
			RetryAfterSeconds *int   `json:"retryAfterSeconds"`
		} `json:"expect"`
	} `json:"errors"`
	Standings   []string `json:"standings"`
	LicenseType []string `json:"license_type"`
	Formats     []string `json:"formats"`
	Visibility  struct {
		Why         string   `json:"why"`
		ClientRules []string `json:"clientRules"`
	} `json:"visibility"`
	IsBogon []struct {
		IP     string `json:"ip"`
		Expect bool   `json:"expect"`
		Why    string `json:"why"`
	} `json:"isBogon"`
	IPv4Mapped []struct {
		IP      string `json:"ip"`
		Carries string `json:"carries"`
		Expect  bool   `json:"expect"`
		Why     string `json:"why"`
	} `json:"ipv4Mapped"`
	BogonResponse struct {
		FlagsFalse   []string                   `json:"flagsFalse"`
		EmptyObjects []string                   `json:"emptyObjects"`
		EmptyLists   []string                   `json:"emptyLists"`
		Answer       map[string]json.RawMessage `json:"answer"`
	} `json:"bogonResponse"`
	Lookup []struct {
		Name   string          `json:"name"`
		Call   string          `json:"call"`
		Target json.RawMessage `json:"target"`
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
		Expect struct {
			Present  map[string]json.RawMessage `json:"present"`
			Objects  []string                   `json:"objects"`
			Empty    []string                   `json:"empty"`
			Absent   []string                   `json:"absent"`
			AbsentIn map[string][]string        `json:"absentIn"`
		} `json:"expect"`
	} `json:"lookup"`
	Batch  []corpusBatch `json:"batch"`
	Bogons struct {
		V4 []string `json:"v4"`
		V6 []string `json:"v6"`
	} `json:"bogons"`
}

type corpusBatch struct {
	Name   string   `json:"name"`
	Input  []string `json:"input"`
	Repeat int      `json:"repeat"`
	Expect struct {
		Keys         []string          `json:"keys"`
		HTTPRequests *int              `json:"httpRequests"`
		BogonKeys    []string          `json:"bogonKeys"`
		ErrorKeys    []string          `json:"errorKeys"`
		KeyCount     int               `json:"keyCount"`
		ErrorKinds   map[string]string `json:"errorKinds"`
	} `json:"expect"`
}
