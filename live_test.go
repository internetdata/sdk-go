// A smoke test against the real API, skipped unless INTERNETDATA_LIVE=1 so a
// normal `go test` stays offline and costs no quota. Set INTERNETDATA_API_KEY
// to exercise a key's fields; without one it runs without a key.
//
//	INTERNETDATA_LIVE=1 go test -run TestLive -v ./...

package internetdata

import (
	"os"
	"testing"
)

func TestLiveLookup(t *testing.T) {
	if os.Getenv("INTERNETDATA_LIVE") != "1" {
		t.Skip("set INTERNETDATA_LIVE=1 to query the real API")
	}
	var opts []Option
	if key := os.Getenv("INTERNETDATA_API_KEY"); key != "" {
		opts = append(opts, WithAPIKey(key))
	}
	client, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r, err := client.Lookup(t.Context(), "146.70.116.130")
	if err != nil {
		t.Fatalf("Lookup(146.70.116.130): %v", err)
	}
	t.Logf("146.70.116.130: IsVpn=%v IsHosting=%v Country=%s Asn=%d",
		r.IsVpn, r.IsHosting, r.Location.Country, r.Asn.Asn)
	if r.Location.Country == "" || r.Asn.Asn == 0 {
		t.Error("146.70.116.130 should carry its country and its AS on every plan")
	}
	if os.Getenv("INTERNETDATA_API_KEY") == "" && (r.IsVpn != nil || r.Location.City != nil) {
		t.Error("an answer without a key includes neither is_vpn nor the city, so both must be absent")
	}

	asn, err := client.LookupASN(t.Context(), 9009)
	if err != nil {
		t.Fatalf("LookupASN(9009): %v", err)
	}
	t.Logf("AS9009: %s, %s", asn.Name, asn.Country)

	domain, err := client.LookupDomain(t.Context(), "example.com")
	if err != nil {
		t.Fatalf("LookupDomain(example.com): %v", err)
	}
	if !domain.IsRegistered {
		t.Error("example.com should be registered")
	}
}
