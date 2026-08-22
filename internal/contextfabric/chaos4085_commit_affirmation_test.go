package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4085 test vocabulary.
//
// affirmationSubject is the subject under test -- the one a fixture
// commits. affirmationOther is a DIFFERENT subject the answer may talk
// about instead, which is the shape the v9 trial's wrong commit actually
// had: the synthesis grounded everything it said on a cohort neighbour and
// named the committed subject nowhere.
var (
	affirmationSubject = SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item:linear:AFFIRM-1", Label: "Committed work item"}
	affirmationOther   = SubjectRef{Kind: SubjectTeam, CanonicalID: "team:NEIGHBOUR", Label: "Neighbouring team"}
)

const (
	affirmationSubjectRef = "acr:v1:work-item:linear:AFFIRM-1"
	affirmationEdgeRef    = "acr:v1:work-item-dependency:linear:AFFIRM-2:linear:AFFIRM-1:blocks"
	affirmationOtherRef   = "acr:v1:team:NEIGHBOUR"
)

// affirmationCandidate is the committed subject's own candidate entry,
// carrying its identity evidence ref.
func affirmationCandidate(state ResolutionState) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID:       "receipt_affirm_padding",
		Subject:         affirmationSubject,
		State:           state,
		MatchReasons:    []string{"hybrid graph search"},
		Confidence:      0.755,
		EvidenceRefIDs:  []string{affirmationSubjectRef},
		MatchMechanisms: []MatchMechanism{MatchLexical, MatchVector},
	}
}

// affirmationResult is a synthesized result carrying one statistically
// committed subject. Callers mutate the returned value to express the
// specific answer shape under test.
func affirmationResult() InvestigationResult {
	return InvestigationResult{
		Status:              InvestigationPartial,
		DirectJudgment:      "The supplied data does not establish an answer.",
		CurrentState:        "One candidate was discovered.",
		StrongestPressures:  []string{},
		Drivers:             []DriverJudgment{},
		RemainingWork:       []Finding{},
		ReadinessGaps:       []Finding{},
		Conflicts:           []Finding{},
		Paths:               []RelationshipPath{},
		Limitations:         []string{},
		EvidenceRefIDs:      []string{},
		ClaimedFacts:        []ClaimedFact{},
		Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "The supplied evidence does not support naming a subject.",
		Warnings:            []string{},
		SubjectResolution: SubjectResolution{
			Candidates: []SubjectCandidate{affirmationCandidate(ResolutionCommitted)},
			Committed:  []SubjectRef{affirmationSubject},
		},
	}
}

// affirmationGraphWithPath supplies a relationship path whose nodes include
// the committed subject, so the dependency edge's ref is ATTRIBUTABLE to it.
// This is the legitimate shape a naive canonical-id-only rule would falsely
// retract: the answer's driver is about the neighbour, and the evidence it
// cites is the edge between them.
func affirmationGraphWithPath() GraphContext {
	return GraphContext{
		Paths: []RelationshipPath{{
			PathID: "path_affirm_0001",
			Nodes:  []SubjectRef{affirmationOther, affirmationSubject},
			Edges: []RelationshipEdge{{
				Type: contractsv1.ContextFabricRelationshipBlocks, From: affirmationOther, To: affirmationSubject,
				Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicObserved,
				EvidenceRefIDs: []string{affirmationEdgeRef},
			}},
			WhyRelevant:    "neighbour blocks the committed subject",
			EvidenceRefIDs: []string{affirmationEdgeRef},
		}},
		DriverCandidates: []DriverJudgment{},
		FactRequirements: []FactRequirement{},
		EvidenceRefIDs:   []string{},
		Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
	}
}

func emptyAffirmationGraph() GraphContext {
	return GraphContext{
		Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
		FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
	}
}

func emptyAffirmationFacts() CanonicalFactBundle {
	return CanonicalFactBundle{
		Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
	}
}

// factsForSubject is a fact bundle containing one canonical fact READ FOR
// the given subject -- the non-model half of the claimed-fact conjunct.
func factsForSubject(subject SubjectRef) CanonicalFactBundle {
	bundle := emptyAffirmationFacts()
	bundle.Facts = []CanonicalFact{{
		Kind: FactStatus, Subject: subject,
		Fields: map[string]FactValue{}, EvidenceRefIDs: []string{affirmationSubjectRef},
		SourceState: SourceAvailable, Source: "test", SourceVersion: "v1",
	}}
	return bundle
}

func statisticalInputs(graph GraphContext, facts CanonicalFactBundle, result InvestigationResult) affirmationInputs {
	return affirmationInputs{
		// Nil Bases: every commit reads CommitBasisUnknown, which is not
		// IdentityProven, which is exactly the population this gate judges.
		Bases:      nil,
		Candidates: result.SubjectResolution.Candidates,
		Graph:      graph,
		Facts:      facts,
	}
}

// ---------------------------------------------------------------------------
// The case-61 regression pin
// ---------------------------------------------------------------------------

// TestChaos4085_UnaffirmedStatisticalCommitIsRetracted reproduces the v9
// trial's case-61 ANSWER shape: a statistically committed subject, and a
// synthesis that grounds everything it says on a DIFFERENT subject and says
// the data establishes nothing. The commit must not survive.
func TestChaos4085_UnaffirmedStatisticalCommitIsRetracted(t *testing.T) {
	result := affirmationResult()
	// Exactly what the trial's responses carried: a claimed fact and a
	// top-level citation, both about the neighbour, none about the
	// committed subject.
	result.ClaimedFacts = []ClaimedFact{{
		ClaimID: "claim-neighbour", Kind: FactWorkload, Subject: affirmationOther,
		Field: "backlog_size", Value: ScalarValue{},
	}}
	result.EvidenceRefIDs = []string{affirmationOtherRef}

	outcomes := applyCommitAffirmation(&result, statisticalInputs(affirmationGraphWithPath(), emptyAffirmationFacts(), result))

	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("an unaffirmed statistical commit must be retracted, got %v", result.SubjectResolution.Committed)
	}
	if result.SubjectResolution.Committed == nil {
		t.Fatal("Committed must stay non-nil: the contract distinguishes an empty list from an unpopulated field")
	}
	if state := result.SubjectResolution.Candidates[0].State; state != ResolutionProposed {
		t.Fatalf("a retracted candidate's state = %q, want %q", state, ResolutionProposed)
	}
	if !result.Coverage.Partial {
		t.Fatal("an answer that lost its subject does not cover what it set out to cover")
	}
	if !hasLimitation(result.Limitations, commitRetractionLimitation) {
		t.Fatalf("the retraction must be disclosed, got %#v", result.Limitations)
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected exactly one retraction outcome, got %d", len(outcomes))
	}
	if outcomes[0].Basis != CommitBasisUnknown || outcomes[0].SubjectKind != SubjectWorkItem {
		t.Fatalf("outcome = %+v, want basis %q and kind %q", outcomes[0], CommitBasisUnknown, SubjectWorkItem)
	}
	if outcomes[0].ProvisionalCommitted != 1 || outcomes[0].FinalCommitted != 0 {
		t.Fatalf("outcome cardinalities = %d -> %d, want 1 -> 0", outcomes[0].ProvisionalCommitted, outcomes[0].FinalCommitted)
	}
}

// TestChaos4085_TopLevelCitationAloneDoesNotAffirm is sol@xhigh change 1's
// dedicated pin. The answer cites the committed subject's OWN evidence ref
// at the top level and nowhere else -- no claim, no driver, no finding names
// it. Result-level citations are validated only for membership in what was
// supplied, so a bulk or incidental citation carries no subject-bound
// claim and must not retain a commit.
func TestChaos4085_TopLevelCitationAloneDoesNotAffirm(t *testing.T) {
	result := affirmationResult()
	result.EvidenceRefIDs = []string{affirmationSubjectRef, affirmationEdgeRef}

	applyCommitAffirmation(&result, statisticalInputs(affirmationGraphWithPath(), factsForSubject(affirmationSubject), result))

	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("a top-level citation alone must not affirm, got %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_DriverNamingTheSubjectButCitingForeignEvidenceDoesNotAffirm
// closes the other half of the same hole: naming the subject is not enough
// either, if what stands behind the naming belongs to something else. The
// driver must be talking ABOUT the subject, not around it.
func TestChaos4085_DriverNamingTheSubjectButCitingForeignEvidenceDoesNotAffirm(t *testing.T) {
	result := affirmationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_foreign_0001", Standing: DriverPrincipal, Category: "relationship",
		Title: "Foreign", Summary: "cites evidence belonging to another subject",
		AffectedSubjects: []SubjectRef{affirmationSubject},
		EvidenceRefIDs:   []string{affirmationOtherRef},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.5,
	}}

	applyCommitAffirmation(&result, statisticalInputs(affirmationGraphWithPath(), emptyAffirmationFacts(), result))

	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("a driver citing foreign evidence must not affirm, got %v", result.SubjectResolution.Committed)
	}
}

// ---------------------------------------------------------------------------
// False-refusal coverage: the legitimate shapes MUST survive
// ---------------------------------------------------------------------------

// TestChaos4085_DriverCitingPathEvidenceAffirms is the legitimate shape
// measured most often in the v9 trial: the answer's driver is about a
// NEIGHBOUR and cites the relationship edge between it and the committed
// subject. The committed subject is genuinely an endpoint of the cited
// evidence, so a rule demanding its own bare ref would falsely retract a
// correct commit.
func TestChaos4085_DriverCitingPathEvidenceAffirms(t *testing.T) {
	result := affirmationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_blocks_0001", Standing: DriverPrincipal, Category: "relationship",
		Title: "Blocks", Summary: "the neighbour blocks the committed subject",
		AffectedSubjects: []SubjectRef{affirmationSubject},
		EvidenceRefIDs:   []string{affirmationEdgeRef},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.55,
	}}

	outcomes := applyCommitAffirmation(&result, statisticalInputs(affirmationGraphWithPath(), emptyAffirmationFacts(), result))

	if len(result.SubjectResolution.Committed) != 1 {
		t.Fatalf("a driver standing on the subject's own path evidence must affirm, got %v", result.SubjectResolution.Committed)
	}
	if len(outcomes) != 0 {
		t.Fatalf("no retraction expected, got %+v", outcomes)
	}
	if hasLimitation(result.Limitations, commitRetractionLimitation) {
		t.Fatal("no retraction happened, so no disclosure may be added")
	}
	if result.Coverage.Partial {
		t.Fatal("no retraction happened, so coverage must be left alone")
	}
}

// TestChaos4085_FindingCitingSubjectEvidenceAffirms covers the finding
// lists, which carry the same subject/evidence rule as drivers.
//
// The fact bundle is what makes affirmationSubjectRef ATTRIBUTABLE to the
// subject here: after codex round 1's HIGH finding 2 the same ref coming
// only from the subject's own candidate entry is circular and no longer
// affirms. Findings have no PathIDs field (and the contract requires every
// finding to carry at least one evidence ref), so evidence and claims are
// the only two grounding forms available to them.
func TestChaos4085_FindingCitingSubjectEvidenceAffirms(t *testing.T) {
	for name, apply := range map[string]func(*InvestigationResult, Finding){
		"remaining_work": func(r *InvestigationResult, f Finding) { r.RemainingWork = []Finding{f} },
		"readiness_gaps": func(r *InvestigationResult, f Finding) { r.ReadinessGaps = []Finding{f} },
		"conflicts":      func(r *InvestigationResult, f Finding) { r.Conflicts = []Finding{f} },
	} {
		t.Run(name, func(t *testing.T) {
			result := affirmationResult()
			apply(&result, Finding{
				FindingID: "finding_affirm_0001", Kind: "status", Summary: "outstanding",
				Subjects: []SubjectRef{affirmationSubject}, EvidenceRefIDs: []string{affirmationSubjectRef},
			})

			applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), factsForSubject(affirmationSubject), result))

			if len(result.SubjectResolution.Committed) != 1 {
				t.Fatalf("a %s finding standing on evidence attributable to the subject must affirm, got %v", name, result.SubjectResolution.Committed)
			}
		})
	}
}

// TestChaos4085_ClaimedFactAffirmsOnlyAlongsideACanonicalFact pins the
// hardened claim conjunct. ClaimedFacts is MODEL output and the result
// contract does not check a claim's value against the canonical facts
// supplied, so a claim alone would let a fabricated assertion about the
// committed subject retain it. It affirms only when the engine's own fact
// read actually returned a fact for that same subject.
func TestChaos4085_ClaimedFactAffirmsOnlyAlongsideACanonicalFact(t *testing.T) {
	claim := ClaimedFact{
		ClaimID: "claim-subject", Kind: FactStatus, Subject: affirmationSubject,
		Field: "state", Value: ScalarValue{},
	}

	withFact := affirmationResult()
	withFact.ClaimedFacts = []ClaimedFact{claim}
	applyCommitAffirmation(&withFact, statisticalInputs(emptyAffirmationGraph(), factsForSubject(affirmationSubject), withFact))
	if len(withFact.SubjectResolution.Committed) != 1 {
		t.Fatalf("a claim standing on a canonical fact for the same subject must affirm, got %v", withFact.SubjectResolution.Committed)
	}

	withoutFact := affirmationResult()
	withoutFact.ClaimedFacts = []ClaimedFact{claim}
	applyCommitAffirmation(&withoutFact, statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), withoutFact))
	if len(withoutFact.SubjectResolution.Committed) != 0 {
		t.Fatalf("an unbacked claim must not affirm on its own, got %v", withoutFact.SubjectResolution.Committed)
	}
}

// TestChaos4085_IdentityProvenCommitSurvivesAnAnswerThatGroundsNothing is
// the FALSE-REFUSAL pin sol@xhigh change 2 exists to protect, and it is
// drawn from a real v9 case: a correctly-identified subject whose synthesis
// carried zero drivers, zero claims and zero citations because the data
// genuinely did not answer the question. That is a legitimate non-answer
// ABOUT THE RIGHT SUBJECT, not a wrong commit.
func TestChaos4085_IdentityProvenCommitSurvivesAnAnswerThatGroundsNothing(t *testing.T) {
	for _, basis := range []CommitBasis{CommitBasisCallerCanonicalID, CommitBasisAuthoritativeIdentity} {
		result := affirmationResult()
		inputs := statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result)
		inputs.Bases = CommitBasisSet{SubjectMapKey(affirmationSubject): basis}

		outcomes := applyCommitAffirmation(&result, inputs)

		if len(result.SubjectResolution.Committed) != 1 {
			t.Fatalf("%s: a proven identity must not be second-guessed by an ungrounded answer, got %v", basis, result.SubjectResolution.Committed)
		}
		if len(outcomes) != 0 {
			t.Fatalf("%s: no retraction expected, got %+v", basis, outcomes)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariants (sol@xhigh change 4)
// ---------------------------------------------------------------------------

// TestChaos4085_ReductionIsMonotoneAndOrderPreserving pins the central
// invariant: the final Committed list is a SUBSEQUENCE of the provisional
// one. Cardinality can only fall, order is preserved, and no subject is
// rewritten.
func TestChaos4085_ReductionIsMonotoneAndOrderPreserving(t *testing.T) {
	affirmedFirst := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:one", Label: "one"}
	affirmedLast := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:three", Label: "three"}

	result := affirmationResult()
	result.SubjectResolution.Committed = []SubjectRef{affirmedFirst, affirmationSubject, affirmedLast}
	result.SubjectResolution.Candidates = []SubjectCandidate{
		affirmationCandidate(ResolutionCommitted),
		{ReceiptID: "receipt_one_padding", Subject: affirmedFirst, State: ResolutionCommitted, MatchReasons: []string{"x"}, Confidence: 1},
		{ReceiptID: "receipt_three_pad", Subject: affirmedLast, State: ResolutionCommitted, MatchReasons: []string{"x"}, Confidence: 1},
	}
	inputs := statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result)
	inputs.Bases = CommitBasisSet{
		SubjectMapKey(affirmedFirst): CommitBasisAuthoritativeIdentity,
		SubjectMapKey(affirmedLast):  CommitBasisAuthoritativeIdentity,
		// affirmationSubject deliberately unrecorded -> unknown -> strict.
	}

	outcomes := applyCommitAffirmation(&result, inputs)

	got := result.SubjectResolution.Committed
	if len(got) != 2 || got[0] != affirmedFirst || got[1] != affirmedLast {
		t.Fatalf("only the unaffirmed subject may be dropped, and order must survive: got %v", got)
	}
	if len(outcomes) != 1 || outcomes[0].ProvisionalCommitted != 3 || outcomes[0].FinalCommitted != 2 {
		t.Fatalf("outcomes = %+v, want one retraction 3 -> 2", outcomes)
	}
	// States mirror the retained commits exactly.
	for _, candidate := range result.SubjectResolution.Candidates {
		wantCommitted := candidate.Subject != affirmationSubject
		if isCommitted := candidate.State == ResolutionCommitted; isCommitted != wantCommitted {
			t.Fatalf("%s: state = %q, want committed = %v", candidate.Subject.CanonicalID, candidate.State, wantCommitted)
		}
	}
}

// TestChaos4085_ReducerIsIdempotent pins that a second pass changes
// nothing -- no second disclosure, no further retraction, no drift.
func TestChaos4085_ReducerIsIdempotent(t *testing.T) {
	result := affirmationResult()
	inputs := statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result)

	first := applyCommitAffirmation(&result, inputs)
	afterFirst := result
	second := applyCommitAffirmation(&result, inputs)

	if len(first) != 1 {
		t.Fatalf("first pass must retract once, got %+v", first)
	}
	if len(second) != 0 {
		t.Fatalf("second pass must be a no-op, got %+v", second)
	}
	if len(result.Limitations) != len(afterFirst.Limitations) {
		t.Fatalf("the disclosure must not be duplicated: %#v", result.Limitations)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("Committed changed on the second pass: %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_ReducerNeverPromotesAnUncommittedCandidate pins the
// asymmetry that makes reading model output safe here: this gate can only
// subtract. A proposed candidate the answer talks about at length is still
// not committed by it.
func TestChaos4085_ReducerNeverPromotesAnUncommittedCandidate(t *testing.T) {
	proposed := SubjectCandidate{
		ReceiptID: "receipt_proposed_pad", Subject: affirmationOther, State: ResolutionProposed,
		MatchReasons: []string{"x"}, Confidence: 0.9, EvidenceRefIDs: []string{affirmationOtherRef},
	}
	result := affirmationResult()
	result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, proposed)
	// The answer is entirely about the PROPOSED subject.
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_other_0001", Standing: DriverPrincipal, Category: "relationship",
		Title: "Other", Summary: "about the proposed subject",
		AffectedSubjects: []SubjectRef{affirmationOther},
		EvidenceRefIDs:   []string{affirmationOtherRef},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.5,
	}}

	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result))

	for _, subject := range result.SubjectResolution.Committed {
		if subject == affirmationOther {
			t.Fatal("this gate must never add a subject to Committed")
		}
	}
	for _, candidate := range result.SubjectResolution.Candidates {
		if candidate.Subject == affirmationOther && candidate.State != ResolutionProposed {
			t.Fatalf("an uncommitted candidate's state must be untouched, got %q", candidate.State)
		}
	}
}

// TestChaos4085_ReducerFailsClosedOnMissingInputs pins the default-retract
// rule: a zero GraphContext, an empty fact bundle and a candidate list with
// no entry for the committed subject leave nothing to affirm with, and the
// answer to "we cannot tell" is retraction.
func TestChaos4085_ReducerFailsClosedOnMissingInputs(t *testing.T) {
	result := affirmationResult()
	result.SubjectResolution.Candidates = []SubjectCandidate{}

	applyCommitAffirmation(&result, affirmationInputs{})

	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("missing inputs must fail closed, got %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_ZeroCommitsIsANoOp pins that a resolution that committed
// nothing is left completely alone -- no disclosure, no coverage change.
func TestChaos4085_ZeroCommitsIsANoOp(t *testing.T) {
	result := affirmationResult()
	result.SubjectResolution.Committed = []SubjectRef{}
	result.SubjectResolution.Candidates = []SubjectCandidate{affirmationCandidate(ResolutionAmbiguous)}

	if outcomes := applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result)); len(outcomes) != 0 {
		t.Fatalf("nothing committed, nothing to retract: %+v", outcomes)
	}
	if len(result.Limitations) != 0 || result.Coverage.Partial {
		t.Fatalf("an untouched result must not gain a disclosure: %#v partial=%v", result.Limitations, result.Coverage.Partial)
	}
}

// TestChaos4085_RetractionDisclosureIsServiceAuthoredAndLeakFree pins the
// two properties the disclosure string itself must hold: it is never the
// caveat displaced to make room for another (a reader cannot reconstruct it
// from anywhere else in the document), and it leaks no internals.
func TestChaos4085_RetractionDisclosureIsServiceAuthoredAndLeakFree(t *testing.T) {
	if !isServiceAuthoredLimitation(commitRetractionLimitation) {
		t.Fatal("the retraction disclosure must never be displaceable")
	}
	for _, leak := range []string{"vector", "embed", "model", "lexical", "confidence", "margin", "truncat", "candidate id", "gate"} {
		if strings.Contains(strings.ToLower(commitRetractionLimitation), leak) {
			t.Fatalf("the disclosure leaks an internal (%q): %q", leak, commitRetractionLimitation)
		}
	}
}

// ---------------------------------------------------------------------------
// End-to-end through Engine.Investigate
// ---------------------------------------------------------------------------

// recordingCommitAffirmationTelemetry embeds the package's existing full
// EngineTelemetry double and adds the one optional method this ticket
// introduces, so the fixture exercises the SAME composition production
// uses (a telemetry that satisfies both interfaces) rather than a
// purpose-built stub that could satisfy the optional one alone.
type recordingCommitAffirmationTelemetry struct {
	*recordingTelemetry
	outcomes []CommitAffirmationOutcome
}

func (r *recordingCommitAffirmationTelemetry) RecordCommitAffirmationRetraction(_ context.Context, _ storage.Principal, outcome CommitAffirmationOutcome) {
	r.outcomes = append(r.outcomes, outcome)
}

// TestChaos4085_EngineRetractsAnUnaffirmedCommitEndToEnd proves the gate is
// actually WIRED -- that it runs inside Investigate, on the object that is
// validated and returned, and that its telemetry reaches a composed sink.
// A reducer nothing calls would pass every test above.
func TestChaos4085_EngineRetractsAnUnaffirmedCommitEndToEnd(t *testing.T) {
	telemetry := &recordingCommitAffirmationTelemetry{recordingTelemetry: &recordingTelemetry{}}
	engine, request := engineForCommitAffirmation(t, telemetry, nil)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("the engine must retract an unaffirmed statistical commit, got %v", result.SubjectResolution.Committed)
	}
	if !hasLimitation(result.Limitations, commitRetractionLimitation) {
		t.Fatalf("the served answer must disclose the retraction, got %#v", result.Limitations)
	}
	if len(telemetry.outcomes) != 1 {
		t.Fatalf("expected one telemetry event, got %d", len(telemetry.outcomes))
	}
	// The result still validated -- an empty Committed list is legal, and a
	// gate that produced an invalid result would have errored above.
	if err := result.Validate(); err != nil {
		t.Fatalf("the retracted result must still be a valid InvestigationResult: %v", err)
	}
}

// TestChaos4085_EngineLeavesAProvenIdentityCommitAlone is the wired
// counterpart of the false-refusal pin: the SAME fixture, with the graph
// reporting a proven basis, keeps its commit.
func TestChaos4085_EngineLeavesAProvenIdentityCommitAlone(t *testing.T) {
	telemetry := &recordingCommitAffirmationTelemetry{recordingTelemetry: &recordingTelemetry{}}
	engine, request := engineForCommitAffirmation(t, telemetry, CommitBasisSet{
		SubjectMapKey(affirmationSubject): CommitBasisAuthoritativeIdentity,
	})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if len(result.SubjectResolution.Committed) != 1 {
		t.Fatalf("a proven-identity commit must survive, got %v", result.SubjectResolution.Committed)
	}
	if len(telemetry.outcomes) != 0 {
		t.Fatalf("no retraction expected, got %+v", telemetry.outcomes)
	}
}

func engineForCommitAffirmation(t *testing.T, telemetry EngineTelemetry, bases CommitBasisSet) (*Engine, InvestigationRequest) {
	t.Helper()
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "release_readiness_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graphReaderStub{
			resolution: SubjectResolution{
				Candidates: []SubjectCandidate{affirmationCandidate(ResolutionCommitted)},
				Committed:  []SubjectRef{affirmationSubject},
			},
			context: emptyAffirmationGraph(),
			bases:   bases,
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return emptyAffirmationFacts(), nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			// The case-61 answer shape: partial, grounded on nothing that
			// names the committed subject.
			draft := affirmationResult()
			draft.SubjectResolution = SubjectResolution{}
			draft.Versions = VersionSet{
				Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
				InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
			}
			return draft, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(100, 0).UTC() },
		NewResultID:    func() string { return "result_12345678" },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	request := validInvestigationRequest()
	request.Question = "which work item is blocked?"
	return engine, request
}

func hasLimitation(limitations []string, want string) bool {
	for _, limitation := range limitations {
		if limitation == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// codex xhigh review, round 1
// ---------------------------------------------------------------------------

// TestChaos4085_SubjectsOwnCandidateRefCannotSelfAffirm is codex round 1's
// HIGH finding 2. A candidate's evidence ref is minted FROM its identity and
// reaches synthesis only because the subject was proposed, so a driver that
// names the committed subject and cites that ref has cited nothing the
// retrieval step did not already assert. Allowing it would let a
// wrong-but-similar candidate support itself out of its own proposal --
// the correlated-model-output failure this gate exists to resist.
func TestChaos4085_SubjectsOwnCandidateRefCannotSelfAffirm(t *testing.T) {
	result := affirmationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_circular_001", Standing: DriverPrincipal, Category: "relationship",
		Title: "Circular", Summary: "names the subject and cites the subject's own candidate ref",
		AffectedSubjects: []SubjectRef{affirmationSubject},
		EvidenceRefIDs:   []string{affirmationSubjectRef},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.5,
	}}

	// No path, no driver candidate, no canonical fact for the subject: the
	// investigation gathered nothing about it beyond proposing it.
	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), emptyAffirmationFacts(), result))

	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("a subject's own candidate ref must not affirm it, got %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_CanonicalFactEvidenceStillAffirms is the narrowness half of
// the pin above: the SAME ref string affirms once the engine's own fact read
// actually returned a fact for the subject carrying it. What was excluded is
// the candidate's self-assertion, not the ref.
func TestChaos4085_CanonicalFactEvidenceStillAffirms(t *testing.T) {
	result := affirmationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_factref_0001", Standing: DriverPrincipal, Category: "status",
		Title: "Grounded", Summary: "cites evidence the fact read returned for the subject",
		AffectedSubjects: []SubjectRef{affirmationSubject},
		EvidenceRefIDs:   []string{affirmationSubjectRef},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicObserved, Confidence: 0.8,
	}}

	applyCommitAffirmation(&result, statisticalInputs(emptyAffirmationGraph(), factsForSubject(affirmationSubject), result))

	if len(result.SubjectResolution.Committed) != 1 {
		t.Fatalf("evidence the fact read returned for the subject must affirm, got %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_PathIDOnlyDriverAffirms is codex round 1's MEDIUM finding 3.
// validate_context_fabric_result.go requires a non-withheld driver to carry
// PathIDs OR EvidenceRefIDs -- not both -- and the synthesis prompt offers a
// relationship path as driver grounding, so a path-grounded driver with no
// evidence refs is a fully valid production answer. Judging only the
// evidence-ref half would falsely retract a correct commit.
func TestChaos4085_PathIDOnlyDriverAffirms(t *testing.T) {
	result := affirmationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_pathonly_001", Standing: DriverPrincipal, Category: "relationship",
		Title: "Blocks", Summary: "grounded on the relationship path alone",
		AffectedSubjects: []SubjectRef{affirmationSubject},
		PathIDs:          []string{"path_affirm_0001"},
		EvidenceRefIDs:   []string{},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.55,
	}}

	applyCommitAffirmation(&result, statisticalInputs(affirmationGraphWithPath(), emptyAffirmationFacts(), result))

	if len(result.SubjectResolution.Committed) != 1 {
		t.Fatalf("a driver grounded on a path the subject is on must affirm, got %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_PathIDForAPathTheSubjectIsNotOnDoesNotAffirm keeps the
// path-id half under the SAME attribution rule as the evidence-ref half: a
// path id is support only when the committed subject is genuinely on that
// path, never merely because the answer named some path.
func TestChaos4085_PathIDForAPathTheSubjectIsNotOnDoesNotAffirm(t *testing.T) {
	graph := affirmationGraphWithPath()
	// A second path that does NOT touch the committed subject.
	graph.Paths = append(graph.Paths, RelationshipPath{
		PathID: "path_foreign_0001",
		Nodes:  []SubjectRef{affirmationOther, {Kind: SubjectTeam, CanonicalID: "team:THIRD", Label: "Third"}},
		Edges: []RelationshipEdge{{
			Type: contractsv1.ContextFabricRelationshipRelatedTo, From: affirmationOther,
			To:         SubjectRef{Kind: SubjectTeam, CanonicalID: "team:THIRD", Label: "Third"},
			Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicObserved,
			EvidenceRefIDs: []string{affirmationOtherRef},
		}},
		WhyRelevant: "unrelated", EvidenceRefIDs: []string{affirmationOtherRef},
	})

	result := affirmationResult()
	result.Drivers = []DriverJudgment{{
		DriverID: "driver_wrongpath_01", Standing: DriverPrincipal, Category: "relationship",
		Title: "Wrong path", Summary: "names the subject but grounds on a path it is not on",
		AffectedSubjects: []SubjectRef{affirmationSubject},
		PathIDs:          []string{"path_foreign_0001"},
		EvidenceRefIDs:   []string{},
		Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.55,
	}}

	applyCommitAffirmation(&result, statisticalInputs(graph, emptyAffirmationFacts(), result))

	if len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("a path the subject is not on must not affirm it, got %v", result.SubjectResolution.Committed)
	}
}

// TestChaos4085_PathGroundedDriverAffirmsRegardlessOfWhichEndpointIsCommitted
// pins codex round 2's HIGH 1 as ACKNOWLEDGED BEHAVIOUR rather than leaving
// it undocumented.
//
// A driver naming the committed subject and grounding on a relationship
// path that subject is on affirms -- and it does so for EITHER endpoint of
// that path, because the two cases are structurally identical: same typed
// driver, same AffectedSubjects shape, same real engine-gathered evidence
// in which the named subject genuinely participates. Distinguishing them
// needs to know which endpoint the question was about, which is exactly
// what resolution got wrong.
//
// This is not a hole left open by oversight. Measured against all 51 commit
// events in the v9 trial, every one of the 18 legitimate statistical
// affirmations grounds on path-derived evidence and nothing else, so a rule
// that refused this shape would retract all of them. The resolution-side
// rule (graphrank's tiedStatisticalTopUnderTruncation) is what refuses the
// demonstrated-unsafe population, without consulting model output at all.
//
// If a future change makes this shape refuse, that is a DELIBERATE recall
// decision and this test is the place it gets re-argued -- not a bug fix.
func TestChaos4085_PathGroundedDriverAffirmsRegardlessOfWhichEndpointIsCommitted(t *testing.T) {
	pathDriver := func(subject SubjectRef) []DriverJudgment {
		return []DriverJudgment{{
			DriverID: "driver_endpoint_001", Standing: DriverPrincipal, Category: "relationship",
			Title: "Blocks", Summary: "grounded on the relationship path",
			AffectedSubjects: []SubjectRef{subject},
			PathIDs:          []string{"path_affirm_0001"},
			EvidenceRefIDs:   []string{},
			Derivation:       DerivationGraphAssociated, EpistemicStatus: EpistemicInferred, Confidence: 0.55,
		}}
	}

	// Endpoint one: the committed subject, named by its own driver.
	committedEndpoint := affirmationResult()
	committedEndpoint.Drivers = pathDriver(affirmationSubject)
	applyCommitAffirmation(&committedEndpoint, statisticalInputs(affirmationGraphWithPath(), emptyAffirmationFacts(), committedEndpoint))
	if len(committedEndpoint.SubjectResolution.Committed) != 1 {
		t.Fatalf("a path-grounded driver naming the committed subject must affirm, got %v", committedEndpoint.SubjectResolution.Committed)
	}

	// Endpoint two: the OTHER end of the same path is named instead. The
	// committed subject is no longer the driver's subject at all, so there
	// is no subject-bound support for it and it must retract -- which is
	// what keeps this rule from degenerating into "any path mentions it".
	otherEndpoint := affirmationResult()
	otherEndpoint.Drivers = pathDriver(affirmationOther)
	applyCommitAffirmation(&otherEndpoint, statisticalInputs(affirmationGraphWithPath(), emptyAffirmationFacts(), otherEndpoint))
	if len(otherEndpoint.SubjectResolution.Committed) != 0 {
		t.Fatalf("a driver about the OTHER endpoint provides no subject-bound support for the committed subject, got %v", otherEndpoint.SubjectResolution.Committed)
	}
}
