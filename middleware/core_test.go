// Pins the client New builds when the caller passes none, which is how every
// framework README sets the middleware up.

package middleware_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	internetdata "github.com/internetdata/sdk-go/v2"
	"github.com/internetdata/sdk-go/v2/middleware"
)

// Outside every bogon block, RFC 5737's documentation ranges included, so the
// lookup cannot be answered locally and has to reach the origin.
const publicIP = "9.9.9.9"

func selectorOf(ip string) middleware.IPSelector[string] {
	return func(string) string { return ip }
}

func TestNewStartsWithOnlyAnAPIKey(t *testing.T) {
	if _, err := middleware.New(middleware.Options[string]{APIKey: "key"}, selectorOf(publicIP)); err != nil {
		t.Fatalf("New with only an APIKey: %v", err)
	}
}

func TestNewStartsWithNoOptions(t *testing.T) {
	if _, err := middleware.New(middleware.Options[string]{}, selectorOf(publicIP)); err != nil {
		t.Fatalf("New with no options: %v", err)
	}
}

// Refused where it is set: every lookup would refuse it, and the middleware
// fail open on every request.
func TestNewRefusesANegativeRetryCount(t *testing.T) {
	retries := -1
	if _, err := middleware.New(middleware.Options[string]{Retries: &retries}, selectorOf(publicIP)); err == nil {
		t.Fatal("New took Retries of -1")
	}
	retries = 0
	if _, err := middleware.New(middleware.Options[string]{Retries: &retries}, selectorOf(publicIP)); err != nil {
		t.Fatalf("New with Retries of 0: %v", err)
	}
}

func TestNewSendsToTheBaseURL(t *testing.T) {
	var requests atomic.Int32
	origin := serving(t, unlimitedAnswer(t), &requests)
	core, err := middleware.New(middleware.Options[string]{APIKey: "key", BaseURL: origin.URL}, selectorOf(publicIP))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lookup, err := core.Evaluate(t.Context(), "request")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if lookup.Err != nil {
		t.Fatalf("lookup failed: %v", lookup.Err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the origin got %d requests, want 1", got)
	}
}

func TestNewRefusesABaseURLWithNoScheme(t *testing.T) {
	if _, err := middleware.New(middleware.Options[string]{BaseURL: "api.internetdata.io"}, selectorOf(publicIP)); err == nil {
		t.Error("New accepted a base URL with no scheme")
	}
}

// A bare mount attaches the record, and a country condition refuses on
// Unlimited's answer, which every keyless lookup gets.
func TestACountryConditionBlocksOnUnlimitedsAnswerAndAnotherCountryPasses(t *testing.T) {
	origin := serving(t, unlimitedAnswer(t), nil)
	for country, blocked := range map[string]bool{"AT": true, "DE": false} {
		core, err := middleware.New(middleware.Options[string]{
			BaseURL:        origin.URL,
			BlockCondition: middleware.Conditions{{"location": middleware.BlockCondition{"country": country}}},
		}, selectorOf(publicIP))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		lookup, err := core.Evaluate(t.Context(), "request")
		if err != nil || lookup.Err != nil {
			t.Fatalf("Evaluate: %v, %v", err, lookup.Err)
		}
		if lookup.Result.Location.Country != "AT" || lookup.Result.Asn.Asn != 9009 {
			t.Errorf("attached %+v, want Unlimited's answer about AS9009 in Austria", lookup.Result.Location)
		}
		if lookup.Blocked != blocked {
			t.Errorf("country %s: Blocked = %v, want %v", country, lookup.Blocked, blocked)
		}
	}
}

// location is on every plan and its city is not, so a city condition on
// Unlimited can never fire: it is reported by its path, not passed over because
// location itself was served.
func TestAPaidMemberInsideAnObjectEveryPlanGetsIsWarnedAboutByItsPath(t *testing.T) {
	origin := serving(t, unlimitedAnswer(t), nil)
	var warnings strings.Builder
	core, err := middleware.New(middleware.Options[string]{
		BaseURL:        origin.URL,
		BlockCondition: middleware.Conditions{{"location": middleware.BlockCondition{"city": "Vienna"}}},
		Logger:         slog.New(slog.NewTextHandler(&warnings, nil)),
	}, selectorOf(publicIP))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lookup, err := core.Evaluate(t.Context(), "request")
	if err != nil || lookup.Err != nil {
		t.Fatalf("Evaluate: %v, %v", err, lookup.Err)
	}
	if lookup.Blocked {
		t.Error("a member the answer does not carry blocked the request")
	}
	got := warnings.String()
	if strings.Count(got, "does not include") != 1 || !strings.Contains(got, "location.city") {
		t.Errorf("warned %q, want one warning naming location.city", got)
	}
}

// is_bot is a paid plan's: a bot condition blocks a bot's address on one, and
// on Unlimited, whose answer carries no is_bot, it is reported once and blocks
// nothing, though the attached IsBot reads false there.
func TestAnIsBotConditionBlocksABotsAddressAndIsReportedOnUnlimited(t *testing.T) {
	for name, blocked := range map[string]bool{"paid-bot": true, "unlimited-answer": false} {
		origin := serving(t, lookupBody(t, name), nil)
		var warnings strings.Builder
		core, err := middleware.New(middleware.Options[string]{
			BaseURL:        origin.URL,
			BlockCondition: middleware.Conditions{{"is_bot": true}},
			Logger:         slog.New(slog.NewTextHandler(&warnings, nil)),
		}, selectorOf(publicIP))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		lookup, err := core.Evaluate(t.Context(), "request")
		if err != nil || lookup.Err != nil {
			t.Fatalf("%s: Evaluate: %v, %v", name, err, lookup.Err)
		}
		if lookup.Result.IsBot != blocked {
			t.Errorf("%s: attached IsBot = %v, want %v", name, lookup.Result.IsBot, blocked)
		}
		if lookup.Blocked != blocked {
			t.Errorf("%s: Blocked = %v, want %v", name, lookup.Blocked, blocked)
		}
		if warned := strings.Contains(warnings.String(), "is_bot"); warned == blocked {
			t.Errorf("%s: warned = %v (%q), want a warning naming is_bot on Unlimited alone", name, warned, warnings.String())
		}
	}
}

// A Result built by hand was served in full, so each of the eight reads as the
// value it holds, and a bot condition decides on it.
func TestAResultBuiltByHandHasEveryMemberServed(t *testing.T) {
	result := &internetdata.Result{LookupResponse: internetdata.LookupResponse{IsBot: true}}
	conditions := middleware.Conditions{{"is_bot": true}}
	if !middleware.Matches(conditions, result) {
		t.Error("a hand-built bot did not match an is_bot condition")
	}
	if missing := middleware.MissingMembers(conditions, result); len(missing) != 0 {
		t.Errorf("MissingMembers = %v, want none", missing)
	}
}

// The AS's type is a paid plan's too, and is reported by its path.
func TestAnAsnTypeTheAnswerDoesNotCarryIsReportedByItsPath(t *testing.T) {
	body := lookupBody(t, "unlimited-answer")
	asn := map[string]any{}
	for key, value := range body["asn"].(map[string]any) {
		if key != "type" {
			asn[key] = value
		}
	}
	body["asn"] = asn
	origin := serving(t, body, nil)
	core, err := middleware.New(middleware.Options[string]{
		BaseURL:        origin.URL,
		BlockCondition: middleware.Conditions{{"asn": middleware.BlockCondition{"type": middleware.AnyOf("hosting", "")}}},
		OnMissingField: middleware.MissingFieldError,
	}, selectorOf(publicIP))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := core.Evaluate(t.Context(), "request"); err == nil || !strings.Contains(err.Error(), "asn.type") {
		t.Errorf("Evaluate = %v, want an error naming asn.type", err)
	}
	client, err := internetdata.New(internetdata.WithBaseURL(origin.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := client.Lookup(t.Context(), publicIP)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if middleware.Matches(middleware.Conditions{{"asn": middleware.BlockCondition{"type": middleware.AnyOf("hosting", "")}}}, result) {
		t.Error("an AS type the answer does not carry matched its empty value")
	}
}

// The client refuses what is not an address before sending it, so a header that
// carried something else is a failed lookup like any other: let through.
func TestASelectorAnsweringSomethingThatIsNotAnAddressFailsOpen(t *testing.T) {
	var requests atomic.Int32
	origin := serving(t, unlimitedAnswer(t), &requests)
	core, err := middleware.New(middleware.Options[string]{
		BaseURL:        origin.URL,
		BlockCondition: middleware.Conditions{{"is_vpn": true}},
	}, selectorOf("example.com"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lookup, err := core.Evaluate(t.Context(), "request")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if lookup.Blocked || lookup.Err == nil {
		t.Errorf("Blocked = %v, Err = %v; want a failed lookup let through", lookup.Blocked, lookup.Err)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("the origin got %d requests, want none", got)
	}
}

// An origin answering every lookup with body about the address it was asked,
// counting the requests when given somewhere to.
func serving(t *testing.T, body map[string]any, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		answer := map[string]any{}
		for key, value := range body {
			answer[key] = value
		}
		answer["ip"] = strings.TrimPrefix(r.URL.Path, "/")
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(answer)
		_, _ = io.WriteString(w, string(raw))
	}))
	t.Cleanup(origin.Close)
	return origin
}

// The corpus's answer on Unlimited, the free plan: a served answer needs every
// member the spec requires, so {"ip": ...} alone is no answer here.
func unlimitedAnswer(t *testing.T) map[string]any {
	t.Helper()
	return lookupBody(t, "unlimited-answer")
}

// The body of the corpus's lookup case of that name.
func lookupBody(t *testing.T, name string) map[string]any {
	t.Helper()
	var data struct {
		Lookup []struct {
			Name string         `json:"name"`
			Body map[string]any `json:"body"`
		} `json:"lookup"`
	}
	if err := json.Unmarshal(readCorpus(t), &data); err != nil {
		t.Fatalf("parsing the corpus: %v", err)
	}
	for _, c := range data.Lookup {
		if c.Name == name {
			return c.Body
		}
	}
	t.Fatalf("no %s in the corpus's lookup cases", name)
	return nil
}
