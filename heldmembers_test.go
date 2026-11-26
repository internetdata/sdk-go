package internetdata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// An answer short of a member whose type overlay.yaml keeps required is read,
// and the member reads false (or empty), while the paths the answer left out
// are what the middleware package reads as absent. Every call that reads a
// lookup says so, the batch's entries and a cached repeat included.
func TestAnAnswerWithoutAHeldMemberIsReadAndItReadsFalse(t *testing.T) {
	whole := func() map[string]any {
		answer := answerAbout("45.83.91.1", nil)
		for _, flag := range heldAtTheirPublicType {
			answer[flag] = true
		}
		answer["asn"] = map[string]any{"asn": 9009, "name": "M247 Europe SRL", "domain": "m247global.com",
			"type": "hosting", "prefix": "45.83.91.0/24", "country": "RO", "rpki_status": "valid"}
		return answer
	}
	without := func(flags []string, asnType bool) map[string]any {
		answer := whole()
		for _, flag := range flags {
			delete(answer, flag)
		}
		if asnType {
			delete(answer["asn"].(map[string]any), "type")
		}
		return answer
	}
	cases := map[string]struct {
		answer map[string]any
		left   []string
	}{
		"nothing absent":        {whole(), nil},
		"every flag absent":     {without(heldAtTheirPublicType, false), heldAtTheirPublicType},
		"the AS's type absent":  {without(nil, true), []string{"asn.type"}},
		"all nine absent":       {without(heldAtTheirPublicType, true), append(slices.Clone(heldAtTheirPublicType), "asn.type")},
		"one flag absent":       {without([]string{"is_ssh"}, false), []string{"is_ssh"}},
		"Unlimited's own shape": {answerAbout("45.83.91.1", nil), heldAtTheirPublicType},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := jsonOf(t, c.answer)
			batch := `{"results":{"45.83.91.1":` + body + `},"errors":{}}`
			reads := map[string]func() (*Result, error){
				"Lookup": func() (*Result, error) {
					return fixedClient(t, &fixedTransport{status: 200, contentType: "application/json", body: body}).
						Lookup(t.Context(), "45.83.91.1")
				},
				"MyIP": func() (*Result, error) {
					return fixedClient(t, &fixedTransport{status: 200, contentType: "application/json", body: body}).
						MyIP(t.Context())
				},
				"LookupBatch": func() (*Result, error) {
					answers, err := fixedClient(t, &fixedTransport{status: 200, contentType: "application/json", body: batch}).
						LookupBatch(t.Context(), []string{"45.83.91.1"})
					return answers["45.83.91.1"].Result, err
				},
			}
			for call, read := range reads {
				result, err := read()
				if err != nil || result == nil {
					t.Fatalf("%s: %v", call, err)
				}
				assertHeldRead(t, call, result, c.answer, c.left)
			}
			// A repeat is served from the cache, which holds the answer as it was read.
			transport := &fixedTransport{status: 200, contentType: "application/json", body: body}
			cached, err := New(WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := cached.Lookup(t.Context(), "45.83.91.1")
				if err != nil {
					t.Fatalf("Lookup: %v", err)
				}
				assertHeldRead(t, "a cached Lookup", result, c.answer, c.left)
			}
			if got := transport.sent(); got != 1 {
				t.Errorf("sent %d requests, want 1", got)
			}
		})
	}
}

func assertHeldRead(t *testing.T, call string, result *Result, served map[string]any, left []string) {
	t.Helper()
	got := slices.Clone(result.unserved)
	slices.Sort(got)
	want := slices.Clone(left)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s left out %v, want %v", call, got, want)
	}
	flags := map[string]bool{
		"is_bot": result.IsBot, "is_carrier": result.IsCarrier, "is_satellite": result.IsSatellite,
		"is_ssh": result.IsSSH, "is_smtp": result.IsSMTP, "is_web": result.IsWeb,
		"is_dns_resolver": result.IsDNSResolver, "is_bittorrent": result.IsBittorrent,
	}
	for flag, read := range flags {
		if served[flag] == true && !read {
			t.Errorf("%s: %s served true and read false", call, flag)
		}
		if _, present := served[flag]; !present && read {
			t.Errorf("%s: %s absent and read true", call, flag)
		}
	}
	if asn, ok := served["asn"].(map[string]any); ok {
		wantType, _ := asn["type"].(string)
		if result.Asn.Type != wantType {
			t.Errorf("%s: asn.type read %q, want %q", call, result.Asn.Type, wantType)
		}
	}
}

// A held member that is present is refused as any required member is: the wrong
// type, or null. In a batch it is that address's error alone.
func TestAHeldMemberPresentButNotWellFormedIsStillRefused(t *testing.T) {
	asn := func(value any) map[string]any {
		return map[string]any{"asn": 9009, "name": "M247 Europe SRL", "domain": "m247global.com",
			"type": value, "prefix": "45.83.91.0/24", "country": "RO", "rpki_status": "valid"}
	}
	var cases []struct {
		name   string
		answer map[string]any
	}
	for _, flag := range heldAtTheirPublicType {
		for _, value := range []any{"true", 1, nil, []any{true}, map[string]any{}} {
			cases = append(cases, struct {
				name   string
				answer map[string]any
			}{fmt.Sprintf("%s %v", flag, value), answerAbout("45.83.91.1", map[string]any{flag: value})})
		}
	}
	for _, value := range []any{1, nil, true, []any{}} {
		cases = append(cases, struct {
			name   string
			answer map[string]any
		}{fmt.Sprintf("asn.type %v", value), answerAbout("45.83.91.1", map[string]any{"asn": asn(value)})})
	}
	good := jsonOf(t, answerAbout("45.83.91.2", nil))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			body := jsonOf(t, c.answer)
			for _, call := range []string{"Lookup", "MyIP"} {
				transport := &fixedTransport{status: 200, contentType: "application/json", body: body}
				assertKind(t, answeringCalls[call](t.Context(), fixedClient(t, transport)), KindServerError, http.StatusOK)
				if got := transport.sent(); got != 2 {
					t.Errorf("%s sent %d requests, want 2", call, got)
				}
			}
			transport := &fixedTransport{status: 200, contentType: "application/json", body: `{"results":{` +
				`"45.83.91.1":` + body + `,"45.83.91.2":` + good + `},"errors":{}}`}
			answers, err := fixedClient(t, transport).LookupBatch(t.Context(), []string{"45.83.91.1", "45.83.91.2"})
			if err != nil {
				t.Fatalf("LookupBatch: %v", err)
			}
			assertKind(t, answers["45.83.91.1"].Err, KindServerError, http.StatusOK)
			if got := answers["45.83.91.2"]; got.Err != nil || got.Result == nil {
				t.Errorf("45.83.91.2 = %+v, want its answer", got)
			}
		})
	}
}

// The AS in a domain's hosting list is the same Asn, so its type may be absent
// there too.
func TestADomainsHostingAsnWithoutATypeIsRead(t *testing.T) {
	answer := corpusBody("domain-paid-answer")
	answer["hosting"] = []any{map[string]any{"ip": "192.0.2.10", "asn": map[string]any{
		"asn": 9009, "name": "M247 Europe SRL", "domain": "m247global.com",
		"prefix": "45.83.91.0/24", "country": "RO", "rpki_status": "valid"}}}
	transport := &fixedTransport{status: 200, contentType: "application/json", body: jsonOf(t, answer)}
	if err := answeringCalls["LookupDomain"](t.Context(), fixedClient(t, transport)); err != nil {
		t.Fatalf("LookupDomain: %v", err)
	}
}

// heldOptional lists what overlay.yaml puts back into required, no more and no
// fewer, so a member held in one place and not the other fails here.
func TestHeldOptionalIsWhatOverlayYamlPutsBackIntoRequired(t *testing.T) {
	overlay, err := os.ReadFile("overlay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for schema, typ := range map[string]any{"LookupResponse": LookupResponse{}, "Asn": Asn{}} {
		match := regexp.MustCompile(`schemas\.` + schema + `\.required\n    update:\n((?:      - \w+\n)+)`).
			FindStringSubmatch(string(overlay))
		if match == nil {
			t.Fatalf("overlay.yaml puts nothing back into %s.required", schema)
		}
		var put []string
		for _, line := range strings.Split(strings.TrimSpace(match[1]), "\n") {
			put = append(put, strings.TrimPrefix(strings.TrimSpace(line), "- "))
		}
		var held []string
		for candidate, names := range heldOptional {
			if fmt.Sprintf("%T", typ) == candidate.String() {
				held = names
			}
		}
		if !slices.Equal(put, held) {
			t.Errorf("overlay.yaml puts %v back into %s.required, heldOptional holds %v", put, schema, held)
		}
	}
}
