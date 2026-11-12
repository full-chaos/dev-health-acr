package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-13562 (acp condition): the device preview's claim form (a body with no
// repository_scopes; the assertion's claim carries the grant) is the
// compatibility form for the live web. For the same request it answers as it
// did before CHAOS-13562, and it never shows a hint the claim does not allow.
// The file uses only helpers that exist before CHAOS-13562, so the same cells
// run on the base and on the head; each prints one PARITY line to compare.

func chaos13562ParityAssertion(t *testing.T, key ed25519.PrivateKey, now time.Time, body []byte, claim []string, jti string) string {
	t.Helper()
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": "org_1",
		"repository_scopes": claim, "permissions": []string{auth.WebAssertionPermissionCredentialIssue},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": http.MethodPost, "path": "/api/v1/oauth/device_approval", "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func chaos13562ParityKeys(prefix string, n int, extra ...string) []string {
	keys := append([]string(nil), extra...)
	for i := 0; i < n; i++ {
		keys = append(keys, fmt.Sprintf("%s/r%04d", prefix, i))
	}
	return keys
}

func TestCHAOS13562_PreviewClaimFormAnswersAsBefore(t *testing.T) {
	stored := []string{"example-org/a", "example-org/b", "other-org/c"}
	cells := []struct {
		name       string
		hints      []string
		claim      []string
		wantStatus int
		wantHints  []string
	}{
		{"claim one stored", stored, []string{"example-org/a"}, 200, []string{"example-org/a"}},
		{"claim two stored", stored, []string{"example-org/a", "example-org/b"}, 200, []string{"example-org/a", "example-org/b"}},
		{"claim organization-wide", stored, []string{"*"}, 200, stored},
		{"claim one stored one not", stored, []string{"example-org/a", "example-org/z"}, 200, []string{"example-org/a"}},
		{"claim none stored", stored, []string{"example-org/z"}, 400, nil},
		{"claim with 101 keys", stored, chaos13562ParityKeys("big-org", 100, "other-org/c"), 200, []string{"other-org/c"}},
		{"claim with 257 keys", stored, chaos13562ParityKeys("big-org", 256, "example-org/b"), 200, []string{"example-org/b"}},
		{"claim owner wildcard", stored, []string{"example-org/*"}, 401, nil},
		{"claim empty", stored, []string{}, 401, nil},
		{"claim not normalized", stored, []string{"Example-Org/A"}, 401, nil},
		{"no hints stored", nil, []string{"example-org/a"}, 200, nil},
	}
	for i, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
				Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
			start := contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}
			if cell.hints != nil {
				hints := slices.Clone(cell.hints)
				start.RepositoryHints = &hints
			}
			created := httptest.NewRecorder()
			app.Handler().ServeHTTP(created, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", start))
			var authorization contractsv1.DeviceAuthorizationResponse
			if err := json.NewDecoder(created.Body).Decode(&authorization); err != nil || created.Code != http.StatusOK {
				t.Fatalf("create: %d %v", created.Code, err)
			}
			body := []byte(`{"schema_version":"device_approval_preview_request.v1","user_code":"` + authorization.UserCode + `"}`)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(auth.WebAssertionHeader, chaos13562ParityAssertion(t, private, now, body, cell.claim, fmt.Sprintf("parity-%d", i)))
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, request)
			var preview contractsv1.DeviceApprovalPreviewResponse
			if recorder.Code == http.StatusOK {
				if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("PARITY %-26s status=%d hints=[%s]", cell.name, recorder.Code, strings.Join(preview.RepositoryHints, ","))
			if recorder.Code != cell.wantStatus || fmt.Sprint(preview.RepositoryHints) != fmt.Sprint(cell.wantHints) {
				t.Fatalf("status %d hints %v, want %d %v", recorder.Code, preview.RepositoryHints, cell.wantStatus, cell.wantHints)
			}
			// Never more than the claim allows: every shown hint is a stored
			// hint the claim names, or the claim is organization-wide.
			for _, hint := range preview.RepositoryHints {
				if !slices.Contains(cell.hints, hint) || !(slices.Contains(cell.claim, hint) || slices.Equal(cell.claim, []string{"*"})) {
					t.Fatalf("hint %q is not a stored hint the claim allows (claim %v)", hint, cell.claim)
				}
			}
		})
	}
}
