package v1

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// rejectionCase names one clause of ContextFabricInterpretedQuestion.validate()
// and a mutation that makes exactly that clause the FIRST one to fail.
type rejectionCase struct {
	name   string
	mutate func(ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion
	want   ContextFabricInterpretationRejectionReason
	// field names the struct field this mutation REPLACES wholesale. The
	// pairwise order test composes one case on top of another, which is
	// only meaningful when the two write different fields -- composing two
	// mutations that both replace the same field silently discards the
	// first, and the "earlier clause wins" assertion would then be testing
	// a question that never violated the earlier clause at all.
	//
	// THREE groups share a field this way, not one: SubjectTerms (count +
	// content), ComparisonTerms (count + content) and FactRequirements
	// (kind + subjects + parameters-count + parameter + duplicate). An
	// earlier version of this comment claimed the fact-requirement clauses
	// were the only such group; a review round showed that was wrong. All
	// three are skipped here and covered by
	// TestDiagnoseContextFabricInterpretedQuestionRejectionOrdersWithinASingleField,
	// which composes them properly.
	field string
}

// interpretationRejectionCases covers every clause the mirror can name, in
// validate()'s own order. The order of this slice is load-bearing for
// TestDiagnoseContextFabricInterpretedQuestionRejectionMatchesValidateStatementOrder
// below, which walks it pairwise.
func interpretationRejectionCases() []rejectionCase {
	longTerm := strings.Repeat("t", ContextFabricSubjectOrComparisonTermMaxLength+1)
	return []rejectionCase{
		{
			name: "shape",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.Shape = "not_a_real_shape"
				return q
			},
			want:  ContextFabricInterpretationRejectionShapeInvalid,
			field: "Shape",
		},
		{
			name: "requested_judgment",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.RequestedJudgment = ""
				return q
			},
			want:  ContextFabricInterpretationRejectionRequestedJudgmentInvalid,
			field: "RequestedJudgment",
		},
		{
			name: "subject_terms count",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.SubjectTerms = distinctTerms(ContextFabricSubjectTermsMaxCount + 1)
				return q
			},
			want:  ContextFabricInterpretationRejectionSubjectTermsMaxCount,
			field: "SubjectTerms",
		},
		{
			name: "comparison_terms count",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.ComparisonTerms = distinctTerms(ContextFabricSubjectTermsMaxCount + 1)
				return q
			},
			want:  ContextFabricInterpretationRejectionComparisonTermsMaxCount,
			field: "ComparisonTerms",
		},
		{
			name: "subject_terms content",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.SubjectTerms = []string{longTerm}
				return q
			},
			want:  ContextFabricInterpretationRejectionSubjectTermsInvalid,
			field: "SubjectTerms",
		},
		{
			name: "comparison_terms content",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.ComparisonTerms = []string{"dup", "dup"}
				return q
			},
			want:  ContextFabricInterpretationRejectionComparisonTermsInvalid,
			field: "ComparisonTerms",
		},
		{
			name: "fact_requirements count",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.FactRequirements = make([]ContextFabricFactRequirement, ContextFabricFactRequirementsMaxCount+1)
				return q
			},
			want:  ContextFabricInterpretationRejectionFactRequirementsMaxCount,
			field: "FactRequirements",
		},
		{
			name: "clarification_reason length",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.ClarificationReason = strings.Repeat("r", ContextFabricClarificationReasonMaxLength+1)
				return q
			},
			want:  ContextFabricInterpretationRejectionClarificationReasonMax,
			field: "ClarificationReason",
		},
		{
			name: "time_context",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.TimeContext = ContextFabricTimeContext{Axis: "not_a_real_axis"}
				return q
			},
			want:  ContextFabricInterpretationRejectionTimeContextInvalid,
			field: "TimeContext",
		},
		{
			name: "fact_requirement kind",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.FactRequirements = []ContextFabricFactRequirement{{Kind: "not_a_fact_kind"}}
				return q
			},
			want:  ContextFabricInterpretationRejectionFactRequirementKindInvalid,
			field: "FactRequirements",
		},
		{
			// Structurally unreachable from the MODEL (there is no wire
			// field for Subjects), but validate() checks it, so the mirror
			// must too -- and a review round proved the omission was not
			// harmless: mutating this branch to unclassified left every
			// mirror, vocabulary and order test green.
			name: "fact_requirement subjects",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				subjects := make([]ContextFabricSubjectRef, 0, 251)
				for i := 0; i < 251; i++ {
					subjects = append(subjects, ContextFabricSubjectRef{
						Kind: ContextFabricSubjectProject, CanonicalID: fmt.Sprintf("project_%d", i), Label: "P",
					})
				}
				q.FactRequirements = []ContextFabricFactRequirement{{Kind: ContextFabricFactStatus, Subjects: subjects}}
				return q
			},
			want:  ContextFabricInterpretationRejectionFactRequirementSubjectsInvalid,
			field: "FactRequirements",
		},
		{
			name: "fact_requirement parameters count",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				params := make(map[string]string, ContextFabricFactRequirementParametersMaxCount+1)
				for i := 0; i <= ContextFabricFactRequirementParametersMaxCount; i++ {
					params[fmt.Sprintf("k%02d", i)] = "v"
				}
				q.FactRequirements = []ContextFabricFactRequirement{{Kind: ContextFabricFactStatus, Parameters: params}}
				return q
			},
			want:  ContextFabricInterpretationRejectionFactRequirementParamsMaxCount,
			field: "FactRequirements",
		},
		{
			name: "fact_requirement parameter",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.FactRequirements = []ContextFabricFactRequirement{{
					Kind:       ContextFabricFactStatus,
					Parameters: map[string]string{" untrimmed": "value"},
				}}
				return q
			},
			want:  ContextFabricInterpretationRejectionFactRequirementParameterInvalid,
			field: "FactRequirements",
		},
		{
			name: "fact_requirement duplicate kind",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.FactRequirements = []ContextFabricFactRequirement{
					{Kind: ContextFabricFactStatus},
					{Kind: ContextFabricFactStatus},
				}
				return q
			},
			want:  ContextFabricInterpretationRejectionFactRequirementKindDuplicate,
			field: "FactRequirements",
		},
		{
			name: "clarification_needed with no reason",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.ClarificationNeeded = true
				q.ClarificationReason = "   "
				return q
			},
			want:  ContextFabricInterpretationRejectionClarificationReasonMissing,
			field: "ClarificationNeeded",
		},
		{
			name: "window_class",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.WindowClass = "not_a_real_window_class"
				return q
			},
			want:  ContextFabricInterpretationRejectionWindowClassInvalid,
			field: "WindowClass",
		},
		{
			name: "window_confidence",
			mutate: func(q ContextFabricInterpretedQuestion) ContextFabricInterpretedQuestion {
				q.WindowConfidence = "not_a_real_confidence"
				return q
			},
			want:  ContextFabricInterpretationRejectionWindowConfidenceInvalid,
			field: "WindowConfidence",
		},
	}
}

func distinctTerms(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "term"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	return out
}

// TestDiagnoseContextFabricInterpretedQuestionRejectionNamesEveryClause is
// the differential: for each clause, the mutated question must actually be
// REJECTED by validate() (otherwise the case is vacuous -- a green test
// against a question the validator happily accepts proves nothing), and the
// mirror must name that clause.
func TestDiagnoseContextFabricInterpretedQuestionRejectionNamesEveryClause(t *testing.T) {
	for _, testCase := range interpretationRejectionCases() {
		t.Run(testCase.name, func(t *testing.T) {
			q := testCase.mutate(validDiagnosisInterpretedQuestion())
			// Vacuity guard first. Every case below asserts a property of
			// a REJECTION; if validate() accepts, there is no rejection to
			// diagnose and the assertion would be meaningless.
			if err := q.Validate(); err == nil {
				t.Fatalf("Validate() = nil for the %q case -- the mutation does not actually reject, so this case proves nothing", testCase.name)
			}
			reason, ok := DiagnoseContextFabricInterpretedQuestionRejection(q)
			if !ok {
				t.Fatalf("DiagnoseContextFabricInterpretedQuestionRejection() ok = false for a question Validate() rejected")
			}
			if reason != testCase.want {
				t.Fatalf("reason = %q, want %q", reason, testCase.want)
			}
		})
	}
}

// TestDiagnoseContextFabricInterpretedQuestionRejectionAcceptsValidQuestion
// pins the other half of the contract: a question validate() ACCEPTS gets
// no name and ok=false, never a fabricated clause.
func TestDiagnoseContextFabricInterpretedQuestionRejectionAcceptsValidQuestion(t *testing.T) {
	q := validDiagnosisInterpretedQuestion()
	if err := q.Validate(); err != nil {
		t.Fatalf("the fixture is not valid: %v", err)
	}
	reason, ok := DiagnoseContextFabricInterpretedQuestionRejection(q)
	if ok {
		t.Fatalf("DiagnoseContextFabricInterpretedQuestionRejection() = (%q, true), want ok=false for a valid question", reason)
	}
	if reason != ContextFabricInterpretationRejectionUnclassified {
		t.Fatalf("reason = %q, want %q for a valid question", reason, ContextFabricInterpretationRejectionUnclassified)
	}
}

// TestDiagnoseContextFabricInterpretedQuestionRejectionMatchesValidateStatementOrder
// is the SOUNDNESS guard, and the reason this mirror exists as a mirror
// rather than as a scan for any violation.
//
// It applies each case's mutation ON TOP OF every EARLIER case's mutation.
// validate() short-circuits left to right, so the earlier clause is the one
// that actually rejects, and the mirror must name THAT one -- never the
// later, also-violated clause. This is the exact defect CHAOS-3784 rounds
// 1-3 shipped and round 4 corrected on the bound-diagnosis side: naming a
// later violation tells an operator to fix a field that was not the reason.
func TestDiagnoseContextFabricInterpretedQuestionRejectionMatchesValidateStatementOrder(t *testing.T) {
	cases := interpretationRejectionCases()
	for i := 1; i < len(cases); i++ {
		earlier, later := cases[i-1], cases[i]
		if earlier.field == later.field {
			// Composition is meaningless here: `later` replaces the same
			// field `earlier` wrote, so the combined question no longer
			// violates the earlier clause at all and asserting that the
			// earlier clause "wins" would assert something false about a
			// question that never had that violation. The fact-requirement
			// clauses are the only such group and they are ordered by the
			// dedicated test below, which composes them on ONE requirement
			// instead of overwriting the slice.
			continue
		}
		t.Run(earlier.name+" wins over "+later.name, func(t *testing.T) {
			q := later.mutate(earlier.mutate(validDiagnosisInterpretedQuestion()))
			if err := q.Validate(); err == nil {
				t.Fatalf("Validate() = nil for the combined mutation -- the case is vacuous")
			}
			reason, ok := DiagnoseContextFabricInterpretedQuestionRejection(q)
			if !ok {
				t.Fatalf("ok = false for a rejected question")
			}
			if reason != earlier.want {
				t.Fatalf("reason = %q, want the EARLIER clause %q -- validate() short-circuits, so the later clause %q was never reached and must not be named",
					reason, earlier.want, later.want)
			}
		})
	}
}

// TestContextFabricInterpretationRejectionReasonVocabularyIsClosed walks
// every declared constant through the canonical table. A member added to
// the constant block without a table row fails here rather than silently
// escaping the "the logged value is a table constant" guarantee.
func TestContextFabricInterpretationRejectionReasonVocabularyIsClosed(t *testing.T) {
	declared := []ContextFabricInterpretationRejectionReason{
		ContextFabricInterpretationRejectionUnclassified,
		ContextFabricInterpretationRejectionShapeInvalid,
		ContextFabricInterpretationRejectionRequestedJudgmentInvalid,
		ContextFabricInterpretationRejectionSubjectTermsMaxCount,
		ContextFabricInterpretationRejectionComparisonTermsMaxCount,
		ContextFabricInterpretationRejectionSubjectTermsInvalid,
		ContextFabricInterpretationRejectionComparisonTermsInvalid,
		ContextFabricInterpretationRejectionFactRequirementsMaxCount,
		ContextFabricInterpretationRejectionClarificationReasonMax,
		ContextFabricInterpretationRejectionTimeContextInvalid,
		ContextFabricInterpretationRejectionFactRequirementKindInvalid,
		ContextFabricInterpretationRejectionFactRequirementSubjectsInvalid,
		ContextFabricInterpretationRejectionFactRequirementParamsMaxCount,
		ContextFabricInterpretationRejectionFactRequirementParameterInvalid,
		ContextFabricInterpretationRejectionFactRequirementKindDuplicate,
		ContextFabricInterpretationRejectionClarificationReasonMissing,
		ContextFabricInterpretationRejectionWindowClassInvalid,
		ContextFabricInterpretationRejectionWindowConfidenceInvalid,
		ContextFabricInterpretationRejectionFactCapabilityParameterNotAllowed,
	}
	for _, reason := range declared {
		if !ValidContextFabricInterpretationRejectionReason(reason) {
			t.Fatalf("declared member %q is missing from canonicalContextFabricInterpretationRejectionReasons", reason)
		}
	}
	if len(declared) != len(canonicalContextFabricInterpretationRejectionReasons) {
		t.Fatalf("declared members = %d, canonical table rows = %d -- the table carries a value this test does not enumerate, or vice versa",
			len(declared), len(canonicalContextFabricInterpretationRejectionReasons))
	}
	if got := CanonicalContextFabricInterpretationRejectionReason("something_the_model_made_up"); got != ContextFabricInterpretationRejectionUnclassified {
		t.Fatalf("CanonicalContextFabricInterpretationRejectionReason(non-member) = %q, want %q -- a non-member must never be returned verbatim", got, ContextFabricInterpretationRejectionUnclassified)
	}
}

// TestCanonicalInterpretationRejectionReasonReturnsTheTableConstant proves
// the property the canonical table exists for: the returned value is the
// TABLE's own constant, not the caller's string.
//
// The first version of this test compared string CONTENTS, which is
// vacuous -- Go string equality cannot distinguish "returned the constant"
// from "returned an equal-valued heap string", and a codex round disproved
// it by mutating Canonical... to return the caller's own dynamically
// allocated string and watching the test stay green. Comparing the backing
// DATA POINTER is what actually settles it: a package constant's bytes live
// in read-only static storage, so a returned value sharing that pointer
// cannot be the runtime-built input.
//
// This is the assertion that makes the CodeQL go/log-injection posture real
// rather than conventional: validating a tainted value and then logging the
// tainted value is a different thing from validating it and logging the
// matched constant, and only the second survives this test.
func TestCanonicalInterpretationRejectionReasonReturnsTheTableConstant(t *testing.T) {
	// Built at runtime from parts so it cannot be the constant, while
	// carrying byte-identical text to a real member.
	callerBuilt := ContextFabricInterpretationRejectionReason(strings.Join([]string{"shape", "invalid"}, "_"))
	if callerBuilt != ContextFabricInterpretationRejectionShapeInvalid {
		t.Fatalf("fixture is wrong: %q must equal the member's text", callerBuilt)
	}
	if unsafe.StringData(string(callerBuilt)) == unsafe.StringData(string(ContextFabricInterpretationRejectionShapeInvalid)) {
		t.Skip("the compiler interned the runtime-built string; this test cannot distinguish the two here")
	}

	got := CanonicalContextFabricInterpretationRejectionReason(callerBuilt)
	if got != ContextFabricInterpretationRejectionShapeInvalid {
		t.Fatalf("got %q, want %q", got, ContextFabricInterpretationRejectionShapeInvalid)
	}
	if unsafe.StringData(string(got)) == unsafe.StringData(string(callerBuilt)) {
		t.Fatalf("CanonicalContextFabricInterpretationRejectionReason returned the CALLER's string, not the table's constant -- a tainted value that merely passes the membership check would reach a log field verbatim")
	}
}

// TestDiagnoseContextFabricInterpretedQuestionRejectionIsTotalOverRejections
// is the completeness guard the vocabulary needs to stay honest: every
// question validate() rejects must get a NAMED reason, never Unclassified.
// Unclassified is reserved for a rejection produced somewhere this mirror
// does not model -- if this validator can produce one, the vocabulary has a
// hole and this test is where it surfaces.
func TestDiagnoseContextFabricInterpretedQuestionRejectionIsTotalOverRejections(t *testing.T) {
	instant := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	extra := []ContextFabricInterpretedQuestion{
		// A range axis missing its end.
		func() ContextFabricInterpretedQuestion {
			q := validDiagnosisInterpretedQuestion()
			q.TimeContext = ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &instant}
			return q
		}(),
		// A current axis carrying timestamps it may not carry.
		func() ContextFabricInterpretedQuestion {
			q := validDiagnosisInterpretedQuestion()
			q.TimeContext = ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent, AsOf: &instant}
			return q
		}(),
		// An untrimmed subject term.
		func() ContextFabricInterpretedQuestion {
			q := validDiagnosisInterpretedQuestion()
			q.SubjectTerms = []string{" leading space"}
			return q
		}(),
		// A fact requirement parameter value over its bound.
		func() ContextFabricInterpretedQuestion {
			q := validDiagnosisInterpretedQuestion()
			q.FactRequirements = []ContextFabricFactRequirement{{
				Kind:       ContextFabricFactStatus,
				Parameters: map[string]string{"k": strings.Repeat("v", ContextFabricFactRequirementParameterValueMaxLength+1)},
			}}
			return q
		}(),
	}
	for _, testCase := range interpretationRejectionCases() {
		extra = append(extra, testCase.mutate(validDiagnosisInterpretedQuestion()))
	}
	for i, q := range extra {
		if err := q.Validate(); err == nil {
			t.Fatalf("case %d: Validate() = nil, so this case is vacuous", i)
		}
		reason, ok := DiagnoseContextFabricInterpretedQuestionRejection(q)
		if !ok || reason == ContextFabricInterpretationRejectionUnclassified {
			t.Fatalf("case %d: reason = (%q, ok=%t) for a REJECTED question -- every rejection this validator produces must be nameable", i, reason, ok)
		}
	}
}

// TestDiagnoseContextFabricInterpretedQuestionRejectionOrdersWithinASingleField
// covers every same-field clause GROUP the pairwise test above must skip.
//
// A review round observed that the earlier version of this test covered only
// the fact-requirement group, and that the ledger's claim "the
// fact-requirement clauses are the only such group" was therefore wrong:
// SubjectTerms has a count clause AND a content clause, and so does
// ComparisonTerms. The claim is corrected here and the two missing groups
// are covered.
//
// One nuance worth recording, because it changes what this test is FOR.
// The round predicted that reversing the mirror's SubjectTerms count/content
// order would leave every existing fixture green. Executed, that is FALSE:
// the pairwise test catches it transitively, because the adjacent pair
// (comparison_terms count, subject_terms content) stops agreeing. So this
// test is not closing a hole through which a defect could actually escape
// today — it is making the coverage DIRECT instead of incidental, so that a
// later edit to the pairwise table cannot silently remove the only thing
// checking these orders.
//
// The fact-requirement group is different and genuinely needs this test:
// those clauses live on one slice, and composing them by overwriting it
// would erase the earlier violation instead of stacking it.
//
// Here the violations are stacked on ONE requirement (or one slice) so both
// clauses are genuinely violated at once, and the mirror must name the one
// validate() actually short-circuits on.
func TestDiagnoseContextFabricInterpretedQuestionRejectionOrdersWithinASingleField(t *testing.T) {
	longTerm := strings.Repeat("t", ContextFabricSubjectOrComparisonTermMaxLength+1)

	// SubjectTerms and ComparisonTerms each carry a COUNT clause and a
	// CONTENT clause, and validate() checks both counts before either
	// content. A question that violates count AND content on the same
	// field must name the count.
	for _, termCase := range []struct {
		name  string
		apply func(*ContextFabricInterpretedQuestion, []string)
		want  ContextFabricInterpretationRejectionReason
	}{
		{
			name:  "subject_terms count wins over subject_terms content",
			apply: func(q *ContextFabricInterpretedQuestion, v []string) { q.SubjectTerms = v },
			want:  ContextFabricInterpretationRejectionSubjectTermsMaxCount,
		},
		{
			name:  "comparison_terms count wins over comparison_terms content",
			apply: func(q *ContextFabricInterpretedQuestion, v []string) { q.ComparisonTerms = v },
			want:  ContextFabricInterpretationRejectionComparisonTermsMaxCount,
		},
	} {
		t.Run(termCase.name, func(t *testing.T) {
			// Over the count bound AND carrying an overlong entry, so both
			// clauses on that one field are violated at once.
			terms := append(distinctTerms(ContextFabricSubjectTermsMaxCount+1), longTerm)
			q := validDiagnosisInterpretedQuestion()
			termCase.apply(&q, terms)
			if err := q.Validate(); err == nil {
				t.Fatalf("Validate() = nil -- the case is vacuous")
			}
			reason, ok := DiagnoseContextFabricInterpretedQuestionRejection(q)
			if !ok {
				t.Fatalf("ok = false for a rejected question")
			}
			if reason != termCase.want {
				t.Fatalf("reason = %q, want %q -- validate() checks the count clause before any content clause", reason, termCase.want)
			}
		})
	}

	badParameters := map[string]string{" untrimmed": "value"}
	for _, testCase := range []struct {
		name         string
		requirements []ContextFabricFactRequirement
		want         ContextFabricInterpretationRejectionReason
		why          string
	}{
		{
			name: "an invalid kind AND a bad parameter on the same requirement",
			requirements: []ContextFabricFactRequirement{
				{Kind: "not_a_fact_kind", Parameters: badParameters},
			},
			want: ContextFabricInterpretationRejectionFactRequirementKindInvalid,
			why:  "validate() checks the kind enum before it ever reaches the parameter loop",
		},
		{
			name: "a bad parameter on the FIRST of two duplicate-kind requirements",
			requirements: []ContextFabricFactRequirement{
				{Kind: ContextFabricFactStatus, Parameters: badParameters},
				{Kind: ContextFabricFactStatus},
			},
			want: ContextFabricInterpretationRejectionFactRequirementParameterInvalid,
			why:  "validate() runs requirement.validate() before the duplicate-kind check, so entry 1 rejects before entry 2 is ever seen",
		},
		{
			name: "duplicate kinds where the SECOND entry also has a bad parameter",
			requirements: []ContextFabricFactRequirement{
				{Kind: ContextFabricFactStatus},
				{Kind: ContextFabricFactStatus, Parameters: badParameters},
			},
			want: ContextFabricInterpretationRejectionFactRequirementParameterInvalid,
			why:  "entry 2's own validate() runs before the duplicate check for entry 2, so the parameter wins even though the duplicate is 'about' the earlier entry",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			q := validDiagnosisInterpretedQuestion()
			q.FactRequirements = testCase.requirements
			if err := q.Validate(); err == nil {
				t.Fatalf("Validate() = nil -- the case is vacuous")
			}
			reason, ok := DiagnoseContextFabricInterpretedQuestionRejection(q)
			if !ok {
				t.Fatalf("ok = false for a rejected question")
			}
			if reason != testCase.want {
				t.Fatalf("reason = %q, want %q -- %s", reason, testCase.want, testCase.why)
			}
		})
	}
}
