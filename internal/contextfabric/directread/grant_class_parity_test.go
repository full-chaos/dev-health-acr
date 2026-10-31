package directread

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12297: the principal class and the policy's caller class read a
// grant exactly as they did before the typed grant class existed. The
// expected values are main's (194d9dc4) behavior, written out.
func TestThePrincipalAndCallerClassesReadAGrantAsBefore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		scopes []string
		class  PrincipalClass
		caller CallerClass
	}{
		{name: "no grant list", scopes: nil, class: ClassUnrestricted, caller: CallerUnrestricted},
		{name: "empty grant list", scopes: []string{}, class: ClassUnrestricted, caller: CallerUnrestricted},
		{name: "star", scopes: []string{"*"}, class: ClassUniversal, caller: CallerUnrestricted},
		{name: "star with spaces", scopes: []string{" * "}, class: ClassUniversal, caller: CallerUnrestricted},
		{name: "one repository", scopes: []string{"a/b"}, class: ClassRestricted, caller: CallerRestricted},
		{name: "repository and star", scopes: []string{"a/b", "*"}, class: ClassUniversal, caller: CallerUnrestricted},
	} {
		got := ClassifyPrincipal(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes})
		if got != tc.class {
			t.Errorf("%s: ClassifyPrincipal = %q, want %q", tc.name, got, tc.class)
		}
		if caller := CallerClassFor(got); caller != tc.caller {
			t.Errorf("%s: CallerClassFor(%q) = %q, want %q", tc.name, got, caller, tc.caller)
		}
	}
	// An out-of-vocabulary class is restricted (fail closed), as before.
	if got := CallerClassFor(PrincipalClass("bogus")); got != CallerRestricted {
		t.Errorf("CallerClassFor(bogus) = %q, want %q", got, CallerRestricted)
	}
}

// project_only reads as restricted in both directread vocabularies, and
// every grant class maps onto a member of each closed vocabulary.
func TestEveryGrantClassMapsOntoTheDirectReadVocabularies(t *testing.T) {
	t.Parallel()
	want := map[auth.GrantClass]struct {
		class  PrincipalClass
		caller CallerClass
	}{
		auth.GrantUnrestricted: {ClassUnrestricted, CallerUnrestricted},
		auth.GrantUniversal:    {ClassUniversal, CallerUnrestricted},
		auth.GrantRestricted:   {ClassRestricted, CallerRestricted},
		auth.GrantProjectOnly:  {ClassRestricted, CallerRestricted},
	}
	for _, grant := range auth.GrantClassVocabulary() {
		if got := PrincipalClassForGrant(grant); got != want[grant].class {
			t.Errorf("PrincipalClassForGrant(%s) = %q, want %q", grant, got, want[grant].class)
		}
		if got := CallerClassForGrant(grant); got != want[grant].caller {
			t.Errorf("CallerClassForGrant(%s) = %q, want %q", grant, got, want[grant].caller)
		}
	}
	if PrincipalClassForGrant("bogus") != ClassRestricted || CallerClassForGrant("bogus") != CallerRestricted {
		t.Error("an unknown grant class must map to restricted in both vocabularies")
	}
}
