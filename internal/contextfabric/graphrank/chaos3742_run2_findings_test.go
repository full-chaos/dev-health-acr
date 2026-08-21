package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjects_InferredTierExpectedKindCommitSkipsCensus is CHAOS-3742
// run-2 root-cause evidence for InferredTierSingleSatisfierVerifiedCount=0/22
// (chaos3742_two_turn_confirmation_test.go's inferred_tier arm, member
// expected_kind): reproduces that arm's EXACT request shape --
// request.ExpectedKinds set to a kind that does NOT match the subject
// ordinary search actually finds (mirrors runTwoTurnInferredTierArm's
// entry.NegativeKind injection) -- against a real ResolveSubjects call.
//
// Disambiguates the two live hypotheses for the finding:
//
//  1. Tracer/CensusFunc mis-wired (never fires when it should): REFUTED --
//     TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate (same
//     package) already proves the tracer receives "evidence_round" and
//     CensusFunc is invoked whenever a resolution is genuinely stalled.
//  2. Real product gap: an inferred-tier commit (explicit, unconfirmed
//     ExpectedKinds present, no receipt) reaches production WITHOUT ever
//     invoking the design brief's kind-insensitivity census, because
//     ordinary search already resolves decisively and resolve.go's own
//     gate (CensusFunc invoked only when
//     `len(resolution.Committed) == 0 && searchTruncated`, resolve.go
//     "committed resolutions pay nothing") skips the census entirely once
//     that happens -- CONFIRMED below.
//
// request.ExpectedKinds never filters candidatesBySubject before this
// gate (its only production consumer pre-gate is the explicit-structure
// ECHO in structure.go, which does not touch candidate pooling; its only
// consumer that COULD affect resolution is narrowPooledKindsByExplicitKinds,
// itself inside the already-stalled-only runShadowEvidenceRoundForResolution
// call) -- so an explicit, uncorroborated, even WRONG kind hint has zero
// effect on whether ordinary search commits.
//
// This test PINS current behavior; it does not rule on whether it is
// correct. CHAOS-4039 (filed alongside this finding, related to CHAOS-3742)
// owns the open product/design question -- whether resolve.go's stalled-
// only gate should widen so an explicit-but-unconfirmed kind/handle hint
// forces the census path even when ordinary search would otherwise commit
// unassisted, or whether the design brief's kind-insensitivity requirement
// is intentionally scoped narrower than "every inferred-tier commit". Do
// not change resolve.go's gate to make this test fail without first
// checking CHAOS-4039's resolution.
func TestResolveSubjects_InferredTierExpectedKindCommitSkipsCensus(t *testing.T) {
	t.Parallel()
	// Ordinary search finds the TRUE subject (a repository) with enough
	// relevance to commit on its own -- unrelated to the kind the caller's
	// explicit (unconfirmed) ExpectedKinds names.
	exact := candidateNode(contextfabric.SubjectRepository, "repository:acme/widgets", "acme/widgets", 0.9, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"acme/widgets": {exact}}}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	censusCalls := 0
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		censusCalls++
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC()}, nil
	}

	request := testRequest()
	// The inferred-tier arm's own shape (runTwoTurnInferredTierArm, member
	// expected_kind): an EXPLICIT ExpectedKinds naming a kind that does NOT
	// match what search will actually find, and no receipt confirming it.
	request.ExpectedKinds = []contextfabric.SubjectKind{contextfabric.SubjectWorkItem}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("acme/widgets"), deps, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly 1 (ordinary search must actually commit for this scenario to reproduce the live arm)", resolution.Committed)
	}
	if resolution.Committed[0].CanonicalID != "repository:acme/widgets" {
		t.Fatalf("resolution.Committed[0] = %#v, want the ordinary search hit, not anything census-derived", resolution.Committed[0])
	}

	// The core finding: an inferred-tier commit -- explicit, unconfirmed,
	// even WRONG-kind-hinted -- never touches the all-kinds census at all.
	if censusCalls != 0 {
		t.Fatalf("CensusFunc called %d times for a committed inferred-tier resolution, want 0 -- an explicit ExpectedKinds hint does not make search wait for census", censusCalls)
	}
	if got := len(tracer.eventsForStage("evidence_round")); got != 0 {
		t.Fatalf("evidence_round trace events = %d, want 0 -- kindInsensitivityProof's own proof point never fires for this commit", got)
	}
}
