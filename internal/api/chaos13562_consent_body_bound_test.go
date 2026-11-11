package api

import (
	"bytes"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/reposlug"
)

// CHAOS-13562: the consent body bound holds the largest legal approve (256
// repositories of 255 bytes), a body padded to exactly the bound is read, and
// one byte more is refused as body_too_large.
func TestCHAOS13562_ConsentBodyBoundHoldsTheLargestLegalApprove(t *testing.T) {
	if oauthConsentBodyMaxBytes != contractsv1.MaxRepositoryScopes*(reposlug.MaxSlugBytes+3)+oauthConsentBodySlackBytes {
		t.Fatalf("oauthConsentBodyMaxBytes = %d is not derived from the named constants", oauthConsentBodyMaxBytes)
	}
	app, clientID := chaos13562ConsentApp(t)
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
	scopes := chaos13562Slugs(contractsv1.MaxRepositoryScopes)
	largest := chaos13562ApproveBody(t, handle, scopes)
	t.Logf("largest legal approve body: %d bytes; bound %d", len(largest), oauthConsentBodyMaxBytes)
	if len(largest) > oauthConsentBodyMaxBytes {
		t.Fatalf("largest legal approve body %d bytes exceeds the bound %d", len(largest), oauthConsentBodyMaxBytes)
	}
	pad := func(body []byte, size int) []byte {
		closing := bytes.LastIndexByte(body, '}')
		padded := append(append(append([]byte{}, body[:closing]...), strings.Repeat(" ", size-len(body))...), body[closing:]...)
		if len(padded) != size {
			t.Fatalf("padded to %d, want %d", len(padded), size)
		}
		return padded
	}
	over, answer := chaos13562Consent(t, app, pad(largest, oauthConsentBodyMaxBytes+1), chaos13562Assertion{scopes: scopes}, "over")
	if over.Code != 400 || answer["error"] != "invalid_request" || answer["error_description"] != "body_too_large" {
		t.Fatalf("bound+1 bytes: %d %v, want 400 invalid_request body_too_large", over.Code, answer)
	}
	at, answer := chaos13562Consent(t, app, pad(largest, oauthConsentBodyMaxBytes), chaos13562Assertion{scopes: scopes}, "at")
	if at.Code != 200 || answer["redirect_url"] == nil {
		t.Fatalf("exactly the bound: %d %v, want 200", at.Code, answer)
	}
}
