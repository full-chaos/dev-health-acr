package auth

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12297: the grant class is the one classification of a grant.
func TestClassifyGrantReadsTheRepositoryGrantList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		scopes []string
		want   GrantClass
	}{
		{name: "no grant list", scopes: nil, want: GrantUnrestricted},
		{name: "empty grant list", scopes: []string{}, want: GrantUnrestricted},
		{name: "star", scopes: []string{"*"}, want: GrantUniversal},
		{name: "star with spaces", scopes: []string{" * "}, want: GrantUniversal},
		{name: "one repository", scopes: []string{"a/b"}, want: GrantRestricted},
		{name: "repository and star", scopes: []string{"a/b", "*"}, want: GrantUniversal},
		{name: "star before a repository", scopes: []string{"*", "a/b"}, want: GrantUniversal},
		{name: "duplicate repositories", scopes: []string{"a/b", "a/b"}, want: GrantRestricted},
		{name: "duplicate stars", scopes: []string{"*", "*"}, want: GrantUniversal},
		{name: "whitespace-only entry", scopes: []string{" "}, want: GrantRestricted},
	} {
		if got := ClassifyGrant(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}); got != tc.want {
			t.Errorf("%s: ClassifyGrant = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// project_only is a restricted subtype; nothing else is restricted except
// the restricted class itself, and an unknown class fails closed.
func TestIsRestrictedHoldsForRestrictedAndProjectOnly(t *testing.T) {
	t.Parallel()
	want := map[GrantClass]bool{GrantUnrestricted: false, GrantUniversal: false, GrantRestricted: true, GrantProjectOnly: true}
	for _, class := range GrantClassVocabulary() {
		if got := class.IsRestricted(); got != want[class] {
			t.Errorf("%s.IsRestricted() = %v, want %v", class, got, want[class])
		}
	}
	if !GrantClass("bogus").IsRestricted() {
		t.Error("an unknown grant class is not restricted; it must fail closed")
	}
}
