package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE PROBE THAT FOUND THE DEFECT, KEPT AS A PIN.
//
// The requirement derivation decides whether a computed step that runs over a
// resolved member set can be served. This seam decides whether a cohort can be
// discovered at all. They are the same question asked at two layers, and for
// three review rounds they were two hand-built answers to it: the derivation
// could not import this package, so it rebuilt the predicate a conjunct at a
// time and a round found the next missing condition each time.
//
// Measured over the fifteen published subject kinds at the commit that filed
// this, the derivation served a ranking row for ALL FIFTEEN while this seam
// could serve THREE. Twelve cells claimed an ordering that nothing computed:
// this seam refused, `DiscoveredCohort` returned nil, `RankCohort` was never
// invoked, and the served answer said the requirement was satisfied.
//
// The fix removed the second answer rather than adding a fourth conjunct --
// both layers now call `contextfabric.CohortMemberKindFor` -- so this test
// passes BY CONSTRUCTION today. That is exactly why it is worth keeping: it is
// the instrument that fails the moment either layer grows a private opinion
// again, and it fails naming the kinds, which is how the original twelve were
// found. A test that can only fail after a regression is the only kind that
// can catch this one.
//
// It lives HERE because this is the only package that can see both sides:
// `cohortKindFromFrame` is unexported and `DeriveRequirements` is exported
// from the package this one imports.

// rankingFrameForKind builds the frame the probe used: a discovered-kind
// cohort over `kind`, whose only goal is to rank or survey it.
//
// `discovered_kind` is chosen deliberately over the other cohort variants. It
// is the variant that carries a member kind with no second axis and no named
// operands, so the ONLY thing that can decide either layer's answer is the
// kind itself -- which is the variable this test sweeps.
func rankingFrameForKind(kind contextfabric.SubjectKind) contextfabric.QuestionFrame {
	return contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
}

// derivationServesRanking reports whether the derivation's `ranking` row has a
// server for this frame.
//
// The obligation seed and the capability list are EMPTY on purpose and it is
// not a shortcut: `ranking` is a COMPUTED obligation, and the derivation's
// computed branch returns before it consults either. Passing the real registry
// would make this test depend on producer declarations that cannot change its
// answer, and would hide the fact that the answer comes from the frame alone.
//
// The second return says whether a ranking row was derived at all. A frame
// that derives none would make the comparison below vacuous, so the caller
// fails on it rather than reading a missing row as "not served".
func derivationServesRanking(frame contextfabric.QuestionFrame) (served bool, found bool) {
	for _, row := range contextfabric.DeriveRequirements(frame, contextfabric.ObligationSeed{}, nil) {
		if row.Obligation != contextfabric.ObligationRanking {
			continue
		}
		// A frame can derive the ranking obligation at more than one role.
		// ANY served row is what this test is about: one served row is one
		// cell claiming an ordering, and the seam either can or cannot
		// produce the member set that ordering runs over.
		found = true
		if row.Served() {
			served = true
		}
	}
	return served, found
}

// TestTheDerivationAndTheSeamAgreeOnEveryPublishedKind is the probe.
//
// For every kind the wire contract publishes: the derivation serves a ranking
// row IF AND ONLY IF this seam can discover a cohort of that kind. The
// biconditional is the point -- a one-directional assertion would be satisfied
// by a derivation that refused everything.
func TestTheDerivationAndTheSeamAgreeOnEveryPublishedKind(t *testing.T) {
	t.Parallel()
	checked, agreed, seamServes := 0, 0, 0
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := contextfabric.SubjectKind(published)
		frame := rankingFrameForKind(kind)

		served, found := derivationServesRanking(frame)
		if !found {
			t.Fatalf("kind %q: the frame derived NO ranking requirement row, so this comparison would compare nothing -- the fixture stopped reaching the coordinate it exists to exercise", kind)
		}

		_, _, basis := cohortKindFromFrame(&frame)
		discoverable := basis == CohortKindFromFrameMemberKind

		checked++
		if discoverable {
			seamServes++
		}
		if served == discoverable {
			agreed++
			continue
		}
		if served {
			t.Errorf("kind %q: the derivation SERVES a ranking row while the seam refuses with basis %q -- the answer would name a server that is never invoked", kind, basis)
			continue
		}
		t.Errorf("kind %q: the seam can discover a cohort but the derivation reports the ranking row unavailable -- a servable question refused before retrieval ran", kind)
	}

	// Non-vacuity, in BOTH directions. A sweep that saw no kinds, or that saw
	// only refusing kinds, would agree with itself and prove nothing about
	// the case that actually broke: a kind both layers serve.
	if checked != contractsv1.ContextFabricSubjectKindCount {
		t.Fatalf("swept %d kinds, the published vocabulary has %d -- the sweep did not cover the vocabulary it claims to", checked, contractsv1.ContextFabricSubjectKindCount)
	}
	if seamServes == 0 {
		t.Fatal("no kind is discoverable at the seam, so every row above agreed on 'refused' and the served direction was never exercised")
	}
	if seamServes == checked {
		t.Fatal("every published kind is discoverable at the seam, so the refusing direction was never exercised -- if an arm really was proven for all fifteen, this test and the allow-list pin move together")
	}
	t.Logf("ranking rows checked: %d; discoverable at the seam: %d; agreements: %d", checked, seamServes, agreed)
}

// TestTheSeamRefusesEveryKindTheDerivationCallsUnservable is the same
// agreement read from the other end, and it exists because the test above
// could be satisfied by two layers that are wrong together in the same way.
//
// This one does not ask the derivation at all. It asserts the seam's basis
// against the SHARED PREDICATE's own reason, which is the thing the derivation
// reads. If the mapping between the two vocabularies ever drifts -- a member
// added below, an arm re-pointed -- the seam's telemetry would name a basis
// that no longer describes the decision, and every consumer of that basis
// would be reading a stale label rather than a wrong answer, which is harder
// to notice.
func TestTheSeamRefusesEveryKindTheDerivationCallsUnservable(t *testing.T) {
	t.Parallel()
	// Every reason the shared vocabulary declares must map to a basis this
	// seam declares. Quantified over the vocabulary, so a new member fails
	// here rather than reaching a log line as an undeclared basis.
	for _, reason := range contextfabric.CohortDiscoverabilityVocabulary() {
		basis := cohortKindBasisForDiscoverability(reason)
		if !ValidCohortKindBasis(basis) {
			t.Errorf("reason %q maps to basis %q, which this seam's vocabulary does not declare", reason, basis)
		}
	}

	checked := 0
	for _, published := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kind := contextfabric.SubjectKind(published)
		frame := rankingFrameForKind(kind)

		_, _, reason := contextfabric.CohortMemberKindFor(frame.SubjectExpression)
		_, _, basis := cohortKindFromFrame(&frame)

		if want := cohortKindBasisForDiscoverability(reason); basis != want {
			t.Errorf("kind %q: the shared predicate said %q, the seam reported basis %q, want %q", kind, reason, basis, want)
		}
		if resolvable := contextfabric.CohortMemberSetResolvable(frame.SubjectExpression); resolvable != (basis == CohortKindFromFrameMemberKind) {
			t.Errorf("kind %q: CohortMemberSetResolvable = %v but the seam's basis is %q; the derivation reads the first and the seam publishes the second, so they must be one decision", kind, resolvable, basis)
		}
		checked++
	}
	if checked != contractsv1.ContextFabricSubjectKindCount {
		t.Fatalf("swept %d kinds, the published vocabulary has %d", checked, contractsv1.ContextFabricSubjectKindCount)
	}
}
