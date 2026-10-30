package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
)

const (
	WebAssertionHeader               = "X-ACR-Web-Assertion"
	WebAssertionAuthenticationMethod = storage.AuthenticationMethodWebAssertion
	maxWebAssertionLifetime          = 30 * time.Second
	webAssertionClockSkew            = 5 * time.Second
	defaultReplayCapacity            = 10_000
	defaultWebAssertionBodyBytes     = 1 << 20
)

var (
	ErrInvalidWebAssertion = errors.New("invalid web assertion")
	ErrWebAssertionReplay  = errors.New("web assertion replay observed")
	// ErrWebAssertionStoreUnavailable means the shared used-id record could not
	// answer. The assertion is refused: fail closed.
	ErrWebAssertionStoreUnavailable = errors.New("web assertion replay store unavailable")
)

// WebAssertionReplayStore records used web-assertion ids. Observe reports
// replay=true when (issuer, jti) was already used; an error means the store
// could not answer.
type WebAssertionReplayStore interface {
	Observe(ctx context.Context, issuer, jti string, expiresAt, now time.Time) (replay bool, err error)
}

type WebAssertionOptions struct {
	Issuer       string
	Audience     string
	JWKSPath     string
	Now          func() time.Time
	MaxBodyBytes int64
	// Replays is the shared used-id record. Nil uses a per-process record,
	// which is only correct for a single instance.
	Replays WebAssertionReplayStore
	Logger  *slog.Logger
}

type WebAssertionVerifier struct {
	issuer       string
	audience     string
	jwks         *authverify.Ed25519JWKSVerifier
	now          func() time.Time
	maxBodyBytes int64
	replays      WebAssertionReplayStore
	logger       *slog.Logger
}

type webAssertionHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type parsedWebAssertionClaims struct {
	Issuer           string      `json:"iss"`
	Audience         string      `json:"aud"`
	Subject          string      `json:"sub"`
	OrganizationID   string      `json:"org_id"`
	RepositoryScopes []string    `json:"repository_scopes"`
	Permissions      []string    `json:"permissions"`
	IssuedAt         json.Number `json:"iat"`
	NotBefore        json.Number `json:"nbf"`
	ExpiresAt        json.Number `json:"exp"`
	JWTID            string      `json:"jti"`
	Method           string      `json:"method"`
	Path             string      `json:"path"`
	BodySHA256       string      `json:"body_sha256"`
	// SessionID and Impersonating are optional (CHAOS-12123): the web
	// session the assertion was made in, and whether it impersonates. The web
	// server derives both from its own session, never from the browser.
	SessionID     string `json:"sid,omitempty"`
	Impersonating bool   `json:"impersonating,omitempty"`
}

func NewWebAssertionVerifier(options WebAssertionOptions) (*WebAssertionVerifier, error) {
	if strings.TrimSpace(options.Issuer) == "" || strings.TrimSpace(options.Audience) == "" || strings.TrimSpace(options.JWKSPath) == "" {
		return nil, ErrInvalidWebAssertion
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = defaultWebAssertionBodyBytes
	}
	verifier := &WebAssertionVerifier{
		issuer: strings.TrimSpace(options.Issuer), audience: strings.TrimSpace(options.Audience), jwks: authverify.NewEd25519JWKSVerifier(options.JWKSPath),
		now: options.Now, maxBodyBytes: options.MaxBodyBytes,
		replays: options.Replays, logger: options.Logger,
	}
	if verifier.replays == nil {
		verifier.replays = &webAssertionReplays{byJTI: make(map[string]time.Time), capacity: defaultReplayCapacity}
	}
	if _, err := verifier.keys(); err != nil {
		return nil, ErrInvalidWebAssertion
	}
	return verifier, nil
}

func (v *WebAssertionVerifier) Verify(r *http.Request) (storage.Principal, error) {
	if r == nil {
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	raw, ok := singleHeader(r, WebAssertionHeader)
	if !ok {
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	header, err := decodeWebAssertion[webAssertionHeader](parts[0])
	if err != nil || header.Algorithm != "EdDSA" || header.Type != "JWT" || !validWebAssertionID(header.KeyID) {
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	keys, err := v.keys()
	if err != nil {
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	key, found := keys[header.KeyID]
	signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[2])
	if !found || signatureErr != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	claims, err := decodeWebAssertion[parsedWebAssertionClaims](parts[1])
	if err != nil || !v.validClaims(claims, r) {
		if err == nil {
			v.logRepositoryScopeFault(r, claims)
		}
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	expiresAt, _ := claims.ExpiresAt.Int64()
	replay, err := v.replays.Observe(r.Context(), v.issuer, claims.JWTID, time.Unix(expiresAt, 0).UTC(), v.now().UTC())
	if err != nil {
		if v.logger != nil {
			digest := sha256.Sum256([]byte(claims.JWTID))
			v.logger.ErrorContext(r.Context(), "web assertion replay store unavailable; assertion refused", "class", "web_assertion_store_unavailable", "jti_digest", hex.EncodeToString(digest[:6]))
		}
		return storage.Principal{}, ErrWebAssertionStoreUnavailable
	}
	if replay {
		return storage.Principal{
			AuthenticationMethod: WebAssertionAuthenticationMethod,
			Subject:              claims.Subject,
			OrgID:                claims.OrganizationID,
		}, ErrWebAssertionReplay
	}
	return storage.Principal{
		AuthenticationMethod: WebAssertionAuthenticationMethod,
		Subject:              claims.Subject,
		OrgID:                claims.OrganizationID,
		RepositoryScopes:     normalizeWebRepositoryScopes(claims.RepositoryScopes),
		Permissions:          normalizeWebPermissions(claims.Permissions),
		SessionID:            strings.TrimSpace(claims.SessionID),
		Impersonating:        claims.Impersonating,
	}, nil
}

func IsWebAssertionReplay(err error) bool {
	return errors.Is(err, ErrWebAssertionReplay)
}

func (v *WebAssertionVerifier) validClaims(claims parsedWebAssertionClaims, r *http.Request) bool {
	issuedAt, issuedAtErr := claims.IssuedAt.Int64()
	notBefore, notBeforeErr := claims.NotBefore.Int64()
	expiresAt, expiresAtErr := claims.ExpiresAt.Int64()
	now := v.now().UTC()
	if issuedAtErr != nil || notBeforeErr != nil || expiresAtErr != nil || claims.Issuer != v.issuer || claims.Audience != v.audience ||
		!validWebAssertionID(claims.Subject) || !validWebAssertionID(claims.OrganizationID) || !validWebAssertionID(claims.JWTID) ||
		issuedAt > now.Add(webAssertionClockSkew).Unix() || notBefore > now.Add(webAssertionClockSkew).Unix() ||
		expiresAt < now.Add(-webAssertionClockSkew).Unix() || expiresAt < issuedAt || expiresAt-issuedAt > int64(maxWebAssertionLifetime/time.Second) ||
		claims.Method != r.Method || claims.Path != r.URL.EscapedPath() || r.URL.RawQuery != "" ||
		!validWebRepositories(claims.RepositoryScopes, claims.Permissions, claims.Method, claims.Path) || !validWebPermissions(claims.Permissions) ||
		(claims.SessionID != "" && !validWebAssertionID(claims.SessionID)) {
		return false
	}
	body, err := readAssertionBody(r, v.maxBodyBytes)
	if err != nil {
		return false
	}
	return claims.BodySHA256 == assertionBodyDigest(body)
}

// logRepositoryScopeFault records which signed repository scope made an
// otherwise well-formed assertion fail, so the bare 401 has a cause in the log.
// The claims are signature-verified by the time this runs.
func (v *WebAssertionVerifier) logRepositoryScopeFault(r *http.Request, claims parsedWebAssertionClaims) {
	if v.logger == nil || validWebRepositories(claims.RepositoryScopes, claims.Permissions, claims.Method, claims.Path) {
		return
	}
	scope, reason := "", "no repository scope"
	switch {
	case len(claims.RepositoryScopes) == 0:
	case len(claims.RepositoryScopes) == 1 && claims.RepositoryScopes[0] == "*":
		scope, reason = "*", "organization-wide scope is only valid for credential issue on an organization-wide path"
	default:
		scope, reason, _ = webRepositoryScopeFault(claims.RepositoryScopes)
	}
	v.logger.WarnContext(r.Context(), "web assertion refused: invalid repository scope", "class", "web_assertion_repository_scope_invalid",
		"scope", truncateLogValue(scope, 128), "reason", reason, "scope_count", len(claims.RepositoryScopes), "organization_id", claims.OrganizationID)
}

func truncateLogValue(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
