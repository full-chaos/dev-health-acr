package contextfabric

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The grouped "project statuses for each team" family (member kind project,
// group kind team) served 0-1 of 10 with the item ceiling and the request
// deadline both retired. The residue was 100% HTTP 422 model-output
// rejection, and one reason carried the majority of it:
// driver_subject_out_of_scope, 14 of the 27 grouped synthesis rejections in
// the rig log, and present on no other family.
//
// The mechanism is a display/validate asymmetry, not model variance. A
// grouped cohort's GROUP entity -- the team -- lives in
// ContextFabricCohortGroup.Subject, whose own doc comment calls it "the
// group entity itself (the team), not one of its members". That whole
// cohort, groups included, is serialized into the synthesis payload
// (genkitruntime's synthesisInput carries `cohort`, and
// ContextFabricCohort.Groups carries `json:"groups"`). synthesisSubjects --
// the allow-set ValidateAgainst checks every model-referenced subject
// against -- enumerated committed resolution, cohort MEMBERS, canonical
// fact subjects and path nodes, and had no groups branch at all.
//
// So the model was shown each team, asked a question whose subject IS the
// teams, and every driver it wrote about one was rejected as "outside the
// investigation". This is the CHAOS-4522 shape one field over: that ticket
// fixed exactly this asymmetry for a cohort member's EVIDENCE refs, and its
// comment in ValidateAgainst says why ("a member's evidence ref was
// displayed to the model and then rejected as unknown evidence").
//
// groupedCohortFixture reproduces the live shape at unit scale: two project
// members, one team group over them, and a driver about the TEAM.
func groupedCohortFixture() (SynthesisInput, SynthesisDraft, SubjectRef) {
	input, draft := closureFixture()
	project := input.Graph.Resolution.Committed[0]
	second := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ops", Label: "Ops"}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_fullchaos", Label: "Fullchaos"}
	input.Graph.Cohort = &Cohort{
		Kind: SubjectProject, Rationale: "fixture", Complete: true,
		Members: []CohortMember{
			{Subject: project, Rank: 1, InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."}},
			{Subject: second, Rank: 2, InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."}},
		},
		Groups: []contractsv1.ContextFabricCohortGroup{{
			Subject:            team,
			MemberCanonicalIDs: []string{project.CanonicalID, second.CanonicalID},
			Complete:           true, Total: 2,
		}},
	}
	return input, draft, team
}

// TestDriverMayNameTheCohortGroupSubject is the pinning test for the 422
// itself. RED before the fix with driver_subject_out_of_scope.
func TestDriverMayNameTheCohortGroupSubject(t *testing.T) {
	t.Parallel()
	input, draft, team := groupedCohortFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{team}
	// The driver's own claim must stay in ITS scope, or requireGroundedClaims
	// rejects for a different reason -- see the shift test below.
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.Drivers[0].Category = "relationship"
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a driver about the cohort's own group subject to be admitted", err)
	}
}

// There is deliberately NO claim-side test for the group branch, and the
// reason is worth stating rather than leaving as an omission. A claim must
// ground against a canonical fact of its own (Kind, Subject); a claim about
// the team therefore requires a team-subject canonical fact in the bundle --
// and synthesisSubjects already admits every Facts[].Subject. So whenever
// the claim arm is reachable at all, the group branch is not what admits
// the subject, and a test written there would pass identically with the fix
// reverted. The first version of this file contained exactly that test and
// it passed RED, which is how the vacuity was caught.
//
// The driver and finding arms have no such coupling: neither needs a fact
// about the subject it names, so both isolate the branch.

// TestFindingMayNameTheCohortGroupSubject covers the third arm, so the fix
// is proved at every site that consults the subject allow-set rather than
// at the one the census happened to concentrate on.
func TestFindingMayNameTheCohortGroupSubject(t *testing.T) {
	t.Parallel()
	input, draft, team := groupedCohortFixture()
	draft.RemainingWork = []Finding{{
		FindingID: "finding_12345678", Kind: "relationship",
		Summary: "The team has open acceptance work.", Subjects: []SubjectRef{team},
		EvidenceRefIDs: []string{"evidence_release_1234"},
	}}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a finding about the cohort's own group subject to be admitted", err)
	}
}

// TestGroupSubjectLabelIsBound is the other half of admitting a subject.
// CHAOS-3755 H3 rejects a real, in-bounds subject presented under a forged
// label. Admitting group subjects without binding their labels would open
// exactly that hole on the group entity -- presenting one team's data under
// another team's name -- so canonicalSubjectLabels must learn groups at the
// same time synthesisSubjects does.
func TestGroupSubjectLabelIsBound(t *testing.T) {
	t.Parallel()
	input, draft, team := groupedCohortFixture()
	forged := team
	forged.Label = "Some Other Team"
	draft.Drivers[0].AffectedSubjects = []SubjectRef{forged}
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.Drivers[0].Category = "relationship"
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want a forged group label to be rejected")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectLabelMismatch {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSubjectLabelMismatch)
	}
}

// TestInventedGroupSubjectIsStillRejected is the negative control: the
// widening admits the groups the ENGINE built, never anything the model can
// mint. A team that appears in no group must still be out of scope.
func TestInventedGroupSubjectIsStillRejected(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{{
		Kind: SubjectTeam, CanonicalID: "team_never_discovered", Label: "Invented",
	}}
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want an invented group subject to be rejected")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSubjectOutOfScope)
	}
}

// TestGroupSubjectIsNotAdmittedWithoutACohort is the attribution control:
// the SAME team subject, with the cohort removed, must still be rejected.
// Without it a mutant that admits every SubjectTeam -- or that returns a
// permissive allow-set -- passes every test above.
func TestGroupSubjectIsNotAdmittedWithoutACohort(t *testing.T) {
	t.Parallel()
	input, draft, team := groupedCohortFixture()
	input.Graph.Cohort = nil
	draft.Drivers[0].AffectedSubjects = []SubjectRef{team}
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want the group subject to be out of scope when no cohort carries it")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want %q", got, RejectionReasonDriverSubjectOutOfScope)
	}
}

// TestDriverAboutAGroupCitingMemberClaimsShiftsToUngrounded is the
// PREDICTED consequence of this fix, pinned deliberately rather than
// discovered later in the rig.
//
// requireGroundedClaims binds every claim a driver cites to the driver's
// OWN affected subjects. The grouped answer's natural shape -- a driver
// about the TEAM citing claims about the team's PROJECTS -- violates that,
// and did so before this change too; it simply never got there, because the
// subject check rejected the driver first. Admitting group subjects
// therefore converts some driver_subject_out_of_scope into
// driver_claim_ungrounded rather than into a served answer.
//
// That is correct behaviour for THIS PR (the rule it enforces is a real
// one: presenting a project's data as the team's is a false assertion), and
// it is the reason the reason-mix will move rather than simply shrink. The
// prompt work in the follow-up PR is what teaches the model to name the
// members it is talking about; this test exists so that follow-up starts
// from a pinned, named behaviour instead of a surprise.
func TestDriverAboutAGroupCitingMemberClaimsShiftsToUngrounded(t *testing.T) {
	t.Parallel()
	input, draft, team := groupedCohortFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{team}
	// draft.ClaimedFacts[0] is about the PROJECT, from closureFixture.
	if got := draft.ClaimedFacts[0].Subject.Kind; got != SubjectProject {
		t.Fatalf("fixture drift: the cited claim must be about a project, got kind %q", got)
	}
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want a team-scoped driver citing a project claim to be rejected")
	}
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverClaimUngrounded {
		t.Fatalf("rejection reason = %q, want %q -- the predicted post-fix shift", got, RejectionReasonDriverClaimUngrounded)
	}
	if !strings.Contains(err.Error(), "outside its own affected subjects") {
		t.Fatalf("error = %v, want the subject-scoping message", err)
	}
}

// --- subject_in_payload telemetry -------------------------------------

// TestSubjectScopeRejectionReportsAnInventedSubjectAsNotInPayload is the
// expected steady state: the model named something nothing in its input
// mentioned, so the rejection is the model's fault and the field says so.
func TestSubjectScopeRejectionReportsAnInventedSubjectAsNotInPayload(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{{
		Kind: SubjectTeam, CanonicalID: "team_never_discovered", Label: "Invented",
	}}
	err := draft.ValidateAgainst(input)
	inPayload, ok := SynthesisSubjectInPayloadOf(err)
	if !ok {
		t.Fatal("SynthesisSubjectInPayloadOf() reported no membership, want a subject-scope rejection to carry one")
	}
	if inPayload {
		t.Fatal("subject_in_payload = true for a subject that appears nowhere in the payload")
	}
}

// TestSubjectScopeRejectionReportsAShownButUncitableSubjectAsInPayload is
// the field's whole reason to exist, and the proof it is not vacuous: a
// resolution CANDIDATE is serialized to the model and deliberately not
// citable (synthesisSubjects' stated rule), so a rejection naming one must
// come back true. Before this ticket, a group subject produced exactly this
// signal and there was no field to carry it.
func TestSubjectScopeRejectionReportsAShownButUncitableSubjectAsInPayload(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	candidate := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unresolved", Label: "Unresolved"}
	input.Graph.Resolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_12345678", Subject: candidate,
		MatchReasons: []string{"lexical"}, Confidence: 0.4,
	}}
	draft.Drivers[0].AffectedSubjects = []SubjectRef{candidate}
	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want %q -- a candidate must stay uncitable", got, RejectionReasonDriverSubjectOutOfScope)
	}
	inPayload, ok := SynthesisSubjectInPayloadOf(err)
	if !ok {
		t.Fatal("SynthesisSubjectInPayloadOf() reported no membership on a subject-scope rejection")
	}
	if !inPayload {
		t.Fatal("subject_in_payload = false for a subject the payload serialized to the model")
	}
}

// TestNonSubjectRejectionCarriesNoPayloadMembership pins the omission: a
// rejection this field does not describe must report ok=false, so the
// telemetry seam prints nothing rather than a misleading default.
func TestNonSubjectRejectionCarriesNoPayloadMembership(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	draft.ClaimedFacts[0].Value = boolScalar(true) // contradicts the canonical value
	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimValueContradicts {
		t.Fatalf("fixture drift: rejection reason = %q, want %q", got, RejectionReasonClaimValueContradicts)
	}
	if _, ok := SynthesisSubjectInPayloadOf(err); ok {
		t.Fatal("SynthesisSubjectInPayloadOf() reported a membership on a rejection that is not subject-scope")
	}
}

// TestEverySubjectThePayloadShowsIsCitableExceptTheDeclaredExceptions is
// the guard against re-introducing this whole class, and it is keyed on the
// QUANTITY (subjects the payload shows) rather than on the call sites that
// happen to add them -- the CHAOS-4962 handoff's own standing lesson, after
// two population guards there missed a defect one layer outside the chosen
// population.
//
// It builds an input where every subject-bearing payload source carries a
// DISTINCT subject, then asserts synthesisSubjects covers synthesisPayloadSubjects
// exactly, minus one named exception. Adding a new payload source without
// admitting it fails here instead of in a family that silently stops
// serving.
//
// STATED LIMIT: a new source added to NEITHER function is invisible to this
// test, because both are hand-maintained enumerations and no mechanical
// check can compare them against the serializer's struct. That residual is
// what the subject_in_payload production field covers -- a real payload
// carrying an unadmitted subject reports true at runtime.
func TestEverySubjectThePayloadShowsIsCitableExceptTheDeclaredExceptions(t *testing.T) {
	t.Parallel()
	input, _, team := groupedCohortFixture()
	candidate := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unresolved", Label: "Unresolved"}
	input.Graph.Resolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_12345678", Subject: candidate,
		MatchReasons: []string{"lexical"}, Confidence: 0.4,
	}}
	driverCandidateSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_candidate_driver", Label: "Candidate Driver Subject"}
	input.Graph.DriverCandidates = []DriverJudgment{{
		DriverID: "driver_87654321", Standing: DriverPrincipal, Category: "relationship",
		Title: "Engine-proposed driver", Summary: "Proposed by retrieval.",
		AffectedSubjects: []SubjectRef{driverCandidateSubject},
	}}

	allowed := synthesisSubjects(input)
	shown := synthesisPayloadSubjects(input)

	// The exceptions registry: subjects the payload shows on purpose and
	// does NOT make citable. One entry today; every entry needs a reason in
	// synthesisSubjects' doc comment.
	exceptions := map[string]string{
		subjectKeyForModel(candidate): "resolution candidate -- an unresolved alternative the investigation never committed to",
	}

	for key := range shown {
		if _, citable := allowed[key]; citable {
			continue
		}
		if _, declared := exceptions[key]; declared {
			continue
		}
		t.Fatalf("a subject the synthesis payload shows the model is neither citable nor a declared exception (key %q) -- this is the CHAOS-4962 display/validate asymmetry reappearing", key)
	}
	for key := range allowed {
		if _, ok := shown[key]; !ok {
			t.Fatalf("a citable subject (key %q) is not in the payload set -- synthesisPayloadSubjects must be a superset", key)
		}
	}
	// Attribution controls: the test is only meaningful if these specific
	// subjects actually reached the sets under test.
	for _, subject := range []SubjectRef{team, driverCandidateSubject} {
		if _, ok := allowed[subjectKeyForModel(subject)]; !ok {
			t.Fatalf("fixture drift: %q must be citable for this test to be attributing anything", subject.CanonicalID)
		}
	}
	if _, ok := allowed[subjectKeyForModel(candidate)]; ok {
		t.Fatal("fixture drift: the resolution candidate must NOT be citable, or the exception is untested")
	}
	if len(exceptions) != 1 {
		t.Fatalf("exceptions registry has %d entries; every entry must be justified in synthesisSubjects' doc comment", len(exceptions))
	}
}
