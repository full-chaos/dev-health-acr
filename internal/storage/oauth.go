package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
)

// OAuth authorization-code login for the hosted MCP endpoint.
//
// An MCP client (Claude Code, Codex, ...) discovers acr-api as its OAuth
// authorization server and runs the authorization-code grant with PKCE. The
// browser consent step reuses the device authorization record: /authorize
// starts one (its raw device and user codes are generated and discarded, so
// neither the device grant nor the typed-code approval page can use it) and
// sends the browser to the web consent page with the request's handle; the
// signed-in user approves or denies it there, and the token endpoint redeems
// the approved record by its hash. The rows below carry what the device record does not:
// the client, its redirect URI, the PKCE challenge, the protected resource the
// token is bound to, and the one-time authorization code.

const (
	// OAuthAuthorizationCodeTTL bounds how long an issued authorization code
	// can be exchanged. RFC 6749 §4.1.2 recommends at most ten minutes.
	OAuthAuthorizationCodeTTL = 2 * time.Minute

	// OAuthClientKindDynamic marks a client registered through RFC 7591
	// dynamic client registration and stored in acr.oauth_clients.
	OAuthClientKindDynamic = oauthvocab.ClientKindDynamic
	// OAuthClientKindMetadataDocument marks a client whose client_id is an
	// HTTPS URL naming its own client ID metadata document. Such clients are
	// never stored.
	OAuthClientKindMetadataDocument = oauthvocab.ClientKindMetadataDocument

	maxOAuthClientNameLength  = 200
	maxOAuthRedirectURIs      = 8
	maxOAuthURILength         = 2048
	maxOAuthStateLength       = 1024
	maxOAuthScopeLength       = 512
	oauthCodeChallengeLength  = 43
	dynamicOAuthClientIDBytes = 16
	dynamicOAuthClientIDStart = "acrc_"
)

var (
	ErrInvalidOAuthClient               = errors.New("invalid oauth client")
	ErrInvalidOAuthAuthorizationRequest = errors.New("invalid oauth authorization request")
	// ErrOAuthAuthorizationCodeUnavailable reports that no unexpired,
	// unconsumed authorization code matches. Unknown, expired and already
	// used codes are deliberately indistinguishable.
	ErrOAuthAuthorizationCodeUnavailable = errors.New("oauth authorization code unavailable")
)

// OAuthSecretHash is the SHA-256 of a high-entropy one-time secret (the
// browser request handle or the authorization code). Raw values are never
// persisted.
type OAuthSecretHash struct{ value [sha256.Size]byte }

func HashOAuthSecret(secret string) OAuthSecretHash {
	return OAuthSecretHash{value: sha256.Sum256([]byte(secret))}
}

func ParseOAuthSecretHash(value string) (OAuthSecretHash, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return OAuthSecretHash{}, ErrInvalidOAuthAuthorizationRequest
	}
	var hash OAuthSecretHash
	copy(hash.value[:], decoded)
	return hash, nil
}

func (h OAuthSecretHash) String() string { return hex.EncodeToString(h.value[:]) }

func (h OAuthSecretHash) IsZero() bool { return h == OAuthSecretHash{} }

// OAuthClient is a dynamically registered public client. Only public clients
// exist: registration never issues a client secret.
type OAuthClient struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
	CreatedAt    time.Time
}

// OAuthAuthorizationRequest is one pending or completed /authorize request.
type OAuthAuthorizationRequest struct {
	HandleHash     OAuthSecretHash
	DeviceCodeHash DeviceCodeHash
	ClientID       string
	ClientKind     string
	RedirectURI    string
	CodeChallenge  string
	Resource       string
	Scope          string
	State          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	CodeHash       *OAuthSecretHash
	CodeExpiresAt  *time.Time
	ConsumedAt     *time.Time
	// BoundOrgID and BoundSubject are the signed-in web user the request is
	// bound to (BindAuthorizationRequestUser); both empty until bound.
	BoundOrgID   string
	BoundSubject string
}

// OAuthDeviceGrant is what POST /device_authorization stores alongside its
// device authorization record for RFC 8628's device-code grant: the client
// and what /token needs when the client polls back with only the
// device_code -- the resource and scope the /device_authorization request
// named. Unlike OAuthAuthorizationRequest it carries no redirect URI or PKCE
// challenge (RFC 8628 has no redirect step) and no code (the client polls
// /token directly with the device code; the device authorization record's
// own state, not a second code, tracks the decision).
type OAuthDeviceGrant struct {
	DeviceCodeHash DeviceCodeHash
	ClientID       string
	ClientKind     string
	Resource       string
	Scope          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// OAuthStore persists dynamic clients, authorization requests and device
// grants.
type OAuthStore interface {
	// RegisterClient stores a new dynamic client. A duplicate client ID is
	// ErrConflict.
	RegisterClient(context.Context, OAuthClient) (OAuthClient, error)
	// GetClient returns a dynamic client or ErrNotFound.
	GetClient(ctx context.Context, clientID string) (OAuthClient, error)
	// CreateAuthorizationRequest stores a new pending request. A duplicate
	// handle or device code hash is ErrConflict.
	CreateAuthorizationRequest(context.Context, OAuthAuthorizationRequest) (OAuthAuthorizationRequest, error)
	// GetAuthorizationRequest returns the request with this handle or
	// ErrNotFound. Expired requests are still returned; callers decide.
	GetAuthorizationRequest(context.Context, OAuthSecretHash) (OAuthAuthorizationRequest, error)
	// BindAuthorizationRequestUser binds the request to the signed-in web user
	// (org and subject) who first opens it, atomically: the first call wins,
	// the same user again is accepted, a different user is ErrConflict, an
	// unknown handle is ErrNotFound. A request that carries no owner at
	// /authorize gets one here, so only that user can decide it.
	BindAuthorizationRequestUser(ctx context.Context, handle OAuthSecretHash, orgID, subject string) error
	// IssueAuthorizationCode attaches the code hash to an unexpired request
	// that has no code yet. Any other state is ErrConflict; an unknown handle
	// is ErrNotFound.
	IssueAuthorizationCode(ctx context.Context, handle OAuthSecretHash, code OAuthSecretHash, codeExpiresAt time.Time) (OAuthAuthorizationRequest, error)
	// ConsumeAuthorizationCode atomically marks the code used and returns its
	// request. Unknown, expired and already consumed codes all return
	// ErrOAuthAuthorizationCodeUnavailable.
	ConsumeAuthorizationCode(context.Context, OAuthSecretHash) (OAuthAuthorizationRequest, error)
	// CreateDeviceGrant stores a new device-code grant. A duplicate device
	// code hash is ErrConflict.
	CreateDeviceGrant(context.Context, OAuthDeviceGrant) (OAuthDeviceGrant, error)
	// GetDeviceGrant returns the grant for this device code hash or
	// ErrNotFound. Expired grants are still returned; callers decide.
	GetDeviceGrant(context.Context, DeviceCodeHash) (OAuthDeviceGrant, error)
}

// IsDynamicOAuthClientID reports whether a client ID has the shape this
// server issues at registration.
func IsDynamicOAuthClientID(clientID string) bool {
	rest, ok := strings.CutPrefix(clientID, dynamicOAuthClientIDStart)
	if !ok || len(rest) != 2*dynamicOAuthClientIDBytes {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// NewDynamicOAuthClientID formats random bytes as a dynamic client ID.
func NewDynamicOAuthClientID(random [dynamicOAuthClientIDBytes]byte) string {
	return dynamicOAuthClientIDStart + hex.EncodeToString(random[:])
}

// ValidateOAuthClient checks a dynamic client before it is stored.
func ValidateOAuthClient(client OAuthClient) error {
	if !IsDynamicOAuthClientID(client.ClientID) || client.CreatedAt.IsZero() {
		return ErrInvalidOAuthClient
	}
	if !utf8.ValidString(client.ClientName) || len(client.ClientName) > maxOAuthClientNameLength || hasControl(client.ClientName) {
		return ErrInvalidOAuthClient
	}
	// Zero redirect URIs is valid here: a client registered for the RFC 8628
	// device-code grant only (CHAOS-6233) declares none, since the device
	// grant has no redirect step. The caller (OAuthService.Register) is what
	// requires at least one when the client also wants authorization_code.
	if len(client.RedirectURIs) > maxOAuthRedirectURIs {
		return ErrInvalidOAuthClient
	}
	seen := make(map[string]struct{}, len(client.RedirectURIs))
	for _, redirect := range client.RedirectURIs {
		if !ValidOAuthRedirectURI(redirect) {
			return ErrInvalidOAuthClient
		}
		if _, duplicate := seen[redirect]; duplicate {
			return ErrInvalidOAuthClient
		}
		seen[redirect] = struct{}{}
	}
	return nil
}

// ValidOAuthRedirectURI accepts an absolute redirect URI without a fragment
// or user info that is either HTTPS or an HTTP loopback address (RFC 8252
// §7.3, the native-client pattern MCP clients use).
func ValidOAuthRedirectURI(value string) bool {
	if value == "" || len(value) > maxOAuthURILength || !utf8.ValidString(value) || hasControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil || parsed.Opaque != "" {
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

// MatchOAuthRedirectURI reports whether a presented redirect_uri satisfies a
// client's registered one. Exact string equality always matches. A native
// app binds an ephemeral loopback port at runtime and cannot pre-register
// it, so RFC 8252 §7.3 requires the authorization server to accept any port
// when the registered URI is itself a loopback URI carrying no port: when
// the registered URI's host is a loopback IP literal (127.0.0.1, ::1) — or,
// for CIMD clients only (allowLocalhost), the localhost hostname the MCP
// CIMD guidance also allows — and the registered URI names no port, the
// presented URI matches for any port sharing the same scheme, host and
// path. A registered URI naming an explicit port, or any non-loopback host,
// keeps exact matching. This allowance is for matching a presented
// redirect_uri against a client's REGISTERED one at /authorize only; /token
// re-checks a presented redirect_uri against the one already verified and
// stored at /authorize, which is always an exact comparison (request to
// request, not request to registration).
func MatchOAuthRedirectURI(registered, presented string, allowLocalhost bool) bool {
	if registered == presented {
		return true
	}
	reg, err := url.Parse(registered)
	if err != nil || reg.Port() != "" || !isLoopbackRedirectHost(reg.Hostname(), allowLocalhost) {
		return false
	}
	pres, err := url.Parse(presented)
	if err != nil || pres.User != nil || pres.Hostname() != reg.Hostname() || pres.Scheme != reg.Scheme || pres.Port() == "" {
		return false
	}
	// Everything except the port must be BYTE-IDENTICAL to the registered
	// URI, so this never widens past the port: rebuild the presented URI
	// with its authority's port removed by editing the raw string (never by
	// reconstructing from parsed fields, which would re-escape the path and
	// silently accept an escaped-path respelling, or drop an empty "?"/"#"
	// marker the parsed Path/RawQuery/Fragment fields cannot distinguish
	// from "absent"). pres.Host always carries the exact authority text
	// that follows "scheme://" (userinfo, if any, comes before it — already
	// refused above by the User check), so trimming the ":<port>" suffix
	// Go itself parsed out of it reproduces the authority exactly as
	// written, brackets included for an IPv6 literal.
	prefix := pres.Scheme + "://" + pres.Host
	if !strings.HasPrefix(presented, prefix) {
		return false
	}
	hostWithoutPort := strings.TrimSuffix(pres.Host, ":"+pres.Port())
	presentedWithoutPort := pres.Scheme + "://" + hostWithoutPort + presented[len(prefix):]
	return presentedWithoutPort == registered
}

func isLoopbackRedirectHost(host string, allowLocalhost bool) bool {
	switch host {
	case "127.0.0.1", "::1":
		return true
	case "localhost":
		return allowLocalhost
	default:
		return false
	}
}

// ValidateOAuthAuthorizationRequest checks a request before it is stored.
func ValidateOAuthAuthorizationRequest(request OAuthAuthorizationRequest) error {
	if request.HandleHash.IsZero() || request.DeviceCodeHash.IsZero() || request.CodeHash != nil || request.CodeExpiresAt != nil || request.ConsumedAt != nil {
		return ErrInvalidOAuthAuthorizationRequest
	}
	if request.CreatedAt.IsZero() || !request.ExpiresAt.After(request.CreatedAt) {
		return ErrInvalidOAuthAuthorizationRequest
	}
	switch request.ClientKind {
	case OAuthClientKindDynamic:
		if !IsDynamicOAuthClientID(request.ClientID) {
			return ErrInvalidOAuthAuthorizationRequest
		}
	case OAuthClientKindMetadataDocument:
		if !ValidOAuthClientMetadataURL(request.ClientID) {
			return ErrInvalidOAuthAuthorizationRequest
		}
	default:
		return ErrInvalidOAuthAuthorizationRequest
	}
	if !ValidOAuthRedirectURI(request.RedirectURI) || !ValidOAuthCodeChallenge(request.CodeChallenge) || !ValidOAuthResource(request.Resource) {
		return ErrInvalidOAuthAuthorizationRequest
	}
	if len(request.Scope) > maxOAuthScopeLength || len(request.State) > maxOAuthStateLength || !utf8.ValidString(request.State) || hasControl(request.State) || hasControl(request.Scope) {
		return ErrInvalidOAuthAuthorizationRequest
	}
	return nil
}

// ValidateOAuthDeviceGrant checks a device grant before it is stored.
func ValidateOAuthDeviceGrant(grant OAuthDeviceGrant) error {
	if grant.DeviceCodeHash.IsZero() {
		return ErrInvalidOAuthAuthorizationRequest
	}
	if grant.CreatedAt.IsZero() || !grant.ExpiresAt.After(grant.CreatedAt) {
		return ErrInvalidOAuthAuthorizationRequest
	}
	switch grant.ClientKind {
	case OAuthClientKindDynamic:
		if !IsDynamicOAuthClientID(grant.ClientID) {
			return ErrInvalidOAuthAuthorizationRequest
		}
	case OAuthClientKindMetadataDocument:
		if !ValidOAuthClientMetadataURL(grant.ClientID) {
			return ErrInvalidOAuthAuthorizationRequest
		}
	default:
		return ErrInvalidOAuthAuthorizationRequest
	}
	if !ValidOAuthResource(grant.Resource) {
		return ErrInvalidOAuthAuthorizationRequest
	}
	if len(grant.Scope) > maxOAuthScopeLength || !utf8.ValidString(grant.Scope) || hasControl(grant.Scope) {
		return ErrInvalidOAuthAuthorizationRequest
	}
	return nil
}

// ValidOAuthCodeChallenge accepts an S256 challenge: the base64url (no
// padding) encoding of a SHA-256 digest, exactly 43 characters.
func ValidOAuthCodeChallenge(value string) bool {
	if len(value) != oauthCodeChallengeLength {
		return false
	}
	for _, r := range value {
		if !isBase64URLRune(r) {
			return false
		}
	}
	return true
}

// ValidOAuthResource accepts an absolute HTTPS (or HTTP loopback) URI without
// a fragment, the RFC 8707 resource indicator shape.
func ValidOAuthResource(value string) bool {
	if value == "" || len(value) > maxOAuthURILength || !utf8.ValidString(value) || hasControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil || parsed.Opaque != "" {
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

// ValidOAuthClientMetadataURL accepts a client ID metadata document URL: an
// HTTPS URL with a non-root path, no fragment, no user info and no dot
// segments (draft-ietf-oauth-client-id-metadata-document §3).
func ValidOAuthClientMetadataURL(value string) bool {
	if value == "" || len(value) > maxOAuthURILength || !utf8.ValidString(value) || hasControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	if parsed.Path == "" || parsed.Path == "/" || strings.HasSuffix(parsed.Path, "/") {
		return false
	}
	return !slices.Contains(strings.Split(parsed.Path, "/"), ".") && !slices.Contains(strings.Split(parsed.Path, "/"), "..")
}

func isBase64URLRune(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// CloneOAuthAuthorizationRequest deep-copies a request.
func CloneOAuthAuthorizationRequest(request OAuthAuthorizationRequest) OAuthAuthorizationRequest {
	if request.CodeHash != nil {
		code := *request.CodeHash
		request.CodeHash = &code
	}
	if request.CodeExpiresAt != nil {
		expires := *request.CodeExpiresAt
		request.CodeExpiresAt = &expires
	}
	if request.ConsumedAt != nil {
		consumed := *request.ConsumedAt
		request.ConsumedAt = &consumed
	}
	return request
}
