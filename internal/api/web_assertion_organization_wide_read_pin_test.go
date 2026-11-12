package api

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// Every route an organization-wide read assertion may name is a protected
// route that takes web assertions, and each of its permission sets holds the
// route's required scope and no credential:issue.
func TestWebAssertionGolden_organizationWideReadRoutesPin(t *testing.T) {
	fixture := loadGolden(t)
	routes := auth.WebAssertionOrganizationWideReadRoutes()
	if len(routes) == 0 {
		t.Fatal("no organization-wide read routes")
	}
	for _, route := range routes {
		index := slices.IndexFunc(fixture.Routes, func(golden goldenRoute) bool {
			return golden.Method == route.Method && golden.Pattern == route.Pattern
		})
		if index < 0 {
			t.Fatalf("%s %s is not a protected acr route", route.Method, route.Pattern)
		}
		golden := fixture.Routes[index]
		if !golden.WebAssertion {
			t.Fatalf("%s %s does not take web assertions", route.Method, route.Pattern)
		}
		if len(route.PermissionSets) == 0 {
			t.Fatalf("%s %s has no permission set", route.Method, route.Pattern)
		}
		for _, set := range route.PermissionSets {
			if !slices.Contains(set, golden.Scope) || slices.Contains(set, auth.WebAssertionPermissionCredentialIssue) || !slices.IsSorted(set) {
				t.Fatalf("%s %s permission set %v: want sorted, holding %s, without %s", route.Method, route.Pattern, set, golden.Scope, auth.WebAssertionPermissionCredentialIssue)
			}
		}
	}
}
