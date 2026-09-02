package contextfabric

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The OFFLINE shadow-agreement measurement: the projected family beside the
// precedence family, on the twelve hand-labelled questions, with every
// disagreement named by class.
//
// THE GROUND TRUTH HERE IS ONE PERSON'S LABEL, and that is stated first
// because it bounds everything below. The shipped labelled set
// (testdata/chaos4632_labelled_questions.json) carries, per question, the
// precedence table's INPUT SIGNALS and the family they should produce. It
// carries no frame, because frames did not exist when it was written. The
// frame labels in this file were written by hand, by the author of this
// slice, reading each question and its recorded note -- they are a
// considered judgement, not a measurement, and a second reader may
// disagree with a row.
//
// WHY NOT DERIVE THE FRAME FROM THE SIGNALS. Because that is the banned
// shape in its purest form. A FamilySample synthesised from a frame (or a
// frame synthesised from a sample) would make one table compute the other's
// expectation, and the agreement number would then measure the quality of
// the synthesiser rather than the difference between the two tables. Every
// row here is labelled against the QUESTION, independently of both.
//
// WHAT THIS MEASUREMENT IS FOR. It is not the gate and it is not the
// decision. The real counter runs in production, where both families are
// computed from the SAME interpretation (family_projection_shadow.go), and
// the flip is decided on that. Twelve hand-labelled questions cannot
// establish a rate. What they CAN do is exhibit each disagreement CLASS on
// a question a human can read, so that when the production counter reports
// a class, someone can see what one looks like.

// labelledFrame is one question's hand-labelled frame, and the reason.
type labelledFrame struct {
	// id matches the labelled question set's own id. The QUESTION TEXT
	// never appears in this file -- the corpus is the authority for it and
	// duplicating it here would create a second copy to drift.
	id string
	// rationale is the one line that justifies the label. Written for a
	// reader who wants to disagree with it, which is the only useful form
	// for a hand label.
	rationale  string
	goals      []InvestigationGoal
	expression SubjectExpression
	temporal   TemporalIntent
	emphasis   []AnswerEmphasis
	dimensions []HealthDimension
}

func labelNamed(term string, kind SubjectKind) SubjectExpression {
	return SubjectExpression{Kind: SubjectExpressionNamed,
		Named: &NamedSubjectExpression{Terms: []string{term}, ExpectedKind: projectionKindPointer(kind)}}
}

func labelScoped(anchor string, member SubjectKind) SubjectExpression {
	return SubjectExpression{Kind: SubjectExpressionChildrenOfScope,
		Scoped: &ScopedSetExpression{AnchorTerms: []string{anchor}, MemberKind: member}}
}

func labelGrouped(group, member SubjectKind) SubjectExpression {
	return SubjectExpression{Kind: SubjectExpressionGroupedMembers,
		Grouped: &GroupedSetExpression{GroupKind: group, MemberKind: member}}
}

// labelledFrames is the hand label for every question in the shipped set.
//
// The anchor and subject TERMS are the caller's own words, carried as
// retrieval pointers exactly as the frame layer defines them -- nothing
// here branches on their text, and the projection never reads them.
func labelledFrames() []labelledFrame {
	return []labelledFrame{
		{
			id:         "qa-grouped-clean",
			rationale:  "'for each team' partitions projects BY team, so the groups are teams and the members are projects; 'and what are the main drivers' adds a second required operation, which is only expressible because Goals is a set.",
			goals:      []InvestigationGoal{GoalAssessState, GoalExplainDrivers},
			expression: labelGrouped(SubjectTeam, SubjectProject),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "qa-grouped-typo",
			rationale:  "The typo-robustness pair. The frame must be IDENTICAL to qa-grouped-clean's; a label that differed would mean the hand labeller read the typo as meaning.",
			goals:      []InvestigationGoal{GoalAssessState, GoalExplainDrivers},
			expression: labelGrouped(SubjectTeam, SubjectProject),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "qb-scoped",
			rationale:  "The named term is the SCOPE, not the subject: the answer is about the team's projects, not about the team. This is the asymmetry, and mislabelling it as a named subject is the CHAOS-4622 defect the design exists to remove.",
			goals:      []InvestigationGoal{GoalAssessState},
			expression: labelScoped("fullchaos", SubjectProject),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "q1-bar-subject-status",
			rationale:  "One named project, one state question. The discriminating control: any set-valued variant here would be structure invented from nothing.",
			goals:      []InvestigationGoal{GoalAssessState},
			expression: labelNamed("Dev Health Ops", SubjectProject),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:        "q2-bar-discovered-cohort",
			rationale: "The set is DISCOVERED (which teams -- unnamed until the system finds them), and 'and why' makes drivers required alongside the ranking. 'Struggling' is a negative-outlier emphasis, which is what makes the ranking required rather than incidental.",
			goals:     []InvestigationGoal{GoalRankOrSurvey, GoalExplainDrivers},
			expression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind,
				Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}},
			temporal: TemporalIntentCurrent,
			emphasis: []AnswerEmphasis{EmphasisNegativeOutliers},
		},
		{
			id:         "neg-single-subject-why",
			rationale:  "A why-phrased question about ONE named project. explain_drivers is the goal; the topology is still a single subject, which is what decision D1 merged into one family.",
			goals:      []InvestigationGoal{GoalExplainDrivers},
			expression: labelNamed("acr", SubjectProject),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "neg-mentions-teams-but-not-grouped",
			rationale:  "Teams SCOPED by a repository, counted. The repository is the anchor and the teams are the members -- the labelled set's own note records that revision 1 had this backwards.",
			goals:      []InvestigationGoal{GoalCountOrAggregate},
			expression: labelScoped("dev-health-acr", SubjectTeam),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "neg-possessive-but-same-kind",
			rationale:  "The possessive SURFACE FORM of the scoped question with none of its asymmetry: the answer is about the team itself, so the team is the subject and there is no scope. The sharpest negative in the set.",
			goals:      []InvestigationGoal{GoalAssessState},
			expression: labelNamed("fullchaos", SubjectTeam),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "pos-scoped-repositories",
			rationale:  "Repositories scoped by a team, surveyed rather than counted -- 'which repositories' asks for the members, not their cardinality. Real asymmetry without the possessive grammar.",
			goals:      []InvestigationGoal{GoalRankOrSurvey},
			expression: labelScoped("platform", SubjectRepository),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:         "pos-grouped-per-phrasing",
			rationale:  "'per X' is the same partition as 'for each X', with a grouping kind that is NOT team -- so a label that only recognises team grouping fails here.",
			goals:      []InvestigationGoal{GoalAssessState},
			expression: labelGrouped(SubjectRepository, SubjectIncident),
			temporal:   TemporalIntentCurrent,
		},
		{
			id:        "neg-explicit-comparison",
			rationale: "Two named projects held against each other over a stated window. A genuine explicit set: the operands are named, not discovered and not grouped.",
			goals:     []InvestigationGoal{GoalCompare},
			expression: SubjectExpression{Kind: SubjectExpressionExplicitSet,
				Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
					projectionNamedOperand("acr", SubjectProject),
					projectionNamedOperand("ask-dev", SubjectProject),
				}}},
			temporal: TemporalIntentBoundedWindow,
		},
		{
			id:        "neg-open-question",
			rationale: "No subject is named and none is to be discovered: the ORGANIZATION is what the question is about. This is the label that disagrees with the shipped set, and the disagreement is behaviour change B5 rather than a labelling error -- see the assertion below.",
			goals:     []InvestigationGoal{GoalAssessState},
			expression: SubjectExpression{Kind: SubjectExpressionOrganizationScope,
				Org: &OrganizationScopeExpression{}},
			temporal: TemporalIntentCurrent,
		},
	}
}

// TestLabelledFramesCoverTheWholeQuestionSet stops the hand labels drifting
// away from the corpus they label.
//
// A label set that covers eleven of twelve questions would report an
// agreement rate over a population nobody declared, and the missing row
// would be invisible: the measurement below iterates the LABELS, so an
// unlabelled question simply would not appear.
func TestLabelledFramesCoverTheWholeQuestionSet(t *testing.T) {
	t.Parallel()
	questions := loadLabelledQuestions(t)
	labels := labelledFrames()

	byID := map[string]bool{}
	for _, label := range labels {
		if byID[label.id] {
			t.Errorf("duplicate hand label for question %q", label.id)
		}
		byID[label.id] = true
		if strings.TrimSpace(label.rationale) == "" {
			t.Errorf("hand label %q carries no rationale -- a label a reader cannot disagree with is not ground truth, it is an assertion", label.id)
		}
	}
	for _, question := range questions {
		if !byID[question.ID] {
			t.Errorf("question %q has no hand-labelled frame; the agreement measurement would silently omit it", question.ID)
		}
		delete(byID, question.ID)
	}
	for id := range byID {
		t.Errorf("hand label %q names no question in the shipped set -- a stale label reads exactly like a covered one", id)
	}
	if len(labels) != len(questions) {
		t.Errorf("%d hand labels for %d questions", len(labels), len(questions))
	}
}

// TestLabelledFramesAreAllLegal is the entry condition for the measurement.
//
// A hand label that does not validate is a labelling error, not a finding
// about the projection: an invalid frame projects to unclassified, so an
// illegal label would manufacture a `projection_unclassified` disagreement
// out of the labeller's own mistake.
func TestLabelledFramesAreAllLegal(t *testing.T) {
	t.Parallel()
	for _, label := range labelledFrames() {
		result := ValidateFrame(QuestionFrame{
			Goals:             label.goals,
			SubjectExpression: label.expression,
			Temporal:          label.temporal,
			Emphasis:          label.emphasis,
			Dimensions:        label.dimensions,
		}, nil, "")
		if result.Outcome != FrameValidationOutcomeValid {
			t.Errorf("hand label %q does not validate (%s / %s) -- fix the LABEL; an illegal label fabricates a disagreement", label.id, result.Failure.Invariant, result.Failure.Detail)
		}
	}
}

// TestShadowAgreementOnTheLabelledCorpus is the offline measurement.
//
// It runs BOTH tables over the same twelve questions -- the projection over
// the hand-labelled frame, the precedence table over the shipped signal
// labels -- and reports the agreement with every disagreement named by
// class. The expected disagreements are PINNED by class and by question, so
// a change that alters which questions disagree fails here and has to be
// described.
//
// The pin is per-question rather than a rate. A rate pin ("<= 1
// disagreement") is satisfied by a completely different question
// disagreeing, which is the movement most worth catching.
func TestShadowAgreementOnTheLabelledCorpus(t *testing.T) {
	t.Parallel()
	questions := loadLabelledQuestions(t)
	byID := map[string]labelledQuestion{}
	for _, question := range questions {
		byID[question.ID] = question
	}

	// EXPECTED DISAGREEMENTS, with the behaviour change each one is.
	// Every other question must agree.
	expected := map[string]FamilyAgreementClass{
		// B5: the shipped set routes an org-wide question to the
		// discovered-cohort RANKING, because its Shape is `open` and
		// precedence row 4 reads {discovered_cohort, open}. The frame says
		// the organization IS the subject, so the projection investigates
		// it. The design's own improvement claim for this route is
		// WITHDRAWN until an organization subject can be committed and a
		// state-ish producer serves it -- so this row measures that the
		// route fires, not that it helps.
		"neg-open-question": FamilyAgreementOrganizationRoute,
	}

	counters := NewFamilyAgreementCounters()
	var lines []string
	for _, label := range labelledFrames() {
		question, ok := byID[label.id]
		if !ok {
			t.Fatalf("hand label %q has no question (the coverage test should have caught this first)", label.id)
		}
		result := ValidateFrame(QuestionFrame{
			Goals:             label.goals,
			SubjectExpression: label.expression,
			Temporal:          label.temporal,
			Emphasis:          label.emphasis,
			Dimensions:        label.dimensions,
		}, nil, "")
		if result.Outcome != FrameValidationOutcomeValid {
			t.Fatalf("hand label %q is illegal (the legality test should have caught this first)", label.id)
		}

		projection := DeriveQuestionFamily(result.Frame)
		precedence := ResolveFamilyForSample(FamilySample{
			Shape:           labelledShapeFor(question),
			SubjectTerms:    labelledSubjectTermsFor(question),
			GroupKind:       SubjectKind(question.ExpectGroupKind),
			ScopeAnchorTerm: question.ExpectScopeAnchor,
			ScopeAnchorKind: SubjectKind(question.ExpectScopeAnchorKind),
			RequestedKind:   SubjectKind(question.ExpectRequestedKind),
		})

		agreement := ClassifyFamilyAgreement(projection, precedence)
		counters.Observe(agreement)
		lines = append(lines, fmt.Sprintf("  %-36s projected=%-26s precedence=%-26s %s",
			label.id, agreement.ProjectedFamily, agreement.PrecedenceFamily, agreement.Class))

		// The precedence side must reproduce the shipped set's OWN
		// expectation. If it does not, the signals this test feeds it are
		// wrong and every disagreement it reports is an artefact of this
		// test rather than a difference between the two tables.
		if got, want := string(precedence.Family), question.ExpectFamily; got != want {
			t.Errorf("%s: the precedence table produced %q from the shipped signal labels, but the set expects %q -- this test is feeding it the wrong signals and its agreement numbers mean nothing", label.id, got, want)
		}

		wantClass, isExpected := expected[label.id]
		switch {
		case isExpected && agreement.Class != wantClass:
			t.Errorf("%s: expected disagreement class %q, got %q", label.id, wantClass, agreement.Class)
		case !isExpected && !agreement.Agreed:
			t.Errorf("%s: UNEXPECTED disagreement -- projected %q (row %s), precedence %q (row %s), class %q. If this is a behaviour change, add it to `expected` with the change it is; if not, it is a defect in the projection.",
				label.id, agreement.ProjectedFamily, agreement.ProjectedRow, agreement.PrecedenceFamily, agreement.PrecedenceRow, agreement.Class)
		}
	}

	if counters.Total != len(questions) {
		t.Fatalf("measured %d comparisons over %d questions", counters.Total, len(questions))
	}
	if counters.Disagreed() != len(expected) {
		t.Errorf("%d disagreements, expected %d", counters.Disagreed(), len(expected))
	}

	sort.Strings(lines)
	classes := make([]string, 0, FamilyAgreementClassCount)
	for _, class := range FamilyAgreementClassVocabulary() {
		classes = append(classes, fmt.Sprintf("%s=%d", class, counters.ByClass[class]))
	}
	t.Logf("SHADOW AGREEMENT, labelled corpus (hand-labelled frames, one person's ground truth)\n%s\n  total=%d agreed=%d disagreed=%d\n  by class: %s",
		strings.Join(lines, "\n"), counters.Total, counters.Agreed, counters.Disagreed(), strings.Join(classes, " "))
}

// labelledShapeFor is the stage-1 Shape the shipped set's own expectations
// imply for a question.
//
// The labelled set predates the frame and records no Shape, but the
// precedence table's rows 4 and 5 read one, so a comparison that fed it an
// empty Shape would send every non-grouped, non-scoped, non-comparison
// question to row 7 and manufacture `precedence_unclassified` on questions
// the shipped table classifies perfectly well.
//
// It is derived from the set's OWN expected family -- the family the set
// says the precedence table must produce -- and the derivation is checked:
// the test above asserts the precedence table, fed these signals, returns
// exactly the family the set expects. A wrong Shape here therefore fails
// loudly rather than quietly shifting the agreement number.
func labelledShapeFor(question labelledQuestion) InvestigationShape {
	switch QuestionFamily(question.ExpectFamily) {
	case QuestionFamilyDiscoveredCohortRanking:
		return ShapeDiscoveredCohort
	case QuestionFamilySubjectInvestigation:
		return ShapeSingleSubject
	default:
		// Grouped, scoped and comparison questions are decided by rows
		// 1-3, which are evaluated BEFORE Shape is read, so the value
		// cannot affect them. Left explicitly empty rather than guessed.
		return ""
	}
}

// labelledSubjectTermsFor supplies the precedence table's row-3 input.
//
// Row 3 fires on ">=2 DISTINCT subject terms", so the term count is a
// routing signal for the shipped table. The shipped set records the
// EXPECTED FAMILY rather than the terms, so the count is derived from that:
// exactly the comparison question is given two terms, and every other
// question one.
//
// THIS IS THE TEST'S BIGGEST BOUND AND IT IS STATED, NOT BURIED. Giving
// every other question a single term means this measurement cannot exhibit
// the comparison-row THEFT (behaviour change B6) -- the case where a
// grouped question happens to carry two distinct terms and the precedence
// table takes it before Shape is read. That is a real and important class;
// it is simply not observable from a corpus that records no terms. The
// production counter sees the real terms and is where B6 will be counted.
func labelledSubjectTermsFor(question labelledQuestion) []string {
	if QuestionFamily(question.ExpectFamily) == QuestionFamilyExplicitComparison {
		return []string{"a", "b"}
	}
	return []string{"a"}
}
