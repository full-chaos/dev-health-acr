package entitlements_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// CHAOS-12301 (ops CHAOS-12224 and CHAOS-12267): go-api answers an inventory
// above its limit with a 503 and a body code. Each code is a typed access
// limitation with its own reason, never a grant and never a retryable outage.
// Every other 503, a body that does not decode included, is the plain
// rejection: reason "status".
func TestAnOverLimitCodeIsATypedAccessLimitationWithItsOwnReason(t *testing.T) {
	for code, want := range map[string]string{
		"acr_access_repository_scope_over_limit": "repository_scope_over_limit",
		"acr_access_project_scope_over_limit":    "project_scope_over_limit",
	} {
		server, _, _ := accessServer(t, http.StatusServiceUnavailable, `{"detail":"Service unavailable","code":"`+code+`"}`)
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err == nil || access.Member || len(access.Projects) != 0 {
			t.Fatalf("503 %s: access %+v err %v, want a rejection and no answer", code, access, err)
		}
		if got := entitlements.RejectionReason(err); got != want {
			t.Fatalf("503 %s: rejection reason %q, want %q", code, got, want)
		}
		if !entitlements.AccessScopeOverLimit(err) {
			t.Fatalf("503 %s: AccessScopeOverLimit is false, want the typed limitation", code)
		}
	}
}

func TestEveryOther503StaysAPlainStatusRejection(t *testing.T) {
	for name, body := range map[string]string{
		"no code":          `{"detail":"Service unavailable"}`,
		"another code":     `{"detail":"Service unavailable","code":"acr_access_something_else"}`,
		"a code of a kind": `{"code":"ACR_ACCESS_PROJECT_SCOPE_OVER_LIMIT"}`,
		"not JSON":         `Service unavailable`,
		"truncated":        `{"code":"acr_access_project_scope_over_limit"`,
		"a non-string":     `{"code":1}`,
		"an empty body":    ``,
	} {
		server, _, _ := accessServer(t, http.StatusServiceUnavailable, body)
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err == nil || access.Member {
			t.Fatalf("503 %s: access %+v err %v, want a rejection", name, access, err)
		}
		if got := entitlements.RejectionReason(err); got != "status" {
			t.Fatalf("503 %s: rejection reason %q, want %q", name, got, "status")
		}
		if entitlements.AccessScopeOverLimit(err) {
			t.Fatalf("503 %s: read as the typed limitation, want a plain outage", name)
		}
	}
}

// The code is read only on a 503: a 200 is the document and every other
// status is a plain rejection, whatever its body says.
func TestAnOverLimitCodeOnAnotherStatusIsNotTheLimitation(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusTooManyRequests} {
		server, _, _ := accessServer(t, status, `{"code":"acr_access_project_scope_over_limit"}`)
		_, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err == nil || entitlements.RejectionReason(err) != "status" || entitlements.AccessScopeOverLimit(err) {
			t.Fatalf("status %d: err %v reason %q, want a plain status rejection", status, err, entitlements.RejectionReason(err))
		}
	}
}

func TestAccessScopeOverLimitIsFalseForEveryOtherError(t *testing.T) {
	for _, err := range []error{nil, entitlements.ErrPrincipalUnknown, entitlements.ErrSessionInvalid} {
		if entitlements.AccessScopeOverLimit(err) {
			t.Fatalf("AccessScopeOverLimit(%v) = true", err)
		}
	}
}
