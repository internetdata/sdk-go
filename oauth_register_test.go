package internetdata

import (
	"encoding/json"
	"strings"
	"testing"
)

// The corpus's oauth.register section: the JSON body, what is refused before
// sending, decoding, the unreadable answers, retries and refusals.
func TestOauthRegisterBodies(t *testing.T) {
	c := oauthCorpusData(t).Register
	for _, bc := range c.Bodies.Cases {
		t.Run(bc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: []oauthReply{{Status: 201, Body: json.RawMessage(everyRequiredMember)}}}
			client := newOauthClient(t, stub, WithBaseURL("https://api.example.test/"))
			if _, err := client.Oauth.Register(t.Context(), bc.Args.RedirectURIs, RegisterOptions{ClientName: bc.Args.ClientName}); err != nil {
				t.Fatalf("Register: %v", err)
			}
			if len(stub.requests) != 1 {
				t.Fatalf("sent %d request(s), want 1", len(stub.requests))
			}
			req := stub.requests[0]
			assertEndpoint(t, req, c.Endpoint)
			if got := req.url; got != "https://api.example.test"+c.Endpoint.Path {
				t.Errorf("requested %s", got)
			}
			if got := req.header.Get("Content-Type"); !strings.HasPrefix(got, c.ContentType) {
				t.Errorf("sent as %q, want %s", got, c.ContentType)
			}
			if !jsonEqual(t, json.RawMessage(req.body), bc.Body) {
				t.Errorf("sent %s, want %s", req.body, bc.Body)
			}
		})
	}
	refusals := append(c.Refusals.Cases,
		oauthRegisterCase{Name: "not UTF-8", Args: oauthRegisterArgs{RedirectURIs: []string{"http://127.0.0.1/cb\xff"}}},
		oauthRegisterCase{Name: "a name that is not UTF-8", Args: oauthRegisterArgs{RedirectURIs: []string{"http://127.0.0.1/cb"}, ClientName: "\xff"}},
		oauthRegisterCase{Name: "nil", Args: oauthRegisterArgs{}},
	)
	for _, rc := range refusals {
		t.Run("refused/"+rc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: []oauthReply{{Status: 201, Body: json.RawMessage(everyRequiredMember)}}}
			_, err := newOauthClient(t, stub).Oauth.Register(t.Context(), rc.Args.RedirectURIs, RegisterOptions{ClientName: rc.Args.ClientName})
			assertOauthOutcome(t, err, oauthExpect{Type: "client", Kind: string(KindBadRequest)})
			if len(stub.requests) != 0 {
				t.Errorf("sent %d request(s), want none", len(stub.requests))
			}
		})
	}
}

func TestOauthRegisterAnswers(t *testing.T) {
	c := oauthCorpusData(t).Register
	uris := []string{"http://127.0.0.1/cb"}
	for _, rc := range c.Responses.Cases {
		t.Run(rc.Name, func(t *testing.T) {
			got, err := newOauthClient(t, &oauthStub{replies: []oauthReply{rc.oauthReply}}).Oauth.Register(t.Context(), uris, RegisterOptions{})
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			members := surfacedMembers(t, got)
			for name, want := range rc.Expect.Present {
				if value, ok := members[name]; !ok || !jsonEqual(t, value, want) {
					t.Errorf("%s = %s, want %s", name, value, want)
				}
			}
			for _, name := range rc.Expect.Absent {
				if value, ok := members[name]; ok {
					t.Errorf("%s = %s, want it absent", name, value)
				}
			}
		})
	}
	for _, uc := range c.Unreadable.Cases {
		t.Run("unreadable/"+uc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: []oauthReply{uc.oauthReply}}
			got, err := newOauthClient(t, stub, WithRetries(0)).Oauth.Register(t.Context(), uris, RegisterOptions{})
			if err == nil {
				t.Fatalf("answered %+v", got)
			}
			status, _ := json.Marshal(uc.Status)
			assertOauthOutcome(t, err, oauthExpect{Type: "client", Kind: string(KindServerError), Status: status})
			if len(stub.requests) != 1 {
				t.Errorf("sent %d request(s), want 1", len(stub.requests))
			}
		})
	}
	for _, ec := range c.Errors {
		t.Run("refused/"+ec.Name, func(t *testing.T) {
			_, err := newOauthClient(t, &oauthStub{replies: []oauthReply{ec.oauthReply}}).Oauth.Register(t.Context(), uris, RegisterOptions{})
			assertOauthOutcome(t, err, ec.Expect)
		})
	}
	for _, rc := range c.Retries {
		t.Run(rc.Name, func(t *testing.T) {
			stub := &oauthStub{replies: rc.Responses}
			_, err := newOauthClient(t, stub).Oauth.Register(t.Context(), rc.Args.RedirectURIs, RegisterOptions{})
			if len(stub.requests) != rc.Expect.Requests {
				t.Errorf("sent %d request(s), want %d", len(stub.requests), rc.Expect.Requests)
			}
			if rc.Expect.Outcome == "ok" {
				if err != nil {
					t.Errorf("Register: %v", err)
				}
				return
			}
			want := rc.Expect.oauthExpect
			want.Type = rc.Expect.Outcome
			assertOauthOutcome(t, err, want)
		})
	}
}

type oauthRegisterCorpus struct {
	Endpoint    oauthEndpoint `json:"endpoint"`
	ContentType string        `json:"contentType"`
	Bodies      struct {
		Cases []struct {
			Name string            `json:"name"`
			Args oauthRegisterArgs `json:"args"`
			Body json.RawMessage   `json:"body"`
		} `json:"cases"`
	} `json:"bodies"`
	Refusals struct {
		Cases []oauthRegisterCase `json:"cases"`
	} `json:"refusals"`
	Responses struct {
		Cases []oauthResponseCase `json:"cases"`
	} `json:"responses"`
	Unreadable struct {
		Cases []struct {
			Name string `json:"name"`
			oauthReply
		} `json:"cases"`
	} `json:"unreadable"`
	Retries []struct {
		Name      string            `json:"name"`
		Args      oauthRegisterArgs `json:"args"`
		Responses []oauthReply      `json:"responses"`
		Expect    struct {
			Requests int    `json:"requests"`
			Outcome  string `json:"outcome"`
			oauthExpect
		} `json:"expect"`
	} `json:"retries"`
	Errors []struct {
		Name string `json:"name"`
		oauthReply
		Expect oauthExpect `json:"expect"`
	} `json:"errors"`
}

type oauthRegisterCase struct {
	Name string            `json:"name"`
	Args oauthRegisterArgs `json:"args"`
}

type oauthRegisterArgs struct {
	RedirectURIs []string `json:"redirectUris"`
	ClientName   string   `json:"clientName"`
}
