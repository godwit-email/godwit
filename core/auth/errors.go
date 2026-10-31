package auth

import "errors"

var (
	// ErrOAuthNotConfigured: the provider has no OAuth client ID in this build.
	ErrOAuthNotConfigured = errors.New("oauth: no client ID is configured for this provider")
	ErrRedirectPortBusy   = errors.New("oauth: the sign-in's callback port is already in use")
	// ErrFlowExpired: the user did not finish signing in within FlowTimeout.
	ErrFlowExpired = errors.New("oauth: sign-in timed out")
	// ErrInvalidGrant: the token endpoint answered invalid_grant (revoked, expired or used).
	ErrInvalidGrant = errors.New("oauth: the authorization is no longer valid")
	// ErrInvalidClient: the token endpoint answered invalid_client (an unknown client, or a wrong secret).
	ErrInvalidClient = errors.New("oauth: the token endpoint does not accept this client (invalid_client)")
)

// ErrAccessDenied: the user declined at the provider.
var ErrAccessDenied = errors.New("oauth: access was denied at the provider")

// ErrScopeMissing: the token response did not include the required scope.
var ErrScopeMissing = errors.New("oauth: the sign-in did not grant the access this app needs")
