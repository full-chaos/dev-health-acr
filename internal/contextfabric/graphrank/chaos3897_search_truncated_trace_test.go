package graphrank

import "testing"

// TestResolveFromMergedCandidatesWithGate_TracerObservesSearchTruncated is
// CHAOS-3897's core proof: the decision-stage event must carry
// SearchTruncated so a reader can tell "truncation preempted the commit
// switch's confidence gates entirely" (CommitGate=="", SearchTruncated==
// true) apart from "the gates evaluated and blocked" (e.g.
// IdentityTrustGateBlocked==true, SearchTruncated==false, from the
// existing CHAOS-3891 coverage below). Reuses the same sole-identity-
// candidate shape TestResolveFromMergedCandidatesWithGate_
// IncompleteLookupNeverCommitsViaIdentity already pins for
// searchTruncated=true, aliasIdentityComplete=false: aliasIdentityComplete
// must be false here too, or the identity_fast_path case (which sits
// BEFORE searchTruncated in the switch precisely because a complete
// keyed lookup survives truncation) would commit instead and never reach
// the branch this test exists to observe. With aliasIdentityComplete=
// false, resolution.go's `case searchTruncated` fires BEFORE LoneFloor/
// TopFloor are ever reached, so CommitGate stays "".
func TestResolveFromMergedCandidatesWithGate_TracerObservesSearchTruncated(t *testing.T) {
	t.Parallel()
	repo := repoAliasCandidate("r1", "chaos-ops")
	identity, terms := identitySideChannels(repo)
	tracer := &captureResolutionTracer{}

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(repo), map[string]string{}, map[string]bool{}, 10, true, true /* searchTruncated */, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, terms, false /* aliasIdentityComplete=false: keep identity_fast_path from preempting the searchTruncated branch */, tracer, "req-truncated")

	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want nothing committed -- searchTruncated preempts every commit gate", resolution.Committed)
	}
	decisions := tracer.eventsForStage("decision")
	if len(decisions) != 1 {
		t.Fatalf("decision events = %#v, want exactly 1", decisions)
	}
	got := decisions[0]
	if got.Outcome != "ambiguous" {
		t.Fatalf("decision event Outcome = %q, want %q", got.Outcome, "ambiguous")
	}
	if got.CommitGate != "" {
		t.Fatalf("decision event CommitGate = %q, want empty -- searchTruncated short-circuits before any gate names itself", got.CommitGate)
	}
	if !got.SearchTruncated {
		t.Fatal("decision event SearchTruncated = false, want true -- this ambiguous outcome must be attributable to truncation, distinct from a gate-blocked one")
	}
}

// TestResolveFromMergedCandidatesWithGate_TracerSearchTruncatedFalseWhenUntruncated
// is the negative control: an ordinary, untruncated LoneFloor commit must
// report SearchTruncated==false, proving the field reflects the real
// per-resolution signal rather than defaulting true or leaking across
// resolutions.
func TestResolveFromMergedCandidatesWithGate_TracerSearchTruncatedFalseWhenUntruncated(t *testing.T) {
	t.Parallel()
	lone := noiseCandidate("ci1", 0.9)
	tracer := &captureResolutionTracer{}

	resolution := ResolveFromMergedCandidatesWithGate(identityBySubject(lone), map[string]string{}, map[string]bool{}, 10, true, false /* searchTruncated=false */, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-untruncated")

	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "ci1" {
		t.Fatalf("resolution.Committed = %#v, want ci1 committed via LoneFloor", resolution.Committed)
	}
	decisions := tracer.eventsForStage("decision")
	if len(decisions) != 1 {
		t.Fatalf("decision events = %#v, want exactly 1", decisions)
	}
	if decisions[0].SearchTruncated {
		t.Fatal("decision event SearchTruncated = true, want false -- this resolution's search was never truncated")
	}
}
