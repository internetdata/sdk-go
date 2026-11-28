// Pins AllowBots from a public peer: a crawler's own user agent reaches the
// origin, at most four an address, and a verified crawler passes a condition it
// otherwise meets, a spoofed or not_bot one does not; any other agent and off
// send none.

package middleware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	internetdata "github.com/internetdata/sdk-go/v2"
	"github.com/internetdata/sdk-go/v2/middleware"
)

const (
	googlebot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	chrome    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/151.0.0.0 Safari/537.36"
	// A public peer, and another: a loopback or documentation address is a
	// bogon, answered locally with no request.
	crawlerPeer      = "66.249.66.1"
	otherCrawlerPeer = "66.249.66.2"
)

// verdictOrigin answers every address as a VPN, with the verdict its table
// holds for the agent sent (or anyVerdict for any agent), and none when
// neither names one. Its answer is the corpus's paid one, since a served answer
// needs every member the spec requires, with is_bot as isBot says and the bot
// member as bot, none when nil.
type verdictOrigin struct {
	verdicts   map[string]string
	anyVerdict string
	isBot      bool
	bot        map[string]any
	body       map[string]any
	mu         sync.Mutex
	agents     []string
	sent       []bool
}

func (o *verdictOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent, sent := r.URL.Query()["user_agent"]
	o.mu.Lock()
	o.sent = append(o.sent, sent)
	o.agents = append(o.agents, strings.Join(agent, ","))
	o.mu.Unlock()
	answer := maps.Clone(o.body)
	answer["ip"] = strings.TrimPrefix(r.URL.Path, "/")
	answer["is_bot"] = o.isBot
	if o.bot != nil {
		answer["bot"] = o.bot
	} else {
		delete(answer, "bot")
	}
	verdict, ok := o.verdicts[strings.Join(agent, ",")]
	if o.anyVerdict != "" {
		verdict, ok = o.anyVerdict, true
	}
	if ok && sent {
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
	options := middleware.Options[string]{APIKey: "key", BaseURL: serve(t, origin), AllowBots: allow}
	return newCore(t, options, condition, logs, selectorOf(publicIP))
}

// peerCore reads the request, an address, as the client's own, and caches no
// answer: a lookup that is sent again reaches the origin again.
func peerCore(t *testing.T, origin *verdictOrigin, condition middleware.Conditions,
	logs *warnings) *middleware.Core[string] {
	t.Helper()
	return newCore(t, peerOptions(t, origin), condition, logs, func(address string) string { return address })
}

func peerOptions(t *testing.T, origin *verdictOrigin) middleware.Options[string] {
	t.Helper()
	client, err := internetdata.New(internetdata.WithAPIKey("key"), internetdata.WithBaseURL(serve(t, origin)),
		internetdata.WithoutCache())
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	return middleware.Options[string]{Client: client, AllowBots: true}
}

func serve(t *testing.T, origin *verdictOrigin) string {
	t.Helper()
	origin.body = lookupBody(t, "paid-full-shape")
	server := httptest.NewServer(origin)
	t.Cleanup(server.Close)
	return server.URL
}

func newCore(t *testing.T, options middleware.Options[string], condition middleware.Conditions,
	logs *warnings, selector middleware.IPSelector[string]) *middleware.Core[string] {
	t.Helper()
	options.BlockCondition = condition
	if logs != nil {
		options.Logger = slog.New(logs)
	}
	core, err := middleware.New(options, selector)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return core
}

var (
	vpnCondition = middleware.Conditions{{"is_vpn": true}}
	botCondition = middleware.Conditions{{"is_bot": true}}
)

func crawlerAgent(n int) string {
	return fmt.Sprintf("Mozilla/5.0 (compatible; Examplebot/%d.0)", n)
}

func evaluate(t *testing.T, core *middleware.Core[string], peer, agent string) *middleware.Lookup {
	t.Helper()
	lookup, err := core.EvaluateAgent(context.Background(), peer, agent)
	if err != nil {
		t.Fatalf("EvaluateAgent: %v", err)
	}
	return lookup
}

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

func TestAllowBotsSendsOnlyAnAgentNamingACrawler(t *testing.T) {
	agents := corpus(t).CrawlerAgents
	if len(agents.Crawlers) == 0 || len(agents.Others) == 0 || len(agents.Terms) == 0 {
		t.Fatalf("the corpus holds no crawler agents: %+v", agents)
	}
	for _, agent := range agents.Crawlers {
		if !middleware.NamesCrawler(agent) {
			t.Errorf("NamesCrawler(%q) = false, the corpus says it names a crawler", agent)
		}
	}
	for _, agent := range agents.Others {
		if middleware.NamesCrawler(agent) {
			t.Errorf("NamesCrawler(%q) = true, the corpus says it names none", agent)
		}
	}
	for _, term := range agents.Terms {
		if inside := "x " + strings.ToUpper(term) + " y"; !middleware.NamesCrawler(inside) {
			t.Errorf("NamesCrawler(%q) = false, want the upper-cased term %q found", inside, term)
		}
	}
	if middleware.NamesCrawler("") {
		t.Error("an empty agent names a crawler")
	}
	// Only A to Z fold: the Kelvin sign lowercases to a k in Unicode, not here.
	if middleware.NamesCrawler("\u212Aeenable-user") {
		t.Error("a Unicode lowercase made a crawler's name out of another")
	}

	origin := &verdictOrigin{anyVerdict: "not_bot", isBot: true}
	core := peerCore(t, origin, botCondition, nil)
	lookup := evaluate(t, core, crawlerPeer, chrome)
	if !slices.Equal(origin.sent, []bool{false}) {
		t.Errorf("a browser's agent reached the origin: %q", origin.agents)
	}
	if !lookup.Blocked {
		t.Error("a browser on a bot's address passed")
	}
}

func TestOneAddressIsLookedUpWithAtMostFourCrawlerAgents(t *testing.T) {
	if middleware.MaxAgentsPerAddress != 4 {
		t.Fatalf("MaxAgentsPerAddress = %d, want 4", middleware.MaxAgentsPerAddress)
	}
	origin := &verdictOrigin{anyVerdict: "verified", isBot: true}
	core := peerCore(t, origin, botCondition, nil)
	for n := 1; n <= 4; n++ {
		if evaluate(t, core, crawlerPeer, crawlerAgent(n)).Blocked {
			t.Errorf("agent %d was refused", n)
		}
	}
	past := evaluate(t, core, crawlerPeer, crawlerAgent(5))
	if !past.Blocked {
		t.Error("an agent past the bound passed as verified")
	}
	if past.Result == nil || past.Result.UserAgent != nil {
		t.Errorf("an agent past the bound was answered a verdict: %+v", past.Result)
	}
	if evaluate(t, core, crawlerPeer, crawlerAgent(1)).Blocked {
		t.Error("a known agent was refused")
	}
	want := []string{crawlerAgent(1), crawlerAgent(2), crawlerAgent(3), crawlerAgent(4), "", crawlerAgent(1)}
	if !slices.Equal(origin.agents, want) {
		t.Errorf("the origin saw agents %q, want %q", origin.agents, want)
	}

	if evaluate(t, core, otherCrawlerPeer, crawlerAgent(5)).Blocked {
		t.Error("another address's first agents were refused: each address has its own bound")
	}
	if got := origin.agents[len(origin.agents)-1]; got != crawlerAgent(5) {
		t.Errorf("another address was looked up with %q, want %q", got, crawlerAgent(5))
	}
}

func TestPastTheBoundAnAddressItsOperatorVouchesForStillPasses(t *testing.T) {
	origin := &verdictOrigin{anyVerdict: "verified", isBot: true, bot: map[string]any{"verified": true}}
	core := peerCore(t, origin, botCondition, nil)
	for n := 1; n <= 5; n++ {
		if evaluate(t, core, crawlerPeer, crawlerAgent(n)).Blocked {
			t.Errorf("agent %d was refused", n)
		}
	}
	if got := origin.agents[len(origin.agents)-1]; got != "" {
		t.Errorf("the fifth agent reached the origin as %q, want none", got)
	}
	if !evaluate(t, core, crawlerPeer, chrome).Blocked {
		t.Error("a browser on that address passed, not judged by the condition alone")
	}
}

// The missing-verdict report is about an agent that was sent, and OnMissingField
// set to error makes each report an error to count: a request past the bound,
// or a browser's, sent none.
func TestTheMissingVerdictIsReportedOnlyForAnAgentThatWasSent(t *testing.T) {
	options := peerOptions(t, &verdictOrigin{isBot: true})
	options.OnMissingField = middleware.MissingFieldError
	core := newCore(t, options, botCondition, nil, func(address string) string { return address })
	for n := 1; n <= 4; n++ {
		if _, err := core.EvaluateAgent(context.Background(), crawlerPeer, crawlerAgent(n)); err == nil {
			t.Errorf("agent %d was sent and its answer carried no verdict, and nothing was reported", n)
		}
	}
	for _, agent := range []string{crawlerAgent(5), chrome, ""} {
		if _, err := core.EvaluateAgent(context.Background(), crawlerPeer, agent); err != nil {
			t.Errorf("%q sent no agent, and the missing verdict was reported: %v", agent, err)
		}
	}
}

// A request that sent no agent has no verdict to lack, so OnMissingField set to
// error fails no browser. Off, a condition on the verdict is still reported.
func TestAConditionOnTheVerdictIsNoPlanGapForARequestThatSentNoAgent(t *testing.T) {
	forged := middleware.Conditions{{"user_agent": middleware.BlockCondition{"verdict": "spoofed"}}}
	asPeer := func(address string) string { return address }
	for _, agent := range []string{chrome, ""} {
		origin := &verdictOrigin{anyVerdict: "spoofed"}
		logs := &warnings{}
		options := peerOptions(t, origin)
		options.OnMissingField = middleware.MissingFieldError
		lookup := evaluate(t, newCore(t, options, forged, logs, asPeer), crawlerPeer, agent)
		if lookup.Blocked {
			t.Errorf("%q was refused", agent)
		}
		if !slices.Equal(origin.sent, []bool{false}) {
			t.Errorf("%q reached the origin as %q, want no agent", agent, origin.agents)
		}
		if len(logs.messages) != 0 {
			t.Errorf("%q warned %q", agent, logs.messages)
		}
	}

	options := peerOptions(t, &verdictOrigin{anyVerdict: "spoofed"})
	options.OnMissingField = middleware.MissingFieldError
	core := newCore(t, options, forged, nil, asPeer)
	for n := 1; n <= 4; n++ {
		evaluate(t, core, crawlerPeer, crawlerAgent(n))
	}
	if evaluate(t, core, crawlerPeer, crawlerAgent(5)).Blocked {
		t.Error("past the bound, the agent was refused: no gap either")
	}

	logs := &warnings{}
	off := botCore(t, &verdictOrigin{}, false, forged, logs)
	for range 2 {
		evaluate(t, off, "request", chrome)
	}
	if len(logs.messages) != 1 || !strings.Contains(logs.messages[0], "user_agent") {
		t.Errorf("off, a condition on the verdict can never match, and says so once: %q", logs.messages)
	}
}
