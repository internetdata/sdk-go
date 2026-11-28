// A spent trial's or a reached cap's answer can change as soon as the
// organization's plan or cap does, so it is cached for a minute at most rather
// than for the client's TTL.

package internetdata

import (
	"testing"
	"time"
)

var cacheLifeFlags = map[string]map[string]any{
	"45.83.91.1": nil,
	"45.83.91.2": {"trial_expired": false, "cap_reached": false},
	"45.83.91.3": {"trial_expired": true},
	"45.83.91.4": {"cap_reached": true},
}

func cacheLifeStub() *stubTransport {
	routes := make(map[string]stubRoute, len(cacheLifeFlags))
	for ip, flags := range cacheLifeFlags {
		routes[ip] = stubRoute{body: answerAbout(ip, flags)}
	}
	return newStub(routes)
}

func TestEachAnswerIsCachedAsLongAsItsFlagsAllow(t *testing.T) {
	cases := []struct {
		ip        string
		ttl, want time.Duration
	}{
		{"45.83.91.1", time.Hour, time.Hour},
		{"45.83.91.2", time.Hour, time.Hour},
		{"45.83.91.3", time.Hour, time.Minute},
		{"45.83.91.4", time.Hour, time.Minute},
		{"45.83.91.3", 5 * time.Second, 5 * time.Second},
		{"45.83.91.4", 5 * time.Second, 5 * time.Second},
		{"45.83.91.1", 5 * time.Second, 5 * time.Second},
	}
	for _, how := range []string{"Lookup", "LookupBatch"} {
		for _, tc := range cases {
			client := newTestClient(t, cacheLifeStub(), WithCache(100, tc.ttl))

			before := time.Now()
			var err error
			if how == "Lookup" {
				_, err = client.Lookup(t.Context(), tc.ip)
			} else {
				_, err = client.LookupBatch(t.Context(), []string{tc.ip, "45.83.91.1"})
			}
			after := time.Now()
			if err != nil {
				t.Fatalf("%s(%s): %v", how, tc.ip, err)
			}

			expires, ok := client.cache.expiry(answerKey{ip: tc.ip})
			if !ok {
				t.Fatalf("%s: %s under a %s TTL was not cached", how, tc.ip, tc.ttl)
			}
			if expires.Before(before.Add(tc.want)) || expires.After(after.Add(tc.want)) {
				t.Errorf("%s: %s under a %s TTL expires %s after the call, want %s",
					how, tc.ip, tc.ttl, expires.Sub(before), tc.want)
			}
		}
	}
}

func TestAReducedAnswerIsStillCachedSoARepeatSendsNothing(t *testing.T) {
	stub := cacheLifeStub()
	client := newTestClient(t, stub)

	if _, err := client.Lookup(t.Context(), "45.83.91.3"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	again, err := client.Lookup(t.Context(), "45.83.91.3")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if again.TrialExpired == nil || !*again.TrialExpired {
		t.Errorf("TrialExpired = %v, want true", again.TrialExpired)
	}
	if stub.count() != 1 {
		t.Errorf("issued %d request(s), want 1", stub.count())
	}
}

func TestAReducedAnswerPastItsLifeIsAskedForAgain(t *testing.T) {
	stub := cacheLifeStub()
	client := newTestClient(t, stub)

	if _, err := client.Lookup(t.Context(), "45.83.91.4"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	held, _ := client.cache.lru.Peek(answerKey{ip: "45.83.91.4"})
	held.expires = time.Now().Add(-time.Millisecond)
	client.cache.lru.Add(answerKey{ip: "45.83.91.4"}, held)
	if _, err := client.Lookup(t.Context(), "45.83.91.4"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if stub.count() != 2 {
		t.Errorf("issued %d request(s), want 2 - a stale answer was served", stub.count())
	}
}
