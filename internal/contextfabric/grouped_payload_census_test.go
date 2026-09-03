package contextfabric

import (
	"errors"
	"math"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// edgeEndpointOnlyInput returns an input carrying a subject that appears ONLY
// as a relationship edge's endpoint -- never as a path node, cohort member,
// committed subject or fact subject.
//
// This is the shape that defeated the hand-written census. That enumeration
// walked path.Nodes and stopped; ContextFabricRelationshipEdge carries its own
// From and To SubjectRefs, both serialized to the model, and neither was ever
// counted. The result was the precise failure the scope-basis field exists to
// prevent: a subject we showed the model, rejected, and then reported as
// "absent" -- blaming the model for citing something we put in front of it.
func edgeEndpointOnlyInput() (SynthesisInput, SynthesisDraft, SubjectRef) {
	input, draft, _ := groupedCohortFixture()
	endpoint := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_edge_only", Label: "Edge Only"}
	path := input.Graph.Paths[0]
	path.Edges = append(path.Edges, RelationshipEdge{
		Type: "BLOCKS", From: path.Nodes[0], To: endpoint,
		Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
		EvidenceRefIDs: []string{"evidence_release_1234"},
	})
	input.Graph.Paths = []RelationshipPath{path}
	return input, draft, endpoint
}

// TestPayloadCensusSeesASubjectCarriedOnlyByAnEdge is the census half.
func TestPayloadCensusSeesASubjectCarriedOnlyByAnEdge(t *testing.T) {
	t.Parallel()
	input, _, endpoint := edgeEndpointOnlyInput()
	shown, ok := synthesisPayloadSubjects(input)
	if !ok {
		t.Fatal("census failed on a well-formed input")
	}
	if _, seen := shown[subjectKeyForModel(endpoint)]; !seen {
		t.Fatal("the census does not see a subject the payload serializes as an edge endpoint -- a hand enumeration is incomplete again")
	}
}

// TestDriverMayNameASubjectCarriedOnlyByAnEdge is the admission half. An edge
// endpoint is engine-minted path structure, exactly like a node: the
// investigation committed to it by building the edge. Showing it and refusing
// it is the same defect this branch exists to close, one field over again.
func TestDriverMayNameASubjectCarriedOnlyByAnEdge(t *testing.T) {
	t.Parallel()
	input, draft, endpoint := edgeEndpointOnlyInput()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{endpoint}
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.Drivers[0].Category = "relationship"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a subject carried by a serialized edge to be citable", err)
	}
}

// TestScopeBasisDescribesTheRejectingSubjectNotTheLastOne closes an
// attribution gap round 2 named. ValidateAgainst short-circuits, so only the
// FIRST offending subject was ever evaluated; a basis computed from the last
// subject of the driver would describe one the validator never reached.
//
// Every prior fixture named a single subject, so a mutant classifying
// `subjects[len-1]` passed all of them. Here the two subjects have
// DIFFERENT bases -- invented, then a resolution candidate -- so the mutant
// and the correct code disagree.
func TestScopeBasisDescribesTheRejectingSubjectNotTheLastOne(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	candidate := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unresolved", Label: "Unresolved"}
	input.Graph.Resolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_12345678", Subject: candidate,
		MatchReasons: []string{"lexical"}, Confidence: 0.4,
	}}
	invented := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_never_discovered", Label: "Invented"}
	draft.Drivers[0].AffectedSubjects = []SubjectRef{invented, candidate}

	basis, ok := SynthesisSubjectScopeBasisOf(draft.ValidateAgainst(input))
	if !ok {
		t.Fatal("no scope basis on a subject-scope rejection")
	}
	if basis != SubjectScopeAbsentFromPayload {
		t.Fatalf("basis = %q, want %q -- the FIRST subject is the one that rejected; %q would describe the candidate the validator never reached",
			basis, SubjectScopeAbsentFromPayload, SubjectScopeShownUncitableByPolicy)
	}
}

// TestScopeBasisAlarmFiresForAPayloadSubjectNoListAdmits drives the alarm end
// to end through the real validator, where every other alarm test drives the
// classifier directly.
//
// The fixture uses interpretation.fact_requirements[].subjects: a genuine
// payload path that the shape census sees and that no allow-set admits. It is
// EMPTY in production today (nothing originates it -- the propagating call
// sites all copy an existing value, and the model has no wire field for it),
// so this is a latent path rather than a live defect. That is exactly what
// makes it the right fixture: if an engine change ever populates it, this
// alarm is what says so, and the value fires for a real reason rather than a
// contrived one.
func TestScopeBasisAlarmFiresForAPayloadSubjectNoListAdmits(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	requested := SubjectRef{Kind: SubjectProject, CanonicalID: "project_requested_only", Label: "Requested Only"}
	input.Interpretation.FactRequirements = []FactRequirement{{
		Kind: FactReadiness, Subjects: []SubjectRef{requested},
	}}
	draft.Drivers[0].AffectedSubjects = []SubjectRef{requested}

	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want a subject-scope rejection", got)
	}
	basis, ok := SynthesisSubjectScopeBasisOf(err)
	if !ok || basis != SubjectScopeShownShouldBeCitable {
		t.Fatalf("basis = %q (present=%v), want %q -- the payload carries this subject and no list excuses it, which is ACR's defect and not the model's",
			basis, ok, SubjectScopeShownShouldBeCitable)
	}
}

// TestASubjectInBothACommittedAndAnExcludedRoleIsCitable is the precedence
// rule, and it is a real bug rather than a hypothetical: the census is a flat
// set keyed by (kind, id), so a subject appearing in TWO payload roles loses
// which role it came from. A project that is both a committed subject and a
// cohort exclusion would be classified uncitable purely because one of its
// occurrences was an exclusion -- refusing a subject the investigation
// actually committed to.
//
// Precedence: CITABLE WINS. A subject the investigation committed to is
// citable regardless of also appearing in an advisory role; the exclusion
// entry says "not a member of this cohort", not "may not be discussed".
func TestASubjectInBothACommittedAndAnExcludedRoleIsCitable(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	dual := input.Graph.Resolution.Committed[0] // committed, and a cohort member
	cohort := *input.Graph.Cohort
	cohort.Exclusions = []contractsv1.ContextFabricCohortExclusion{{
		Subject: dual, Reason: "also excluded from the grouped view",
	}}
	input.Graph.Cohort = &cohort

	uncitable := synthesisUncitableShownSubjects(input)
	allowed := synthesisSubjects(input)
	key := subjectKeyForModel(dual)
	if _, citable := allowed[key]; !citable {
		t.Fatal("fixture drift: the dual-role subject must be committed, or this test proves nothing")
	}
	if _, excluded := uncitable[key]; excluded {
		t.Fatal("a subject that is BOTH committed and excluded was recorded as uncitable -- one advisory occurrence must not revoke a committed subject")
	}

	draft.Drivers[0].AffectedSubjects = []SubjectRef{dual}
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.Drivers[0].Category = "relationship"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a committed subject to stay citable despite an exclusion entry", err)
	}
}

// TestCensusFailureReportsBasisUnavailableRatherThanSilence closes the blind
// spot sol named: omitting the field when the census fails makes the alarm
// silently absent exactly when its own measurement broke. An explicit value
// is diagnosable; a missing field is indistinguishable from "not a
// subject-scope rejection".
//
// The failure is driven for REAL, not asserted on the constant. An earlier
// version of this test checked only that the constant existed and was in the
// vocabulary, and a mutation restoring the silent path SURVIVED it -- the test
// proved a value was declared, not that anything ever returns it. A
// non-finite float is the cheapest genuine json.Marshal failure, and it sits
// on a payload field the census actually serializes.
func TestCensusFailureReportsBasisUnavailableRatherThanSilence(t *testing.T) {
	t.Parallel()
	if !ValidSynthesisSubjectScopeBasis(SubjectScopeBasisUnavailable) {
		t.Fatal("basis_unavailable must be a member of the closed vocabulary, or the telemetry seam reports it as unclassified")
	}
	input, draft, _ := groupedCohortFixture()
	unmarshalable := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unresolved", Label: "Unresolved"}
	input.Graph.Resolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_12345678", Subject: unmarshalable,
		MatchReasons: []string{"lexical"},
		Confidence:   math.Inf(1), // json.Marshal refuses a non-finite float
	}}
	if _, ok := synthesisPayloadSubjects(input); ok {
		t.Fatal("fixture drift: the census succeeded, so this test is not exercising the failure path")
	}

	draft.Drivers[0].AffectedSubjects = []SubjectRef{{
		Kind: SubjectTeam, CanonicalID: "team_never_discovered", Label: "Invented",
	}}
	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want a subject-scope rejection", got)
	}
	basis, ok := SynthesisSubjectScopeBasisOf(err)
	if !ok {
		t.Fatal("no basis reported when the census failed -- a broken instrument must say so, not go quiet")
	}
	if basis != SubjectScopeBasisUnavailable {
		t.Fatalf("basis = %q, want %q -- reporting %q here would blame the model on a measurement that never happened",
			basis, SubjectScopeBasisUnavailable, SubjectScopeAbsentFromPayload)
	}
}

// TestSubjectScopeBasisVocabularyIsClosed gives the scope-basis vocabulary the
// same fail-closed boundary every other closed vocabulary in this package has.
//
// It was missing. I minted a new closed vocabulary and did not give it the
// closure test its siblings all carry, which is the sort of omission that goes
// unnoticed until a value escapes onto a log line verbatim. Found while
// checking whether the two new values needed golden or schema coverage.
func TestSubjectScopeBasisVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	if ValidSynthesisSubjectScopeBasis("a question the user typed") {
		t.Fatal("an arbitrary string must not be a valid scope basis")
	}
	// A rogue value must not reach a telemetry field: the accessor reports
	// "no basis" rather than handing the caller's own string back.
	rogue := &SynthesisRejection{SubjectScopeBasis: "a question the user typed", err: errors.New("x")}
	if _, ok := SynthesisSubjectScopeBasisOf(rogue); ok {
		t.Fatal("SynthesisSubjectScopeBasisOf() accepted a value outside the vocabulary")
	}
	if _, ok := SynthesisSubjectScopeBasisOf(errors.New("not a rejection at all")); ok {
		t.Fatal("SynthesisSubjectScopeBasisOf() reported a basis for a non-rejection")
	}
	if _, ok := SynthesisSubjectScopeBasisOf(nil); ok {
		t.Fatal("SynthesisSubjectScopeBasisOf(nil) reported a basis")
	}
	// Every member is valid, so the table and the constants cannot drift apart.
	for _, basis := range []SynthesisSubjectScopeBasis{
		SubjectScopeAbsentFromPayload, SubjectScopeShownUncitableByPolicy,
		SubjectScopeShownShouldBeCitable, SubjectScopeBasisUnavailable,
	} {
		if !ValidSynthesisSubjectScopeBasis(basis) {
			t.Fatalf("declared member %q is not in the canonical table", basis)
		}
	}
}
