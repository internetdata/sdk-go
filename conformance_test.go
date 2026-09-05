// Asserts the shared conformance corpus that every InternetData SDK asserts.
//
// The corpus is generated into testdata/ and is identical across languages, so
// a behavior that drifts here fails here rather than surfacing as two client
// libraries quietly disagreeing about the same refusal.

package internetdata

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
)

// The error map is the load-bearing half of this corpus. Three of the four
// vpndetection SDKs turned a 404 into a retryable server_error, which no
// language's own tests caught and this fixture does.
func TestARefusalIsClassifiedByStatusAndRetryAfterNotByItsCode(t *testing.T) {
	for _, c := range corpus(t).Errors {
		t.Run(c.Name, func(t *testing.T) {
			stub := newStub(map[string]stubRoute{
				pathMetadata: {status: c.Status, body: json.RawMessage(c.Body), headers: c.Headers},
			})
			// No retries, so a retryable failure surfaces rather than looping.
			client := newTestClient(t, stub, WithRetries(0))

			_, err := client.Metadata(t.Context(), "bogon_ip_v1")
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
			// The rc, not a status-derived sentence: NOT_LICENSED and
			// LICENSE_EXPIRED are both 403 and mean different things to do next.
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

// A retryable classification is only worth anything if the retry actually
// happens, and a non-retryable one only if it does not.
func TestOnlyTheRetryableRefusalsAreRetried(t *testing.T) {
	for _, c := range corpus(t).Errors {
		t.Run(c.Name, func(t *testing.T) {
			stub := newStub(map[string]stubRoute{
				pathMetadata: {status: c.Status, body: json.RawMessage(c.Body), headers: c.Headers},
			})
			client := newTestClient(t, stub, WithRetries(1))

			if _, err := client.Metadata(t.Context(), "bogon_ip_v1"); err == nil {
				t.Fatal("Metadata should have failed")
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

func TestTheStandingsAreExactlyWhatTheCorpusPins(t *testing.T) {
	got := []string{string(StandingLicensed), string(StandingExpired), string(StandingUnlicensed)}
	assertSameSet(t, "standings", got, corpus(t).Standings)
}

func TestTheRedistributionRightsAreExactlyWhatTheCorpusPins(t *testing.T) {
	got := []string{
		string(RedistributionEvaluation),
		string(RedistributionInternal),
		string(RedistributionRedistribute),
	}
	assertSameSet(t, "redistribution", got, corpus(t).Redistribution)
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
// here instead of going quietly unasserted. The private ids are deliberately
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

	databases, err := client.List(t.Context())
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
	if databases, err := newTestClient(t, empty).List(t.Context()); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(databases) != 0 {
		t.Errorf("an empty listing came back as %v", basesOf(databases))
	}

	unknown := newStub(map[string]stubRoute{
		pathList: {body: catalog("a_family_this_library_has_never_heard_of")},
	})
	databases, err := newTestClient(t, unknown).List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := basesOf(databases); !slices.Equal(got, []string{"a_family_this_library_has_never_heard_of"}) {
		t.Errorf("List = %v, so the answer was filtered against something compiled in", got)
	}
}

// Two keys, two answers, and a repeat is a new request. A listing cached
// against one key and handed to another is exactly how a private family leaks
// without anyone editing a listing at all, and licences start and end, so a
// catalog has no shelf life this library could assume.
func assertAListingIsNeverReused(t *testing.T) {
	const private = "a_family_built_for_one_customer"
	stub := newStub(nil)
	stub.byKey = map[string]map[string]stubRoute{
		"key-commissioner": {pathList: {body: catalog("bogon_ip", private)}},
		"key-stranger":     {pathList: {body: catalog("bogon_ip")}},
	}

	theirs, err := newKeyedTestClient(t, stub, "key-commissioner").List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	stranger := newKeyedTestClient(t, stub, "key-stranger")
	others, err := stranger.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := stranger.List(t.Context()); err != nil {
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

func newTestClient(t *testing.T, stub *stubTransport, opts ...Option) *Client {
	t.Helper()
	return newKeyedTestClient(t, stub, "test-key", opts...)
}

func newKeyedTestClient(
	t *testing.T, stub *stubTransport, key string, opts ...Option,
) *Client {
	t.Helper()
	client, err := New(key, append([]Option{WithHTTPClient(stub.client())}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
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
	Standings      []string `json:"standings"`
	Redistribution []string `json:"redistribution"`
	Formats        []string `json:"formats"`
	Visibility     struct {
		Why         string   `json:"why"`
		ClientRules []string `json:"clientRules"`
	} `json:"visibility"`
}
