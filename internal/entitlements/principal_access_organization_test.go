package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

// CHAOS-13566 (acp ruling Q4 on CHAOS-13561): acr learns the two answers the
// ops read sends once it reads the principal's own read listing, before ops
// sends them. Kind "organization" is an org-wide reader in an organization
// that has repositories; it carries no repos key.

func TestPrincipalAccessDecodesKindOrganization(t *testing.T) {
	server, _, _ := accessServer(t, http.StatusOK, accessDocument(false, `{"kind":"organization"}`))
	access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if !access.Member || !access.OrganizationWide || access.Repositories != nil || access.Projects != nil {
		t.Fatalf("access = %+v, want an org-wide member with no repository list", access)
	}
}

func TestPrincipalAccessKindsOtherThanOrganizationAreNotOrgWide(t *testing.T) {
	for _, scope := range []string{`{"kind":"none"}`, `{"kind":"repos","repos":["acme/api"]}`} {
		server, _, _ := accessServer(t, http.StatusOK, accessDocument(false, scope))
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err != nil {
			t.Fatalf("%s: %v", scope, err)
		}
		if access.OrganizationWide {
			t.Fatalf("%s: OrganizationWide = true", scope)
		}
	}
}

func TestPrincipalAccessRefusesAMalformedOrganizationScope(t *testing.T) {
	for name, scope := range map[string]string{
		"organization with repos":       `{"kind":"organization","repos":["acme/api"]}`,
		"organization with empty repos": `{"kind":"organization","repos":[]}`,
		"organization in upper case":    `{"kind":"Organization"}`,
		"org":                           `{"kind":"org"}`,
		"all":                           `{"kind":"all"}`,
	} {
		server, _, _ := accessServer(t, http.StatusOK, accessDocument(false, scope))
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err == nil || access.Member || access.OrganizationWide {
			t.Errorf("%s: access %+v err %v, want an unavailable rejection", name, access, err)
		}
	}
}

// The read-listing cap (more than 256 readable repositories) is a typed
// limitation with its own reason, like the inventory limits: it grants
// nothing and a retry cannot clear it.
func TestPrincipalAccessAllowlistTooLargeIsATypedLimitation(t *testing.T) {
	server, _, _ := accessServer(t, http.StatusServiceUnavailable, `{"detail":"Service unavailable","code":"acr_access_repository_allowlist_too_large"}`)
	access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
	if err == nil || access.Member || errors.Is(err, entitlements.ErrPrincipalUnknown) {
		t.Fatalf("access %+v err %v, want a rejection", access, err)
	}
	if !entitlements.AccessScopeOverLimit(err) || entitlements.RejectionReason(err) != "repository_allowlist_too_large" {
		t.Fatalf("over limit %v reason %q, want the typed repository_allowlist_too_large", entitlements.AccessScopeOverLimit(err), entitlements.RejectionReason(err))
	}
}
