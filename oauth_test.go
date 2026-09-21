// The OAuth accessor against the shared corpus's oauth section. Nothing here
// reads oauth.deferred: those operations are not in this release.

package internetdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOauthRequestsCarryNoCredential(t *testing.T) {
	c := oauthCorpusData(t)
	key := c.NoCredential.APIKey
	stub := &oauthStub{replies: []oauthReply{{Status: 200, Body: json.RawMessage(everyRequiredMember)}}}
	client := newOauthClient(t, stub, WithAPIKey(key))
	client.Oauth.sleep = func(context.Context, time.Duration) error { return nil }
	ctx := t.Context()

	if _, err := client.Oauth.Metadata(ctx); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	device, err := client.Oauth.DeviceAuthorization(ctx, "internetdata-cli", DeviceAuthorizationOptions{Scope: "account.read"})
	if err != nil {
		t.Fatalf("DeviceAuthorization: %v", err)
	}
	if _, err := client.Oauth.ExchangeDeviceCode(ctx, "internetdata-cli", "mo_dc_x"); err != nil {
		t.Fatalf("ExchangeDeviceCode: %v", err)
	}
	if _, err := client.Oauth.ExchangeRefreshToken(ctx, "internetdata-cli", "mo_rt_x"); err != nil {
		t.Fatalf("ExchangeRefreshToken: %v", err)
	}
	if err := client.Oauth.Revoke(ctx, "internetdata-cli", "mo_rt_x"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := client.Oauth.PollDeviceToken(ctx, "internetdata-cli", device); err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}

	if len(stub.requests) != 6 {
		t.Fatalf("saw %d request(s), want 6", len(stub.requests))
	}
	for _, req := range stub.requests {
		for _, name := range c.NoCredential.ForbiddenHeaders {
			if req.header.Get(name) != "" {
				t.Errorf("%s %s carried %s", req.method, req.path, name)
			}
		}
		query, _ := url.ParseQuery(req.query)
		for _, name := range c.NoCredential.ForbiddenQuery {
			if query.Has(name) {
				t.Errorf("%s %s carried the %s query parameter", req.method, req.path, name)
			}
		}
		leaked := strings.Contains(req.url, key) || strings.Contains(req.body, key)
		for _, values := range req.header {
			leaked = leaked || strings.Contains(strings.Join(values, " "), key)
		}
		if leaked {
			t.Errorf("%s %s carried the API key", req.method, req.path)
		}
	}
}

// Keyless on purpose: nothing about these operations needs a key.
func TestOauthFormBodiesAndEndpoints(t *testing.T) {
	c := oauthCorpusData(t)
	metadataStub := &oauthStub{replies: []oauthReply{{Status: 200, Body: json.RawMessage(everyRequiredMember)}}}
	if _, err := newOauthClient(t, metadataStub).Oauth.Metadata(t.Context()); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	assertEndpoint(t, metadataStub.requests[0], c.Endpoints["metadata"])

	for _, fc := range c.Forms.Cases {
		t.Run(fc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: []oauthReply{{Status: 200, Body: json.RawMessage(everyRequiredMember)}}}
			if err := callOauth(t.Context(), newOauthClient(t, stub), fc.Operation, fc.Args); err != nil {
				t.Fatalf("%s: %v", fc.Operation, err)
			}
			req := stub.requests[0]
			assertEndpoint(t, req, c.Endpoints[fc.Endpoint])
			if !strings.HasPrefix(req.header.Get("Content-Type"), c.Forms.ContentType) {
				t.Errorf("Content-Type = %q, want %s", req.header.Get("Content-Type"), c.Forms.ContentType)
			}
			sent, err := url.ParseQuery(req.body)
			if err != nil {
				t.Fatalf("the body %q is not a form: %v", req.body, err)
			}
			got := map[string]string{}
			for name, values := range sent {
				if len(values) != 1 {
					t.Errorf("%s was sent %d times", name, len(values))
				}
				got[name] = values[0]
			}
			if !maps.Equal(got, fc.Fields) {
				t.Errorf("sent %v, want %v", got, fc.Fields)
			}
		})
	}
}

func TestOauthResponsesDecode(t *testing.T) {
	c := oauthCorpusData(t)
	for _, operation := range []string{"metadata", "deviceAuthorization", "token", "revoke"} {
		var cases []oauthResponseCase
		if err := json.Unmarshal(c.Responses[operation], &cases); err != nil {
			t.Fatalf("responses.%s: %v", operation, err)
		}
		for _, rc := range cases {
			t.Run(operation+"/"+rc.Name, func(t *testing.T) {
				stub := &oauthStub{replies: []oauthReply{rc.oauthReply}}
				client := newOauthClient(t, stub)
				var decoded any
				var err error
				switch operation {
				case "metadata":
					decoded, err = client.Oauth.Metadata(t.Context())
				case "deviceAuthorization":
					decoded, err = client.Oauth.DeviceAuthorization(t.Context(), "internetdata-cli", DeviceAuthorizationOptions{})
				case "token":
					decoded, err = client.Oauth.ExchangeDeviceCode(t.Context(), "internetdata-cli", "mo_dc_x")
				case "revoke":
					err = client.Oauth.Revoke(t.Context(), "internetdata-cli", "mo_rt_x")
				}
				if err != nil {
					t.Fatalf("%s: %v", operation, err)
				}
				if decoded == nil {
					return
				}
				members := surfacedMembers(t, decoded)
				for name, want := range rc.Expect.Present {
					got, ok := members[name]
					if !ok {
						t.Errorf("%s is absent, want %s", name, want)
						continue
					}
					if !jsonEqual(t, got, want) {
						t.Errorf("%s = %s, want %s", name, got, want)
					}
				}
				for _, name := range rc.Expect.Absent {
					if got, ok := members[name]; ok {
						t.Errorf("%s = %s, want it absent", name, got)
					}
				}
			})
		}
	}
}

// No corpus case: every response there decodes. One member left out per case,
// since a body missing two at once let a defaulting decoder survive elsewhere.
func TestOauthAnAnswerMissingARequiredMemberFails(t *testing.T) {
	required := map[string][]string{
		"metadata":            {"issuer", "authorization_endpoint", "token_endpoint"},
		"deviceAuthorization": {"device_code", "user_code", "verification_uri", "expires_in", "interval"},
		"exchangeDeviceCode":  {"access_token", "token_type", "expires_in"},
	}
	for operation, members := range required {
		for _, member := range members {
			t.Run(operation+"/"+member, func(t *testing.T) {
				var body map[string]json.RawMessage
				if err := json.Unmarshal([]byte(everyRequiredMember), &body); err != nil {
					t.Fatal(err)
				}
				delete(body, member)
				raw, _ := json.Marshal(body)
				stub := &oauthStub{replies: []oauthReply{{Status: 200, Body: raw}}}
				client := newOauthClient(t, stub, WithRetries(0))
				err := callOauth(t.Context(), client, operation,
					oauthArgs{ClientID: "internetdata-cli", DeviceCode: "mo_dc_x"})
				var apiErr *Error
				var refused *OauthError
				if !errors.As(err, &apiErr) || errors.As(err, &refused) {
					t.Fatalf("error was %v, want the ordinary *Error", err)
				}
				if apiErr.Kind != KindServerError || apiErr.StatusCode != 200 {
					t.Errorf("Kind %q, StatusCode %d, want server_error and 200", apiErr.Kind, apiErr.StatusCode)
				}
			})
		}
	}
}

func TestOauthErrorsAreClassified(t *testing.T) {
	for _, ec := range oauthCorpusData(t).Errors.Cases {
		t.Run(ec.Name, func(t *testing.T) {
			stub := &oauthStub{replies: []oauthReply{ec.oauthReply}}
			_, err := newOauthClient(t, stub).Oauth.ExchangeDeviceCode(t.Context(), "internetdata-cli", "mo_dc_x")
			if err == nil {
				t.Fatal("the exchange succeeded")
			}
			assertOauthOutcome(t, err, ec.Expect)
		})
	}
}

func TestOauthRetries(t *testing.T) {
	for _, rc := range oauthCorpusData(t).Retries.Cases {
		t.Run(rc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: rc.Responses}
			err := callOauth(t.Context(), newOauthClient(t, stub), rc.Operation, rc.Args)
			if len(stub.requests) != rc.Expect.Requests {
				t.Errorf("sent %d request(s), want %d", len(stub.requests), rc.Expect.Requests)
			}
			if rc.Expect.Outcome == "ok" {
				if err != nil {
					t.Errorf("%s: %v", rc.Operation, err)
				}
				return
			}
			want := rc.Expect.oauthExpect
			want.Type = rc.Expect.Outcome
			assertOauthOutcome(t, err, want)
		})
	}
}

// Waits are asserted exactly, through the seam that replaces the sleep AND the
// clock together, so the deadline reads the same time the waits spent.
func TestPollDeviceToken(t *testing.T) {
	for _, pc := range oauthCorpusData(t).Poll.Cases {
		t.Run(pc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: pc.Responses}
			client := newOauthClient(t, stub)
			var waits []float64
			start := time.Now()
			elapsed := time.Duration(0)
			reads := 0
			client.Oauth.now = func() time.Time {
				reads++
				if reads > oauthLoopBound {
					t.Fatalf("read the clock %d times: the poll does not end", reads)
				}
				return start.Add(elapsed)
			}
			client.Oauth.sleep = func(_ context.Context, d time.Duration) error {
				if len(waits) == oauthLoopBound {
					t.Fatalf("waited %d times: the poll does not end", len(waits))
				}
				waits = append(waits, d.Seconds())
				elapsed += d
				return nil
			}

			token, err := client.Oauth.PollDeviceToken(t.Context(), pc.ClientID, &pc.Device)

			if !reflect.DeepEqual(waits, pc.Expect.Waits) {
				t.Errorf("waited %v, want %v", waits, pc.Expect.Waits)
			}
			if len(stub.requests) != pc.Expect.Requests {
				t.Errorf("sent %d request(s), want %d", len(stub.requests), pc.Expect.Requests)
			}
			form := map[string]string{
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
				"device_code": pc.Device.DeviceCode,
				"client_id":   pc.ClientID,
			}
			for _, req := range stub.requests {
				sent, _ := url.ParseQuery(req.body)
				got := map[string]string{}
				for name := range sent {
					got[name] = sent.Get(name)
				}
				if req.path != "/oauth/token" || !maps.Equal(got, form) {
					t.Errorf("polled %s with %v, want /oauth/token with %v", req.path, got, form)
				}
			}
			if pc.Expect.Outcome == "token" {
				if err != nil {
					t.Fatalf("PollDeviceToken: %v", err)
				}
				if want := pc.Expect.Token["access_token"]; want != "" && token.AccessToken != want {
					t.Errorf("access_token = %q, want %q", token.AccessToken, want)
				}
				return
			}
			want := pc.Expect.oauthExpect
			want.Type = pc.Expect.Outcome
			assertOauthOutcome(t, err, want)
		})
	}
}

// No corpus case: a handle has to stop the wait before the first request.
func TestPollDeviceTokenStopsWhenCanceled(t *testing.T) {
	stub := &oauthStub{replies: []oauthReply{{Status: 400, Body: json.RawMessage(`{"error":"authorization_pending"}`)}}}
	client := newOauthClient(t, stub)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := client.Oauth.PollDeviceToken(ctx, "internetdata-cli",
		&DeviceAuthorization{DeviceCode: "mo_dc_x", ExpiresIn: 900, Interval: 5})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error was %v, want one that is context.Canceled", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("settled after %s, want under 1s", waited)
	}
	if len(stub.requests) != 0 {
		t.Errorf("sent %d request(s) after the cancel, want none", len(stub.requests))
	}
}

// Satisfies every operation's required members at once.
const everyRequiredMember = `{"issuer":"https://api.example.test",` +
	`"authorization_endpoint":"https://api.example.test/oauth/authorize",` +
	`"token_endpoint":"https://api.example.test/oauth/token","device_code":"mo_dc_x","user_code":"BCDF-GHJK",` +
	`"verification_uri":"https://app.example.test/device","expires_in":900,"interval":1,` +
	`"access_token":"mo_at_x","token_type":"Bearer"}`

func assertOauthOutcome(t *testing.T, err error, want oauthExpect) {
	t.Helper()
	var refused *OauthError
	isOauth := errors.As(err, &refused)
	switch want.Type {
	case "oauth", "accessDenied", "expiredToken":
		if !isOauth {
			t.Fatalf("error was %v, want an *OauthError", err)
		}
		if want.ErrorCode != "" && refused.ErrorCode != want.ErrorCode {
			t.Errorf("ErrorCode = %q, want %q", refused.ErrorCode, want.ErrorCode)
		}
		if description, asserted := want.description(t); asserted && refused.ErrorDescription != description {
			t.Errorf("ErrorDescription = %q, want %q", refused.ErrorDescription, description)
		}
		if want.Message != "" && err.Error() != want.Message {
			t.Errorf("message = %q, want %q", err.Error(), want.Message)
		}
		if errors.Is(err, ErrOauthAccessDenied) != (want.Type == "accessDenied") {
			t.Errorf("errors.Is(err, ErrOauthAccessDenied) is wrong for a %s", want.Type)
		}
		if errors.Is(err, ErrOauthExpiredToken) != (want.Type == "expiredToken") {
			t.Errorf("errors.Is(err, ErrOauthExpiredToken) is wrong for a %s", want.Type)
		}
	case "client":
		if isOauth {
			t.Fatalf("error was the OAuth error %v, want the ordinary *Error", err)
		}
	default:
		t.Fatalf("the corpus names an unknown outcome %q", want.Type)
	}
	status, statusAsserted := want.status(t)
	if refused != nil && statusAsserted && refused.StatusCode != status {
		t.Errorf("StatusCode = %d, want %d", refused.StatusCode, status)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error was %v, which does not unwrap to an *Error", err)
	}
	if want.Kind != "" && string(apiErr.Kind) != want.Kind {
		t.Errorf("Kind = %q, want %q", apiErr.Kind, want.Kind)
	}
	if want.Retryable != nil && apiErr.Retryable() != *want.Retryable {
		t.Errorf("Retryable() = %v, want %v", apiErr.Retryable(), *want.Retryable)
	}
	if refused == nil && statusAsserted && apiErr.StatusCode != status {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, status)
	}
}

func assertEndpoint(t *testing.T, req oauthRequest, want oauthEndpoint) {
	t.Helper()
	if req.method != want.Method || req.path != want.Path {
		t.Errorf("requested %s %s, want %s %s", req.method, req.path, want.Method, want.Path)
	}
}

func callOauth(ctx context.Context, client *Client, operation string, args oauthArgs) error {
	var err error
	switch operation {
	case "metadata":
		_, err = client.Oauth.Metadata(ctx)
	case "deviceAuthorization":
		_, err = client.Oauth.DeviceAuthorization(ctx, args.ClientID,
			DeviceAuthorizationOptions{Scope: args.Scope, Resource: args.Resource})
	case "exchangeDeviceCode":
		_, err = client.Oauth.ExchangeDeviceCode(ctx, args.ClientID, args.DeviceCode)
	case "exchangeRefreshToken":
		_, err = client.Oauth.ExchangeRefreshToken(ctx, args.ClientID, args.RefreshToken)
	case "revoke":
		err = client.Oauth.Revoke(ctx, args.ClientID, args.Token)
	default:
		return errors.New("the corpus names an unknown operation " + operation)
	}
	return err
}

// The members a decoded type serves, under the names the corpus uses: the json
// tags, with the two mslm: fields under the names they surface as.
func surfacedMembers(t *testing.T, decoded any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	for wire, surfaced := range map[string]string{"mslm:apikey_id": "apikey_id", "mslm:apikey": "apikey"} {
		if value, ok := members[wire]; ok {
			members[surfaced] = value
			delete(members, wire)
		}
	}
	return members
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		t.Fatalf("comparing unreadable JSON %s and %s", a, b)
	}
	return reflect.DeepEqual(left, right)
}

func newOauthClient(t *testing.T, stub *oauthStub, opts ...Option) *Client {
	t.Helper()
	base := []Option{WithBaseURL("https://api.example.test"), WithHTTPClient(&http.Client{Transport: stub})}
	client, err := New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stub.t = t
	return client
}

// Past this many requests, waits or clock reads, a loop under test fails its
// test: one that never ends would otherwise hang it while memory grows.
const oauthLoopBound = 16

// Answers in order, repeating the last reply, and records what left the client.
// OAuth calls run on the test's goroutine, so the bound can end the test there.
type oauthStub struct {
	t        *testing.T
	mu       sync.Mutex
	replies  []oauthReply
	requests []oauthRequest
}

type oauthRequest struct {
	method, path, query, url, body string
	header                         http.Header
}

func (s *oauthStub) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == oauthLoopBound {
		s.t.Fatalf("sent %d requests: the call under test does not end", len(s.requests))
	}
	s.requests = append(s.requests, oauthRequest{
		method: req.Method, path: req.URL.Path, query: req.URL.RawQuery, url: req.URL.String(),
		body: string(body), header: req.Header.Clone(),
	})
	reply := s.replies[0]
	if len(s.replies) > 1 {
		s.replies = s.replies[1:]
	}
	payload := []byte(reply.Body)
	if reply.RawBody != nil {
		payload = []byte(*reply.RawBody)
	}
	return &http.Response{
		StatusCode: reply.Status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Request:    req,
	}, nil
}

func oauthCorpusData(t *testing.T) oauthCorpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/testdata.json")
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	var data struct {
		Oauth oauthCorpus `json:"oauth"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parsing the corpus: %v", err)
	}
	return data.Oauth
}

type oauthCorpus struct {
	Endpoints    map[string]oauthEndpoint `json:"endpoints"`
	NoCredential struct {
		APIKey           string   `json:"apiKey"`
		ForbiddenHeaders []string `json:"forbiddenHeaders"`
		ForbiddenQuery   []string `json:"forbiddenQuery"`
	} `json:"noCredential"`
	Forms struct {
		ContentType string `json:"contentType"`
		Cases       []struct {
			Name      string            `json:"name"`
			Operation string            `json:"operation"`
			Endpoint  string            `json:"endpoint"`
			Args      oauthArgs         `json:"args"`
			Fields    map[string]string `json:"fields"`
		} `json:"cases"`
	} `json:"forms"`
	Responses map[string]json.RawMessage `json:"responses"`
	Errors    struct {
		Cases []struct {
			Name string `json:"name"`
			oauthReply
			Expect oauthExpect `json:"expect"`
		} `json:"cases"`
	} `json:"errors"`
	Retries struct {
		Cases []struct {
			Name      string       `json:"name"`
			Operation string       `json:"operation"`
			Args      oauthArgs    `json:"args"`
			Responses []oauthReply `json:"responses"`
			Expect    struct {
				Requests int    `json:"requests"`
				Outcome  string `json:"outcome"`
				oauthExpect
			} `json:"expect"`
		} `json:"cases"`
	} `json:"retries"`
	Poll struct {
		Cases []struct {
			Name      string              `json:"name"`
			ClientID  string              `json:"clientId"`
			Device    DeviceAuthorization `json:"device"`
			Responses []oauthReply        `json:"responses"`
			Expect    struct {
				Outcome  string            `json:"outcome"`
				Requests int               `json:"requests"`
				Waits    []float64         `json:"waits"`
				Token    map[string]string `json:"token"`
				oauthExpect
			} `json:"expect"`
		} `json:"cases"`
	} `json:"poll"`
}

type oauthEndpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type oauthArgs struct {
	ClientID     string `json:"clientId"`
	Scope        string `json:"scope"`
	Resource     string `json:"resource"`
	DeviceCode   string `json:"deviceCode"`
	RefreshToken string `json:"refreshToken"`
	Token        string `json:"token"`
}

type oauthReply struct {
	Status  int             `json:"status"`
	Body    json.RawMessage `json:"body"`
	RawBody *string         `json:"rawBody"`
}

type oauthResponseCase struct {
	Name string `json:"name"`
	oauthReply
	Expect struct {
		Present map[string]json.RawMessage `json:"present"`
		Absent  []string                   `json:"absent"`
	} `json:"expect"`
}

// An outcome as the error, retry and poll cases spell it; the latter two name
// it outcome rather than type. A member the case leaves out is not asserted,
// and an explicit null is: no description, or a status of 0 for a local refusal.
type oauthExpect struct {
	Type             string          `json:"type"`
	ErrorCode        string          `json:"errorCode"`
	ErrorDescription json.RawMessage `json:"errorDescription"`
	Status           json.RawMessage `json:"status"`
	Kind             string          `json:"kind"`
	Retryable        *bool           `json:"retryable"`
	Message          string          `json:"message"`
}

func (e oauthExpect) description(t *testing.T) (string, bool) {
	var description *string
	if len(e.ErrorDescription) == 0 || json.Unmarshal(e.ErrorDescription, &description) != nil {
		return "", false
	}
	if description == nil {
		return "", true
	}
	return *description, true
}

func (e oauthExpect) status(t *testing.T) (int, bool) {
	var status *int
	if len(e.Status) == 0 || json.Unmarshal(e.Status, &status) != nil {
		return 0, false
	}
	if status == nil {
		return 0, true
	}
	return *status, true
}
