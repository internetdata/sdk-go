// The published module looking addresses, an AS and a domain up: keyless
// against prod and staging, and with the staging key.
//
// Nothing here pins a field COUNT. What a served answer must satisfy on every
// plan: a member the wire carried reached the result, and one it left out is
// absent from it rather than false or zero, inside a shared object too.

package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	internetdata "github.com/internetdata/sdk-go/v2"
)

const (
	// A stable hosting address, and the one the corpus and the README use.
	probe = "146.70.116.130"
	// The second public address a refused batch needs: a batch of one is
	// answered as its lookup, keyless or on the free plan.
	other      = "8.8.8.8"
	probeAS    = 9009
	probeName  = "example.com"
	apiStaging = "https://api-staging.internetdata.io"
)

// Both deployments, keyless: prod through the client's default base URL.
var keylessAPIs = []struct{ name, base string }{
	{"prod", ""},
	{"staging", apiStaging},
}

func TestALookupWithoutAKeyIsUnlimitedsAnswer(t *testing.T) {
	for _, api := range keylessAPIs {
		t.Run(api.name, func(t *testing.T) {
			client, rec := lookupClient(t, api.base, "")

			r, err := client.Lookup(t.Context(), probe)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", probe, err)
			}

			if r.IP != probe || r.IsBogon {
				t.Errorf("answered about %s, bogon %v; want %s, served", r.IP, r.IsBogon, probe)
			}
			if r.Location.Country == "" || r.Asn.Asn == 0 {
				t.Errorf("the country %q or the AS %d is empty, and both are on every plan",
					r.Location.Country, r.Asn.Asn)
			}
			// Unlimited's answer: none of a paid plan's members, and no trial.
			if r.IsVpn != nil || r.Vpn != nil || r.Location.City != nil || r.TrialExpired != nil {
				t.Errorf("a keyless answer carries a paid member: is_vpn %v, vpn %v, city %v, trial_expired %v",
					r.IsVpn, r.Vpn, r.Location.City, r.TrialExpired)
			}
			assertServedAsSent(t, r.LookupResponse, rec.jsonBody(t, "/"+probe))
		})
	}
}

func TestAnASAndADomainWithoutAKeyAreUnlimitedsAnswers(t *testing.T) {
	for _, api := range keylessAPIs {
		t.Run(api.name, func(t *testing.T) {
			client, rec := lookupClient(t, api.base, "")

			asn, err := client.LookupASN(t.Context(), probeAS)
			if err != nil {
				t.Fatalf("LookupASN(%d): %v", probeAS, err)
			}
			if asn.Asn != probeAS || asn.Name == "" {
				t.Errorf("AS%d answered as AS%d named %q", probeAS, asn.Asn, asn.Name)
			}
			if asn.Prefixes != nil || asn.Whois != nil {
				t.Error("a keyless AS answer carries its prefixes or its whois, which are a paid plan's")
			}
			assertServedAsSent(t, *asn, rec.jsonBody(t, "/asn/9009"))

			domain, err := client.LookupDomain(t.Context(), probeName)
			if err != nil {
				t.Fatalf("LookupDomain(%s): %v", probeName, err)
			}
			if domain.Domain != probeName || !domain.IsRegistered {
				t.Errorf("%s answered as %q, registered %v", probeName, domain.Domain, domain.IsRegistered)
			}
			if domain.DNS != nil || domain.IPRanges != nil || domain.IsDisposable != nil {
				t.Error("a keyless domain answer carries its records, ranges or is_disposable, which are a paid plan's")
			}
			assertServedAsSent(t, *domain, rec.jsonBody(t, "/domain/"+probeName))
		})
	}
}

func TestMyIPWithoutAKeyNamesTheCallersAddress(t *testing.T) {
	for _, api := range keylessAPIs {
		t.Run(api.name, func(t *testing.T) {
			client, _ := lookupClient(t, api.base, "")

			r, err := client.MyIP(t.Context())
			if err != nil {
				t.Fatalf("MyIP: %v", err)
			}
			if _, err := netip.ParseAddr(r.IP); err != nil || r.IsBogon {
				t.Errorf("MyIP answered %q, bogon %v; want the public address this run calls from", r.IP, r.IsBogon)
			}
		})
	}
}

func TestABatchWithoutAKeyIsRefusedOnceAndItsBogonStillAnswered(t *testing.T) {
	for _, api := range keylessAPIs {
		t.Run(api.name, func(t *testing.T) {
			client, rec := lookupClient(t, api.base, "")

			got, err := client.LookupBatch(t.Context(), []string{probe, other, "10.0.0.1"})
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}

			if bogon := got["10.0.0.1"]; bogon.Err != nil || !bogon.Result.IsBogon {
				t.Errorf("10.0.0.1 was not answered locally: %+v", bogon)
			}
			for _, ip := range []string{probe, other} {
				var refused *internetdata.Error
				if !errors.As(got[ip].Err, &refused) {
					t.Fatalf("%s: a keyless batch answered %+v, want an unauthorized error", ip, got[ip])
				}
				if refused.Kind != internetdata.KindUnauthorized || refused.StatusCode != http.StatusUnauthorized ||
					refused.Retryable() {
					t.Errorf("%s: refused as %s %d, want unauthorized 401, not retried", ip, refused.Kind, refused.StatusCode)
				}
			}
			assertAskedOnce(t, rec, "/batch")
		})
	}
}

func TestABogonIsAnsweredWithoutTouchingTheNetwork(t *testing.T) {
	client, err := internetdata.New(internetdata.WithHTTPClient(&http.Client{Transport: refusing{}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r, err := client.Lookup(t.Context(), "10.0.0.1")
	if err != nil {
		t.Fatalf("Lookup(10.0.0.1): %v", err)
	}

	if !r.IsBogon || !internetdata.IsBogon("10.0.0.1") {
		t.Error("a private address must be answered locally, and the standalone function must agree")
	}
	// Computed rather than served, so it carries every flag whatever the plan.
	answer := reflect.ValueOf(r.LookupResponse)
	for i := range answer.NumField() {
		name, _, _ := strings.Cut(answer.Type().Field(i).Tag.Get("json"), ",")
		if !strings.HasPrefix(name, "is_") || name == "is_bogon" {
			continue
		}
		flag := reflect.Indirect(answer.Field(i))
		if !flag.IsValid() || flag.Bool() {
			t.Errorf("%s must be present and false on a bogon", name)
		}
	}
}

// The staging key is on the free plan: its organization's first 100 lookups a
// UTC day answer every member, and the rest Unlimited's, each saying which.
func TestAFreePlanKeysAnswerIsAPaidOneOrUnlimitedsAndSaysWhich(t *testing.T) {
	client, rec := keyedClient(t)

	r, err := client.Lookup(t.Context(), probe)
	if err != nil {
		t.Fatalf("Lookup(%s): %v", probe, err)
	}
	assertCarriedKey(t, rec)

	raw := rec.jsonBody(t, "/"+probe)
	expired, ok := raw["trial_expired"].(bool)
	if !ok || r.TrialExpired == nil || *r.TrialExpired != expired {
		t.Fatalf("trial_expired is %v on the wire and %v on the result, want the same boolean",
			raw["trial_expired"], r.TrialExpired)
	}
	if hosting := r.IsHosting != nil; hosting == expired {
		t.Errorf("is_hosting served: %v, trial_expired: %v; one must be the other's opposite", hosting, expired)
	}
	assertServedAsSent(t, r.LookupResponse, raw)
}

func TestAFreePlanKeysBatchOfTwoIsRefusedAsABadRequestOnce(t *testing.T) {
	client, rec := keyedClient(t)

	got, err := client.LookupBatch(t.Context(), []string{probe, other})
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	assertCarriedKey(t, rec)

	for _, ip := range []string{probe, other} {
		var refused *internetdata.Error
		if !errors.As(got[ip].Err, &refused) {
			t.Fatalf("%s: a free-plan key's batch of two answered %+v, want a refusal", ip, got[ip])
		}
		if refused.Kind != internetdata.KindBadRequest || refused.StatusCode != http.StatusPaymentRequired {
			t.Errorf("%s: refused as %s %d, want bad_request 402", ip, refused.Kind, refused.StatusCode)
		}
	}
	assertAskedOnce(t, rec, "/batch")
}

func TestAFreePlanKeysBatchOfOneIsAnswered(t *testing.T) {
	client, rec := keyedClient(t)

	got, err := client.LookupBatch(t.Context(), []string{probe})
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	assertCarriedKey(t, rec)

	if got[probe].Err != nil {
		t.Fatalf("a free-plan key's batch of one failed: %v", got[probe].Err)
	}
	if got[probe].Result.TrialExpired == nil {
		t.Error("a free-plan key's answer carries no trial_expired")
	}
}

func TestMyEntitlementNamesTheKeysPlanAndItsUsage(t *testing.T) {
	client, rec := keyedClient(t)

	acct, err := client.MyEntitlement(t.Context())
	if err != nil {
		t.Fatalf("MyEntitlement: %v", err)
	}
	assertCarriedKey(t, rec)

	if acct.Plan.Key == "" || acct.Usage.Quota <= 0 || !acct.Usage.WindowEnd.After(acct.Usage.WindowStart) {
		t.Errorf("plan %q, quota %d, window %s to %s: want a plan, an allowance and a window",
			acct.Plan.Key, acct.Usage.Quota, acct.Usage.WindowStart, acct.Usage.WindowEnd)
	}
}

// A client on the lookup host with a recorder of its own, keyless when key is
// empty and on prod when base is.
func lookupClient(t *testing.T, base, key string) (*internetdata.Client, *recorder) {
	t.Helper()
	rec := &recorder{key: key, bodies: map[string]json.RawMessage{}}
	opts := []internetdata.Option{internetdata.WithHTTPClient(&http.Client{Transport: rec})}
	if base != "" {
		opts = append(opts, internetdata.WithBaseURL(base))
	}
	if key != "" {
		opts = append(opts, internetdata.WithAPIKey(key))
	}
	client, err := internetdata.New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client, rec
}

// The staging key's client, on the host the lookups answer at rather than
// the apex the database suite uses, and skipped without the key.
func keyedClient(t *testing.T) (*internetdata.Client, *recorder) {
	t.Helper()
	if reason := skipReason(); reason != "" {
		t.Skip(reason)
	}
	return lookupClient(t, apiStaging, stagingKey())
}

// Asserted before anything keyed is trusted: an unsent key answers the keyless
// shape, and an empty fact list would satisfy any check over it.
func assertCarriedKey(t *testing.T, rec *recorder) {
	t.Helper()
	facts := rec.seen()
	if len(facts) == 0 {
		t.Fatal("no request was recorded, so nothing shows the key was sent")
	}
	for _, f := range facts {
		if !f.carriedKey {
			t.Fatalf("a request to %s went without the staging key", f.path)
		}
	}
}

func assertAskedOnce(t *testing.T, rec *recorder, path string) {
	t.Helper()
	var paths []string
	for _, f := range rec.seen() {
		paths = append(paths, f.path)
	}
	if !slices.Equal(paths, []string{path}) {
		t.Errorf("asked for %v, want one %s, never retried", paths, path)
	}
}

// A member the wire carried reached the result, and one it did not is absent
// from it, never false or zero: at the top level, and inside each object
// served with members.
func assertServedAsSent(t *testing.T, answer any, raw map[string]any) {
	t.Helper()
	value := reflect.ValueOf(answer)
	for i := range value.NumField() {
		name, _, _ := strings.Cut(value.Type().Field(i).Tag.Get("json"), ",")
		member := value.Field(i)
		served, onTheWire := raw[name]
		if member.Kind() == reflect.Pointer && member.IsNil() {
			if onTheWire {
				t.Errorf("%s was served and the client dropped it", name)
			}
			continue
		}
		if !onTheWire {
			t.Errorf("%s was not served, so the result must not read as %v", name, reflect.Indirect(member))
			continue
		}
		// `{}` is a detail object present and empty, under a false flag.
		inner, ok := served.(map[string]any)
		if ok && len(inner) > 0 && reflect.Indirect(member).Kind() == reflect.Struct {
			assertServedAsSent(t, reflect.Indirect(member).Interface(), inner)
		}
	}
}

func (rec *recorder) jsonBody(t *testing.T, path string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.rawBody(t, path), &body); err != nil {
		t.Fatalf("the answer for %s is not a JSON object: %v", path, err)
	}
	return body
}

type refusing struct{}

func (refusing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("the bogon path reached the network")
}
