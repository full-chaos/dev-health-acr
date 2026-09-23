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

// OAuthDeviceCodeGrantType is RFC 8628's device-code grant_type value.
const OAuthDeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// ErrOAuthUnavailable reports a storage or dependency failure.
//
// CHAOS-6278: every wrap below carries the real underlying error alongside
// this sentinel (`%w: <label>: %w`, ErrOAuthUnavailable, err), not just a
// static label -- before this change every site here discarded the actual
// dependency error and returned only ErrOAuthUnavailable plus static prose,
// which is what made internal/api's oauth dependency-failure log
// unfixable at the LOGGING layer alone: the real cause (a
// *storage.DependencyErrorClass, for a Postgres failure) was already gone
// by the time it reached the route handler. errors.Is(err,
// ErrOAuthUnavailable) is unaffected by this -- it still wraps first.
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
	// RedirectMismatch is set only for an invalid_redirect_uri refusal: the
	// registered and presented redirect_uri origins (scheme+host+port, no
	// path/query/fragment) so the caller can log what diverged without ever
	// logging a full redirect URI.
	RedirectMismatch *OAuthRedirectMismatch
	// RetryAfter is set for an RFC 8628 slow_down refusal: how long the
	// client must wait before polling again. Zero for every other refusal.
	RetryAfter time.Duration
}

// OAuthRedirectMismatch is diagnostic-only, never a secret: the origins of a
// redirect_uri that failed to match a client's registration.
type OAuthRedirectMismatch struct {
	// Registered is every registered redirect_uri's origin, in order.
	Registered []string
	// Presented is the presented redirect_uri's origin, or "" if it did not
	// parse to one.
	Presented string
}

func (e *OAuthError) Error() string { return "oauth: " + e.Code + " (" + e.Outcome + ")" }

func oauthError(code, outcome string, redirectable bool) *OAuthError {
	return &OAuthError{Code: code, Outcome: outcome, Redirectable: redirectable}
}

// OAuthClientMetadataFetcher resolves a client ID metadata document. A nil
// fetcher disables metadata-document clients.
type OAuthClientMetadataFetcher interface {
	Fetch(ctx context.Context, clientID string) (OAuthClientMetadata, error)
}

// OAuthClientMetadata is the part of a client's metadata this server uses.
type OAuthClientMetadata struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// OAuthConfig configures the OAuth service.
type OAuthConfig struct {
	// Issuer is the authorization server identifier: the acr-api public
	// origin, e.g. https://acr.example.com.
	Issuer string
	// Resources are the protected resources credentials may be bound to:
	// the public URLs of hosted MCP endpoints.
	Resources []string
	// ClientMetadata resolves client ID metadata documents; nil disables them.
	ClientMetadata OAuthClientMetadataFetcher
	Now            func() time.Time
	Random         io.Reader
}

// OAuthService runs registration, authorization and token exchange.
type OAuthService struct {
	store     storage.OAuthStore
	devices   OAuthConsentAuthority
	issuer    string
	resources []string
	metadata  OAuthClientMetadataFetcher
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
	var metadata OAuthClientMetadataFetcher
	if !storage.IsNil(cfg.ClientMetadata) {
		metadata = cfg.ClientMetadata
	}
	return &OAuthService{
		store: store, devices: devices, issuer: cfg.Issuer, resources: resources,
		metadata: metadata, now: cfg.Now, random: cfg.Random,
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

// ValidOAuthConsentURL accepts the web consent page URL /authorize redirects
// to: https (or http on a loopback host, for tests), a host, a non-root path,
// and no user info, query or fragment, so exactly one handle parameter is
// appended. internal/config applies the same rule to ACR_OAUTH_CONSENT_URL
// (a test pins the two against one table).
func ValidOAuthConsentURL(value string) bool {
	// A bare "?" or "#" parses to an empty query or fragment, so refuse the
	// characters themselves: the handle must be the only query parameter.
	if strings.ContainsAny(value, "?#") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Path == "" || parsed.Path == "/" {
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

// ClientMetadataDocumentsSupported reports whether metadata-document client
// IDs are accepted.
func (s *OAuthService) ClientMetadataDocumentsSupported() bool { return s.metadata != nil }

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
// requested values). RFC 7591 §2 defaults grant_types to ["authorization_code"]
// when omitted; CHAOS-6233 additionally accepts the RFC 8628 device-code
// grant. A client declaring ONLY device_code needs no redirect_uri (the
// device grant has no redirect step) and may register with none; a client
// that also names authorization_code (or omits grant_types, the default)
// still needs at least one, exactly as before.
func (s *OAuthService) Register(ctx context.Context, request OAuthRegistrationRequest) (storage.OAuthClient, error) {
	switch request.TokenEndpointAuthMethod {
	case "", "none":
	default:
		return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
	}
	wantsAuthorizationCode := len(request.GrantTypes) == 0
	wantsDeviceCode := false
	for _, grant := range request.GrantTypes {
		switch grant {
		case "authorization_code", "refresh_token":
			wantsAuthorizationCode = true
		case OAuthDeviceCodeGrantType:
			wantsDeviceCode = true
		default:
			return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
	}
	if !wantsAuthorizationCode && !wantsDeviceCode {
		return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
	}
	for _, responseType := range request.ResponseTypes {
		if responseType != "code" {
			return storage.OAuthClient{}, oauthError("invalid_client_metadata", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
	}
	var random [16]byte
	if err := s.read(random[:]); err != nil {
		return storage.OAuthClient{}, fmt.Errorf("%w: read random client id: %w", ErrOAuthUnavailable, err)
	}
	client := storage.OAuthClient{
		ClientID:     storage.NewDynamicOAuthClientID(random),
		ClientName:   strings.TrimSpace(request.ClientName),
		RedirectURIs: append([]string(nil), request.RedirectURIs...),
		CreatedAt:    s.now().UTC(),
	}
	if len(client.RedirectURIs) == 0 && wantsAuthorizationCode {
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
		return storage.OAuthClient{}, fmt.Errorf("%w: register client: %w", ErrOAuthUnavailable, err)
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

// ResolveClient identifies a client by its ID: a dynamic registration, or a
// metadata document when enabled. Anything else is invalid_client.
func (s *OAuthService) ResolveClient(ctx context.Context, clientID string) (OAuthResolvedClient, error) {
	switch {
	case storage.IsDynamicOAuthClientID(clientID):
		client, err := s.store.GetClient(ctx, clientID)
		if errors.Is(err, storage.ErrNotFound) {
			return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClient, false)
		}
		if err != nil {
			return OAuthResolvedClient{}, fmt.Errorf("%w: read client: %w", ErrOAuthUnavailable, err)
		}
		return OAuthResolvedClient{ClientID: client.ClientID, Kind: storage.OAuthClientKindDynamic, Name: client.ClientName, RedirectURIs: client.RedirectURIs}, nil
	case s.metadata != nil && storage.ValidOAuthClientMetadataURL(clientID):
		document, err := s.metadata.Fetch(ctx, clientID)
		if err != nil {
			return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
		if document.ClientID != clientID || len(document.RedirectURIs) == 0 {
			return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
		switch document.TokenEndpointAuthMethod {
		case "", "none":
		default:
			return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClientMetadata, false)
		}
		for _, redirect := range document.RedirectURIs {
			if !storage.ValidOAuthRedirectURI(redirect) {
				return OAuthResolvedClient{}, oauthError("invalid_client", oauthvocab.OutcomeInvalidClientMetadata, false)
			}
		}
		name := strings.TrimSpace(document.ClientName)
		if len(name) > 200 || !utf8.ValidString(name) || strings.ContainsFunc(name, isControlRune) {
			name = ""
		}
		return OAuthResolvedClient{ClientID: clientID, Kind: storage.OAuthClientKindMetadataDocument, Name: name, RedirectURIs: document.RedirectURIs}, nil
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

// OAuthAuthorization is a started authorization: the browser handle the web
// consent page decides it with.
type OAuthAuthorization struct {
	Handle    string
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
	allowLocalhost := client.Kind == storage.OAuthClientKindMetadataDocument
	matched := request.RedirectURI != ""
	if matched {
		matched = false
		for _, candidate := range client.RedirectURIs {
			if storage.MatchOAuthRedirectURI(candidate, request.RedirectURI, allowLocalhost) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return OAuthAuthorization{}, &OAuthError{
			Code: "invalid_request", Outcome: oauthvocab.OutcomeInvalidRedirectURI,
			RedirectMismatch: &OAuthRedirectMismatch{
				Registered: redirectOrigins(client.RedirectURIs),
				Presented:  safeRedirectOrigin(request.RedirectURI),
			},
		}
	}
	// The presented redirect_uri, not the registered template it matched
	// (which may carry no port for a loopback client) — this is the value
	// the code is bound to and /token must see again, exactly.
	redirectURI := request.RedirectURI
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
		return OAuthAuthorization{}, fmt.Errorf("%w: start consent: %w", ErrOAuthUnavailable, err)
	}
	handle, err := s.secret(oauthHandleBytes)
	if err != nil {
		return OAuthAuthorization{}, fmt.Errorf("%w: generate consent handle: %w", ErrOAuthUnavailable, err)
	}
	now := s.now().UTC()
	_, err = s.store.CreateAuthorizationRequest(ctx, storage.OAuthAuthorizationRequest{
		HandleHash: storage.HashOAuthSecret(handle), DeviceCodeHash: device.DeviceCodeHash,
		ClientID: client.ClientID, ClientKind: client.Kind, RedirectURI: redirectURI,
		CodeChallenge: request.CodeChallenge, Resource: resource, Scope: scope, State: request.State,
		CreatedAt: now, ExpiresAt: device.ExpiresAt,
	})
	if err != nil {
		return OAuthAuthorization{}, fmt.Errorf("%w: store authorization request: %w", ErrOAuthUnavailable, err)
	}
	return OAuthAuthorization{Handle: handle, ExpiresAt: device.ExpiresAt, Client: client, Resource: resource, Scope: scope}, nil
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

// Consent refusals the web consent page shows. Each is an OAuthError whose
// Code the consent route returns and whose Outcome the telemetry line carries.
const (
	// OAuthConsentCodeInvalid: no request has this handle, or the decision
	// itself is not allowed (repositories outside the approver's grant).
	OAuthConsentCodeInvalid = "invalid_request"
	// OAuthConsentCodeExpired: the request outlived its ten minutes.
	OAuthConsentCodeExpired = "expired"
	// OAuthConsentCodeCompleted: the request was already decided.
	OAuthConsentCodeCompleted = "already_completed"
)

// OAuthConsentRequest is what the web consent page shows before the signed-in
// user decides: who is asking, where the browser returns, and for what.
type OAuthConsentRequest struct {
	ClientName string
	ClientKind string
	// RedirectOrigin is the scheme, host and port of the verified redirect
	// URI: where the browser goes after the decision.
	RedirectOrigin string
	Resource       string
	Scopes         []string
	ExpiresAt      time.Time
}

// OAuthConsentDecision is a recorded decision. RedirectURL is the client's
// registered redirect URI with code (approved) or error=access_denied
// (denied), state and iss.
type OAuthConsentDecision struct {
	RedirectURL string
	ClientKind  string
}

const oauthConsentDecisionRedacted = "auth.OAuthConsentDecision{redacted}"

func (OAuthConsentDecision) String() string   { return oauthConsentDecisionRedacted }
func (OAuthConsentDecision) GoString() string { return oauthConsentDecisionRedacted }

// pendingConsent returns the undecided, unexpired request behind a handle.
// The returned client kind is set whenever the request was found, so refusals
// after that point still report it. With resumable, a request whose approval
// was recorded but whose code was never attached (a failure between the two)
// is also returned, so the approving user can finish it; the consent
// authority accepts that approval again only from the same user
// (ApproveForOAuth), and a request with a code is never returned.
func (s *OAuthService) pendingConsent(ctx context.Context, handle string, resumable bool) (storage.OAuthAuthorizationRequest, error) {
	if !validOAuthHandle(handle) {
		return storage.OAuthAuthorizationRequest{}, oauthError(OAuthConsentCodeInvalid, oauthvocab.OutcomeInvalidRequest, false)
	}
	request, err := s.store.GetAuthorizationRequest(ctx, storage.HashOAuthSecret(handle))
	if errors.Is(err, storage.ErrNotFound) {
		return storage.OAuthAuthorizationRequest{}, oauthError(OAuthConsentCodeInvalid, oauthvocab.OutcomeInvalidRequest, false)
	}
	if err != nil {
		return storage.OAuthAuthorizationRequest{}, fmt.Errorf("%w: read authorization request: %w", ErrOAuthUnavailable, err)
	}
	if request.CodeHash != nil {
		return request, oauthError(OAuthConsentCodeCompleted, oauthvocab.OutcomeAlreadyCompleted, false)
	}
	if !request.ExpiresAt.After(s.now().UTC()) {
		return request, oauthError(OAuthConsentCodeExpired, oauthvocab.OutcomeExpired, false)
	}
	state, err := s.devices.StateForOAuth(ctx, request.DeviceCodeHash)
	if err != nil {
		return request, fmt.Errorf("%w: read consent: %w", ErrOAuthUnavailable, err)
	}
	switch state {
	case OAuthDeviceStatePending:
		return request, nil
	case OAuthDeviceStateApproved:
		if resumable {
			return request, nil
		}
		return request, oauthError(OAuthConsentCodeCompleted, oauthvocab.OutcomeAlreadyCompleted, false)
	case OAuthDeviceStateExpired:
		return request, oauthError(OAuthConsentCodeExpired, oauthvocab.OutcomeExpired, false)
	default:
		return request, oauthError(OAuthConsentCodeCompleted, oauthvocab.OutcomeAlreadyCompleted, false)
	}
}

// validOAuthHandle accepts the shape Authorize issues: 32 random bytes,
// base64url without padding.
func validOAuthHandle(handle string) bool {
	if len(handle) != base64.RawURLEncoding.EncodedLen(oauthHandleBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(handle)
	return err == nil
}

// ConsentRequest returns what the web consent page shows for an undecided
// request.
func (s *OAuthService) ConsentRequest(ctx context.Context, handle string) (OAuthConsentRequest, string, error) {
	request, err := s.pendingConsent(ctx, handle, true)
	if err != nil {
		return OAuthConsentRequest{}, request.ClientKind, err
	}
	client, err := s.ResolveClient(ctx, request.ClientID)
	if err != nil {
		var refusal *OAuthError
		if errors.As(err, &refusal) {
			return OAuthConsentRequest{}, request.ClientKind, oauthError(OAuthConsentCodeInvalid, oauthvocab.OutcomeInvalidClient, false)
		}
		return OAuthConsentRequest{}, request.ClientKind, err
	}
	origin, err := redirectOrigin(request.RedirectURI)
	if err != nil {
		return OAuthConsentRequest{}, request.ClientKind, fmt.Errorf("%w: stored redirect uri", ErrOAuthUnavailable)
	}
	return OAuthConsentRequest{
		ClientName: client.Name, ClientKind: request.ClientKind, RedirectOrigin: origin,
		Resource: request.Resource, Scopes: strings.Fields(request.Scope), ExpiresAt: request.ExpiresAt.UTC(),
	}, request.ClientKind, nil
}

func redirectOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrOAuthUnavailable
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// redirectOrigins returns each redirect URI's origin (scheme+host+port), for
// diagnostics only; an unparseable entry contributes "".
func redirectOrigins(uris []string) []string {
	origins := make([]string, len(uris))
	for i, uri := range uris {
		origins[i] = safeRedirectOrigin(uri)
	}
	return origins
}

// safeRedirectOrigin returns a redirect URI's origin for diagnostics, or ""
// when it does not parse to one — never the full URI (path, query, secrets).
func safeRedirectOrigin(raw string) string {
	origin, err := redirectOrigin(raw)
	if err != nil {
		return ""
	}
	return origin
}

// ApproveConsent records the signed-in user's approval of the request behind
// a handle (org from the principal, the chosen repositories within the
// principal's grant) and issues its one authorization code. A request is
// decided at most once: once a code is attached, or after a deny, every
// later decision is already_completed. If attaching the code fails after the
// approval was recorded, the same user can approve again to finish.
func (s *OAuthService) ApproveConsent(ctx context.Context, handle string, principal storage.Principal, repositoryScopes []string) (OAuthConsentDecision, error) {
	request, err := s.pendingConsent(ctx, handle, true)
	if err != nil {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, err
	}
	if err := s.devices.ApproveForOAuth(ctx, principal, request.DeviceCodeHash, repositoryScopes); err != nil {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, consentDecisionError(err)
	}
	code, err := s.secret(oauthCodeBytes)
	if err != nil {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, fmt.Errorf("%w: generate authorization code: %w", ErrOAuthUnavailable, err)
	}
	_, err = s.store.IssueAuthorizationCode(ctx, request.HandleHash, storage.HashOAuthSecret(code), s.now().UTC().Add(storage.OAuthAuthorizationCodeTTL))
	if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrNotFound) {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, oauthError(OAuthConsentCodeCompleted, oauthvocab.OutcomeAlreadyCompleted, false)
	}
	if err != nil {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, fmt.Errorf("%w: issue code: %w", ErrOAuthUnavailable, err)
	}
	return OAuthConsentDecision{ClientKind: request.ClientKind, RedirectURL: s.redirectURL(request, url.Values{"code": {code}})}, nil
}

// DenyConsent records the signed-in user's denial; the browser returns to the
// client with error=access_denied.
func (s *OAuthService) DenyConsent(ctx context.Context, handle string, principal storage.Principal) (OAuthConsentDecision, error) {
	request, err := s.pendingConsent(ctx, handle, false)
	if err != nil {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, err
	}
	if err := s.devices.DenyForOAuth(ctx, principal, request.DeviceCodeHash); err != nil {
		return OAuthConsentDecision{ClientKind: request.ClientKind}, consentDecisionError(err)
	}
	return OAuthConsentDecision{ClientKind: request.ClientKind, RedirectURL: s.redirectURL(request, url.Values{"error": {"access_denied"}})}, nil
}

// consentDecisionError maps a consent authority failure to its refusal.
func consentDecisionError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidDeviceFlow):
		return oauthError(OAuthConsentCodeInvalid, oauthvocab.OutcomeInvalidRequest, false)
	case errors.Is(err, storage.ErrDeviceAuthorizationConflict):
		return oauthError(OAuthConsentCodeCompleted, oauthvocab.OutcomeAlreadyCompleted, false)
	case errors.Is(err, storage.ErrDeviceAuthorizationExpired), errors.Is(err, storage.ErrDeviceAuthorizationNotFound):
		return oauthError(OAuthConsentCodeExpired, oauthvocab.OutcomeExpired, false)
	default:
		return fmt.Errorf("%w: record consent: %w", ErrOAuthUnavailable, err)
	}
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
		return OAuthToken{}, fmt.Errorf("%w: consume code: %w", ErrOAuthUnavailable, err)
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
		return OAuthToken{ClientKind: pending.ClientKind}, fmt.Errorf("%w: issue credential: %w", ErrOAuthUnavailable, err)
	}
	lifetime := DeviceCredentialLifetime
	if issued.Credential.ExpiresAt != nil {
		lifetime = issued.Credential.ExpiresAt.Sub(s.now().UTC())
	}
	return OAuthToken{Issued: issued, ExpiresIn: lifetime, Scope: scope, ClientKind: pending.ClientKind}, nil
}

// OAuthDeviceAuthorizationRequest carries POST /device_authorization's form
// parameters (RFC 8628 §3.1).
type OAuthDeviceAuthorizationRequest struct {
	ClientID string
	Scope    string
	Resource string
}

// OAuthDeviceAuthorizationStart is a started RFC 8628 device authorization:
// what POST /device_authorization returns.
type OAuthDeviceAuthorizationStart struct {
	DeviceCode string
	UserCode   string
	ExpiresIn  time.Duration
	Interval   time.Duration
	ClientKind string
}

const oauthDeviceAuthorizationStartRedacted = "auth.OAuthDeviceAuthorizationStart{redacted}"

func (OAuthDeviceAuthorizationStart) String() string   { return oauthDeviceAuthorizationStartRedacted }
func (OAuthDeviceAuthorizationStart) GoString() string { return oauthDeviceAuthorizationStartRedacted }

// StartDeviceAuthorization validates an RFC 8628 device_authorization
// request and starts it: resolves the client (the same dynamic-registration
// or client ID metadata document resolution Authorize uses), a supported
// scope, and a resource this server issues for, then starts the underlying
// device authorization and records the client, resource and scope /token
// needs when the client polls back with only the device_code. Unlike
// Authorize, no refusal here is redirectable: RFC 8628 has no redirect step,
// so every refusal is answered directly.
func (s *OAuthService) StartDeviceAuthorization(ctx context.Context, request OAuthDeviceAuthorizationRequest) (OAuthDeviceAuthorizationStart, error) {
	client, err := s.ResolveClient(ctx, request.ClientID)
	if err != nil {
		return OAuthDeviceAuthorizationStart{}, err
	}
	if len(request.Scope) > maxOAuthScopeLength || strings.ContainsFunc(request.Scope, isControlRune) {
		return OAuthDeviceAuthorizationStart{ClientKind: client.Kind}, oauthError("invalid_request", oauthvocab.OutcomeInvalidRequest, false)
	}
	scope, ok := NormalizeOAuthScope(request.Scope)
	if !ok {
		return OAuthDeviceAuthorizationStart{ClientKind: client.Kind}, oauthError("invalid_scope", oauthvocab.OutcomeInvalidScope, false)
	}
	resource, ok := s.resolveResource(request.Resource)
	if !ok {
		return OAuthDeviceAuthorizationStart{ClientKind: client.Kind}, oauthError("invalid_target", oauthvocab.OutcomeInvalidTarget, false)
	}
	start, err := s.devices.StartDeviceGrant(ctx)
	if err != nil {
		return OAuthDeviceAuthorizationStart{ClientKind: client.Kind}, fmt.Errorf("%w: start device grant: %w", ErrOAuthUnavailable, err)
	}
	now := s.now().UTC()
	_, err = s.store.CreateDeviceGrant(ctx, storage.OAuthDeviceGrant{
		DeviceCodeHash: start.DeviceCodeHash, ClientID: client.ClientID, ClientKind: client.Kind,
		Resource: resource, Scope: scope, CreatedAt: now, ExpiresAt: start.ExpiresAt,
	})
	if err != nil {
		return OAuthDeviceAuthorizationStart{ClientKind: client.Kind}, fmt.Errorf("%w: store device grant: %w", ErrOAuthUnavailable, err)
	}
	return OAuthDeviceAuthorizationStart{
		DeviceCode: start.DeviceCode, UserCode: start.UserCode, ExpiresIn: storage.DeviceAuthorizationTTL,
		Interval: start.Interval, ClientKind: client.Kind,
	}, nil
}

// OAuthDeviceTokenRequest carries an RFC 8628 device-code /token poll's form
// parameters.
type OAuthDeviceTokenRequest struct {
	GrantType  string
	DeviceCode string
	ClientID   string
}

// ExchangeDeviceCode answers one RFC 8628 /token poll for a device grant
// started by StartDeviceAuthorization: authorization_pending while
// undecided, slow_down if polled faster than the interval, access_denied or
// expired_token if the user denied it or it timed out, or a token once
// approved. Unlike Exchange (authorization_code, single-use, consumed
// before any other check), a device-code poll is retried on purpose, so
// nothing here is consumed until the grant is actually approved.
func (s *OAuthService) ExchangeDeviceCode(ctx context.Context, request OAuthDeviceTokenRequest) (OAuthToken, error) {
	if request.GrantType != OAuthDeviceCodeGrantType {
		return OAuthToken{}, oauthError("unsupported_grant_type", oauthvocab.OutcomeUnsupportedGrantType, false)
	}
	if request.DeviceCode == "" || len(request.DeviceCode) > 128 {
		return OAuthToken{}, oauthError("invalid_grant", oauthvocab.OutcomeInvalidGrant, false)
	}
	normalized, ok := NormalizeDeviceCode(request.DeviceCode)
	if !ok {
		return OAuthToken{}, oauthError("invalid_grant", oauthvocab.OutcomeInvalidGrant, false)
	}
	hash := storage.HashDeviceCode(normalized)
	grant, err := s.store.GetDeviceGrant(ctx, hash)
	if errors.Is(err, storage.ErrNotFound) {
		return OAuthToken{}, oauthError("invalid_grant", oauthvocab.OutcomeInvalidGrant, false)
	}
	if err != nil {
		return OAuthToken{}, fmt.Errorf("%w: read device grant: %w", ErrOAuthUnavailable, err)
	}
	refuse := func(code, outcome string) (OAuthToken, error) {
		return OAuthToken{ClientKind: grant.ClientKind}, oauthError(code, outcome, false)
	}
	if request.ClientID != grant.ClientID {
		return refuse("invalid_grant", oauthvocab.OutcomeClientMismatch)
	}
	scope, ok := NormalizeOAuthScope(grant.Scope)
	if !ok {
		return refuse("invalid_grant", oauthvocab.OutcomeInvalidGrant)
	}
	issued, err := s.devices.PollDeviceGrant(ctx, hash, grant.Resource, strings.Fields(scope))
	if err != nil {
		var pollError *DevicePollError
		if errors.As(err, &pollError) {
			return OAuthToken{ClientKind: grant.ClientKind}, mapDevicePollOutcome(pollError)
		}
		return OAuthToken{ClientKind: grant.ClientKind}, fmt.Errorf("%w: poll device grant: %w", ErrOAuthUnavailable, err)
	}
	lifetime := DeviceCredentialLifetime
	if issued.Credential.ExpiresAt != nil {
		lifetime = issued.Credential.ExpiresAt.Sub(s.now().UTC())
	}
	return OAuthToken{Issued: issued, ExpiresIn: lifetime, Scope: scope, ClientKind: grant.ClientKind}, nil
}

// mapDevicePollOutcome maps a device poll refusal to its RFC 8628 OAuth
// error code and telemetry outcome. The wire error codes ARE the
// DevicePollErrorKind values -- RFC 8628 defines the same strings this
// package's legacy JSON device flow already used (DevicePollErrorKind's own
// doc comment).
func mapDevicePollOutcome(pollError *DevicePollError) error {
	switch pollError.Kind {
	case DevicePollAuthorizationPending:
		return oauthDeviceGrantError(DevicePollAuthorizationPending, oauthvocab.OutcomeAuthorizationPending, 0)
	case DevicePollSlowDown:
		return oauthDeviceGrantError(DevicePollSlowDown, oauthvocab.OutcomeSlowDown, pollError.RetryAfter)
	case DevicePollAccessDenied:
		return oauthDeviceGrantError(DevicePollAccessDenied, oauthvocab.OutcomeAccessDenied, 0)
	case DevicePollExpiredToken:
		return oauthDeviceGrantError(DevicePollExpiredToken, oauthvocab.OutcomeExpired, 0)
	default:
		return oauthDeviceGrantError(DevicePollInvalidGrant, oauthvocab.OutcomeInvalidGrant, 0)
	}
}

func oauthDeviceGrantError(kind DevicePollErrorKind, outcome string, retryAfter time.Duration) error {
	return &OAuthError{Code: string(kind), Outcome: outcome, RetryAfter: retryAfter}
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
