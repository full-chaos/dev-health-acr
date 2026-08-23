package devhealthsource

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// CHAOS-4099 stage-2 lane evidence (precondition A obligation 3, recorded
// 2026-08-22): both authorization sentinels -- noRepositorySentinel and
// orphanedRepositorySentinel -- fail closed against every scoped principal
// because graphrank.ScopeMatch's exact-equality branch is never validated
// against the caller side. A principal whose scope were literally
// "acr-context-fabric:no-repository" WOULD match the sentinel (verified by
// hand at the time). That is not a defect: the sentinels are UNISSUABLE as
// scopes in the first place, closed one layer away at the credential
// boundary (internal/auth.NormalizeRepositoryScopes, which
// validRepositoryScope backs) rather than in graphrank's matcher itself.
//
// This test converts that hand-verified claim into a guard. It does not
// duplicate ScopeMatch's own behavior (graphrank/scope_test.go already pins
// that); it pins the OTHER half of the safety argument -- that the
// sentinel values can never legitimately reach a principal's
// RepositoryScopes in the first place, so ScopeMatch's own unvalidated
// exact-match branch never actually has to defend against them in
// production.
func TestChaos4099_NoAuthorizationSentinelIsAnIssuableRepositoryScope(t *testing.T) {
	t.Parallel()

	sentinels := []string{noRepositorySentinel, orphanedRepositorySentinel}
	for _, sentinel := range sentinels {
		if _, err := auth.NormalizeRepositoryScopes([]string{sentinel}); err == nil {
			t.Fatalf("NormalizeRepositoryScopes(%q) succeeded, want rejection: this sentinel must never be issuable as a real repository scope", sentinel)
		}
	}

	// Positive control, in the same pass: an ordinary "owner/repo" scope
	// -- the shape workItemAuthorization/repoAuthorization actually put on
	// a node's own authorization list -- is NOT rejected by the same
	// validator. Without this, a validator that rejected EVERYTHING would
	// pass the test above for the wrong reason.
	if _, err := auth.NormalizeRepositoryScopes([]string{"full.chaos/dev-health-ops"}); err != nil {
		t.Fatalf("NormalizeRepositoryScopes(ordinary slug) failed: %v, want acceptance (positive control)", err)
	}
}

// TestChaos4099_SentinelConstantsContainTheColonValidRepositoryScopeRejectsOn
// pins WHY the rejection above holds, not just THAT it does: both
// sentinels contain a ':', and internal/auth's own scope grammar admits
// exactly two '/'-separated parts with no ':' anywhere -- a schema
// property, not an accident this test wants to survive a sentinel string
// ever being edited to drop the colon without anyone revisiting this
// guard.
func TestChaos4099_SentinelConstantsContainTheColonValidRepositoryScopeRejectsOn(t *testing.T) {
	t.Parallel()

	for _, sentinel := range []string{noRepositorySentinel, orphanedRepositorySentinel} {
		if !containsColon(sentinel) {
			t.Fatalf("sentinel %q no longer contains ':' -- the rejection this guard pins may no longer hold for the reason recorded; re-verify NormalizeRepositoryScopes still rejects it", sentinel)
		}
	}
}

func containsColon(s string) bool {
	for _, r := range s {
		if r == ':' {
			return true
		}
	}
	return false
}
