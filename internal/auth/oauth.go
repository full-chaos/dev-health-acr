package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OAuth 2.1 authorization-code login with PKCE for hosted MCP clients
// (MCP authorization, RFC 6749, RFC 7636, RFC 8707, RFC 7591, RFC 9207).
//
// acr-api is the authorization server; the hosted MCP endpoint is the only
// protected resource it issues credentials for. The issued credential is the
// ordinary opaque fcacr_ bearer with the device flow's org and repository
// scopes, bound to the resource the request named.

// OAuthScope is the scope an OAuth request gets when it names none: every
// scope the approval grants.
var OAuthScope = ScopeContextRead + " " + ScopeEvidenceRead

// oauthScopes are the scopes an OAuth request may ask for, in canonical order.
var oauthScopes = oauthvocab.ScopeVocabulary()

// NormalizeOAuthScope parses a space-separated scope parameter: empty means
// every supported scope; otherwise each token must be a supported scope. The
// result is canonical (supported order, no duplicates, space-joined).
func NormalizeOAuthScope(raw string) (string, bool) {
	requested := strings.Fields(raw)
	if len(requested) == 0 {
		return OAuthScope, true
	}
	for _, scope := range requested {
		if !slices.Contains(oauthScopes, scope) {
			return "", false
		}
	}
	granted := make([]string, 0, len(oauthScopes))
	for _, scope := range oauthScopes {
		if slices.Contains(requested, scope) {
			granted = append(granted, scope)
		}
	}
	return strings.Join(granted, " "), true
}

const (
	oauthHandleBytes       = 32
	oauthCodeBytes         = 32
	oauthVerifierMinLength = 43
	oauthVerifierMaxLength = 128
	maxOAuthStateLength    = 1024
	maxOAuthScopeLength    = 512
)

// ErrOAuthUnavailable reports a storage or dependency failure.
var ErrOAuthUnavailable = errors.New("oauth service unavailable")

// OAuthError is a refusal with an OAuth error code (RFC 6749 §4.1.2.1, §5.2,
// RFC 7591 §3.2.2, RFC 8707 §2) and the closed telemetry outcome behind it.
// Redirectable reports whether the client's redirect URI was verified, so
// the refusal may be sent to it; otherwise it is shown to the user only.
type OAuthError struct {
	Code         string
	Outcome      string
	Redirectable bool
	// RedirectURL is set for a redirectable refusal: the client's registered
	// redirect URI (never the value the request carried) with error, state
	// and iss.
	RedirectURL string
}

func (e *OAuthError) Error() string { return "oauth: " + e.Code + " (" + e.Outcome + ")" }

func oauthError(code, outcome string, redirectable bool) *OAuthError {
	return &OAuthError{Code: code, Outcome: outcome, Redirectable: redirectable}
}

// OAuthConfig configures the OAuth service.
type OAuthConfig struct {
	// Issuer is the authorization server identifier: the acr-api public
	// origin, e.g. https://acr.example.com.
	Issuer string
	// Resources are the protected resources credentials may be bound to:
	// the public URLs of hosted MCP endpoints.
	Resources []string
	Now       func() time.Time
	Random    io.Reader
}

// OAuthService runs registration, authorization and token exchange.
type OAuthService struct {
	store     storage.OAuthStore
	devices   OAuthConsentAuthority
	issuer    string
	resources []string
	now       func() time.Time
	randomMu  sync.Mutex
	random    io.Reader
}

// ErrInvalidOAuthConfig reports an unusable OAuth configuration.
var ErrInvalidOAuthConfig = errors.New("invalid oauth configuration")

// NewOAuthService validates the configuration and builds the service.
func NewOAuthService(store storage.OAuthStore, devices OAuthConsentAuthority, cfg OAuthConfig) (*OAuthService, error) {
	if storage.IsNil(store) || storage.IsNil(devices) {
		return nil, ErrInvalidOAuthConfig
	}
	if !ValidOAuthIssuer(cfg.Issuer) || len(cfg.Resources) == 0 {
		return nil, ErrInvalidOAuthConfig
	}
	resources := make([]string, 0, len(cfg.Resources))
	for _, resource := range cfg.Resources {
		if !storage.ValidOAuthResource(resource) || slices.Contains(resources, resource) {
			return nil, ErrInvalidOAuthConfig
		}
		resources = append(resources, resource)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if storage.IsNil(cfg.Random) {
		cfg.Random = rand.Reader
	}
	return &OAuthService{
		store: store, devices: devices, issuer: cfg.Issuer, resources: resources,
		now: cfg.Now, random: cfg.Random,
	}, nil
}

// ValidOAuthIssuer accepts an HTTPS origin (or an HTTP loopback origin for
// tests) with no path, query or fragment (RFC 8414 §2 allows a path; this
// server does not use one, so its metadata sits at the root well-known URL).
func ValidOAuthIssuer(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.HasSuffix(value, "/") {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return true
	case "http":
		host := parsed.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	default:
		return false
	}
}

// Issuer returns the authorization server identifier.
func (s *OAuthService) Issuer() string { return s.issuer }

// Resources returns the protected resources this server issues for.
func (s *OAuthService) Resources() []string { return append([]string(nil), s.resources...) }

// OAuthRegistrationRequest is an RFC 7591 registration request.
type OAuthRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// Register stores a new public client. Registration never issues a secret:
// a client asking for a confidential authentication method is refused. A
// request naming refresh_token among its grant types is accepted and answered
// with authorization_code alone (RFC 7591 §3.2.1 lets the server replace
// requested values).
func (s *OAuthService) Register(ctx context.Context, request OAuthRegistrationRequest) (storage.OAuthClient, error) {
	switch request.TokenEndpointAuthMethod {
	case "", "none":
	default:
		return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
	}
	for _, grant := range request.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
	}
	for _, responseType := range request.ResponseTypes {
		if responseType != "code" {
			return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
	}
	var random [16]byte
	if err := s.read(random[:]); err != nil {
		return storage.OAuthClient{}, ErrOAuthUnavailable
	}
	client := storage.OAuthClient{
		ClientID:     storage.NewDynamicOAuthClientID(random),
		ClientName:   strings.TrimSpace(request.ClientName),
		RedirectURIs: append([]string(nil), request.RedirectURIs...),
		CreatedAt:    s.now().UTC(),
	}
	if len(client.RedirectURIs) == 0 {
		return storage.OAuthClient{}, oauthError("invalid_redirect_uri", oauthvocab.OutcomeInvalidRedirectURI, false)
	}
	for _, redirect := range client.RedirectURIs {
		if !storage.ValidOAuthRedirectURI(redirect) {
			return storage.OAuthClient{}, oauthError("invalid_redirect_uri", oauthvocab.OutcomeInvalidRedirectURI, false)
		}
	}
	if storage.ValidateOAuthClient(client) != nil {
		return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
	}
	stored, err := s.store.RegisterClient(ctx, client)
	if err != nil {
		return storage.OAuthClient{}, fmt.Errorf("%w: register client", ErrOAuthUnavailable)
	}
	return stored, nil
}

// OAuthResolvedClient is a client identified for one request.
type OAuthResolvedClient struct {
	ClientID     string
	Kind         string
	Name         string
	RedirectURIs []string
}

// ResolveClient identifies a client by its ID: a dynamic registration.
// Anything else is invalid_client.
func (s *OAuthService) ResolveClient(ctx context.Context, clientID string) (OAuthResolvedClient, error) {
	switch {
	case storage.IsDynamicOAuthClientID(clientID):
		client, err := s.store.GetClient(ctx, clientID)
		if errors.Is(err, storage.ErrNotFound) {
			return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClient, false)
		}
		if err != nil {
			return OAuthResolvedClient{}, fmt.Errorf("%w: read client", ErrOAuthUnavailable)
		}
		return OAuthResolvedClient{ClientID: client.ClientID, Kind: storage.OAuthClientKindDynamic, Name: client.ClientName, RedirectURIs: client.RedirectURIs}, nil
	default:
		return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClient, false)
	}
}

func isControlRune(r rune) bool { return r < 0x20 || r == 0x7f }

// OAuthAuthorizeRequest carries the /authorize query parameters.
type OAuthAuthorizeRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
	State               string
}

// OAuthAuthorization is a started authorization: the browser handle the
// consent page checks with and the user code the user approves.
type OAuthAuthorization struct {
	Handle    string
	UserCode  string
	ExpiresAt time.Time
	Client    OAuthResolvedClient
	Resource  string
	Scope     string
}

const oauthAuthorizationRedacted = "auth.OAuthAuthorization{redacted}"

func (OAuthAuthorization) String() string   { return oauthAuthorizationRedacted }
func (OAuthAuthorization) GoString() string { return oauthAuthorizationRedacted }

// Authorize validates an authorization request and starts its consent.
// Client and redirect URI are verified first; until both are, no refusal is
// redirectable (RFC 6749 §4.1.2.1).
func (s *OAuthService) Authorize(ctx context.Context, request OAuthAuthorizeRequest) (OAuthAuthorization, error) {
	client, err := s.ResolveClient(ctx, request.ClientID)
	if err != nil {
		return OAuthAuthorization{}, err
	}
	registered := slices.Index(client.RedirectURIs, request.RedirectURI)
	if request.RedirectURI == "" || registered < 0 {
		return OAuthAuthorization{}, oauthError("invalid_request", oauthvocab.OutcomeInvalidRedirectURI, false)
	}
	redirectURI := client.RedirectURIs[registered]
	refuse := func(code, outcome string) (OAuthAuthorization, error) {
		return OAuthAuthorization{}, &OAuthError{
			Code: code, Outcome: outcome, Redirectable: true,
			RedirectURL: s.redirectURL(storage.OAuthAuthorizationRequest{RedirectURI: redirectURI, State: request.State}, url.Values{"error": {code}}),
		}
	}
	if request.ResponseType != "code" {
		return refuse("unsupported_response_type", oauthvocab.OutcomeUnsupportedResponseType)
	}
	if request.CodeChallengeMethod != "S256" || !storage.ValidOAuthCodeChallenge(request.CodeChallenge) {
		return refuse("invalid_request", oauthvocab.OutcomePKCERequired)
	}
	resource, ok := s.resolveResource(request.Resource)
	if !ok {
		return refuse("invalid_target", oauthvocab.OutcomeInvalidTarget)
	}
	if len(request.State) > maxOAuthStateLength || len(request.Scope) > maxOAuthScopeLength ||
		!utf8.ValidString(request.State) || strings.ContainsFunc(request.State, isControlRune) || strings.ContainsFunc(request.Scope, isControlRune) {
		return refuse("invalid_request", oauthvocab.OutcomeInvalidRequest)
	}
	scope, ok := NormalizeOAuthScope(request.Scope)
	if !ok {
		return refuse("invalid_scope", oauthvocab.OutcomeInvalidScope)
	}
	device, err := s.devices.StartForOAuth(ctx)
	if err != nil {
		return OAuthAuthorization{}, fmt.Errorf("%w: start consent", ErrOAuthUnavailable)
	}
	handle, err := s.secret(oauthHandleBytes)
	if err != nil {
		return OAuthAuthorization{}, ErrOAuthUnavailable
	}
	now := s.now().UTC()
	_, err = s.store.CreateAuthorizationRequest(ctx, storage.OAuthAuthorizationRequest{
		HandleHash: storage.HashOAuthSecret(handle), DeviceCodeHash: device.DeviceCodeHash,
		ClientID: client.ClientID, ClientKind: client.Kind, RedirectURI: redirectURI,
		CodeChallenge: request.CodeChallenge, Resource: resource, Scope: scope, State: request.State,
		CreatedAt: now, ExpiresAt: device.ExpiresAt,
	})
	if err != nil {
		return OAuthAuthorization{}, fmt.Errorf("%w: store authorization request", ErrOAuthUnavailable)
	}
	return OAuthAuthorization{Handle: handle, UserCode: device.UserCode, ExpiresAt: device.ExpiresAt, Client: client, Resource: resource, Scope: scope}, nil
}

// resolveResource returns the resource a request binds to: the one it named,
// when this server issues for it, or the only configured resource when it
// named none.
func (s *OAuthService) resolveResource(requested string) (string, bool) {
	if requested == "" {
		if len(s.resources) == 1 {
			return s.resources[0], true
		}
		return "", false
	}
	if slices.Contains(s.resources, requested) {
		return requested, true
	}
	return "", false
}

// OAuthConsentState is what the consent page learns on each check.
type OAuthConsentState string

const (
	OAuthConsentPending  OAuthConsentState = "pending"
	OAuthConsentApproved OAuthConsentState = "approved"
	OAuthConsentDenied   OAuthConsentState = "denied"
	OAuthConsentExpired  OAuthConsentState = "expired"
)

// OAuthConsent is the result of one consent check. RedirectURL is set when
// the browser must now go back to the client: with a code once approved, or
// with access_denied once denied.
type OAuthConsent struct {
	State       OAuthConsentState
	RedirectURL string
	ClientKind  string
}

// Consent checks the approval of the request behind a browser handle. The
// first check after approval issues the one authorization code; the store
// attaches a code only to a request that has none, so every later check is
// already_completed and a code is never issued twice.
func (s *OAuthService) Consent(ctx context.Context, handle string) (OAuthConsent, error) {
	if len(handle) == 0 || len(handle) > 128 {
		return OAuthConsent{}, oauthError("invalid_request", oauthvocab.OutcomeInvalidRequest, false)
	}
	request, err := s.store.GetAuthorizationRequest(ctx, storage.HashOAuthSecret(handle))
	if errors.Is(err, storage.ErrNotFound) {
		return OAuthConsent{}, oauthError("invalid_request", oauthvocab.OutcomeInvalidRequest, false)
	}
	if err != nil {
		return OAuthConsent{}, fmt.Errorf("%w: read authorization request", ErrOAuthUnavailable)
	}
	now := s.now().UTC()
	if !request.ExpiresAt.After(now) {
		return OAuthConsent{State: OAuthConsentExpired, ClientKind: request.ClientKind}, nil
	}
	state, err := s.devices.StateForOAuth(ctx, request.DeviceCodeHash)
	if err != nil {
		return OAuthConsent{}, fmt.Errorf("%w: read consent", ErrOAuthUnavailable)
	}
	switch state {
	case OAuthDeviceStatePending:
		return OAuthConsent{State: OAuthConsentPending, ClientKind: request.ClientKind}, nil
	case OAuthDeviceStateDenied:
		return OAuthConsent{State: OAuthConsentDenied, ClientKind: request.ClientKind, RedirectURL: s.redirectURL(request, url.Values{"error": {"access_denied"}})}, nil
	case OAuthDeviceStateApproved:
	default:
		return OAuthConsent{State: OAuthConsentExpired, ClientKind: request.ClientKind}, nil
	}
	code, err := s.secret(oauthCodeBytes)
	if err != nil {
		return OAuthConsent{}, ErrOAuthUnavailable
	}
	_, err = s.store.IssueAuthorizationCode(ctx, request.HandleHash, storage.HashOAuthSecret(code), now.Add(storage.OAuthAuthorizationCodeTTL))
	if errors.Is(err, storage.ErrConflict) {
		return OAuthConsent{ClientKind: request.ClientKind}, oauthError("invalid_request", oauthvocab.OutcomeAlreadyCompleted, false)
	}
	if err != nil {
		return OAuthConsent{}, fmt.Errorf("%w: issue code", ErrOAuthUnavailable)
	}
	return OAuthConsent{State: OAuthConsentApproved, ClientKind: request.ClientKind, RedirectURL: s.redirectURL(request, url.Values{"code": {code}})}, nil
}

func (s *OAuthService) redirectURL(request storage.OAuthAuthorizationRequest, values url.Values) string {
	target, err := url.Parse(request.RedirectURI)
	if err != nil {
		return ""
	}
	query := target.Query()
	for key, value := range values {
		query[key] = value
	}
	if request.State != "" {
		query.Set("state", request.State)
	}
	query.Set("iss", s.issuer)
	target.RawQuery = query.Encode()
	return target.String()
}

// OAuthTokenRequest carries the token endpoint's form parameters.
type OAuthTokenRequest struct {
	GrantType    string
	Code         string
	RedirectURI  string
	ClientID     string
	CodeVerifier string
	Resource     string
}

// OAuthToken is an issued access token.
type OAuthToken struct {
	Issued     IssuedCredential
	ExpiresIn  time.Duration
	Scope      string
	ClientKind string
}

const oauthTokenRedacted = "auth.OAuthToken{redacted}"

func (OAuthToken) String() string   { return oauthTokenRedacted }
func (OAuthToken) GoString() string { return oauthTokenRedacted }

// Exchange redeems an authorization code. The code is consumed before any
// other check, so a refused exchange (wrong verifier, wrong redirect URI,
// wrong resource) spends it and cannot be retried.
func (s *OAuthService) Exchange(ctx context.Context, request OAuthTokenRequest) (OAuthToken, error) {
	if request.GrantType != "authorization_code" {
		return OAuthToken{}, oauthError("unsupported_grant_type", oauthvocab.OutcomeUnsupportedGrantType, false)
	}
	if request.Code == "" || len(request.Code) > 128 {
		return OAuthToken{}, oauthError("invalid_grant", oauthvocab.OutcomeInvalidGrant, false)
	}
	pending, err := s.store.ConsumeAuthorizationCode(ctx, storage.HashOAuthSecret(request.Code))
	if errors.Is(err, storage.ErrOAuthAuthorizationCodeUnavailable) {
		return OAuthToken{}, oauthError("invalid_grant", oauthvocab.OutcomeInvalidGrant, false)
	}
	if err != nil {
		return OAuthToken{}, fmt.Errorf("%w: consume code", ErrOAuthUnavailable)
	}
	refuse := func(code, outcome string) (OAuthToken, error) {
		return OAuthToken{ClientKind: pending.ClientKind}, oauthError(code, outcome, false)
	}
	if request.ClientID != pending.ClientID {
		return refuse("invalid_grant", oauthvocab.OutcomeClientMismatch)
	}
	if request.RedirectURI != pending.RedirectURI {
		return refuse("invalid_grant", oauthvocab.OutcomeRedirectMismatch)
	}
	if !VerifyPKCES256(request.CodeVerifier, pending.CodeChallenge) {
		return refuse("invalid_grant", oauthvocab.OutcomePKCEMismatch)
	}
	if request.Resource != "" && request.Resource != pending.Resource {
		return refuse("invalid_target", oauthvocab.OutcomeResourceMismatch)
	}
	scope, ok := NormalizeOAuthScope(pending.Scope)
	if !ok {
		return refuse("invalid_grant", oauthvocab.OutcomeInvalidGrant)
	}
	issued, err := s.devices.RedeemForResource(ctx, pending.DeviceCodeHash, pending.Resource, strings.Fields(scope))
	if errors.Is(err, ErrOAuthDeviceNotApproved) {
		return refuse("invalid_grant", oauthvocab.OutcomeInvalidGrant)
	}
	if err != nil {
		return OAuthToken{ClientKind: pending.ClientKind}, fmt.Errorf("%w: issue credential", ErrOAuthUnavailable)
	}
	lifetime := DeviceCredentialLifetime
	if issued.Credential.ExpiresAt != nil {
		lifetime = issued.Credential.ExpiresAt.Sub(s.now().UTC())
	}
	return OAuthToken{Issued: issued, ExpiresIn: lifetime, Scope: scope, ClientKind: pending.ClientKind}, nil
}

// VerifyPKCES256 reports whether a code verifier (RFC 7636 §4.1: 43 to 128
// unreserved characters) hashes to the S256 challenge.
func VerifyPKCES256(verifier, challenge string) bool {
	if len(verifier) < oauthVerifierMinLength || len(verifier) > oauthVerifierMaxLength || !storage.ValidOAuthCodeChallenge(challenge) {
		return false
	}
	for _, r := range verifier {
		unreserved := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_' || r == '~'
		if !unreserved {
			return false
		}
	}
	digest := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

func (s *OAuthService) secret(size int) (string, error) {
	buf := make([]byte, size)
	if err := s.read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *OAuthService) read(buf []byte) error {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	_, err := io.ReadFull(s.random, buf)
	return err
}
