package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
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
)

type WebAssertionOptions struct {
	Issuer       string
	Audience     string
	JWKSPath     string
	Now          func() time.Time
	MaxBodyBytes int64
}

type WebAssertionVerifier struct {
	issuer       string
	audience     string
	jwksPath     string
	now          func() time.Time
	maxBodyBytes int64
	replays      webAssertionReplays
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
		issuer: strings.TrimSpace(options.Issuer), audience: strings.TrimSpace(options.Audience), jwksPath: options.JWKSPath,
		now: options.Now, maxBodyBytes: options.MaxBodyBytes,
		replays: webAssertionReplays{byJTI: make(map[string]time.Time), capacity: defaultReplayCapacity},
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
		return storage.Principal{}, ErrInvalidWebAssertion
	}
	expiresAt, _ := claims.ExpiresAt.Int64()
	if v.replays.observe(claims.JWTID, time.Unix(expiresAt, 0).UTC(), v.now().UTC()) {
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
		!validWebRepositories(claims.RepositoryScopes) || !validWebPermissions(claims.Permissions) {
		return false
	}
	body, err := readAssertionBody(r, v.maxBodyBytes)
	if err != nil {
		return false
	}
	return claims.BodySHA256 == assertionBodyDigest(body)
}
