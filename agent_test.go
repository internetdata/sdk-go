// Pins the user agent a lookup sends: as user_agent on a single lookup and
// MyIP, as user_agents on a batch keyed by each address as sent, and the cache
// and the shared requests keyed on the address and the agent together.

package internetdata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const googlebot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

// agentOrigin answers every lookup, a batch's included, and records what each
// request carried. An answer to an address sent with an agent carries a
// verdict naming that agent, so a test can tell whose answer it got.
type agentOrigin struct {
	delay   time.Duration
	mu      sync.Mutex
	queries []map[string][]string
	bodies  []map[string]json.RawMessage
}

func (o *agentOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if o.delay > 0 {
		time.Sleep(o.delay)
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && r.URL.Path == "/batch" {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(raw, &body)
		var request struct {
			Ips        []string          `json:"ips"`
			UserAgents map[string]string `json:"user_agents"`
		}
		_ = json.Unmarshal(raw, &request)
		o.mu.Lock()
		o.bodies = append(o.bodies, body)
		o.mu.Unlock()
		results := map[string]any{}
		for _, ip := range request.Ips {
			results[ip] = answerFor(ip, request.UserAgents[ip])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "errors": map[string]any{}})
		return
	}
	query := r.URL.Query()
	o.mu.Lock()
	o.queries = append(o.queries, query)
	o.mu.Unlock()
	ip := strings.TrimPrefix(r.URL.Path, "/")
	if ip == "myip" {
		ip = "9.9.9.9"
	}
	_ = json.NewEncoder(w).Encode(answerFor(ip, query.Get("user_agent")))
}

func answerFor(ip, agent string) map[string]any {
	var members map[string]any
	if agent != "" {
		members = map[string]any{"user_agent": map[string]any{"agent": agent, "kind": "bot", "verdict": "verified"}}
	}
	return answerAbout(ip, members)
}

func (o *agentOrigin) requests() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.queries) + len(o.bodies)
}

func agentClient(t *testing.T, origin *agentOrigin, opts ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(origin)
	t.Cleanup(server.Close)
	client, err := New(append([]Option{WithBaseURL(server.URL), WithAPIKey("key")}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestTheUserAgentReachesTheOriginAsTheQueryParameter(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin, WithoutCache())
	ctx := context.Background()
	long := strings.Repeat("a", 2000)

	result, err := client.Lookup(ctx, "9.9.9.9", UserAgent(googlebot))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.UserAgent == nil || result.UserAgent.Verdict != VerdictVerified || result.UserAgent.Agent != googlebot {
		t.Fatalf("the answer's UserAgent is %+v, want the verdict the origin answered", result.UserAgent)
	}
	if _, err := client.MyIP(ctx, UserAgent(googlebot)); err != nil {
		t.Fatalf("MyIP: %v", err)
	}
	if _, err := client.Lookup(ctx, "9.9.9.9", UserAgent(long)); err != nil {
		t.Fatalf("Lookup with a long agent: %v", err)
	}
	plain, err := client.Lookup(ctx, "9.9.9.9")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if plain.UserAgent != nil {
		t.Errorf("an answer to a lookup sent without an agent carries %+v", plain.UserAgent)
	}
	if _, err := client.Lookup(ctx, "9.9.9.9", UserAgent("")); err != nil {
		t.Fatalf("Lookup with an empty agent: %v", err)
	}
	if _, err := client.MyIP(ctx, UserAgent("")); err != nil {
		t.Fatalf("MyIP with an empty agent: %v", err)
	}

	want := []string{googlebot, googlebot, long, "", "", ""}
	if len(origin.queries) != len(want) {
		t.Fatalf("the origin saw %d requests, want %d", len(origin.queries), len(want))
	}
	for i, query := range origin.queries {
		got, sent := query["user_agent"]
		switch {
		case want[i] == "" && sent:
			t.Errorf("request %d sent user_agent %q, want none", i, got)
		case want[i] != "" && (len(got) != 1 || got[0] != want[i]):
			t.Errorf("request %d sent user_agent %q, want %.40q", i, got, want[i])
		}
	}
}

func TestABatchSendsUserAgentsKeyedByTheAddressAsSent(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin, WithoutCache())
	results, err := client.LookupBatch(context.Background(),
		[]string{"::ffff:8.8.8.8", "1.1.1.1", "9.9.9.9", "2001:4860:4801:0010:0000:0000:0000:0001"},
		UserAgents(map[string]string{"::ffff:8.8.8.8": googlebot, "1.1.1.1": "", "unasked": "x"}))
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if len(origin.bodies) != 1 {
		t.Fatalf("the origin saw %d batches, want 1", len(origin.bodies))
	}
	var agents map[string]string
	if err := json.Unmarshal(origin.bodies[0]["user_agents"], &agents); err != nil {
		t.Fatalf("user_agents %s: %v", origin.bodies[0]["user_agents"], err)
	}
	if len(agents) != 1 || agents["8.8.8.8"] != googlebot {
		t.Errorf("user_agents is %v, want only 8.8.8.8, as ips sends it", agents)
	}
	mapped := results["::ffff:8.8.8.8"].Result
	if mapped == nil || mapped.UserAgent == nil || mapped.UserAgent.Agent != googlebot {
		t.Errorf("the mapped address's answer is %+v, want its agent's verdict", results["::ffff:8.8.8.8"])
	}
	if plain := results["9.9.9.9"].Result; plain == nil || plain.UserAgent != nil {
		t.Errorf("an address sent without an agent answered %+v", results["9.9.9.9"])
	}
}

func TestABatchWithNoAgentSendsNoUserAgents(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin, WithoutCache())
	if _, err := client.LookupBatch(context.Background(), []string{"1.1.1.1", "9.9.9.9"}); err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if _, err := client.LookupBatch(context.Background(), []string{"1.1.1.1", "9.9.9.9"},
		UserAgents(map[string]string{"1.1.1.1": ""}), UserAgent("")); err != nil {
		t.Fatalf("LookupBatch with empty agents: %v", err)
	}
	for i, body := range origin.bodies {
		if raw, ok := body["user_agents"]; ok {
			t.Errorf("batch %d sent user_agents %s, want the key absent", i, raw)
		}
	}
}

// Each chunk carries only its own addresses' agents, and a chunk with none
// sends no user_agents at all.
func TestUserAgentsGoesOnlyWithAChunkWhoseAddressesCarryOne(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin, WithoutCache())
	ips := make([]string, batchMax+2)
	for i := range ips {
		ips[i] = "11." + strconv.Itoa(i/65536) + "." + strconv.Itoa(i/256%256) + "." + strconv.Itoa(i%256)
	}
	last := ips[len(ips)-1]
	if _, err := client.LookupBatch(context.Background(), ips, Concurrency(1),
		UserAgents(map[string]string{last: googlebot})); err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if len(origin.bodies) != 2 {
		t.Fatalf("the origin saw %d batches, want 2", len(origin.bodies))
	}
	if raw, ok := origin.bodies[0]["user_agents"]; ok {
		t.Errorf("the first chunk sent user_agents %s, though none of its addresses carries one", raw)
	}
	var agents map[string]string
	if err := json.Unmarshal(origin.bodies[1]["user_agents"], &agents); err != nil || len(agents) != 1 ||
		agents[last] != googlebot {
		t.Errorf("the second chunk sent user_agents %s, want only %s's", origin.bodies[1]["user_agents"], last)
	}
}

// UserAgent on a batch is every address's agent, and UserAgents names one
// apart.
func TestUserAgentOnABatchIsEveryAddresssAgent(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin, WithoutCache())
	if _, err := client.LookupBatch(context.Background(), []string{"1.1.1.1", "9.9.9.9"},
		UserAgent(googlebot), UserAgents(map[string]string{"9.9.9.9": "curl/8.0"})); err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	var agents map[string]string
	_ = json.Unmarshal(origin.bodies[0]["user_agents"], &agents)
	if len(agents) != 2 || agents["1.1.1.1"] != googlebot || agents["9.9.9.9"] != "curl/8.0" {
		t.Errorf("user_agents is %v", agents)
	}
}

// 8.8.8.8 and ::ffff:8.8.8.8 are one address on the wire, and a request keys
// user_agents by address, so two agents for it take two requests.
func TestOneAddressPassedTwiceWithTwoAgentsIsAskedForEach(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin)
	results, err := client.LookupBatch(context.Background(), []string{"8.8.8.8", "::ffff:8.8.8.8"},
		UserAgents(map[string]string{"8.8.8.8": googlebot, "::ffff:8.8.8.8": "curl/8.0"}))
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	for ip, agent := range map[string]string{"8.8.8.8": googlebot, "::ffff:8.8.8.8": "curl/8.0"} {
		got := results[ip].Result
		if got == nil || got.UserAgent == nil || got.UserAgent.Agent != agent {
			t.Errorf("%s answered %+v, want %s's verdict", ip, results[ip], agent)
		}
	}
	if len(origin.bodies) != 2 {
		t.Errorf("the origin saw %d batches, want 2", len(origin.bodies))
	}
}

func TestTheCacheHitsOnlyOnTheSameAddressAndAgent(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin)
	ctx := context.Background()
	steps := []struct {
		agent string
		sends int
	}{
		{"", 1},
		{googlebot, 2},
		{googlebot, 2},
		{"curl/8.0", 3},
		{"", 3},
		{"curl/8.0", 3},
	}
	for i, step := range steps {
		result, err := client.Lookup(ctx, "9.9.9.9", UserAgent(step.agent))
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := origin.requests(); got != step.sends {
			t.Errorf("step %d (agent %.10q): %d requests so far, want %d", i, step.agent, got, step.sends)
		}
		if step.agent == "" && result.UserAgent != nil || step.agent != "" &&
			(result.UserAgent == nil || result.UserAgent.Agent != step.agent) {
			t.Errorf("step %d answered UserAgent %+v, want the answer for %.10q", i, result.UserAgent, step.agent)
		}
	}
	// A batch entry is cached on the pair too, either way round.
	results, err := client.LookupBatch(ctx, []string{"9.9.9.9", "1.1.1.1"},
		UserAgents(map[string]string{"9.9.9.9": googlebot, "1.1.1.1": googlebot}))
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if origin.requests() != 4 || len(origin.bodies) != 1 {
		t.Fatalf("%d requests, %d batches; want the batch to send only 1.1.1.1", origin.requests(), len(origin.bodies))
	}
	var ips []string
	_ = json.Unmarshal(origin.bodies[0]["ips"], &ips)
	if len(ips) != 1 || ips[0] != "1.1.1.1" {
		t.Errorf("the batch sent %v, want only the pair the cache does not hold", ips)
	}
	if results["9.9.9.9"].Result.UserAgent == nil {
		t.Error("the batch's cached answer lost its verdict")
	}
	if _, err := client.Lookup(ctx, "1.1.1.1", UserAgent(googlebot)); err != nil || origin.requests() != 4 {
		t.Errorf("a lookup of the batch's pair sent a request: %d requests, %v", origin.requests(), err)
	}
	if _, err := client.Lookup(ctx, "1.1.1.1"); err != nil || origin.requests() != 5 {
		t.Errorf("the plain lookup of the batch's address was served from its agent's answer: %d requests, %v",
			origin.requests(), err)
	}
}

func TestConcurrentMissesShareARequestOnlyForTheSameAddressAndAgent(t *testing.T) {
	origin := &agentOrigin{delay: 150 * time.Millisecond}
	client := agentClient(t, origin)
	agents := []string{"", googlebot, "curl/8.0"}
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan string, callers*len(agents))
	for _, agent := range agents {
		for range callers {
			wg.Go(func() {
				result, err := client.Lookup(context.Background(), "9.9.9.9", UserAgent(agent))
				switch {
				case err != nil:
					errs <- err.Error()
				case agent == "" && result.UserAgent != nil:
					errs <- "a plain lookup took an agent's answer"
				case agent != "" && (result.UserAgent == nil || result.UserAgent.Agent != agent):
					errs <- "a lookup for " + agent + " took another's answer"
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for message := range errs {
		t.Error(message)
	}
	if got := origin.requests(); got != len(agents) {
		t.Errorf("%d concurrent callers over %d agents sent %d requests, want one per agent",
			callers*len(agents), len(agents), got)
	}
}

func TestABogonCarriesNoUserAgentAndSendsNothing(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin)
	result, err := client.Lookup(context.Background(), "10.0.0.1", UserAgent(googlebot))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if result.UserAgent != nil || !result.IsBogon {
		t.Errorf("the bogon answered %+v, want a bogon with no UserAgent", result)
	}
	results, err := client.LookupBatch(context.Background(), []string{"10.0.0.1", "192.168.0.1"},
		UserAgent(googlebot))
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	for ip, answer := range results {
		if answer.Result == nil || answer.Result.UserAgent != nil {
			t.Errorf("%s answered %+v, want no UserAgent", ip, answer)
		}
	}
	if got := origin.requests(); got != 0 {
		t.Errorf("bogons sent %d requests", got)
	}
}

func TestMyEntitlementRefusesAUserAgent(t *testing.T) {
	origin := &agentOrigin{}
	client := agentClient(t, origin)
	_, err := client.MyEntitlement(context.Background(), UserAgent(googlebot))
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Kind != KindBadRequest {
		t.Errorf("MyEntitlement with a user agent answered %v, want a bad_request", err)
	}
	if origin.requests() != 0 {
		t.Errorf("the refused call sent %d requests", origin.requests())
	}
}
