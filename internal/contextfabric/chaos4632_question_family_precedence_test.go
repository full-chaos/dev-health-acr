package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4632 §4.2 precedence-table tests. RED ON origin/main by compile
// failure -- none of these symbols exist there.

// TestPrecedenceTableRows drives every row of the §4.2 table, in order,
// plus the boundary case that makes each row's condition load-bearing.
//
// TABLE-DRIVEN AND EXHAUSTIVE BY ROW, deliberately: the design's whole
// claim is that this is a TABLE, not control flow, so the test that proves
// it has to be a table too. Each case names the row it exercises, so a
// failure says which rule broke rather than only which family came out.
func TestPrecedenceTableRows(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		sample FamilySample
		family QuestionFamily
		row    FamilyPrecedenceRow
	}{
		{
			name:   "row 1: group kind alone decides, above everything",
			sample: FamilySample{GroupKind: contractsv1.ContextFabricSubjectTeam},
			family: QuestionFamilyGroupedCohortStatus,
			row:    FamilyPrecedenceRowGroupKind,
		},
		{
			// THE ROW-ORDER PROOF. Every later row's condition is
			// satisfied simultaneously: comparison terms (row 3), an
			// anchor asymmetry (row 2), and a single_subject Shape (row
			// 5/6). Row 1 must still win. Under the ORIGINAL Shape-first
			// table this returned subject_status, which is the
			// contradiction with §7 that round 1 of the review found.
			name: "row 1 outranks rows 2, 3 and 5 when all four conditions hold",
			sample: FamilySample{
				GroupKind:       contractsv1.ContextFabricSubjectTeam,
				ScopeAnchorTerm: "fullchaos",
				ScopeAnchorKind: contractsv1.ContextFabricSubjectTeam,
				RequestedKind:   contractsv1.ContextFabricSubjectProject,
				ComparisonTerms: []string{"last quarter"},
				SubjectTerms:    []string{"alpha", "beta"},
				Shape:           ShapeSingleSubject,
			},
			family: QuestionFamilyGroupedCohortStatus,
			row:    FamilyPrecedenceRowGroupKind,
		},
		{
			name: "row 2: anchor asymmetry -- Q-B's shape",
			sample: FamilySample{
				ScopeAnchorTerm: "fullchaos",
				ScopeAnchorKind: contractsv1.ContextFabricSubjectTeam,
				RequestedKind:   contractsv1.ContextFabricSubjectProject,
				Shape:           ShapeSingleSubject,
			},
			family: QuestionFamilyScopedCohortStatus,
			row:    FamilyPrecedenceRowScopeAnchor,
		},
		{
			// The asymmetry is the whole condition. Same kind on both
			// sides is NOT a scoped cohort -- it is a question about the
			// named subject itself, and row 2 must decline so a later row
			// can decide. Without this, "the fullchaos team's team"
			// nonsense would route to a scoped cohort.
			name: "row 2 declines when anchor kind equals requested kind",
			sample: FamilySample{
				ScopeAnchorTerm: "fullchaos",
				ScopeAnchorKind: contractsv1.ContextFabricSubjectTeam,
				RequestedKind:   contractsv1.ContextFabricSubjectTeam,
				Shape:           ShapeSingleSubject,
			},
			family: QuestionFamilySubjectInvestigation,
			row:    FamilyPrecedenceRowSingleSubject,
		},
		{
			// An anchor with no stated kind cannot establish an
			// asymmetry. Refusing here rather than assuming is the same
			// refuse-to-guess discipline row 7 encodes -- guessing would
			// send a plain single-subject question to a cohort answer.
			name: "row 2 declines when the anchor names no kind",
			sample: FamilySample{
				ScopeAnchorTerm: "fullchaos",
				RequestedKind:   contractsv1.ContextFabricSubjectProject,
				Shape:           ShapeSingleSubject,
			},
			family: QuestionFamilySubjectInvestigation,
			row:    FamilyPrecedenceRowSingleSubject,
		},
		{
			name:   "row 3: explicit comparison terms",
			sample: FamilySample{ComparisonTerms: []string{"alpha"}, Shape: ShapeSingleSubject},
			family: QuestionFamilyExplicitComparison,
			row:    FamilyPrecedenceRowComparison,
		},
		{
			name:   "row 3: two distinct subject terms",
			sample: FamilySample{SubjectTerms: []string{"alpha", "beta"}, Shape: ShapeSingleSubject},
			family: QuestionFamilyExplicitComparison,
			row:    FamilyPrecedenceRowComparison,
		},
		{
			// DISTINCTNESS, not slice length. Q-A's own captures emit
			// two-element SubjectTerms for a question that is a grouped
			// cohort, so counting raw length would fire row 3 on
			// near-duplicates of one term and route a cohort question to
			// a two-sided comparison.
			name:   "row 3 declines on case- and whitespace-duplicate terms",
			sample: FamilySample{SubjectTerms: []string{"Project", "project "}, Shape: ShapeSingleSubject},
			family: QuestionFamilySubjectInvestigation,
			row:    FamilyPrecedenceRowSingleSubject,
		},
		{
			name:   "row 3 declines on empty-string terms",
			sample: FamilySample{SubjectTerms: []string{"alpha", "  ", ""}, Shape: ShapeSingleSubject},
			family: QuestionFamilySubjectInvestigation,
			row:    FamilyPrecedenceRowSingleSubject,
		},
		{
			name:   "row 4: discovered cohort shape",
			sample: FamilySample{Shape: ShapeDiscoveredCohort},
			family: QuestionFamilyDiscoveredCohortRanking,
			row:    FamilyPrecedenceRowCohortShape,
		},
		{
			name:   "row 4: open shape",
			sample: FamilySample{Shape: ShapeOpen},
			family: QuestionFamilyDiscoveredCohortRanking,
			row:    FamilyPrecedenceRowCohortShape,
		},
		{
			name:   "rows 5+6 merged by D1: single subject",
			sample: FamilySample{Shape: ShapeSingleSubject},
			family: QuestionFamilySubjectInvestigation,
			row:    FamilyPrecedenceRowSingleSubject,
		},
		{
			// explicit_cohort with fewer than two distinct named members
			// is not something this table can route. unclassified is the
			// honest answer, and it is what the Q-A/Q-B explicit_cohort
			// replicates land on with no GroupKind or anchor emitted --
			// stated plainly in §4.2 rather than papered over.
			name:   "row 7: explicit cohort with no named members is unclassified",
			sample: FamilySample{Shape: ShapeExplicitCohort},
			family: QuestionFamilyUnclassified,
			row:    FamilyPrecedenceRowNone,
		},
		{
			name:   "row 7: nothing at all is unclassified, never an error",
			sample: FamilySample{},
			family: QuestionFamilyUnclassified,
			row:    FamilyPrecedenceRowNone,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			outcome := ResolveFamilyForSample(testCase.sample)
			if outcome.Family != testCase.family {
				t.Errorf("Family = %q, want %q", outcome.Family, testCase.family)
			}
			if outcome.Row != testCase.row {
				t.Errorf("Row = %q, want %q", outcome.Row, testCase.row)
			}
		})
	}
}

// TestPrecedenceDoesNotReadShapeUntilRowFour is THE structural property the
// design claims, asserted directly rather than inferred from the row cases
// above.
//
// §4.2: "The table's structural property is real and checkable on the
// captures: it does not read Shape until rows 4-6, so the three Shape
// values the captures exhibit cannot by themselves split one question
// across families."
//
// The proof: hold the structure signals fixed, vary Shape across ALL FOUR
// vocabulary members, and require one family out. If any row above 4 ever
// started consulting Shape, this fails.
func TestPrecedenceDoesNotReadShapeUntilRowFour(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name   string
		signal FamilySample
		family QuestionFamily
	}{
		{
			name:   "group kind fixed (row 1)",
			signal: FamilySample{GroupKind: contractsv1.ContextFabricSubjectTeam},
			family: QuestionFamilyGroupedCohortStatus,
		},
		{
			name: "anchor asymmetry fixed (row 2)",
			signal: FamilySample{
				ScopeAnchorTerm: "fullchaos",
				ScopeAnchorKind: contractsv1.ContextFabricSubjectTeam,
				RequestedKind:   contractsv1.ContextFabricSubjectProject,
			},
			family: QuestionFamilyScopedCohortStatus,
		},
		{
			name:   "comparison terms fixed (row 3)",
			signal: FamilySample{ComparisonTerms: []string{"alpha"}},
			family: QuestionFamilyExplicitComparison,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			for _, shape := range allInvestigationShapes() {
				sample := fixture.signal
				sample.Shape = shape
				if got := ResolveFamilyForSample(sample).Family; got != fixture.family {
					t.Errorf("with Shape=%q the family became %q, want %q -- a row above 4 is reading Shape", shape, got, fixture.family)
				}
			}
		})
	}
}

// TestCapturedReplicatesMisRouteWithoutTheNewSignals applies the table to
// the SIX REAL replicate captures (kiac/dh_0830, REAL data --
// ~/.cache/acr-kiac-askdev/results/triage-*.json, transcribed verbatim
// from §4.2's ground-truth table) and asserts exactly what those captures
// CAN prove, and nothing more.
//
// WHAT THEY CANNOT PROVE, stated first because an earlier revision of the
// design overclaimed here and round 2 caught it: GroupKind and
// ScopeAnchorTerm do not exist on today's wire, so they are null in all
// six captures and rows 1 and 2 cannot fire. The captures therefore cannot
// validate the routing this design depends on. Asserting a "correct"
// family here would be circular -- it would assume the very emission this
// slice exists to measure. That is why S2's gate is labelled semantic
// correctness on a hand-labelled set, not anything derivable from these
// six rows.
//
// WHAT THEY DO PROVE is the NEGATIVE result: rows 3-6 alone cannot give
// either question ONE family. That is asserted below.
//
// FINDING, recorded here because it CORRECTS §4.2's own prose. The design
// predicts that applying this table literally to the captures "routes Q-A's
// discovered samples to discovered_cohort_ranking, Q-B r1 to
// subject_status, and the explicit-cohort samples to unclassified". Two of
// the six do not behave that way. Both Q-A TYPO replicates carry two
// distinct subject terms ("each team" + "project statuses" / "project"),
// so ROW 3 fires before Shape is ever read and both route to
// explicit_comparison -- including the one whose Shape is
// discovered_cohort. So Q-A is mis-routed by row 3 as well as by rows 4-6,
// which strengthens the design's conclusion rather than weakening it: the
// case for row 1 is broader than the doc states, because the grouped
// question is stolen by the comparison row too, not only split by Shape.
func TestCapturedReplicatesMisRouteWithoutTheNewSignals(t *testing.T) {
	t.Parallel()
	captures := []struct {
		replicate string
		question  string
		sample    FamilySample
	}{
		{"Q-A typo r1", "Q-A", FamilySample{Shape: ShapeDiscoveredCohort, SubjectTerms: []string{"each team", "project statuses"}}},
		{"Q-A typo r2", "Q-A", FamilySample{Shape: ShapeExplicitCohort, SubjectTerms: []string{"each team", "project"}}},
		{"Q-A clean r1", "Q-A", FamilySample{Shape: ShapeDiscoveredCohort, SubjectTerms: []string{"each team"}}},
		{"Q-A clean r2", "Q-A", FamilySample{Shape: ShapeDiscoveredCohort, SubjectTerms: nil}},
		{"Q-B r1", "Q-B", FamilySample{Shape: ShapeSingleSubject, SubjectTerms: []string{"fullchaos team"}}},
		{"Q-B r2", "Q-B", FamilySample{Shape: ShapeExplicitCohort, SubjectTerms: []string{"fullchaos team"}}},
	}

	perQuestion := map[string]map[QuestionFamily]struct{}{}
	for _, capture := range captures {
		family := ResolveFamilyForSample(capture.sample).Family
		if perQuestion[capture.question] == nil {
			perQuestion[capture.question] = map[QuestionFamily]struct{}{}
		}
		perQuestion[capture.question][family] = struct{}{}

		// Rows 1 and 2 must be unreachable from a capture carrying
		// neither signal. If either ever fires here, the table started
		// guessing a grouping or a scope it was never told.
		if family == QuestionFamilyGroupedCohortStatus || family == QuestionFamilyScopedCohortStatus {
			t.Errorf("%s resolved to %q from a capture carrying NO GroupKind and NO ScopeAnchorTerm", capture.replicate, family)
		}
	}

	// The negative result: neither question gets ONE family out of its own
	// replicates. This is the measured justification for rows 1 and 2
	// existing at all -- and it is a property of the CAPTURES, so it holds
	// without assuming anything about emission.
	for _, question := range []string{"Q-A", "Q-B"} {
		if len(perQuestion[question]) < 2 {
			t.Errorf("%s resolved to a single family %v across its replicates; the captures are supposed to demonstrate that rows 3-6 ALONE split one question across families, which is why rows 1 and 2 are needed", question, perQuestion[question])
		}
	}
}

// TestUnreachableFamiliesAreNeverRoutedTo pins the deliberate declaration
// gap: trend and investment_allocation are vocabulary members with no path
// through the table in this slice (§4.2), because neither has a
// time_series/breakdown declaration to plan against until S3.
//
// The property is asserted over EVERY row-triggering shape rather than by
// reading the table's source, because "no case returns it" is a claim
// about the whole function, not about the lines a reader happened to look
// at.
func TestUnreachableFamiliesAreNeverRoutedTo(t *testing.T) {
	t.Parallel()
	unreachable := map[QuestionFamily]struct{}{}
	for _, family := range UnreachableQuestionFamilies() {
		unreachable[family] = struct{}{}
	}
	kinds := []SubjectKind{"", contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject}
	for _, shape := range append(allInvestigationShapes(), "") {
		for _, groupKind := range kinds {
			for _, anchorKind := range kinds {
				for _, requestedKind := range kinds {
					for _, anchor := range []string{"", "fullchaos"} {
						for _, terms := range [][]string{nil, {"a"}, {"a", "b"}} {
							sample := FamilySample{
								Shape: shape, GroupKind: groupKind, ScopeAnchorTerm: anchor,
								ScopeAnchorKind: anchorKind, RequestedKind: requestedKind, SubjectTerms: terms,
							}
							got := ResolveFamilyForSample(sample).Family
							if _, bad := unreachable[got]; bad {
								t.Fatalf("sample %+v routed to declared-unreachable family %q", sample, got)
							}
						}
					}
				}
			}
		}
	}
}

// TestModelPickDowngradesAreClassified pins the diagnosis half: an
// operator must be able to tell WHY the model's own pick was not used.
//
// The design's round-2 review added attempted_family and
// incompatibility_reason precisely because the original event recorded only
// the outcome, so a downgraded decision could not be diagnosed from the
// run's own artifacts -- the exact bar AGENTS.md's CANONICAL ARCHITECTURE
// section sets, and the same class lane-4579's codex round found in its
// finding 5.
func TestModelPickDowngradesAreClassified(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		sample     FamilySample
		attempted  QuestionFamily
		reason     FamilyIncompatibilityReason
		downgraded bool
	}{
		{
			name:       "an out-of-vocabulary pick is unrecognized and is never echoed back",
			sample:     FamilySample{Shape: ShapeSingleSubject, ModelFamilyUnrecognized: true},
			attempted:  "",
			reason:     FamilyIncompatibilityUnrecognized,
			downgraded: true,
		},
		{
			name:       "a declared-unreachable pick is named as such, not silently ignored",
			sample:     FamilySample{Shape: ShapeSingleSubject, ModelFamily: QuestionFamilyTrend},
			attempted:  QuestionFamilyTrend,
			reason:     FamilyIncompatibilityUnreachable,
			downgraded: true,
		},
		{
			name:       "a vocabulary pick the structure does not support is a structural mismatch",
			sample:     FamilySample{Shape: ShapeSingleSubject, ModelFamily: QuestionFamilyDiscoveredCohortRanking},
			attempted:  QuestionFamilyDiscoveredCohortRanking,
			reason:     FamilyIncompatibilityStructuralMismatch,
			downgraded: true,
		},
		{
			name:       "an agreeing pick is recorded as attempted so agreement is distinguishable from silence",
			sample:     FamilySample{Shape: ShapeSingleSubject, ModelFamily: QuestionFamilySubjectInvestigation},
			attempted:  QuestionFamilySubjectInvestigation,
			reason:     "",
			downgraded: false,
		},
		{
			name:       "no pick at all is NOT a downgrade -- there was nothing to override",
			sample:     FamilySample{Shape: ShapeSingleSubject},
			attempted:  "",
			reason:     "",
			downgraded: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			outcome := ResolveFamilyForSample(testCase.sample)
			if outcome.AttemptedFamily != testCase.attempted {
				t.Errorf("AttemptedFamily = %q, want %q", outcome.AttemptedFamily, testCase.attempted)
			}
			if outcome.IncompatibilityReason != testCase.reason {
				t.Errorf("IncompatibilityReason = %q, want %q", outcome.IncompatibilityReason, testCase.reason)
			}
			if outcome.Downgraded != testCase.downgraded {
				t.Errorf("Downgraded = %v, want %v", outcome.Downgraded, testCase.downgraded)
			}
		})
	}
}

// TestPrecedenceTableIsTotal proves the table has no undefined input: over
// a wide cross-product it always returns a vocabulary member and a named
// row, never an empty family standing in for "could not decide".
//
// unclassified IS the could-not-decide answer, and it is a real member.
// The distinction matters because an empty family would silently satisfy a
// `!= someFamily` check downstream while meaning nothing.
func TestPrecedenceTableIsTotal(t *testing.T) {
	t.Parallel()
	kinds := []SubjectKind{"", contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject, "not_a_kind"}
	for _, shape := range append(allInvestigationShapes(), "", "not_a_shape") {
		for _, groupKind := range kinds {
			for _, anchor := range []string{"", "x"} {
				for _, anchorKind := range kinds {
					sample := FamilySample{Shape: shape, GroupKind: groupKind, ScopeAnchorTerm: anchor, ScopeAnchorKind: anchorKind}
					outcome := ResolveFamilyForSample(sample)
					if !ValidQuestionFamily(outcome.Family) {
						t.Fatalf("sample %+v produced non-vocabulary family %q", sample, outcome.Family)
					}
					if outcome.Row == "" {
						t.Fatalf("sample %+v produced an empty precedence row", sample)
					}
				}
			}
		}
	}
}
