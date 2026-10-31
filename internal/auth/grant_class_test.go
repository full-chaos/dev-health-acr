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

// CHAOS-12299: a project grant makes the caller project_only, whatever its
// repository list says; an empty project grant changes nothing.
func TestClassifyGrantReadsAProjectGrantFirst(t *testing.T) {
	t.Parallel()
	project := []string{"project.v2:github:P-1"}
	for _, tc := range []struct {
		name     string
		projects []string
		scopes   []string
		want     GrantClass
	}{
		{name: "a project grant alone", projects: project, want: GrantProjectOnly},
		{name: "a project grant beside an empty list", projects: project, scopes: []string{}, want: GrantProjectOnly},
		{name: "a project grant beside a star", projects: project, scopes: []string{"*"}, want: GrantProjectOnly},
		{name: "a project grant beside a repository", projects: project, scopes: []string{"a/b"}, want: GrantProjectOnly},
		{name: "an empty project grant", projects: []string{}, scopes: nil, want: GrantUnrestricted},
		{name: "an empty project grant beside a repository", projects: []string{}, scopes: []string{"a/b"}, want: GrantRestricted},
	} {
		if got := ClassifyGrant(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes, ProjectScopes: tc.projects}); got != tc.want {
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
