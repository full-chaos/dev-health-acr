package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
