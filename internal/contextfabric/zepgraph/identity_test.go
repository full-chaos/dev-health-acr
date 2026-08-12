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
