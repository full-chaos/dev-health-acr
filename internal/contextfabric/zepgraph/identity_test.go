package zepgraph

import "testing"

// These tests exercise encodeScope/scopeContains/decodeScope directly,
// bypassing contract validation entirely, to prove the encoder itself fails
// closed on unencodable input -- independent of the v1 contract-layer
// rejection in ContextFabricAuthorizationScope.Validate() and
// boundedEvidenceRefs. Both layers must hold on their own: the contract
// check keeps bad input out of the adapter at all, and this layer ensures
// that if a '|'-bearing value ever reached the encoder anyway (a future
// caller that skips validation, a bug in the contract check, etc.) it could
// never widen authorization to "matches everything".

func TestEncodeScopeReturnsWildcardOnlyForGenuinelyEmptyInput(t *testing.T) {
	if got := encodeScope(nil); got != "*" {
		t.Fatalf("encodeScope(nil) = %q, want \"*\"", got)
	}
	if got := encodeScope([]string{}); got != "*" {
		t.Fatalf("encodeScope([]) = %q, want \"*\"", got)
	}
}

// TestEncodeScopeNeverWidensToWildcardOnUnencodableValue is the direct fix
// for F1: a non-empty input that produces zero usable values (every value
// unencodable) must fail closed, never fall back to "*".
func TestEncodeScopeNeverWidensToWildcardOnUnencodableValue(t *testing.T) {
	for _, values := range [][]string{
		{"full-chaos/private|leak"}, // contains the separator
		{"  "},                      // empty after trimming
		{"full-chaos/private|leak", "another|bad"},
	} {
		encoded := encodeScope(values)
		if encoded == "*" {
			t.Fatalf("encodeScope(%#v) = %q: a non-empty, fully-unencodable input widened to the wildcard", values, encoded)
		}
		if encoded != scopeDeniedSentinel {
			t.Fatalf("encodeScope(%#v) = %q, want the fail-closed sentinel", values, encoded)
		}
	}
}

// TestEncodeScopePartialDropFailsClosedInsteadOfNarrowing is the "partial
// drop" case: one usable value alongside one unencodable value must not
// silently encode to just the usable value (which would read later as "this
// scope only ever had one entry" instead of "an input value was rejected").
func TestEncodeScopePartialDropFailsClosedInsteadOfNarrowing(t *testing.T) {
	encoded := encodeScope([]string{"full-chaos/clean", "full-chaos/private|leak"})
	if encoded != scopeDeniedSentinel {
		t.Fatalf("encodeScope() with one bad value = %q, want the fail-closed sentinel (not a silently narrowed encoding of just the clean value)", encoded)
	}
	if scopeContains(encoded, "full-chaos/clean") {
		t.Fatal("a partially-dropped scope still matched the surviving clean value")
	}
}

// TestScopeContainsNeverMatchesTheDeniedSentinel is the other half of the
// F1 fix: even if encodeScope's fail-closed sentinel reaches scopeContains
// directly, it must never authorize any value -- the whole point of failing
// closed is that nothing downstream can accidentally treat it as "matches
// everything" the way the old bare-separator encoding did.
func TestScopeContainsNeverMatchesTheDeniedSentinel(t *testing.T) {
	for _, probe := range []string{"", "*", "totally/unrelated", "full-chaos/private|leak"} {
		if scopeContains(scopeDeniedSentinel, probe) {
			t.Fatalf("scopeContains(sentinel, %q) = true, want false: the sentinel must authorize nothing", probe)
		}
	}
}

// TestDecodeScopeTreatsTheDeniedSentinelAsEmpty guards the evidence/alias
// read path: decodeScope must not treat the raw sentinel string as literal
// decoded data (e.g. one bogus "evidence ref").
func TestDecodeScopeTreatsTheDeniedSentinelAsEmpty(t *testing.T) {
	if got := decodeScope(scopeDeniedSentinel); len(got) != 0 {
		t.Fatalf("decodeScope(sentinel) = %#v, want empty", got)
	}
}

// TestScopeContainsAcceptsPrincipalSideWildcards is the direct regression
// for the CHAOS-3752 Reset 0 review must-do: principal-side wildcard scopes
// ("*", "owner/*"), both valid per internal/auth.RepositoryAllowed and
// internal/auth.validRepositoryScope, must match a node's specific encoded
// authorization list instead of matching nothing.
func TestScopeContainsAcceptsPrincipalSideWildcards(t *testing.T) {
	t.Parallel()
	encoded := encodeScope([]string{"acme/repo-x", "acme/repo-y"})
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"global wildcard matches any encoded repository", "*", true},
		{"owner wildcard matches an encoded repository under that owner", "acme/*", true},
		{"owner wildcard does not match an unrelated owner", "other/*", false},
		{"exact match still works alongside wildcard handling", "acme/repo-x", true},
		{"exact match on an absent repository still fails", "acme/repo-z", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := scopeContains(encoded, tc.value); got != tc.want {
				t.Fatalf("scopeContains(%q, %q) = %v, want %v", encoded, tc.value, got, tc.want)
			}
		})
	}
}

// TestScopeContainsWildcardNeverWidensPastTheDeniedSentinelOrEmptyEncoding
// proves the wildcard fix does not resurrect the F1 fail-open bug: a
// principal-side wildcard must still authorize nothing against the
// fail-closed sentinel, and an owner wildcard must not match when the
// encoded list simply has no repository under that owner.
func TestScopeContainsWildcardNeverWidensPastTheDeniedSentinelOrEmptyEncoding(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"*", "acme/*"} {
		if scopeContains(scopeDeniedSentinel, value) {
			t.Fatalf("scopeContains(sentinel, %q) = true, want false", value)
		}
	}
	onlyOtherOwner := encodeScope([]string{"other/repo-y"})
	if scopeContains(onlyOtherOwner, "acme/*") {
		t.Fatal("owner wildcard matched an encoded scope with no repository under that owner")
	}
	// A bare "/*" (empty owner) must not match every entry as if it were a
	// second global wildcard.
	if scopeContains(onlyOtherOwner, "/*") {
		t.Fatal("empty-owner wildcard must not match")
	}
}

// TestScopeContainsDeniesMissingOrEmptyAuthorizationAttribute is the probe
// for Codex finding G3(a): scopeContains("", "*") previously returned true
// because the "*" (global wildcard) branch fired before checking whether
// encoded represented an actual authorization list at all. encodeScope
// never legitimately produces "" (empty repo list encodes to "*", not
// ""), so an empty encoded attribute can only mean the attribute is
// missing or malformed -- absence of a scope must deny, never authorize,
// regardless of how permissive the caller-side value is.
func TestScopeContainsDeniesMissingOrEmptyAuthorizationAttribute(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"*", "acme/*", "acme/repo-x"} {
		if scopeContains("", value) {
			t.Fatalf("scopeContains(\"\", %q) = true, want false: a missing/empty authorization attribute must never authorize", value)
		}
	}
}

// TestScopeContainsOwnerWildcardRejectsMalformedSlugEntries is the probe
// for Codex finding G3(b): the owner/* matcher split each decoded entry on
// its first "/" without validating the result was a well-formed
// "owner/repo" slug (per internal/auth.NormalizeRepositorySlug), so a
// malformed entry like "acme/" (empty repo) or "acme/not/real" (extra
// segment) still satisfied an "acme/*" wildcard by owner-prefix alone.
func TestScopeContainsOwnerWildcardRejectsMalformedSlugEntries(t *testing.T) {
	t.Parallel()
	encoded := scopeSeparator + "acme/" + scopeSeparator + "acme/not/real" + scopeSeparator
	if scopeContains(encoded, "acme/*") {
		t.Fatalf("scopeContains(%q, \"acme/*\") = true, want false: malformed entries must not satisfy an owner wildcard", encoded)
	}
}
