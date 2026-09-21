package internetdata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/internetdata/sdk-go/v2/internal/api"
)

// OauthAPI signs a person in on their own machine with the OAuth device flow,
// so a program can be handed one of their API keys instead of asking them to
// paste it. Reached through Client.Oauth.
//
// Every call takes a client ID, which is issued on request from
// support@internetdata.io. None of these requests carries the client's API
// key, and none needs one.
type OauthAPI struct {
	// A second generated client, built without the API key's request editor.
	api     *api.ClientWithResponses
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
// time the server answers slow_down. It stops at the first answer that is
// neither pending nor slow_down: a denial satisfies errors.Is(err,
// ErrOauthAccessDenied), an expired code errors.Is(err, ErrOauthExpiredToken),
// and so does running out of device.ExpiresIn, counted from this call, with a
// StatusCode of 0. Canceling ctx stops the wait and any request in flight.
func (o *OauthAPI) PollDeviceToken(
	ctx context.Context, clientID string, device *DeviceAuthorization,
) (*TokenResponse, error) {
	if device == nil {
		return nil, &Error{Kind: KindBadRequest, Message: "a device authorization is required"}
	}
	interval := time.Duration(device.Interval) * time.Second
	if device.Interval < 1 {
		interval = 5 * time.Second
	}
	deadline := o.now().Add(time.Duration(device.ExpiresIn) * time.Second)
	for {
		if err := o.sleep(ctx, interval); err != nil {
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
			interval += 5 * time.Second
		default:
			return nil, err
		}
	}
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
		return nil, errorFromTransport(err)
	}
	for _, name := range required {
		if _, ok := members[name]; !ok {
			return nil, &Error{
				Kind: KindServerError, Message: "the answer carried no " + name, StatusCode: res.StatusCode,
			}
		}
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errorFromTransport(err)
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
