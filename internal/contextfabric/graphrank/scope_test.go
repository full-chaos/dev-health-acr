package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// These tests port zepgraph/identity_test.go's scopeContains probes to
// graphrank.ScopeMatch, which is the wildcard/owner-wildcard matching core
// CHAOS-3752 extracted out of scopeContains (see scope.go's doc comment).
// ScopeMatch operates on an already-decoded []string, so the pipe-encoding
// mechanics and the fail-closed sentinel (scopeDeniedSentinel) that guard
// zepgraph's wire representation have no equivalent here and are
// deliberately NOT ported -- a native-list backend like falkorgraph never
// produces or needs them. What DOES port is the wildcard-matching semantics
// itself: owner-wildcard validation via internal/auth.NormalizeRepositorySlug
// and the "absence/emptiness must deny, never authorize" rule, both of which
// are backend-neutral and must hold identically for every graph adapter.

// TestScopeMatchAcceptsPrincipalSideWildcards is the direct port of
// zepgraph's TestScopeContainsAcceptsPrincipalSideWildcards: a principal-side
// wildcard ("*", "owner/*") must match a node's specific decoded
// authorization list, not just an exact slug.
func TestScopeMatchAcceptsPrincipalSideWildcards(t *testing.T) {
	t.Parallel()
	entries := []string{"acme/repo-x", "acme/repo-y"}
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"global wildcard matches any entry", "*", true},
		{"owner wildcard matches an entry under that owner", "acme/*", true},
		{"owner wildcard does not match an unrelated owner", "other/*", false},
		{"exact match still works alongside wildcard handling", "acme/repo-x", true},
		{"exact match on an absent repository still fails", "acme/repo-z", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ScopeMatch(entries, tc.value); got != tc.want {
				t.Fatalf("ScopeMatch(%#v, %q) = %v, want %v", entries, tc.value, got, tc.want)
			}
		})
	}
}

// TestScopeMatchEmptyEntriesOnlyMatchesTheGlobalWildcard proves ScopeMatch's
// documented behavior for a genuinely empty (but PRESENT) decoded entries
// list: only a caller-side global "*" matches (per authorize.go's doc
// comment -- an object with no specific entries of its own is still visible
// to a principal holding org-wide "*" access), while an owner wildcard or an
// exact slug matches nothing, since there is no entry to match against.
func TestScopeMatchEmptyEntriesOnlyMatchesTheGlobalWildcard(t *testing.T) {
	t.Parallel()
	for _, entries := range [][]string{nil, {}} {
		if !ScopeMatch(entries, "*") {
			t.Fatalf("ScopeMatch(%#v, \"*\") = false, want true: a caller-side global wildcard authorizes unconditionally", entries)
		}
		for _, value := range []string{"acme/*", "acme/repo-x"} {
			if ScopeMatch(entries, value) {
				t.Fatalf("ScopeMatch(%#v, %q) = true, want false: an empty entries list has no entry to match a non-wildcard value against", entries, value)
			}
		}
	}
}

// TestAuthorizedAttributesDeniesMissingAuthorizationAttribute is the direct
// port of zepgraph's TestScopeContainsDeniesMissingOrEmptyAuthorizationAttribute
// (Codex finding G3(a)), translated to graphrank's real equivalent: zepgraph's
// scopeContains("", value) probed its own wire-level sentinel for "the
// attribute is missing from the node/edge entirely" -- graphrank's
// equivalent of "missing" is the attribute KEY being absent from the
// Attributes map altogether (see authorize.go's doc comment), checked one
// level up from ScopeMatch by AuthorizedAttributes/scopeContainsAttr. A
// missing authorization attribute must deny every caller-side value,
// including "*", regardless of how permissive it is.
func TestAuthorizedAttributesDeniesMissingAuthorizationAttribute(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"*", "acme/*", "acme/repo-x"} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{value}}
		// attributes carries no "authorization_repositories" key at all.
		attributes := map[string]interface{}{"canonical_id": "project_1"}
		if AuthorizedAttributes(principal, contextfabric.RequestedScope{}, attributes) {
			t.Fatalf("AuthorizedAttributes() with a missing authorization attribute and principal scope %q = true, want false: absence must never authorize", value)
		}
	}
}

// TestScopeMatchOwnerWildcardRejectsMalformedSlugEntries is the direct port
// of zepgraph's TestScopeContainsOwnerWildcardRejectsMalformedSlugEntries
// (Codex finding G3(b)): the owner/* matcher must validate each decoded
// entry as a well-formed "owner/repo" slug via
// internal/auth.NormalizeRepositorySlug before trusting its owner prefix, so
// a malformed entry like "acme/" (empty repo) or "acme/not/real" (extra
// segment) cannot satisfy an "acme/*" wildcard by owner-prefix alone.
func TestScopeMatchOwnerWildcardRejectsMalformedSlugEntries(t *testing.T) {
	t.Parallel()
	entries := []string{"acme/", "acme/not/real"}
	if ScopeMatch(entries, "acme/*") {
		t.Fatalf("ScopeMatch(%#v, \"acme/*\") = true, want false: malformed entries must not satisfy an owner wildcard", entries)
	}
}

// TestScopeMatchOwnerWildcardDoesNotWidenPastWhatIsEncoded is the direct
// port of zepgraph's
// TestScopeContainsWildcardNeverWidensPastTheDeniedSentinelOrEmptyEncoding,
// with the pipe-encoding-specific fail-closed-sentinel sub-case dropped
// (ScopeMatch never sees zepgraph's sentinel -- that is
// TestScopeMatchDeniesOnEmptyOrNilEntries's job at the "decoded to nothing"
// level). What ports directly: an owner wildcard must not match when the
// entries list simply has no repository under that owner, and a bare "/*"
// (empty owner) must not match every entry as if it were a second global
// wildcard.
func TestScopeMatchOwnerWildcardDoesNotWidenPastWhatIsEncoded(t *testing.T) {
	t.Parallel()
	onlyOtherOwner := []string{"other/repo-y"}
	if ScopeMatch(onlyOtherOwner, "acme/*") {
		t.Fatal("owner wildcard matched an entries list with no repository under that owner")
	}
	if ScopeMatch(onlyOtherOwner, "/*") {
		t.Fatal("empty-owner wildcard must not match")
	}
}

// TestAnyScopeMatchMatchesIfAnyValueMatches proves AnyScopeMatch is a plain
// OR over ScopeMatch -- no zepgraph analogue existed for this helper
// directly (it is a graphrank addition used by AuthorizedAttributes), but it
// has zero coverage of its own and is on the exported surface this task asks
// to verify.
func TestAnyScopeMatchMatchesIfAnyValueMatches(t *testing.T) {
	t.Parallel()
	entries := []string{"acme/repo-x"}
	if !AnyScopeMatch(entries, []string{"other/repo-z", "acme/repo-x"}) {
		t.Fatal("AnyScopeMatch() = false, want true when at least one value matches")
	}
	if AnyScopeMatch(entries, []string{"other/repo-z", "other/*"}) {
		t.Fatal("AnyScopeMatch() = true, want false when no value matches")
	}
}
