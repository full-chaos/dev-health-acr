package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The approver reference (CHAOS-13586): a second token web signs with the web
// assertion key for the two approval calls. acr checks it against the web
// assertion of the same request and forwards only it to the policy decision
// point (auth-service), which reads the approver's session live. Architecture:
// web docs/auth-acr-device-approval.md; diagram docs/diagrams/device-approval.mmd.
const (
	ApproverReferenceHeader = "X-ACR-Approver-Reference"
	// approverReferenceType is neither the web assertion's typ (JWT) nor an
	// access token's (at+jwt).
	approverReferenceType     = "web-approver+jwt"
	approverReferenceAudience = "auth-service"
	approverReferenceAction   = "acr.credential.issue"
	// approverReferenceMaxLength is the policy decision point's
	// principal_reference bound (authorization_request.v1).
	approverReferenceMaxLength = 1000
)

// The closed reasons an approver reference is not attached to the principal.
// They go to the log, never to the caller. A request with no reference carries
// no reason (the principal is unchanged); an approval names it "absent".
const (
	ApproverReferenceAbsent    = "absent"
	ApproverReferenceMalformed = "malformed"
	ApproverReferenceSignature = "signature"
	ApproverReferenceClaims    = "claims"
	ApproverReferencePair      = "pair"
)

type approverReferenceClaims struct {
	Action         string      `json:"action"`
	Audience       string      `json:"aud"`
	ExpiresAt      json.Number `json:"exp"`
	IssuedAt       json.Number `json:"iat"`
	Impersonating  bool        `json:"impersonating"`
	Issuer         string      `json:"iss"`
	JWTID          string      `json:"jti"`
	NotBefore      json.Number `json:"nbf"`
	OrganizationID string      `json:"org_id"`
	Request        string      `json:"req"`
	SessionID      string      `json:"sid"`
	Subject        string      `json:"sub"`
}

// attachApproverReference checks the request's approver reference against the
// verified web assertion (raw, claims, principal) and returns the principal
// with the reference or with the reason it has none. It never refuses the
// request: only an approval reads the result, and only while its decision
// check is on.
func (v *WebAssertionVerifier) attachApproverReference(r *http.Request, raw string, assertion parsedWebAssertionClaims, principal storage.Principal) storage.Principal {
	reference, fault := v.approverReference(r, raw, assertion, principal)
	principal.ApproverReference, principal.ApproverReferenceFault = reference, fault
	return principal
}

func (v *WebAssertionVerifier) approverReference(r *http.Request, raw string, assertion parsedWebAssertionClaims, principal storage.Principal) (string, string) {
	values := r.Header.Values(ApproverReferenceHeader)
	if len(values) == 0 {
		return "", ""
	}
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || len(values[0]) > approverReferenceMaxLength {
		return "", ApproverReferenceMalformed
	}
	token := values[0]
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", ApproverReferenceMalformed
	}
	header, err := decodeWebAssertion[webAssertionHeader](parts[0])
	if err != nil || header.Algorithm != "EdDSA" || header.Type != approverReferenceType || !validWebAssertionID(header.KeyID) {
		return "", ApproverReferenceMalformed
	}
	keys, err := v.keys()
	if err != nil {
		return "", ApproverReferenceSignature
	}
	key, found := keys[header.KeyID]
	signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[2])
	if !found || signatureErr != nil || !ed25519.Verify(key.Public, []byte(parts[0]+"."+parts[1]), signature) || !v.keyInTimeBound(r, header.KeyID, key) {
		return "", ApproverReferenceSignature
	}
	claims, err := decodeWebAssertion[approverReferenceClaims](parts[1])
	if err != nil || !v.validApproverClaims(claims) {
		return "", ApproverReferenceClaims
	}
	if !approverPairs(claims, raw, assertion, principal) {
		return "", ApproverReferencePair
	}
	return token, ""
}

func (v *WebAssertionVerifier) validApproverClaims(claims approverReferenceClaims) bool {
	issuedAt, issuedAtErr := claims.IssuedAt.Int64()
	notBefore, notBeforeErr := claims.NotBefore.Int64()
	expiresAt, expiresAtErr := claims.ExpiresAt.Int64()
	now := v.now().UTC()
	return issuedAtErr == nil && notBeforeErr == nil && expiresAtErr == nil &&
		claims.Issuer == v.issuer && claims.Audience == approverReferenceAudience && claims.Action == approverReferenceAction &&
		validWebAssertionID(claims.Subject) && validWebAssertionID(claims.OrganizationID) && validWebAssertionID(claims.JWTID) &&
		validWebAssertionID(claims.SessionID) && validWebAssertionID(claims.Request) &&
		issuedAt <= now.Add(webAssertionClockSkew).Unix() && notBefore <= now.Add(webAssertionClockSkew).Unix() &&
		expiresAt >= now.Add(-webAssertionClockSkew).Unix() && expiresAt >= issuedAt &&
		expiresAt-issuedAt <= int64(maxWebAssertionLifetime.Seconds())
}

// approverPairs binds the reference to ONE approval: req is the SHA-256 of the
// exact web assertion header this request carried, the reference names the same
// approver, and it never outlives that assertion. The assertion's sid is
// compared only when web signed one; the policy decision point reads the
// reference's sid live either way.
func approverPairs(claims approverReferenceClaims, raw string, assertion parsedWebAssertionClaims, principal storage.Principal) bool {
	digest := sha256.Sum256([]byte(raw))
	if claims.Request != base64.RawURLEncoding.EncodeToString(digest[:]) {
		return false
	}
	if claims.Subject != principal.Subject || claims.OrganizationID != principal.OrgID || claims.Impersonating != principal.Impersonating {
		return false
	}
	if principal.SessionID != "" && claims.SessionID != principal.SessionID {
		return false
	}
	referenceIssued, _ := claims.IssuedAt.Int64()
	referenceExpires, _ := claims.ExpiresAt.Int64()
	assertionIssued, issuedErr := assertion.IssuedAt.Int64()
	assertionExpires, expiresErr := assertion.ExpiresAt.Int64()
	return issuedErr == nil && expiresErr == nil && referenceExpires <= assertionExpires &&
		referenceIssued >= assertionIssued-int64(webAssertionClockSkew.Seconds())
}
