package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// CHAOS-13586: the approver reference is checked against the web assertion of
// the same request; only a reference that passes is attached to the principal.

type approverCase struct {
	now       time.Time
	private   ed25519.PrivateKey
	verifier  *WebAssertionVerifier
	request   *http.Request
	assertion map[string]any
	raw       string
}

func newApproverCase(t *testing.T) *approverCase {
	t.Helper()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema_version":"device_approval_request.v1","user_code":"ABCD2345","repository_scopes":["*"]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
	assertion := webAssertionClaims(now, request, body)
	assertion["repository_scopes"] = []string{"*"}
	assertion["permissions"] = []string{WebAssertionPermissionCredentialIssue}
	c := &approverCase{now: now, private: private, verifier: verifier, request: request, assertion: assertion}
	c.raw = signTestWebAssertion(t, private, "current", assertion)
	request.Header.Set(WebAssertionHeader, c.raw)
	return c
}

func (c *approverCase) claims() map[string]any {
	digest := sha256.Sum256([]byte(c.raw))
	return map[string]any{
		"action": "acr.credential.issue", "aud": "auth-service", "iss": "https://web.example.test",
		"sub": "user_123", "org_id": "org_123", "sid": "ses_fQw-UpG0T3qKYw5bnC1PEA", "impersonating": false,
		"iat": c.now.Unix(), "nbf": c.now.Unix(), "exp": c.now.Add(30 * time.Second).Unix(), "jti": "reference_1",
		"req": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
}

func (c *approverCase) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signTestWebAssertionWithHeader(t, c.private, map[string]any{"alg": "EdDSA", "typ": "web-approver+jwt", "kid": "current"}, claims)
}

func (c *approverCase) verify(t *testing.T, references ...string) (string, string) {
	t.Helper()
	for _, reference := range references {
		c.request.Header.Add(ApproverReferenceHeader, reference)
	}
	principal, err := c.verifier.Verify(c.request)
	if err != nil {
		t.Fatalf("the web assertion itself was refused: %v", err)
	}
	return principal.ApproverReference, principal.ApproverReferenceFault
}

func TestApproverReference_aMatchingPairIsAttached(t *testing.T) {
	c := newApproverCase(t)
	token := c.sign(t, c.claims())

	reference, fault := c.verify(t, token)

	if reference != token || fault != "" {
		t.Fatalf("reference = %q fault = %q; want the token and no fault", reference, fault)
	}
}

func TestApproverReference_noHeaderLeavesThePrincipalUnchanged(t *testing.T) {
	c := newApproverCase(t)

	reference, fault := c.verify(t)

	if reference != "" || fault != "" {
		t.Fatalf("reference = %q fault = %q; want neither", reference, fault)
	}
}

func TestApproverReference_refusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		fault  string
		mutate func(c *approverCase, claims map[string]any)
		token  func(t *testing.T, c *approverCase, claims map[string]any) []string
	}{
		{name: "req of another assertion", fault: ApproverReferencePair, mutate: func(_ *approverCase, m map[string]any) { m["req"] = strings.Repeat("A", 43) }},
		{name: "another subject", fault: ApproverReferencePair, mutate: func(_ *approverCase, m map[string]any) { m["sub"] = "user_456" }},
		{name: "another organization", fault: ApproverReferencePair, mutate: func(_ *approverCase, m map[string]any) { m["org_id"] = "org_456" }},
		{name: "impersonating differs", fault: ApproverReferencePair, mutate: func(_ *approverCase, m map[string]any) { m["impersonating"] = true }},
		{name: "outlives its assertion", fault: ApproverReferencePair, mutate: func(c *approverCase, m map[string]any) {
			m["iat"], m["nbf"], m["exp"] = c.now.Add(5*time.Second).Unix(), c.now.Add(5*time.Second).Unix(), c.now.Add(35*time.Second).Unix()
		}},
		{name: "issued before its assertion", fault: ApproverReferencePair, mutate: func(c *approverCase, m map[string]any) {
			m["iat"], m["nbf"], m["exp"] = c.now.Add(-6*time.Second).Unix(), c.now.Add(-6*time.Second).Unix(), c.now.Add(20*time.Second).Unix()
		}},
		{name: "audience acr-api", fault: ApproverReferenceClaims, mutate: func(_ *approverCase, m map[string]any) { m["aud"] = "acr-api" }},
		{name: "another issuer", fault: ApproverReferenceClaims, mutate: func(_ *approverCase, m map[string]any) { m["iss"] = "https://other.example.test" }},
		{name: "another action", fault: ApproverReferenceClaims, mutate: func(_ *approverCase, m map[string]any) { m["action"] = "acr.context.read" }},
		{name: "no sid", fault: ApproverReferenceClaims, mutate: func(_ *approverCase, m map[string]any) { delete(m, "sid") }},
		{name: "life over 30 s", fault: ApproverReferenceClaims, mutate: func(c *approverCase, m map[string]any) { m["exp"] = c.now.Add(31 * time.Second).Unix() }},
		{name: "expired", fault: ApproverReferenceClaims, mutate: func(c *approverCase, m map[string]any) {
			m["iat"], m["nbf"], m["exp"] = c.now.Add(-40*time.Second).Unix(), c.now.Add(-40*time.Second).Unix(), c.now.Add(-10*time.Second).Unix()
		}},
		{name: "iat in the future only", fault: ApproverReferenceClaims, mutate: func(c *approverCase, m map[string]any) { m["iat"] = c.now.Add(10 * time.Second).Unix() }},
		{name: "nbf in the future only", fault: ApproverReferenceClaims, mutate: func(c *approverCase, m map[string]any) { m["nbf"] = c.now.Add(10 * time.Second).Unix() }},
		{name: "exp before iat", fault: ApproverReferenceClaims, mutate: func(c *approverCase, m map[string]any) { m["exp"] = c.now.Add(-time.Second).Unix() }},
		{name: "a claim the contract lacks", fault: ApproverReferenceClaims, mutate: func(_ *approverCase, m map[string]any) { m["repository_scopes"] = []string{"*"} }},
		{name: "the web assertion in the approver header", fault: ApproverReferenceMalformed, token: func(_ *testing.T, c *approverCase, _ map[string]any) []string { return []string{c.raw} }},
		{name: "unknown kid", fault: ApproverReferenceSignature, token: func(t *testing.T, c *approverCase, m map[string]any) []string {
			return []string{signTestWebAssertionWithHeader(t, c.private, map[string]any{"alg": "EdDSA", "typ": "web-approver+jwt", "kid": "other"}, m)}
		}},
		{name: "another key", fault: ApproverReferenceSignature, token: func(t *testing.T, _ *approverCase, m map[string]any) []string {
			_, other, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			return []string{signTestWebAssertionWithHeader(t, other, map[string]any{"alg": "EdDSA", "typ": "web-approver+jwt", "kid": "current"}, m)}
		}},
		{name: "two headers", fault: ApproverReferenceMalformed, token: func(t *testing.T, c *approverCase, m map[string]any) []string {
			token := c.sign(t, m)
			return []string{token, token}
		}},
		{name: "over 1000 characters", fault: ApproverReferenceMalformed, mutate: func(_ *approverCase, m map[string]any) { m["jti"] = strings.Repeat("j", 700) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newApproverCase(t)
			claims := c.claims()
			if test.mutate != nil {
				test.mutate(c, claims)
			}
			tokens := []string{c.sign(t, claims)}
			if test.token != nil {
				tokens = test.token(t, c, claims)
			}

			reference, fault := c.verify(t, tokens...)

			if reference != "" || fault != test.fault {
				t.Fatalf("reference = %q fault = %q; want none and %q", reference, fault, test.fault)
			}
		})
	}
}

// A sid the web assertion signs must equal the reference's.
func TestApproverReference_aSignedAssertionSidMustMatch(t *testing.T) {
	c := newApproverCase(t)
	c.assertion["sid"] = "ses_AAAAAAAAAAAAAAAAAAAAAQ"
	c.raw = signTestWebAssertion(t, c.private, "current", c.assertion)
	c.request.Header.Set(WebAssertionHeader, c.raw)

	reference, fault := c.verify(t, c.sign(t, c.claims()))

	if reference != "" || fault != ApproverReferencePair {
		t.Fatalf("reference = %q fault = %q; want none and pair", reference, fault)
	}
}

// C-1 (iii): the approver reference in the web assertion header is no web assertion.
func TestApproverReference_isRefusedAsAWebAssertion(t *testing.T) {
	c := newApproverCase(t)
	c.request.Header.Set(WebAssertionHeader, c.sign(t, c.claims()))

	if _, err := c.verifier.Verify(c.request); err == nil {
		t.Fatal("an approver reference was accepted as a web assertion")
	}
}
