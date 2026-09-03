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

// --- subject scope basis telemetry -----------------------------------

// TestScopeBasisReportsASubjectTheModelInvented is the expected steady
// state: nothing in the payload mentioned the subject.
func TestScopeBasisReportsASubjectTheModelInvented(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	draft.Drivers[0].AffectedSubjects = []SubjectRef{{
		Kind: SubjectTeam, CanonicalID: "team_never_discovered", Label: "Invented",
	}}
	basis, ok := SynthesisSubjectScopeBasisOf(draft.ValidateAgainst(input))
	if !ok {
		t.Fatal("no scope basis on a subject-scope rejection")
	}
	if basis != SubjectScopeAbsentFromPayload {
		t.Fatalf("basis = %q, want %q", basis, SubjectScopeAbsentFromPayload)
	}
}

// TestScopeBasisReportsAResolutionCandidateAsUncitableByPolicy is the
// distinction the boolean this replaced could not make. A candidate IS shown
// to the model, so the old field said true, and true was documented as an
// ACR defect -- so ordinary model misuse would have raised a false
// ACR-defect alert. Adversarial review caught that before it shipped.
func TestScopeBasisReportsAResolutionCandidateAsUncitableByPolicy(t *testing.T) {
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
		t.Fatalf("rejection reason = %q, want a candidate to stay uncitable", got)
	}
	basis, _ := SynthesisSubjectScopeBasisOf(err)
	if basis != SubjectScopeShownUncitableByPolicy {
		t.Fatalf("basis = %q, want %q -- a candidate is shown on purpose and uncitable on purpose", basis, SubjectScopeShownUncitableByPolicy)
	}
}

// TestScopeBasisReportsACohortExclusionAsUncitableByPolicy is the second
// source, and the one that exposed the original guard as circular: the whole
// cohort is serialized to the model, Exclusions included
// (ContextFabricCohortExclusion carries the subject AND the reason it was
// removed), and the first version of synthesisPayloadSubjects was built by
// copying the allow-set, so it could not see a source the allow-set omitted.
// Citing an excluded subject asserts a membership the engine denied.
func TestScopeBasisReportsACohortExclusionAsUncitableByPolicy(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	excluded := SubjectRef{Kind: SubjectProject, CanonicalID: "project_excluded", Label: "Excluded"}
	input.Graph.Cohort.Exclusions = []contractsv1.ContextFabricCohortExclusion{{
		Subject: excluded, Reason: "out of the requested window",
	}}
	draft.Drivers[0].AffectedSubjects = []SubjectRef{excluded}
	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("rejection reason = %q, want an excluded subject to stay uncitable", got)
	}
	basis, _ := SynthesisSubjectScopeBasisOf(err)
	if basis != SubjectScopeShownUncitableByPolicy {
		t.Fatalf("basis = %q, want %q -- an exclusion is shown to the model and must not read as an ACR defect", basis, SubjectScopeShownUncitableByPolicy)
	}
}

// TestNonSubjectRejectionCarriesNoScopeBasis keeps the field honest at the
// boundary: a rejection it does not describe must carry no basis at all.
func TestNonSubjectRejectionCarriesNoScopeBasis(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	draft.ClaimedFacts[0].Value = boolScalar(true)
	err := draft.ValidateAgainst(input)
	if got := SynthesisRejectionReasonOf(err); got != RejectionReasonClaimValueContradicts {
		t.Fatalf("fixture drift: rejection reason = %q", got)
	}
	if _, ok := SynthesisSubjectScopeBasisOf(err); ok {
		t.Fatal("a scope basis is present on a rejection that is not subject-scope")
	}
}

// TestEverySubjectThePayloadShowsIsCitableOrADeclaredException is the guard
// against re-introducing this class, rewritten after adversarial review
// showed the first version could not fail.
//
// That version derived the payload set from the allow-set, so it was
// structurally blind to any source the allow-set omitted -- which is exactly
// how Cohort.Exclusions escaped both. The three sets are now built
// independently and the invariant is an EXACT partition: every subject the
// payload shows is citable or deliberately uncitable, never neither, and
// nothing is both.
//
// STATED LIMIT, unchanged and irreducible here: a payload source added to
// NEITHER synthesisSubjects nor synthesisPayloadSubjects is invisible to
// this test, because both are hand-maintained enumerations and this package
// cannot see genkitruntime's serializer struct. That residual is what the
// production telemetry covers -- such a subject rejects as
// shown_should_be_citable only if it reaches the payload set, so the honest
// statement is that the field catches the one-sided omission and this test
// catches the other; a two-sided omission is caught by neither, and closing
// it needs the serializer and the allow-set to share one source of truth.
func TestEverySubjectThePayloadShowsIsCitableOrADeclaredException(t *testing.T) {
	t.Parallel()
	input, _, team := groupedCohortFixture()
	// Round 2 finding: the fixture's committed project is ALSO a cohort member
	// and a path node, so deleting the committed or canonical-fact branch of
	// the allow-set changed nothing and those mutants survived. Give each of
	// those two sources a subject that appears nowhere else, so deleting
	// either branch is observable.
	soleCommitted := SubjectRef{Kind: SubjectProject, CanonicalID: "project_only_committed", Label: "Only Committed"}
	input.Graph.Resolution.Committed = append(input.Graph.Resolution.Committed, soleCommitted)
	soleFactSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_only_fact", Label: "Only Fact"}
	input.Facts.Facts = append(input.Facts.Facts, CanonicalFact{
		Kind: FactReadiness, Subject: soleFactSubject,
		Fields:         map[string]FactValue{"release_ready": BooleanFactValue(false)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "ops", SourceVersion: "v1",
	})
	candidate := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unresolved", Label: "Unresolved"}
	input.Graph.Resolution.Candidates = []SubjectCandidate{{
		ReceiptID: "receipt_12345678", Subject: candidate,
		MatchReasons: []string{"lexical"}, Confidence: 0.4,
	}}
	excluded := SubjectRef{Kind: SubjectProject, CanonicalID: "project_excluded", Label: "Excluded"}
	input.Graph.Cohort.Exclusions = []contractsv1.ContextFabricCohortExclusion{{
		Subject: excluded, Reason: "out of the requested window",
	}}
	driverCandidateSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_candidate_driver", Label: "Candidate Driver Subject"}
	input.Graph.DriverCandidates = []DriverJudgment{{
		DriverID: "driver_87654321", Standing: DriverPrincipal, Category: "relationship",
		Title: "Engine-proposed driver", Summary: "Proposed by retrieval.",
		AffectedSubjects: []SubjectRef{driverCandidateSubject},
		// Carries evidence of its OWN, which nothing else in the input
		// supplies. Without this the DriverCandidates evidence allowance had
		// no test that failed when it was deleted -- proved by running that
		// mutation, which SURVIVED the whole battery before this line.
		EvidenceRefIDs: []string{"evidence_candidate_9876"},
	}}

	allowed := synthesisSubjects(input)
	shown, censusOK := synthesisPayloadSubjects(input)
	if !censusOK {
		t.Fatal("payload census failed on a well-formed input")
	}
	uncitable := synthesisUncitableShownSubjects(input)

	for key := range shown {
		_, citable := allowed[key]
		_, declared := uncitable[key]
		if citable == declared {
			t.Fatalf("subject %q is %s -- the payload must partition into citable and deliberately-uncitable, with no third category", key,
				map[bool]string{true: "BOTH citable and declared uncitable", false: "NEITHER citable nor a declared exception (the display/validate asymmetry reappearing)"}[citable])
		}
	}
	for key := range allowed {
		if _, ok := shown[key]; !ok {
			t.Fatalf("citable subject %q is not in the payload set -- synthesisPayloadSubjects must be a superset", key)
		}
	}
	for key := range uncitable {
		if _, ok := shown[key]; !ok {
			t.Fatalf("deliberately-uncitable subject %q is not in the payload set", key)
		}
	}
	// Attribution controls: this test means nothing unless these specific
	// subjects actually reached the sets under test, on the sides claimed.
	for _, subject := range []SubjectRef{team, driverCandidateSubject, soleCommitted, soleFactSubject} {
		if _, ok := allowed[subjectKeyForModel(subject)]; !ok {
			t.Fatalf("fixture drift: %q must be citable", subject.CanonicalID)
		}
	}
	for _, subject := range []SubjectRef{candidate, excluded} {
		if _, ok := uncitable[subjectKeyForModel(subject)]; !ok {
			t.Fatalf("fixture drift: %q must be a declared exception, or its case is untested", subject.CanonicalID)
		}
	}
}

// TestDriverMayCiteEvidenceCarriedOnlyByAnEngineDriverCandidate pins the
// evidence half of the widening on its own. The engine hands the model its
// candidate drivers WITH their evidence refs, and the reuse-degrade path
// serves those candidates verbatim as an answer's drivers, so that evidence
// was publishable by ACR and "unknown" when the model cited it.
//
// This test exists because deleting that allowance survived the entire
// mutation battery: every other fixture supplied the same evidence ref from
// some other source, so nothing depended on the new branch.
func TestDriverMayCiteEvidenceCarriedOnlyByAnEngineDriverCandidate(t *testing.T) {
	t.Parallel()
	input, draft, _ := groupedCohortFixture()
	subject := input.Graph.Resolution.Committed[0]
	input.Graph.DriverCandidates = []DriverJudgment{{
		DriverID: "driver_87654321", Standing: DriverPrincipal, Category: "relationship",
		Title: "Engine-proposed driver", Summary: "Proposed by retrieval.",
		AffectedSubjects: []SubjectRef{subject},
		EvidenceRefIDs:   []string{"evidence_candidate_9876"},
	}}
	draft.Drivers[0].EvidenceRefIDs = []string{"evidence_candidate_9876"}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want evidence carried only by an engine driver candidate to be citable", err)
	}
}

// TestScopeBasisAlarmFiresForAShownSubjectOnNoExceptionList pins the ALARM
// branch, and it is a direct unit test of the classifier rather than a test
// driven through ValidateAgainst, deliberately.
//
// In a correct build that branch is UNREACHABLE from any production input:
// TestEverySubjectThePayloadShowsIsCitableOrADeclaredException asserts the
// payload partitions exactly into citable and deliberately-uncitable, so no
// real synthesis input can produce a subject that is shown and on neither
// list. That is precisely why it needs a direct test -- a mutation making
// the alarm never fire survived the entire battery, because every
// end-to-end fixture is, by the invariant, incapable of reaching it.
//
// The branch still has to work: the day a new payload source is added to the
// serializer and to synthesisPayloadSubjects but not to either list, this is
// the value that says so in production, and the partition test above cannot
// see that case at all.
func TestScopeBasisAlarmFiresForAShownSubjectOnNoExceptionList(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_shown_not_admitted", Label: "Shown"}
	key := subjectKeyForModel(subject)
	payload := map[string]struct{}{key: {}}

	if got := synthesisSubjectScopeBasis(subject, payload, map[string]struct{}{}); got != SubjectScopeShownShouldBeCitable {
		t.Fatalf("basis = %q, want %q -- a subject the payload shows and no list excuses is ACR's defect", got, SubjectScopeShownShouldBeCitable)
	}
	// The three inputs must each decide the outcome, or the classifier is
	// not actually reading them.
	if got := synthesisSubjectScopeBasis(subject, map[string]struct{}{}, map[string]struct{}{}); got != SubjectScopeAbsentFromPayload {
		t.Fatalf("basis = %q for a subject in no payload, want %q", got, SubjectScopeAbsentFromPayload)
	}
	if got := synthesisSubjectScopeBasis(subject, payload, map[string]struct{}{key: {}}); got != SubjectScopeShownUncitableByPolicy {
		t.Fatalf("basis = %q for a declared exception, want %q", got, SubjectScopeShownUncitableByPolicy)
	}
}
