// Pins AllowBots from a public peer: the request's own user agent reaches the
// origin, a verified crawler passes a condition it otherwise meets, a spoofed
// or not_bot one does not, and off sends no agent.

package middleware_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/internetdata/sdk-go/v2/middleware"
)

const googlebot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

// verdictOrigin answers every address as a VPN, with the verdict its table
// holds for the agent sent, and none when the table is nil. Its answer is the
// corpus's paid one, since a served answer needs every member the spec requires.
type verdictOrigin struct {
	verdicts map[string]string
	body     map[string]any
	mu       sync.Mutex
	agents   []string
	sent     []bool
}

func (o *verdictOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent, sent := r.URL.Query()["user_agent"]
	o.mu.Lock()
	o.sent = append(o.sent, sent)
	o.agents = append(o.agents, strings.Join(agent, ","))
	o.mu.Unlock()
	answer := maps.Clone(o.body)
	answer["ip"] = strings.TrimPrefix(r.URL.Path, "/")
	if verdict, ok := o.verdicts[strings.Join(agent, ",")]; ok && sent {
		answer["user_agent"] = map[string]any{"agent": "Googlebot", "kind": "bot", "verdict": verdict}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

type warnings struct {
	mu       sync.Mutex
	messages []string
}

func (w *warnings) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnings) WithAttrs([]slog.Attr) slog.Handler       { return w }
func (w *warnings) WithGroup(string) slog.Handler            { return w }
func (w *warnings) Handle(_ context.Context, record slog.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, record.Message)
	return nil
}

func botCore(t *testing.T, origin *verdictOrigin, allow bool, condition middleware.Conditions,
	logs *warnings) *middleware.Core[string] {
	t.Helper()
	origin.body = lookupBody(t, "paid-full-shape")
	server := httptest.NewServer(origin)
	t.Cleanup(server.Close)
	options := middleware.Options[string]{
		APIKey: "key", BaseURL: server.URL, AllowBots: allow, BlockCondition: condition,
	}
	if logs != nil {
		options.Logger = slog.New(logs)
	}
	core, err := middleware.New(options, selectorOf(publicIP))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return core
}

var vpnCondition = middleware.Conditions{{"is_vpn": true}}

func TestAllowBotsSendsTheRequestsOwnUserAgent(t *testing.T) {
	origin := &verdictOrigin{verdicts: map[string]string{googlebot: "verified"}}
	core := botCore(t, origin, true, nil, nil)
	lookup, err := core.EvaluateAgent(context.Background(), "request", googlebot)
	if err != nil {
		t.Fatalf("EvaluateAgent: %v", err)
	}
	if len(origin.agents) != 1 || origin.agents[0] != googlebot {
		t.Errorf("the origin saw user_agent %q, want the request's own", origin.agents)
	}
	if lookup.Result == nil || lookup.Result.UserAgent == nil {
		t.Errorf("the attached answer lost its verdict: %+v", lookup)
	}
}

func TestAllowBotsSendsNoAgentForAnEmptyHeader(t *testing.T) {
	origin := &verdictOrigin{}
	core := botCore(t, origin, true, nil, nil)
	if _, err := core.EvaluateAgent(context.Background(), "request", ""); err != nil {
		t.Fatalf("EvaluateAgent: %v", err)
	}
	if _, err := core.Evaluate(context.Background(), "request"); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for i, sent := range origin.sent {
		if sent {
			t.Errorf("request %d sent user_agent %q for no header", i, origin.agents[i])
		}
	}
}

func TestAVerifiedCrawlerPassesAConditionItOtherwiseMeets(t *testing.T) {
	cases := []struct {
		verdict string
		blocked bool
	}{
		{"verified", false},
		{"spoofed", true},
		{"not_bot", true},
		{"unverifiable", true},
	}
	for _, tc := range cases {
		t.Run(tc.verdict, func(t *testing.T) {
			origin := &verdictOrigin{verdicts: map[string]string{googlebot: tc.verdict}}
			core := botCore(t, origin, true, vpnCondition, nil)
			lookup, err := core.EvaluateAgent(context.Background(), "request", googlebot)
			if err != nil {
				t.Fatalf("EvaluateAgent: %v", err)
			}
			if lookup.Blocked != tc.blocked {
				t.Errorf("a %s crawler on a VPN: Blocked %v, want %v", tc.verdict, lookup.Blocked, tc.blocked)
			}
			if lookup.Result == nil || lookup.Result.UserAgent == nil ||
				string(lookup.Result.UserAgent.Verdict) != tc.verdict {
				t.Errorf("the attached answer is %+v, want its %s verdict", lookup.Result, tc.verdict)
			}
		})
	}
}

func TestAllowBotsOffSendsNoAgentAndLetsNoCrawlerThrough(t *testing.T) {
	origin := &verdictOrigin{verdicts: map[string]string{googlebot: "verified"}}
	core := botCore(t, origin, false, vpnCondition, nil)
	lookup, err := core.EvaluateAgent(context.Background(), "request", googlebot)
	if err != nil {
		t.Fatalf("EvaluateAgent: %v", err)
	}
	if len(origin.sent) != 1 || origin.sent[0] {
		t.Errorf("AllowBots off sent user_agent %q", origin.agents)
	}
	if !lookup.Blocked {
		t.Error("AllowBots off let a VPN through")
	}
}

func TestAConditionNamingUserAgentMatchesWithAllowBots(t *testing.T) {
	forged := middleware.Conditions{{"user_agent": middleware.BlockCondition{"verdict": "spoofed"}}}
	for verdict, blocked := range map[string]bool{"spoofed": true, "verified": false, "not_bot": false} {
		origin := &verdictOrigin{verdicts: map[string]string{googlebot: verdict}}
		core := botCore(t, origin, true, forged, nil)
		lookup, err := core.EvaluateAgent(context.Background(), "request", googlebot)
		if err != nil {
			t.Fatalf("EvaluateAgent: %v", err)
		}
		if lookup.Blocked != blocked {
			t.Errorf("{user_agent: {verdict: spoofed}} on a %s answer: Blocked %v, want %v",
				verdict, lookup.Blocked, blocked)
		}
	}
}

// A keyless client is never answered a verdict, so AllowBots lets nothing
// through, and says so once rather than on every request.
func TestAnAnswerWithoutAVerdictWarnsOnce(t *testing.T) {
	origin := &verdictOrigin{}
	logs := &warnings{}
	core := botCore(t, origin, true, vpnCondition, logs)
	for range 3 {
		lookup, err := core.EvaluateAgent(context.Background(), "request", googlebot)
		if err != nil {
			t.Fatalf("EvaluateAgent: %v", err)
		}
		if !lookup.Blocked {
			t.Error("an answer without a verdict let a VPN through")
		}
	}
	var about []string
	for _, message := range logs.messages {
		if strings.Contains(message, "user_agent") {
			about = append(about, message)
		}
	}
	if len(about) != 1 {
		t.Errorf("warned %d times about the missing verdict, want once: %q", len(about), logs.messages)
	}

	quiet := &warnings{}
	off := botCore(t, &verdictOrigin{}, false, vpnCondition, quiet)
	if _, err := off.EvaluateAgent(context.Background(), "request", googlebot); err != nil {
		t.Fatalf("EvaluateAgent: %v", err)
	}
	if len(quiet.messages) != 0 {
		t.Errorf("AllowBots off warned %q", quiet.messages)
	}
}
