package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// The typed-code approval page looks a device grant up by user code (the
// preview). It must show the scopes the grant asked for, so a user approving
// data:read sees data:read.
func TestChaos7106DevicePreviewShowsRequestedScopes(t *testing.T) {
	for name, tc := range map[string]struct {
		scope string
		want  []string
	}{
		"data:read requested":      {scope: "data:read", want: []string{"data:read"}},
		"several, canonical":       {scope: "data:read context:read", want: []string{"context:read", "data:read"}},
		"none requested = default": {scope: "", want: []string{"context:read", "evidence:read"}},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
			if err != nil {
				t.Fatal(err)
			}
			extra := url.Values{}
			if tc.scope != "" {
				extra.Set("scope", tc.scope)
			}
			started, _ := startOAuthDeviceAuthorization(t, app, extra)
			userCode := started["user_code"].(string)
			body := contractsv1.DeviceApprovalPreviewRequest{SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema, UserCode: userCode}
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, oauthDeviceApprovalRequest(t, body, []string{"*"}, "preview_scopes"))
			if recorder.Code != http.StatusOK {
				t.Fatalf("preview: %d %s", recorder.Code, recorder.Body.String())
			}
			var preview contractsv1.DeviceApprovalPreviewResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(preview.RequestedScopes, tc.want) {
				t.Fatalf("requested_scopes = %v, want %v (body %s)", preview.RequestedScopes, tc.want, recorder.Body.String())
			}
		})
	}
}

// failingGrantLookupStore is the memory OAuth store with a GetDeviceGrant that
// fails after the grant was created: a storage outage on the preview path.
type failingGrantLookupStore struct {
	storage.OAuthStore
	fail *bool
}

func (s failingGrantLookupStore) GetDeviceGrant(ctx context.Context, hash storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error) {
	if *s.fail {
		return storage.OAuthDeviceGrant{}, errors.New("simulated grant store outage: secret-dsn-detail")
	}
	return s.OAuthStore.GetDeviceGrant(ctx, hash)
}

func previewRequestFor(t *testing.T, userCode, jti string) *http.Request {
	t.Helper()
	body := contractsv1.DeviceApprovalPreviewRequest{SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema, UserCode: userCode}
	return oauthDeviceApprovalRequest(t, body, []string{"*"}, jti)
}

// The scope answer the page shows must be diagnosable from the Info/Warn log
// alone: which scopes were shown and where they came from, and why a lookup
// failed. Scope names are a closed vocabulary; no code or id is logged.
func TestChaos7106PreviewDecisionIsLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		scope      string
		wantSource string
		wantScopes string
	}{
		"grant scopes":      {scope: "data:read", wantSource: `"requested_scopes_source":"grant"`, wantScopes: `"requested_scopes":"data:read"`},
		"unnamed = default": {scope: "", wantSource: `"requested_scopes_source":"grant"`, wantScopes: `"requested_scopes":"context:read evidence:read"`},
	} {
		t.Run(name, func(t *testing.T) {
			app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
			if err != nil {
				t.Fatal(err)
			}
			extra := url.Values{}
			if tc.scope != "" {
				extra.Set("scope", tc.scope)
			}
			started, _ := startOAuthDeviceAuthorization(t, app, extra)
			userCode := started["user_code"].(string)
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, previewRequestFor(t, userCode, "preview_log_"+name))
			if recorder.Code != http.StatusOK {
				t.Fatalf("preview: %d %s", recorder.Code, recorder.Body.String())
			}
			out := logs.String()
			for _, want := range []string{"device approval preview", tc.wantSource, tc.wantScopes} {
				if !strings.Contains(out, want) {
					t.Errorf("log lacks %s: %s", want, out)
				}
			}
			if strings.Contains(out, userCode) {
				t.Errorf("log leaks the user code: %s", out)
			}
		})
	}
}

func TestChaos7106PreviewLookupFailureIsLogged(t *testing.T) {
	fail := false
	store := failingGrantLookupStore{OAuthStore: memory.NewOAuthStore(func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }), fail: &fail}
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Store: store, Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, _ := startOAuthDeviceAuthorization(t, app, url.Values{"scope": {"data:read"}})
	userCode := started["user_code"].(string)
	fail = true
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, previewRequestFor(t, userCode, "preview_log_fail"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("preview with a failing grant lookup: %d %s", recorder.Code, recorder.Body.String())
	}
	out := logs.String()
	for _, want := range []string{"device approval preview scope lookup failed", `"reason":"device_grant_lookup_failed"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s: %s", want, out)
		}
	}
	for _, leak := range []string{userCode, "secret-dsn-detail"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}
}
