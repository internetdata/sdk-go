package internetdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"sync"
	"testing"
)

// Answers every request with one fixed response, its body bytes exactly as
// given, and counts the requests: what a proxy in front of the API, or an API
// having a bad day, sends back.
type fixedTransport struct {
	status      int
	contentType string
	body        string
	mu          sync.Mutex
	requests    int
}

func (f *fixedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	header := http.Header{}
	if f.contentType != "" {
		header.Set("Content-Type", f.contentType)
	}
	return &http.Response{
		StatusCode: f.status,
		Status:     http.StatusText(f.status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader([]byte(f.body))),
		Request:    req,
	}, nil
}

func (f *fixedTransport) sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// Every call that reads a JSON answer, each reduced to its error. A batch's is
// the error its first address carries.
var answeringCalls = map[string]func(ctx context.Context, c *Client) error{
	"Lookup": func(ctx context.Context, c *Client) error {
		_, err := c.Lookup(ctx, "45.83.91.1")
		return err
	},
	"LookupASN": func(ctx context.Context, c *Client) error {
		_, err := c.LookupASN(ctx, 9009)
		return err
	},
	"LookupDomain": func(ctx context.Context, c *Client) error {
		_, err := c.LookupDomain(ctx, "example.com")
		return err
	},
	"MyIP": func(ctx context.Context, c *Client) error {
		_, err := c.MyIP(ctx)
		return err
	},
	"MyEntitlement": func(ctx context.Context, c *Client) error {
		_, err := c.MyEntitlement(ctx)
		return err
	},
	"LookupBatch": func(ctx context.Context, c *Client) error {
		answers, err := c.LookupBatch(ctx, []string{"45.83.91.1", "45.83.91.2"})
		if err != nil {
			return err
		}
		return answers["45.83.91.1"].Err
	},
	"Database.List": func(ctx context.Context, c *Client) error {
		_, err := c.Database.List(ctx)
		return err
	},
	"Database.Metadata": func(ctx context.Context, c *Client) error {
		_, err := c.Database.Metadata(ctx, "bogon_ip_v1")
		return err
	},
	"Database.Checksums": func(ctx context.Context, c *Client) error {
		_, err := c.Database.Checksums(ctx, "bogon_ip_v1", FormatCSVGZ)
		return err
	},
	"Database.Downloads": func(ctx context.Context, c *Client) error {
		_, err := c.Database.Downloads(ctx, 5)
		return err
	},
	"Oauth.Metadata": func(ctx context.Context, c *Client) error {
		_, err := c.Oauth.Metadata(ctx)
		return err
	},
	"Oauth.DeviceAuthorization": func(ctx context.Context, c *Client) error {
		_, err := c.Oauth.DeviceAuthorization(ctx, "cid", DeviceAuthorizationOptions{})
		return err
	},
}

func fixedClient(t *testing.T, transport *fixedTransport, opts ...Option) *Client {
	t.Helper()
	client, err := New(append([]Option{
		WithHTTPClient(&http.Client{Transport: transport}), WithoutCache(), WithRetries(1),
	}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func assertKind(t *testing.T, err error, kind ErrorKind, status int) {
	t.Helper()
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want an *Error", err, err)
	}
	if apiErr.Kind != kind || apiErr.StatusCode != status {
		t.Errorf("err = %s with status %d, want %s with status %d", apiErr.Kind, apiErr.StatusCode, kind, status)
	}
}

// A 2xx a call cannot read as its answer is the server failing, so it is the
// SDK's own server_error, carrying the status, and retried like a 5xx.
func TestAnUnreadable2xxIsARetriedServerErrorCarryingItsStatus(t *testing.T) {
	bodies := map[string]struct{ contentType, body string }{
		"an HTML page":                 {"text/html", "<html>proxy</html>"},
		"an HTML page labeled as JSON": {"application/json", "<html>proxy</html>"},
		"an empty body":                {"application/json", ""},
		"null":                         {"application/json", "null"},
		"an array":                     {"application/json", "[]"},
		"an empty object":              {"application/json", "{}"},
		"a string":                     {"application/json", `"ok"`},
	}
	for name, call := range answeringCalls {
		for what, body := range bodies {
			t.Run(name+" given "+what, func(t *testing.T) {
				t.Parallel()
				transport := &fixedTransport{status: http.StatusOK, contentType: body.contentType, body: body.body}
				assertKind(t, call(t.Context(), fixedClient(t, transport)), KindServerError, http.StatusOK)
				if got := transport.sent(); got != 2 {
					t.Errorf("sent %d requests, want 2: the answer is retried", got)
				}
			})
		}
	}
}

// A member the spec requires, sent as null, missing or of the wrong type, makes
// the answer unreadable, and so does an object served short of one, a whois
// record or an AS prefix. json.Unmarshal would read each as a zero value.
func TestAnAnswerMissingARequiredMemberIsUnreadable(t *testing.T) {
	unlimited := func(members map[string]any) string {
		answer := answerAbout("45.83.91.1", nil)
		for name, value := range members {
			if value == nil {
				delete(answer, name)
				continue
			}
			answer[name] = value
		}
		encoded, _ := json.Marshal(answer)
		return string(encoded)
	}
	asn := func(members map[string]any) string {
		answer := corpusBody("asn-paid-answer")
		maps.Copy(answer, members)
		encoded, _ := json.Marshal(answer)
		return string(encoded)
	}
	domain := func(members map[string]any) string {
		answer := corpusBody("domain-paid-answer")
		maps.Copy(answer, members)
		encoded, _ := json.Marshal(answer)
		return string(encoded)
	}
	cases := []struct {
		call, name, body string
	}{
		{"Lookup", "ip null", unlimited(map[string]any{"ip": json.RawMessage("null")})},
		{"Lookup", "is_tor missing", unlimited(map[string]any{"is_tor": nil})},
		{"Lookup", "is_tor a string", unlimited(map[string]any{"is_tor": "yes"})},
		{"Lookup", "tor missing", unlimited(map[string]any{"tor": nil})},
		{"Lookup", "whois short of a key", unlimited(map[string]any{"whois": map[string]any{"id": "x"}})},
		{"Lookup", "location empty", unlimited(map[string]any{"location": map[string]any{}})},
		{"MyIP", "abuse missing", unlimited(map[string]any{"abuse": nil})},
		{"LookupASN", "registry missing", `{"asn":9009,"name":"M247 Europe SRL","country":"RO",` +
			`"allocated":"2005-06-06","domain":"m247global.com"}`},
		{"LookupASN", "a prefix short of a key",
			asn(map[string]any{"prefixes": []any{map[string]any{"prefix": "x"}}})},
		{"LookupASN", "hosted_domains short of total",
			asn(map[string]any{"hosted_domains": map[string]any{"domains": []any{}}})},
		{"LookupASN", "upstreams holding a string", asn(map[string]any{"upstreams": []any{"174"}})},
		{"LookupDomain", "has_mx missing", `{"domain":"example.com","is_registered":true,"has_a":true,` +
			`"has_aaaa":true,"has_ns":true,"has_cname":false}`},
		{"LookupDomain", "a hosting entry short of its asn",
			domain(map[string]any{"hosting": []any{map[string]any{"ip": "192.0.2.10"}}})},
		{"LookupDomain", "a hosting entry's asn short of a key", domain(map[string]any{"hosting": []any{
			map[string]any{"ip": "192.0.2.10", "asn": map[string]any{"asn": 64496}}}})},
		{"LookupDomain", "cohosted_domains total a string", domain(map[string]any{"cohosted_domains": map[string]any{
			"ip": "192.0.2.10", "total": "3", "domains": []any{}}})},
		{"LookupDomain", "web short of its links", domain(map[string]any{"web": map[string]any{
			"url": "https://example.com/", "title": "", "description": "", "last_seen": "2026-11-18"}})},
		{"LookupDomain", "web links hosts null", domain(map[string]any{"web": map[string]any{
			"url": "https://example.com/", "title": "", "description": "", "last_seen": "2026-11-18",
			"links": map[string]any{"total": 0, "hosts": nil}}})},
		{"LookupDomain", "redirects short of domains", domain(map[string]any{"redirects": map[string]any{"total": 2}})},
		{"LookupDomain", "backlinks holding a number", domain(map[string]any{"backlinks": map[string]any{
			"total": 1, "domains": []any{7}}})},
		{"MyEntitlement", "usage missing", `{"org_id":"85bb51e4-2eb6-4a31-8e4d-02ba8b98fe61",` +
			`"apikey":{"id":"0ab424cc-7619-4dad-b027-afacdc2cedb0","expires":null,"allowed_cidrs":[]},` +
			`"plan":{"key":"max","name":"Max","tier":"max"}}`},
		{"MyEntitlement", "plan name missing", `{"org_id":"85bb51e4-2eb6-4a31-8e4d-02ba8b98fe61",` +
			`"apikey":{"id":"0ab424cc-7619-4dad-b027-afacdc2cedb0","expires":null,"allowed_cidrs":[]},` +
			`"plan":{"key":"max","tier":"max"},"usage":{"requests":1,"quota":5,"hard_limit":null,` +
			`"window_start":"2026-11-01T00:00:00Z","window_end":"2026-12-01T00:00:00Z"}}`},
	}
	for _, c := range cases {
		t.Run(c.call+" "+c.name, func(t *testing.T) {
			t.Parallel()
			transport := &fixedTransport{status: http.StatusOK, contentType: "application/json", body: c.body}
			err := answeringCalls[c.call](t.Context(), fixedClient(t, transport))
			assertKind(t, err, KindServerError, http.StatusOK)
			if got := transport.sent(); got != 2 {
				t.Errorf("sent %d requests, want 2", got)
			}
		})
	}
}

// A batch entry the client cannot read is that address's error alone, and the
// chunk's other answers stand.
func TestABatchEntryItCannotReadIsThatAddressesErrorAlone(t *testing.T) {
	good, _ := json.Marshal(answerAbout("45.83.91.1", map[string]any{"is_vpn": true}))
	for _, entry := range []string{`"x"`, `{"is_vpn":false}`, `null`} {
		t.Run(entry, func(t *testing.T) {
			transport := &fixedTransport{status: http.StatusOK, contentType: "application/json", body: `{"results":{` +
				`"45.83.91.1":` + string(good) + `,"45.83.91.2":` + entry + `},"errors":{}}`}
			answers, err := fixedClient(t, transport).LookupBatch(t.Context(), []string{"45.83.91.1", "45.83.91.2"})
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}
			if got := answers["45.83.91.1"]; got.Err != nil || got.Result == nil || !BoolValue(got.Result.IsVpn) {
				t.Errorf("45.83.91.1 = %+v, want its answer", got)
			}
			assertKind(t, answers["45.83.91.2"].Err, KindServerError, http.StatusOK)
			if got := transport.sent(); got != 1 {
				t.Errorf("sent %d requests, want 1: an entry is never retried on its own", got)
			}
		})
	}
}

// A 4xx is classified by its status before anything decodes its body, so a
// proxy's page labeled as JSON is the refusal it is, sent once.
func TestA4xxWhoseBodyIsNotTheAPIsIsClassifiedByItsStatus(t *testing.T) {
	cases := []struct {
		status int
		body   string
		kind   ErrorKind
	}{
		{http.StatusUnauthorized, "<html>401</html>", KindUnauthorized},
		{http.StatusForbidden, "", KindForbidden},
		{http.StatusNotFound, "<html>404</html>", KindBadRequest},
		{http.StatusTooManyRequests, "<html>429</html>", KindQuotaExceeded},
	}
	calls := map[string]func(ctx context.Context, c *Client) error{
		"Database.DownloadURL": func(ctx context.Context, c *Client) error {
			_, err := c.Database.DownloadURL(ctx, "bogon_ip_v1", FormatCSVGZ)
			return err
		},
	}
	maps.Copy(calls, answeringCalls)
	for name, call := range calls {
		for _, c := range cases {
			t.Run(name+" "+http.StatusText(c.status), func(t *testing.T) {
				t.Parallel()
				transport := &fixedTransport{status: c.status, contentType: "application/json", body: c.body}
				assertKind(t, call(t.Context(), fixedClient(t, transport)), c.kind, c.status)
				if got := transport.sent(); got != 1 {
					t.Errorf("sent %d requests, want 1", got)
				}
			})
		}
	}
}

// A paid domain answer's network on each address, co-hosted domains, home page,
// redirects and backlinks are read as served, and each is nil on Unlimited,
// never an empty value standing for one the plan leaves out.
func TestAPaidDomainAnswersHostingPageAndLinksAreRead(t *testing.T) {
	paid, _ := json.Marshal(corpusBody("domain-paid-answer"))
	transport := &fixedTransport{status: http.StatusOK, contentType: "application/json", body: string(paid)}
	answer, err := fixedClient(t, transport).LookupDomain(t.Context(), "example.com")
	if err != nil {
		t.Fatalf("LookupDomain: %v", err)
	}
	if answer.Hosting == nil || len(*answer.Hosting) != 2 || (*answer.Hosting)[1].IP != "2001:db8::10" ||
		(*answer.Hosting)[1].Asn.Prefix != "2001:db8::/32" || (*answer.Hosting)[0].Asn.Asn != 64496 {
		t.Errorf("Hosting = %+v, want both addresses with their network", answer.Hosting)
	}
	if c := answer.CohostedDomains; c == nil || c.IP != "192.0.2.10" || c.Total != 3 || len(c.Domains) != 3 {
		t.Errorf("CohostedDomains = %+v", c)
	}
	if w := answer.Web; w == nil || w.URL != "https://example.com/" || w.Title != "Example Domain" ||
		w.LastSeen != "2026-11-18" || w.Links.Total != 1 || len(w.Links.Hosts) != 1 || w.Links.Hosts[0] != "www.iana.org" {
		t.Errorf("Web = %+v", w)
	}
	if r := answer.Redirects; r == nil || r.Total != 2 || len(r.Domains) != 2 {
		t.Errorf("Redirects = %+v", r)
	}
	if b := answer.Backlinks; b == nil || b.Total != 3 || len(b.Domains) != 3 || b.Domains[2] != "wiki.example" {
		t.Errorf("Backlinks = %+v", b)
	}

	unlimited, _ := json.Marshal(corpusBody("domain-unlimited-answer"))
	transport = &fixedTransport{status: http.StatusOK, contentType: "application/json", body: string(unlimited)}
	answer, err = fixedClient(t, transport).LookupDomain(t.Context(), "example.com")
	if err != nil {
		t.Fatalf("LookupDomain: %v", err)
	}
	if answer.Hosting != nil || answer.CohostedDomains != nil || answer.Web != nil || answer.Redirects != nil ||
		answer.Backlinks != nil {
		t.Errorf("Unlimited answer = %+v, want the five paid members nil", answer)
	}
}
