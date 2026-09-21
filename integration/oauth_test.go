// The published module's OAuth surface against staging, on a keyless client.
// Nothing polls, since nobody approves the sign-in; one device authorization a run.

package integration

import (
	"errors"
	"slices"
	"testing"

	internetdata "github.com/internetdata/sdk-go/v2"
)

const (
	// The only client the server registers, already public in the CLI's source.
	oauthClientID  = "internetdata-cli"
	stagingConsole = "https://app-staging.internetdata.io"
)

func TestOauthMetadataNamesStagingAsTheIssuer(t *testing.T) {
	client := keylessClient(t)

	metadata, err := client.Oauth.Metadata(t.Context())
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if metadata.Issuer != staging {
		t.Errorf("issuer is %q, want %q", metadata.Issuer, staging)
	}
	if metadata.DeviceAuthorizationEndpoint == nil {
		t.Error("device_authorization_endpoint is absent")
	}
	if methods := metadata.CodeChallengeMethodsSupported; methods == nil || !slices.Contains(*methods, "S256") {
		t.Errorf("code_challenge_methods_supported is %v, want it to hold S256", methods)
	}
}

func TestOauthRevokeAcceptsAnyToken(t *testing.T) {
	client := keylessClient(t)

	if err := client.Oauth.Revoke(t.Context(), oauthClientID, "mo_rt_sdk-ci-not-a-token"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
}

func TestOauthAnUnknownDeviceCodeHasExpired(t *testing.T) {
	client := keylessClient(t)

	_, err := client.Oauth.ExchangeDeviceCode(t.Context(), oauthClientID, "mo_dc_sdk-ci-not-a-code")
	var refused *internetdata.OauthError
	expired := errors.Is(err, internetdata.ErrOauthExpiredToken) && errors.As(err, &refused)
	if !expired || refused.StatusCode != 400 {
		t.Fatalf("error was %v, want an expired token answered with status 400", err)
	}
}

// slow_down passes: the server allows 30 a minute per source address, shared
// by everything building on this box or runner.
func TestOauthDeviceAuthorizationStartsASignIn(t *testing.T) {
	client := keylessClient(t)

	device, err := client.Oauth.DeviceAuthorization(t.Context(), oauthClientID,
		internetdata.DeviceAuthorizationOptions{Scope: "account.read"})
	var refused *internetdata.OauthError
	if errors.As(err, &refused) && refused.ErrorCode == "slow_down" {
		t.Logf("refused with slow_down, which passes: %v", err)
		return
	}
	if err != nil {
		t.Fatalf("DeviceAuthorization: %v", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" {
		t.Errorf("device_code %q or user_code %q is empty", device.DeviceCode, device.UserCode)
	}
	// The console's, not the apex's: the API is served at the apex here, and a
	// swap of a leading `api` label once matched nothing and sent every approval
	// to the landing page, which a suffix check alone still passes.
	if device.VerificationURI != stagingConsole+"/device" {
		t.Errorf("verification_uri is %q, want %s/device", device.VerificationURI, stagingConsole)
	}
	if device.ExpiresIn <= 0 || device.Interval <= 0 {
		t.Errorf("expires_in %d and interval %d, want both positive", device.ExpiresIn, device.Interval)
	}
}

// Keyless on purpose: no OAuth request carries a key, so none of these needs
// the staging credential and none skips without it.
func keylessClient(t *testing.T) *internetdata.Client {
	t.Helper()
	client, err := internetdata.New(internetdata.WithBaseURL(staging))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}
