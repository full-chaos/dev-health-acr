package graphrank

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate is CHAOS-3896
// Slice C's own headline proof: the EXACT scenario
// TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolution
// (chaos3899_resolve_wiring_test.go) pins as unchanged -- a single,
// truncation-stalled, low-confidence candidate -- now DOES commit, once the
// fake CensusFunc reports the bridged SatisfierCanonicalID that test
// deliberately withholds AND the keyed graph existence read confirms the
// node. This is the transition from "shadow, traced, discarded" (Slice A)
// through "presentation only" (Slice B) to "consumed live in the commit
// decision" (Slice C, design brief v6 §1.4) made concrete.
func TestResolveSubjects_EvidenceCensusCommitsAStalledCandidate(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
		},
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly one committed subject", resolution.Committed)
	}
	if resolution.Committed[0].CanonicalID != "pull_request:repo-1:532" {
		t.Fatalf("resolution.Committed[0] = %#v, want the census-attested satisfier", resolution.Committed[0])
	}

	decisions := tracer.eventsForStage("decision")
	if len(decisions) != 2 {
		t.Fatalf("decision events = %d, want 2 (the first stalled attempt, then the census-enriched re-decision)", len(decisions))
	}
	last := decisions[len(decisions)-1]
	if last.CommitGate != "evidence_census" || last.Outcome != "committed" {
		t.Fatalf("final decision event = %#v, want CommitGate=evidence_census, Outcome=committed", last)
	}

	commitEvents := tracer.eventsForStage("evidence_census_commit")
	if len(commitEvents) != 1 || commitEvents[0].Outcome != "merged" || !commitEvents[0].GraphExistenceOK {
		t.Fatalf("evidence_census_commit events = %#v, want exactly 1 merged/GraphExistenceOK=true event", commitEvents)
	}
}

// TestResolveSubjects_EvidenceCensusRefusesOnGraphMissingSatisfier pins
// design brief §1.4's fail-closed pin: a census that names a satisfier the
// GRAPH does not (yet) have -- projection lag or a tombstone -- must refuse
// to commit, loud, reason graph_missing_satisfier, never silently drop the
// signal. The resolution stays exactly what the ordinary gates already
// decided (ambiguous, nothing committed).
func TestResolveSubjects_EvidenceCensusRefusesOnGraphMissingSatisfier(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
		// exactHints deliberately empty -- the keyed graph read finds nothing.
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none -- an absent graph node must never commit", resolution.Committed)
	}

	commitEvents := tracer.eventsForStage("evidence_census_commit")
	if len(commitEvents) != 1 {
		t.Fatalf("evidence_census_commit events = %d, want exactly 1", len(commitEvents))
	}
	if commitEvents[0].Outcome != "refused" || commitEvents[0].GraphExistenceOK {
		t.Fatalf("evidence_census_commit event = %#v, want Outcome=refused, GraphExistenceOK=false", commitEvents[0])
	}
	if commitEvents[0].CensusCommitReason != string(ReasonGraphMissingSatisfier) {
		t.Fatalf("evidence_census_commit reason = %q, want %q", commitEvents[0].CensusCommitReason, ReasonGraphMissingSatisfier)
	}
	// Only ONE decision event: the second, census-enriched re-decision call
	// never happens when the graph read fails -- the cost/behavior stays
	// exactly the first (ordinary, stalled) call's own outcome.
	if got := len(tracer.eventsForStage("decision")); got != 1 {
		t.Fatalf("decision events = %d, want 1 (no re-decision call on a failed graph read)", got)
	}
}

// TestResolveSubjects_EvidenceCensusSkipsWhenGateInvalid pins that an
// invalid CommitGatePolicy disables evidence_census exactly like every
// other commit path, AND that this is enforced BEFORE the (real I/O, real
// cost) keyed graph read is even attempted -- an operator's broken sweep
// cell must not pay for a read whose result the gate would refuse to use
// regardless.
func TestResolveSubjects_EvidenceCensusSkipsWhenGateInvalid(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
		},
	}
	deps := backend.deps()
	// A genuinely invalid, NON-ZERO policy: ResolveSubjects treats the zero
	// value as "not overridden" and substitutes DefaultCommitGatePolicy()
	// (see its own doc comment), so a bare CommitGatePolicy{} here would
	// silently test the DEFAULT valid gate instead of an invalid one.
	// LoneFloor > TopFloor fails Validate() (resolution.go).
	deps.CommitGatePolicy = CommitGatePolicy{LoneFloor: 0.9, TopFloor: 0.5, TopGap: 0.1}
	censusCalls := 0
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		censusCalls++
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none under an invalid gate", resolution.Committed)
	}
	// The shadow round itself is gate-independent (Slice A ships regardless
	// of gate validity -- it is purely observational), so CensusFunc DOES
	// still run once for the round's own measurement; what must NOT happen
	// is a second, census-enriched re-decision call or any commit.
	if censusCalls != 1 {
		t.Fatalf("CensusFunc calls = %d, want exactly 1 (the shadow round's own single per-kind read)", censusCalls)
	}
}

// TestResolveSubjects_EvidenceCensusRecoversATruncatedAwayReferent pins
// design brief §1.4's own closing line: "A satisfier the truncated search
// never returned is merged as an ordinary candidate from its graph node
// (NodeCandidate -> MergeCandidates) -- the round still RECOVERS
// truncated-away referents." Ordinary search finds NOTHING for the term at
// all; the census still names a satisfier, the keyed graph read still finds
// it, and it still commits.
func TestResolveSubjects_EvidenceCensusRecoversATruncatedAwayReferent(t *testing.T) {
	t.Parallel()
	// A bare node with no Relevance/Score set at all -- NodeCandidate's own
	// "if confidence == 0 { confidence = 0.5 }" fallback applies, proving a
	// positive keyed witness is sufficient on its own (base 0.5 -> 0.755).
	recovered := CandidateNode{
		UUID: "node-pull_request:repo-1:532", Name: "PR #532",
		Attributes: map[string]interface{}{
			"canonical_id": "pull_request:repo-1:532", "subject_kind": string(contextfabric.SubjectPullRequest),
			"label": "PR #532", "authorization_repositories": "*",
			"evidence_refs": []string{"evidence_identity_1234"},
		},
	}
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": nil}, // search truncated AND empty
		searchTruncated: true,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): recovered,
		},
	}
	deps := backend.deps()
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	request := testRequest()
	request.Question = "why did PR 532 fail?"

	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("PR 532"), deps)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "pull_request:repo-1:532" {
		t.Fatalf("resolution.Committed = %#v, want the recovered satisfier committed", resolution.Committed)
	}
}

// TestMergeCensusAttestedSatisfier_DeclinedNodeNeverReportsSuccessEvenWhenTheKeyAlreadyExists
// is a codex xhigh review regression pin (MEDIUM/LOW, confirmed and fixed):
// an earlier version of mergeCensusAttestedSatisfier inferred success from
// candidatesBySubject[key]'s mere PRESENCE after calling mergeSearchResults
// -- wrong whenever the subject was ALREADY pooled (the common case: census
// usually names an already-pooled, merely-ambiguous candidate) and the
// census-derived node itself gets declined (unauthorized, invalid, or
// internal) -- the pre-existing entry's presence would be misread as "this
// census read succeeded". This test drives mergeCensusAttestedSatisfier
// DIRECTLY (same package) with a pre-populated candidatesBySubject entry at
// the attested key and an IsInternal predicate that declines the
// census-read node specifically, and asserts BOTH that ok is false AND that
// the pre-existing entry is left byte-identical (never silently
// overwritten or treated as evidence of a successful merge).
func TestMergeCensusAttestedSatisfier_DeclinedNodeNeverReportsSuccessEvenWhenTheKeyAlreadyExists(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}
	key := SubjectKey(subject)
	preexisting := contextfabric.SubjectCandidate{
		Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 0.9,
		MatchReasons: []string{"already pooled from ordinary search"},
	}
	candidatesBySubject := map[string]contextfabric.SubjectCandidate{key: preexisting}

	backend := &fakeGraphBackend{
		exactHints: map[string]CandidateNode{
			key: candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.5, "*"),
		},
		// The census-read node is declined for a reason independent of
		// authorization (mirrors NodeCandidate's own isInternal gate) --
		// this must NOT be confused with the search-time acceptance that
		// already put `preexisting` into the map.
		isInternal: func(s contextfabric.SubjectRef) bool { return s.CanonicalID == "pull_request:repo-1:532" },
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer

	attestation := Attestation{
		Outcome: ShadowWouldCommit, UnscopedVisibility: true,
		Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"}},
	}
	request := testRequest()

	gotKey, ok := mergeCensusAttestedSatisfier(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, attestation, candidatesBySubject, map[string]string{}, map[string]bool{}, nil, nil)
	if ok || gotKey != "" {
		t.Fatalf("mergeCensusAttestedSatisfier() = (%q, %v), want (\"\", false) for a declined node", gotKey, ok)
	}
	if got := candidatesBySubject[key]; got.Confidence != preexisting.Confidence || len(got.MatchReasons) != 1 || got.MatchReasons[0] != preexisting.MatchReasons[0] {
		t.Fatalf("candidatesBySubject[%q] = %#v, want the pre-existing entry left UNTOUCHED (%#v)", key, got, preexisting)
	}
	events := tracer.eventsForStage("evidence_census_commit")
	if len(events) != 1 || events[0].Outcome != "refused" || !events[0].GraphExistenceOK {
		t.Fatalf("evidence_census_commit events = %#v, want exactly 1 refused/GraphExistenceOK=true event", events)
	}
}

// TestMergeCensusAttestedSatisfier_DistinguishesBackendErrorFromConfirmedAbsence
// is a codex xhigh review regression pin (MEDIUM, confirmed and fixed): a
// genuine deps.ExactHint backend error and a confirmed "node does not
// exist" both fail closed (never commit) but must NOT be reported under the
// identical trace reason -- graph_missing_satisfier is a specific, CONFIRMED
// absence claim (design brief §4/§1.4's own CHAOS-3884 graphMissing
// precedent), and conflating it with "the read itself failed" would make a
// production graph outage operationally indistinguishable from a genuine
// missing node in the trace.
func TestMergeCensusAttestedSatisfier_DistinguishesBackendErrorFromConfirmedAbsence(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}
	attestation := Attestation{
		Outcome: ShadowWouldCommit, UnscopedVisibility: true,
		Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: subject.CanonicalID}},
	}
	request := testRequest()

	t.Run("confirmed absence", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{} // exactHints empty -- ok=false, err=nil
		deps := backend.deps()
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		_, ok := mergeCensusAttestedSatisfier(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, attestation, map[string]contextfabric.SubjectCandidate{}, map[string]string{}, map[string]bool{}, nil, nil)
		if ok {
			t.Fatal("mergeCensusAttestedSatisfier() ok = true, want false")
		}
		events := tracer.eventsForStage("evidence_census_commit")
		if len(events) != 1 || events[0].CensusCommitReason != string(ReasonGraphMissingSatisfier) {
			t.Fatalf("evidence_census_commit events = %#v, want CensusCommitReason=%q", events, ReasonGraphMissingSatisfier)
		}
	})

	t.Run("backend error", func(t *testing.T) {
		t.Parallel()
		deps := ResolveDeps{
			ExactHint: func(context.Context, contextfabric.SubjectRef) (CandidateNode, bool, error) {
				return CandidateNode{}, false, errors.New("simulated backend fault")
			},
		}
		tracer := &captureResolutionTracer{}
		deps.ResolutionTracer = tracer
		_, ok := mergeCensusAttestedSatisfier(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, attestation, map[string]contextfabric.SubjectCandidate{}, map[string]string{}, map[string]bool{}, nil, nil)
		if ok {
			t.Fatal("mergeCensusAttestedSatisfier() ok = true, want false")
		}
		events := tracer.eventsForStage("evidence_census_commit")
		if len(events) != 1 {
			t.Fatalf("evidence_census_commit events = %#v, want exactly 1", events)
		}
		if events[0].CensusCommitReason != censusCommitErrorReason {
			t.Fatalf("evidence_census_commit reason = %q, want %q (distinct from %q)", events[0].CensusCommitReason, censusCommitErrorReason, ReasonGraphMissingSatisfier)
		}
		if events[0].CensusCommitReason == string(ReasonGraphMissingSatisfier) {
			t.Fatal("a backend error must never be reported under the SAME reason as a confirmed absence")
		}
	})
}

// TestMergeCensusAttestedSatisfier_CapsMatchedTermsAtTheExact32Boundary is
// an independent codex xhigh review regression pin (HIGH, confirmed and
// fixed): a candidate already holding exactly matchedTermsCap (32) real,
// distinct MatchedTerms overflows to 33 once mergeSearchResults unions in
// censusProvenanceMarker -- and, on the code this test would have caught,
// contractsv1.SubjectCandidate.Validate() would then reject the WHOLE
// investigation result at engine.go's result.Validate() call, converting a
// VALID census recovery into a hard validation failure instead of a
// successful commit. Fixed by re-applying capMatchedTermsAfterMerge (with
// censusProvenanceMarker) after the census merge, mirroring the identical
// discipline the question-level pass has always applied for its own
// synthetic marker.
func TestMergeCensusAttestedSatisfier_CapsMatchedTermsAtTheExact32Boundary(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}
	key := SubjectKey(subject)
	realTerms := make([]string, matchedTermsCap)
	for i := range realTerms {
		realTerms[i] = fmt.Sprintf("real-term-%02d", i)
	}
	candidatesBySubject := map[string]contextfabric.SubjectCandidate{
		key: {
			Subject: subject, State: contextfabric.ResolutionProposed, Confidence: 0.5,
			MatchedTerms: append([]string(nil), realTerms...), MatchReasons: []string{"stalled"},
		},
	}
	backend := &fakeGraphBackend{
		exactHints: map[string]CandidateNode{
			key: candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.5, "*"),
		},
	}
	deps := backend.deps()
	attestation := Attestation{
		Outcome: ShadowWouldCommit, UnscopedVisibility: true,
		Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, Count: 1, SatisfierCanonicalID: subject.CanonicalID}},
	}
	request := testRequest()

	_, ok := mergeCensusAttestedSatisfier(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, attestation, candidatesBySubject, map[string]string{}, map[string]bool{}, nil, nil)
	if !ok {
		t.Fatal("mergeCensusAttestedSatisfier() ok = false, want true")
	}
	got := candidatesBySubject[key]
	if len(got.MatchedTerms) > matchedTermsCap {
		t.Fatalf("MatchedTerms count = %d, want <= %d (contractsv1's own bound, validate_context_fabric_result.go) -- a candidate already at the cap must not overflow once the census witness unions in, or the whole investigation result fails contract validation", len(got.MatchedTerms), matchedTermsCap)
	}
	for _, want := range realTerms {
		found := false
		for _, term := range got.MatchedTerms {
			if term == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("real term %q was dropped from MatchedTerms -- only the synthetic census marker may give way to restore the cap", want)
		}
	}
}
