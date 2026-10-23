package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// POST /api/v1/oauth/token serves two grants and picks the handler by Content-Type alone (handleDeviceToken): only the
// form-encoded media type reaches the token exchange; every other Content-Type, and none, reaches the device-code grant.
// The endpoint-profile row names the selector content_type/application/x-www-form-urlencoded and relies on this fallback.
func TestOAuthTokenDispatchesTheExchangeOnlyForAFormContentType(t *testing.T) {
	deviceBody := `{"schema_version":"` + contractsv1.DeviceTokenRequestSchema + `","grant_type":"` + contractsv1.DeviceCodeGrantType + `","device_code":"nonexistent"}`
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "the-subject-jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	cases := []struct {
		name        string
		contentType string
		body        string
		exchange    bool
	}{
		{"form", "application/x-www-form-urlencoded", form.Encode(), true},
		{"form with a charset parameter", "application/x-www-form-urlencoded; charset=utf-8", form.Encode(), true},
		{"form in upper case", "APPLICATION/X-WWW-FORM-URLENCODED", form.Encode(), true},
		{"json", "application/json", deviceBody, false},
		{"plain text", "text/plain", deviceBody, false},
		{"no content type", "", deviceBody, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exchanger := &fakeWorkloadTokenExchanger{}
			app := newTokenExchangeTestApp(t, exchanger)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(c.body))
			if c.contentType != "" {
				request.Header.Set("Content-Type", c.contentType)
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if got := exchanger.lastSubjectToken != ""; got != c.exchange {
				t.Fatalf("exchange reached = %v, want %v (status %d body %s)", got, c.exchange, response.Code, response.Body.String())
			}
			if !c.exchange {
				var deviceError contractsv1.OAuthDeviceErrorResponse
				if err := json.Unmarshal(response.Body.Bytes(), &deviceError); err != nil || deviceError.SchemaVersion != contractsv1.OAuthDeviceErrorSchema {
					t.Fatalf("the device-code grant did not answer: %s", response.Body.String())
				}
			}
		})
	}
}
