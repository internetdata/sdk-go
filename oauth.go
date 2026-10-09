package internetdata

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/internetdata/sdk-go/v2/internal/api"
)

// OauthAPI signs a person in on their own machine with the OAuth device flow,
// so a program can be handed one of their API keys instead of asking them to
// paste it, or through a browser redirect with the authorization code flow.
// Reached through Client.Oauth.
//
// Every call takes a client ID, which is issued on request from
// support@internetdata.io. None of these requests carries the client's API
// key, and none needs one.
type OauthAPI struct {
	// A second generated client, built without the API key's request editor.
	api     *api.ClientWithResponses
	baseURL string
	retries int
	// Seams for the poll's wait and its deadline, replaced together in tests.
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

// Metadata is the authorization server's discovery document. Nothing else
// here needs it: every request is built from the client's base URL.
func (o *OauthAPI) Metadata(ctx context.Context) (*OauthMetadata, error) {
	return withRetry(ctx, o.retries, func() (*OauthMetadata, error) {
		res, err := o.api.OauthMetadata(ctx)
		return decodeOauth[OauthMetadata](res, err, "issuer", "authorization_endpoint", "token_endpoint")
	})
}

// DeviceAuthorization starts a device sign-in: show the person UserCode and
// VerificationURI, then hand the answer to PollDeviceToken. It consumes
// nothing, so it is retried like any read; a refusal such as slow_down comes
// back as an *OauthError.
func (o *OauthAPI) DeviceAuthorization(
	ctx context.Context, clientID string, opts DeviceAuthorizationOptions,
) (*DeviceAuthorization, error) {
	form := url.Values{"client_id": {clientID}}
	if opts.Scope != "" {
		form.Set("scope", opts.Scope)
	}
	if opts.Resource != "" {
		form.Set("resource", opts.Resource)
	}
	return withRetry(ctx, o.retries, func() (*DeviceAuthorization, error) {
		res, err := o.api.OauthDeviceAuthorizationWithBody(ctx, formContentType, formBody(form))
		return decodeOauth[DeviceAuthorization](res, err,
			"device_code", "user_code", "verification_uri", "expires_in", "interval")
	})
}

// ExchangeDeviceCode asks once whether the person has approved a device
// sign-in. Until they do the answer is an *OauthError coded
// authorization_pending; PollDeviceToken is the loop around it.
//
// Never retried: an approved code is spent by the answer that carries the
// tokens, so a retry after a lost response could only lose them.
func (o *OauthAPI) ExchangeDeviceCode(ctx context.Context, clientID, deviceCode string) (*TokenResponse, error) {
	return o.exchange(ctx, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {clientID},
	})
}

// ExchangeRefreshToken trades a refresh token for a new pair. The old one is
// spent whatever happens next, so this is never retried. A refresh names the
// API key the person picked (ApikeyID) but never reveals it again (Apikey).
func (o *OauthAPI) ExchangeRefreshToken(
	ctx context.Context, clientID, refreshToken string,
) (*TokenResponse, error) {
	return o.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	})
}

// ExchangeAuthorizationCode trades the code a sign-in's redirect brought back
// for tokens. codeVerifier is the PKCE verifier whose challenge went into the
// authorization URL, and redirectURI that URL's, exactly.
//
// Never retried: the server spends the code on first read, before it checks
// the verifier, so a retry could only be refused.
func (o *OauthAPI) ExchangeAuthorizationCode(
	ctx context.Context, clientID, code, codeVerifier, redirectURI string,
) (*TokenResponse, error) {
	return o.exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {codeVerifier},
	})
}

// AuthorizationURL is the URL to open in the person's browser for the
// authorization code flow. It makes no request. Once they decide, the server
// redirects to redirectURI with a code for ExchangeAuthorizationCode (and the
// state, when one was given), or with an error.
//
// A required value that is empty or not UTF-8 is refused with a bad_request
// *Error.
func (o *OauthAPI) AuthorizationURL(
	clientID, redirectURI, codeChallenge string, opts AuthorizationURLOptions,
) (string, error) {
	params := [][2]string{
		{"response_type", "code"},
		{"client_id", clientID},
		{"redirect_uri", redirectURI},
		{"code_challenge", codeChallenge},
		{"code_challenge_method", "S256"},
		{"scope", opts.Scope},
		{"state", opts.State},
		{"resource", opts.Resource},
	}
	var query []string
	for i, p := range params {
		name, value := p[0], p[1]
		if value == "" && i > 4 {
			continue
		}
		if value == "" || !utf8.ValidString(value) {
			return "", &Error{Kind: KindBadRequest, Message: name + " must be a non-empty UTF-8 string"}
		}
		query = append(query, name+"="+percentEncode(value))
	}
	return o.baseURL + "/oauth/authorize?" + strings.Join(query, "&"), nil
}

// CreatePkce makes a fresh PKCE pair for one sign-in, from 32 bytes of the
// system's secure random source.
func (o *OauthAPI) CreatePkce() Pkce {
	raw := make([]byte, 32)
	// Never fails from Go 1.24: a broken random source crashes the program.
	_, _ = rand.Read(raw)
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	return Pkce{Verifier: verifier, Challenge: o.PkceChallenge(verifier), Method: "S256"}
}

// PkceChallenge is the S256 challenge for a PKCE verifier: its SHA-256, as
// unpadded base64url.
func (o *OauthAPI) PkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Revoke ends a token. A refresh token ends the whole sign-in and every token
// it issued, which is how a program signs the machine out; an access token
// ends only itself. The server answers the same for any token, known or not.
func (o *OauthAPI) Revoke(ctx context.Context, clientID, token string) error {
	form := url.Values{"token": {token}, "client_id": {clientID}}
	_, err := withRetry(ctx, o.retries, func() (struct{}, error) {
		res, err := o.api.OauthRevokeWithBody(ctx, formContentType, formBody(form))
		if err != nil {
			return struct{}{}, errorFromTransport(err)
		}
		body, err := readBody(res)
		if err != nil {
			return struct{}{}, err
		}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			return struct{}{}, oauthFailure(res, body)
		}
		return struct{}{}, nil
	})
	return err
}

// PollDeviceToken waits for the person to approve a device sign-in and
// returns its tokens.
//
// It waits device.Interval seconds (5 when that is below 1) before EVERY
// request, the first included, and adds 5 more for the rest of the call each
// time the server answers slow_down. No wait runs past device.ExpiresIn,
// counted from this call: one that would ends at it, with no request after.
// It stops at the first answer that is neither pending nor slow_down: a denial
// satisfies errors.Is(err, ErrOauthAccessDenied), an expired code
// errors.Is(err, ErrOauthExpiredToken), and so does running out of
// device.ExpiresIn, with a StatusCode of 0. Canceling ctx stops the wait and
// any request in flight.
func (o *OauthAPI) PollDeviceToken(
	ctx context.Context, clientID string, device *DeviceAuthorization,
) (*TokenResponse, error) {
	if device == nil {
		return nil, &Error{Kind: KindBadRequest, Message: "a device authorization is required"}
	}
	interval := seconds(device.Interval)
	if device.Interval < 1 {
		interval = 5 * time.Second
	}
	deadline := o.now().Add(seconds(device.ExpiresIn))
	for {
		// Only to the deadline: past it the outcome is the local expiry anyway,
		// and the interval is the server's word, whatever it says.
		if err := o.sleep(ctx, min(interval, max(deadline.Sub(o.now()), 0))); err != nil {
			return nil, err
		}
		if !o.now().Before(deadline) {
			return nil, &OauthError{ErrorCode: "expired_token"}
		}
		token, err := o.ExchangeDeviceCode(ctx, clientID, device.DeviceCode)
		var refused *OauthError
		if err == nil || !errors.As(err, &refused) {
			return token, err
		}
		switch refused.ErrorCode {
		case "authorization_pending":
		case "slow_down":
			interval = min(interval, math.MaxInt64-5*time.Second) + 5*time.Second
		default:
			return nil, err
		}
	}
}

// seconds converts a server's count of seconds, saturating where a Duration
// runs out (about 292 years) rather than wrapping around. Compared as int64,
// since where int is 32 bits that bound does not fit in one, and n never
// reaches it.
func seconds(n int) time.Duration {
	if int64(n) > math.MaxInt64/int64(time.Second) {
		return math.MaxInt64
	}
	return time.Duration(n) * time.Second
}

func (o *OauthAPI) exchange(ctx context.Context, form url.Values) (*TokenResponse, error) {
	res, err := o.api.OauthTokenWithBody(ctx, formContentType, formBody(form))
	return decodeOauth[TokenResponse](res, err, "access_token", "token_type", "expires_in")
}

// DeviceAuthorizationOptions narrows a device sign-in. An empty field is left
// out of the request rather than sent empty.
type DeviceAuthorizationOptions struct {
	// Scope is one space-delimited string, sent as given. The server narrows it
	// to what the client may ask for.
	Scope string
	// Resource is the API the tokens are meant for.
	Resource string
}

// AuthorizationURLOptions is what an authorization URL asks for beyond what
// every one carries. An empty field is left out of the URL.
type AuthorizationURLOptions struct {
	// Scope is one space-delimited string, sent as given. The server narrows it
	// to what the client may ask for.
	Scope string
	// State comes back on the redirect unchanged, so the caller can tell the
	// answer is to its own request.
	State string
	// Resource is the API the tokens are meant for.
	Resource string
}

// Pkce is one sign-in's PKCE pair: Challenge goes in the authorization URL,
// Verifier to ExchangeAuthorizationCode.
type Pkce struct {
	// Verifier is 32 random bytes as 43 characters of unpadded base64url.
	Verifier string
	// Challenge is the verifier's SHA-256, as unpadded base64url.
	Challenge string
	// Method is always S256, the only method the server accepts.
	Method string
}

// OauthMetadata is the authorization server's discovery document. An optional
// member the server did not send is nil.
type OauthMetadata struct {
	Issuer                                     string    `json:"issuer"`
	AuthorizationEndpoint                      string    `json:"authorization_endpoint"`
	TokenEndpoint                              string    `json:"token_endpoint"`
	DeviceAuthorizationEndpoint                *string   `json:"device_authorization_endpoint,omitempty"`
	RevocationEndpoint                         *string   `json:"revocation_endpoint,omitempty"`
	ScopesSupported                            *[]string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported                     *[]string `json:"response_types_supported,omitempty"`
	GrantTypesSupported                        *[]string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethodsSupported              *[]string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported          *[]string `json:"token_endpoint_auth_methods_supported,omitempty"`
	AuthorizationResponseIssParameterSupported *bool     `json:"authorization_response_iss_parameter_supported,omitempty"`
	ClientIDMetadataDocumentSupported          *bool     `json:"client_id_metadata_document_supported,omitempty"`
	ServiceDocumentation                       *string   `json:"service_documentation,omitempty"`
}

// DeviceAuthorization is a started device sign-in. ExpiresIn and Interval are
// seconds.
type DeviceAuthorization struct {
	DeviceCode              string  `json:"device_code"`
	UserCode                string  `json:"user_code"`
	VerificationURI         string  `json:"verification_uri"`
	VerificationURIComplete *string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int     `json:"expires_in"`
	Interval                int     `json:"interval"`
}

// TokenResponse is what a completed sign-in or a refresh hands back.
//
// ApikeyID is set when the person picked one of their API keys and may still
// reveal it. Apikey, the key itself, additionally needs a sign-in rather than a
// refresh and a key whose secret can be read back, so ApikeyID without Apikey
// is normal. An empty Scope is present, not nil.
type TokenResponse struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int     `json:"expires_in"`
	RefreshToken *string `json:"refresh_token,omitempty"`
	Scope        *string `json:"scope,omitempty"`
	ApikeyID     *string `json:"mslm:apikey_id,omitempty"`
	Apikey       *string `json:"mslm:apikey,omitempty"`
}

// The two refusals a device sign-in ends on, matched with errors.Is against
// what an OauthAPI call returns.
var (
	ErrOauthAccessDenied = errors.New("internetdata: the person denied the sign-in")
	ErrOauthExpiredToken = errors.New("internetdata: the sign-in code expired")
)

// OauthError is the authorization server refusing a request: a 4xx whose body
// names an OAuth error code. It is never worth retrying as it stands.
//
// errors.As to *Error also works, through Unwrap, with a Kind that follows the
// status; a 401 here means an unregistered client ID, never the API key.
type OauthError struct {
	// ErrorCode is the server's code, such as authorization_pending or
	// invalid_grant. A code this library has never seen is kept as sent.
	ErrorCode string
	// ErrorDescription is the server's explanation, or empty when it sent none.
	ErrorDescription string
	// StatusCode is the HTTP status, or 0 when the refusal was reached locally:
	// PollDeviceToken running past the code's lifetime.
	StatusCode int
}

func (e *OauthError) Error() string {
	if e.ErrorDescription == "" {
		return e.ErrorCode
	}
	return e.ErrorCode + ": " + e.ErrorDescription
}

// Is answers errors.Is for ErrOauthAccessDenied and ErrOauthExpiredToken.
func (e *OauthError) Is(target error) bool {
	switch target {
	case ErrOauthAccessDenied:
		return e.ErrorCode == "access_denied"
	case ErrOauthExpiredToken:
		return e.ErrorCode == "expired_token"
	}
	return false
}

// Unwrap is the same refusal as an *Error, whose Kind follows the status and
// is bad_request when the refusal was local.
func (e *OauthError) Unwrap() error {
	if e.StatusCode == 0 {
		return &Error{Kind: KindBadRequest, Message: e.Error()}
	}
	failure := errorFromResponse(e.StatusCode, http.Header{}, nil)
	failure.Message = e.Error()
	return failure
}

const formContentType = "application/x-www-form-urlencoded"

// Every byte but A-Z a-z 0-9 - . _ ~ as %XX. QueryEscape alone sends a space
// as +, and escapes a literal + as %2B, so every + it leaves is a space.
func percentEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

// url.Values.Encode sends a + in a value as %2B, which the server must receive
// as a +, and a space as +.
func formBody(form url.Values) io.Reader {
	return strings.NewReader(form.Encode())
}

func decodeOauth[T any](res *http.Response, err error, required ...string) (*T, error) {
	if err != nil {
		return nil, errorFromTransport(err)
	}
	body, err := readBody(res)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, oauthFailure(res, body)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, unreadable(res.StatusCode, "the answer was not a JSON object")
	}
	for _, name := range required {
		if _, ok := members[name]; !ok {
			return nil, unreadable(res.StatusCode, "the answer carried no "+name)
		}
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, unreadable(res.StatusCode, "the answer did not decode: "+err.Error())
	}
	return &out, nil
}

func readBody(res *http.Response) ([]byte, error) {
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errorFromTransport(err)
	}
	return body, nil
}

// Only a 4xx whose body is a JSON object with a STRING error member is an OAuth
// refusal. Every 5xx, whatever it says, is the server failing, and is retried
// wherever the operation retries.
func oauthFailure(res *http.Response, body []byte) error {
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		var members map[string]any
		if json.Unmarshal(body, &members) == nil {
			if code, ok := members["error"].(string); ok {
				description, _ := members["error_description"].(string)
				return &OauthError{ErrorCode: code, ErrorDescription: description, StatusCode: res.StatusCode}
			}
		}
	}
	return errorFromResponse(res.StatusCode, res.Header, body)
}
