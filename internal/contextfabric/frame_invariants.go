package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4452 stage 2 (S7b-i), design §13.5.2: the cross-field invariants,
// in FOUR phases.
//
// SHADOW ONLY -- see frame_vocab.go's package-level note.
//
// WHY THE PHASE SPLIT IS LOAD-BEARING RATHER THAN TIDINESS (law L4, and
// round 2's P1-6). The feedback listed these as ONE set. They are not:
// I10, I14, I16 and I18 read DERIVED values, and the §13.1 flow runs
// validation BEFORE normalization. As one phase they were either
// evaluated before their inputs existed, or obligations were added AFTER
// validation to a frame the design calls immutable. Neither is
// acceptable. The order is now explicit and this file implements exactly
// it:
//
//	consensus -> winning sample WHOLE -> A1 (emitted fields)
//	  -> normalize/derive -> A2 (derived values)
//	  -> repair once if A1 or A2 failed -> FRAME IMMUTABLE
//	  -> resolution (phase B) -> fact read (phase C)
//
// THE FRAME BECOMES IMMUTABLE AT THE END OF A2, NOT A1.
//
// PHASES B AND C ARE DECLARED HERE AND WIRED ELSEWHERE, and that is stated
// rather than left to be discovered. I11 and I12 cannot be decided before
// subject resolution; I13 cannot be decided before the fact read, because
// grouping is built INVERSELY from fact-row team keys AFTER the read
// (chaos4636_grouped_cohort.go:75-122). Their predicates are pure
// functions below so the vocabulary spans i1...i19 and a later slice wires
// them without inventing the rule; this slice validates A1 and A2 only.
// Saying "the invariants are enforced" would be the inaccurate-coverage
// failure acr AGENTS.md names -- a reader who sees a claim stops
// verifying.
//
// A PHASE-B OR PHASE-C FAILURE IS NOT A REPAIR TARGET AND NEVER A
// REFUSAL. It is a resolution or evidence outcome that produces a
// clarification, a narrowed answer, or a requirement outcome with a
// disclosed impact -- never a frame edit. Treating a resolution result as
// a malformed frame would refuse questions that are perfectly well-formed
// and merely unanswerable on this org's data, which North Star check 4
// forbids and check 13 would trip over constantly.

// FrameInvariant is the closed telemetry vocabulary of frame invariants,
// i1...i19.
//
// THE VOCABULARY MUST SPAN EVERY DECLARED INVARIANT, and this is the third
// time that has had to be said. Round 2's P2-4 found the vocabulary capped
// at i10, so I14 could fail with no value to record it. The revision that
// fixed it reintroduced the same defect one member later -- I18 existed
// while the vocabulary stopped at i17 (the author's own handoff item 7).
// An invariant whose failure is unobservable is not enforced. The registry
// test asserts every declared invariant has a vocabulary member and every
// member has a spec.
type FrameInvariant string

const (
	// FrameInvariantI1: exactly one variant pointer is non-nil, and it is
	// the one Kind names.
	FrameInvariantI1 FrameInvariant = "i1"
	// FrameInvariantI2: explicit_set has >= 2 operands.
	FrameInvariantI2 FrameInvariant = "i2"
	// FrameInvariantI3: named_subject has >= 1 non-blank term. This is
	// the subject-terms-omission prevention AT THE FRAME LEVEL;
	// end-to-end prevention needs seam 7.
	FrameInvariantI3 FrameInvariant = "i3"
	// FrameInvariantI4: discovered_kind's MemberKind is a vocabulary
	// member.
	FrameInvariantI4 FrameInvariant = "i4"
	// FrameInvariantI5: children_of_scope has >= 1 anchor term and a
	// valid MemberKind.
	FrameInvariantI5 FrameInvariant = "i5"
	// FrameInvariantI6: grouped_members has both kinds valid and
	// DIFFERENT.
	FrameInvariantI6 FrameInvariant = "i6"
	// FrameInvariantI7: compare implies explicit_set.
	FrameInvariantI7 FrameInvariant = "i7"
	// FrameInvariantI8: a trend or change goal implies a non-current
	// Temporal.
	FrameInvariantI8 FrameInvariant = "i8"
	// FrameInvariantI9: a count goal implies a set-valued Kind.
	FrameInvariantI9 FrameInvariant = "i9"
	// FrameInvariantI10: Obligations is non-empty and in-vocabulary.
	// PHASE A2.
	FrameInvariantI10 FrameInvariant = "i10"
	// FrameInvariantI11: the RESOLVED anchor's kind differs from
	// MemberKind. PHASE B.
	FrameInvariantI11 FrameInvariant = "i11"
	// FrameInvariantI12: the operands resolve to >= 2 DISTINCT subjects.
	// PHASE B.
	FrameInvariantI12 FrameInvariant = "i12"
	// FrameInvariantI13: the group axis placed at least one member.
	// PHASE C.
	FrameInvariantI13 FrameInvariant = "i13"
	// FrameInvariantI14: non-empty Emphasis implies a derived ranking
	// obligation. PHASE A2.
	FrameInvariantI14 FrameInvariant = "i14"
	// FrameInvariantI15: Goals is non-empty AND every member is in the
	// closed vocabulary -- the same pairing I10 makes for obligations.
	// See checkI15 for why membership is checked here and not left to
	// the sanitization boundary.
	FrameInvariantI15 FrameInvariant = "i15"
	// FrameInvariantI16: every axis the frame SETS is discharged (law
	// L2, per-frame). PHASE A2.
	FrameInvariantI16 FrameInvariant = "i16"
	// FrameInvariantI17: organization_scope with a count goal requires
	// Org.MemberKind.
	FrameInvariantI17 FrameInvariant = "i17"
	// FrameInvariantI18: the derived Shape matches the emitted one, or
	// the divergence is recorded and the derived value wins. PHASE A2.
	FrameInvariantI18 FrameInvariant = "i18"
	// FrameInvariantI19: a SubjectOperand is a well-formed discriminated
	// union and its variant satisfies I3 or I5.
	FrameInvariantI19 FrameInvariant = "i19"
)

// FrameValidationPhase names when an invariant can be evaluated.
type FrameValidationPhase string

const (
	// FrameValidationPhaseA1 reads ONLY model-emitted fields and runs
	// BEFORE normalization.
	FrameValidationPhaseA1 FrameValidationPhase = "a1"
	// FrameValidationPhaseA2 reads DERIVED values and runs AFTER
	// normalization.
	FrameValidationPhaseA2 FrameValidationPhase = "a2"
	// FrameValidationPhaseB is resolution-dependent.
	FrameValidationPhaseB FrameValidationPhase = "b"
	// FrameValidationPhaseC is post-fact-read.
	FrameValidationPhaseC FrameValidationPhase = "c"
)

// FrameField names a field an invariant reads. Closed vocabulary, and it
// exists FOR law L4's property test: "every invariant declares the fields
// it reads; assert no A1 invariant names a derived field."
//
// Without this declaration L4 is unfalsifiable -- you can read the code
// and believe it, which is exactly what happened to the design twice.
type FrameField string

const (
	// -- Model-EMITTED fields. An A1 invariant may read these.
	FrameFieldGoals                 FrameField = "goals"
	FrameFieldSubjectExpressionKind FrameField = "subject_expression_kind"
	FrameFieldTemporal              FrameField = "temporal"
	FrameFieldEmphasis              FrameField = "emphasis"
	FrameFieldDimensions            FrameField = "dimensions"
	FrameFieldEmittedShape          FrameField = "emitted_shape"

	// -- The subject variant's PARTS, named individually.
	//
	// THE GRANULARITY IS THE FIX. A single "subject_expression_variant"
	// token was the defect three rounds in a row: I3 READS the variant in
	// order to check that terms exist, but it only CONSTRAINS the terms --
	// and a bound built on the blunt token handed an I3 repair permission
	// over the operands, the member kind and ExpectedKind as well. A field
	// vocabulary that cannot express "the terms, and nothing else" forces
	// every rule written against it to be too coarse.
	FrameFieldSubjectTerms FrameField = "subject_terms"
	FrameFieldAnchorTerms  FrameField = "anchor_terms"
	FrameFieldOperands     FrameField = "operands"
	FrameFieldMemberKind   FrameField = "member_kind"
	FrameFieldGroupKind    FrameField = "group_kind"
	FrameFieldExpectedKind FrameField = "expected_kind"

	// -- DERIVED values. Reading one of these makes an invariant A2 or
	// later, by law L4.
	FrameFieldObligations    FrameField = "obligations"
	FrameFieldDerivedShape   FrameField = "derived_shape"
	FrameFieldAxisDischarges FrameField = "axis_discharges"

	// -- RESOLUTION and EVIDENCE state. Phase B and C only. An
	// obligation may never be derived from any of these (§13.2.3), which
	// is what keeps the frame from being a moving target.
	FrameFieldResolvedAnchorKind    FrameField = "resolved_anchor_kind"
	FrameFieldResolvedOperandSubjes FrameField = "resolved_operand_subjects"
	FrameFieldGroupMembership       FrameField = "group_membership"
)

// derivedFrameFields is the set of fields that are NOT model-emitted. Law
// L4's property test asserts no A1 spec names one.
var derivedFrameFields = map[FrameField]bool{
	FrameFieldObligations:           true,
	FrameFieldDerivedShape:          true,
	FrameFieldAxisDischarges:        true,
	FrameFieldResolvedAnchorKind:    true,
	FrameFieldResolvedOperandSubjes: true,
	FrameFieldGroupMembership:       true,
}

// FrameInvariantSpec declares one invariant's phase, the fields it READS,
// and the fields its condition CONSTRAINS.
//
// READS AND CONSTRAINS ARE DIFFERENT THINGS, and conflating them is the
// single defect that survived three adversarial rounds in three disguises.
// The repair bound asks "what may this repair change?", and the first
// three implementations answered it with the Reads list, which is the
// answer to a different question. I3 READS the subject variant in order to
// check that its terms exist; it CONSTRAINS only the terms. I2 READS the
// operands to count them; it CONSTRAINS only how many there are. Using
// Reads as the proxy gave an I3 repair authority over ExpectedKind and the
// operands, and an I2 repair authority to replace the comparison's
// subjects outright -- both executed by review, both accepted.
//
// So: law L4 quantifies over READS (an invariant may not be evaluated
// before its inputs exist). The repair bound quantifies over CONSTRAINS
// (a repair may only correct what the server has proven inconsistent).
// Neither list is derived from the other, and the registry test asserts
// Constrains is a subset of Reads -- an invariant cannot constrain
// something it never looked at.
type FrameInvariantSpec struct {
	ID    FrameInvariant
	Phase FrameValidationPhase
	Reads []FrameField
	// Constrains is empty for every invariant a repair can never target:
	// the derived-value invariants (I10, I16, I18), which are outcomes of
	// the frame rather than fields of it, and every phase-B and phase-C
	// invariant, which §13.6 rule 6 says repair is never invoked for.
	//
	// PATHS, not field tokens. A token names a level; a path names a
	// place in the tree, and prefix semantics let an invariant constrain
	// a LIST (`...operands`) distinctly from its ELEMENTS
	// (`...operands[]`). That distinction is the one four review rounds
	// kept finding missing, and as a path it is a property of the grammar
	// rather than a rule anyone has to remember.
	Constrains []FramePath
}

// frameInvariantSpecs is the invariant table IN EVALUATION ORDER.
//
// ORDER IS THE CONTRACT, not a listing convenience: telemetry records the
// FIRST failure in table order, and oracle O3 asserts each mutation
// reports a specific invariant BY NAME. Reordering this slice changes what
// a malformed frame reports, so it is pinned by the registry test.
//
// The A1 order is §13.5.2's own table order (I1, I17, I2, I3, I19, I4, I5,
// I6, I7, I8, I9, I15), then A2's (I10, I14, I18, I16), then B, then C.
var frameInvariantSpecs = []FrameInvariantSpec{
	{ID: FrameInvariantI1, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldSubjectExpressionKind, FrameFieldSubjectTerms, FrameFieldAnchorTerms, FrameFieldOperands, FrameFieldMemberKind, FrameFieldGroupKind},
		Constrains: []FramePath{"subject_expression.kind"}},
	{ID: FrameInvariantI17, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldGoals, FrameFieldSubjectExpressionKind, FrameFieldMemberKind},
		Constrains: []FramePath{"subject_expression.org.member_kind"}},
	{ID: FrameInvariantI2, Phase: FrameValidationPhaseA1,
		Reads: []FrameField{FrameFieldOperands},
		// The LIST, with no `[]` marker: I2's condition is a COUNT, so it
		// may change how many operands there are and never what one IS.
		Constrains: []FramePath{"subject_expression.explicit.operands"}},
	{ID: FrameInvariantI3, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldSubjectTerms},
		Constrains: []FramePath{"subject_expression.named.terms"}},
	{ID: FrameInvariantI19, Phase: FrameValidationPhaseA1,
		Reads: []FrameField{FrameFieldOperands},
		// The ELEMENTS, with the marker: a malformed operand is exactly
		// what I19 exists to have corrected.
		Constrains: []FramePath{"subject_expression.explicit.operands[]"}},
	{ID: FrameInvariantI4, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldMemberKind},
		Constrains: []FramePath{"subject_expression.discovered.member_kind"}},
	{ID: FrameInvariantI5, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldAnchorTerms, FrameFieldMemberKind},
		Constrains: []FramePath{"subject_expression.scoped.anchor_terms", "subject_expression.scoped.member_kind"}},
	{ID: FrameInvariantI6, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldGroupKind, FrameFieldMemberKind},
		Constrains: []FramePath{"subject_expression.grouped.group_kind", "subject_expression.grouped.member_kind"}},
	{ID: FrameInvariantI7, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldGoals, FrameFieldSubjectExpressionKind},
		Constrains: []FramePath{"goals", "subject_expression.kind"}},
	{ID: FrameInvariantI8, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldGoals, FrameFieldTemporal},
		Constrains: []FramePath{"goals", "temporal"}},
	{ID: FrameInvariantI9, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldGoals, FrameFieldSubjectExpressionKind},
		Constrains: []FramePath{"goals", "subject_expression.kind"}},
	{ID: FrameInvariantI15, Phase: FrameValidationPhaseA1,
		Reads:      []FrameField{FrameFieldGoals},
		Constrains: []FramePath{"goals"}},

	// The A2 invariants read DERIVED values and constrain NOTHING: an
	// obligation set is an outcome of the frame, not a field of it, so
	// there is nothing for a repair to write. I14 is the exception that
	// proves the rule -- it is discharged by ADDING a goal, so it
	// constrains the goal axis and not the obligations it reads.
	{ID: FrameInvariantI10, Phase: FrameValidationPhaseA2,
		Reads: []FrameField{FrameFieldObligations}},
	{ID: FrameInvariantI14, Phase: FrameValidationPhaseA2,
		Reads:      []FrameField{FrameFieldObligations, FrameFieldEmphasis, FrameFieldGoals},
		Constrains: []FramePath{"goals"}},
	{ID: FrameInvariantI18, Phase: FrameValidationPhaseA2,
		Reads: []FrameField{FrameFieldSubjectExpressionKind, FrameFieldEmittedShape, FrameFieldDerivedShape}},
	{ID: FrameInvariantI16, Phase: FrameValidationPhaseA2,
		Reads: []FrameField{FrameFieldObligations, FrameFieldAxisDischarges}},

	// Phase B and C constrain NOTHING because repair is never invoked for
	// them (§13.6 rule 6). An empty Constrains list is what makes that
	// rule structural rather than a comment.
	{ID: FrameInvariantI11, Phase: FrameValidationPhaseB,
		Reads: []FrameField{FrameFieldAnchorTerms, FrameFieldMemberKind, FrameFieldResolvedAnchorKind}},
	{ID: FrameInvariantI12, Phase: FrameValidationPhaseB,
		Reads: []FrameField{FrameFieldOperands, FrameFieldResolvedOperandSubjes}},

	{ID: FrameInvariantI13, Phase: FrameValidationPhaseC,
		Reads: []FrameField{FrameFieldGroupKind, FrameFieldMemberKind, FrameFieldGroupMembership}},
}

// FrameInvariantConstrainsPath reports whether an invariant's condition
// permits a repair to change path, using the path grammar's prefix
// semantics.
func FrameInvariantConstrainsPath(spec FrameInvariantSpec, path FramePath) bool {
	for _, constrained := range spec.Constrains {
		if PathConstrainedBy(constrained, path) {
			return true
		}
	}
	return false
}

// FrameInvariantCount is nineteen.
const FrameInvariantCount = 19

// FrameInvariantSpecs returns the invariant table in evaluation order.
func FrameInvariantSpecs() []FrameInvariantSpec {
	out := make([]FrameInvariantSpec, len(frameInvariantSpecs))
	copy(out, frameInvariantSpecs)
	return out
}

// ValidFrameInvariant reports membership in the closed telemetry
// vocabulary.
func ValidFrameInvariant(value FrameInvariant) bool {
	for _, spec := range frameInvariantSpecs {
		if spec.ID == value {
			return true
		}
	}
	return false
}

// FrameInvariantReadsDerivedField reports whether an invariant's declared
// reads include a derived value. Law L4's property test is exactly
// "no A1 invariant reads a derived field", and this is the predicate it
// quantifies over.
func FrameInvariantReadsDerivedField(spec FrameInvariantSpec) bool {
	for _, field := range spec.Reads {
		if derivedFrameFields[field] {
			return true
		}
	}
	return false
}

// FrameValidationFailure is one invariant failure. It carries the
// invariant BY NAME because that is what telemetry records and what
// oracle O3 asserts.
type FrameValidationFailure struct {
	Invariant FrameInvariant
	Phase     FrameValidationPhase
	// Detail is a short, CLOSED-VOCABULARY reason code -- never the
	// question text, never a subject identifier, never a term. It exists
	// so that two different ways of failing one invariant (an operand
	// with both pointers nil vs. one whose pointer disagrees with its
	// Kind) are distinguishable in a run's own artifacts.
	Detail FrameFailureDetail
}

// FrameFailureDetail is the closed vocabulary of failure reason codes.
type FrameFailureDetail string

const (
	FrameFailureNoVariant             FrameFailureDetail = "no_variant_set"
	FrameFailureMultipleVariants      FrameFailureDetail = "multiple_variants_set"
	FrameFailureVariantKindMismatch   FrameFailureDetail = "variant_disagrees_with_kind"
	FrameFailureKindUnset             FrameFailureDetail = "kind_unset"
	FrameFailureTooFewOperands        FrameFailureDetail = "too_few_operands"
	FrameFailureNoTerms               FrameFailureDetail = "no_terms"
	FrameFailureBlankTerm             FrameFailureDetail = "blank_term"
	FrameFailureNoAnchorTerms         FrameFailureDetail = "no_anchor_terms"
	FrameFailureMemberKindUnset       FrameFailureDetail = "member_kind_unset"
	FrameFailureMemberKindInvalid     FrameFailureDetail = "member_kind_invalid"
	FrameFailureGroupKindUnset        FrameFailureDetail = "group_kind_unset"
	FrameFailureGroupKindInvalid      FrameFailureDetail = "group_kind_invalid"
	FrameFailureGroupEqualsMember     FrameFailureDetail = "group_kind_equals_member_kind"
	FrameFailureCompareNeedsSet       FrameFailureDetail = "compare_requires_explicit_set"
	FrameFailureTrendNeedsTemporal    FrameFailureDetail = "trend_requires_non_current_temporal"
	FrameFailureCountNeedsSetKind     FrameFailureDetail = "count_requires_set_valued_kind"
	FrameFailureOrgCountNeedsMember   FrameFailureDetail = "org_count_requires_member_kind"
	FrameFailureNoGoals               FrameFailureDetail = "goal_set_empty"
	FrameFailureGoalOutsideVocabulary FrameFailureDetail = "goal_outside_vocabulary"
	FrameFailureNoObligations         FrameFailureDetail = "obligation_set_empty"
	FrameFailureObligationInvalid     FrameFailureDetail = "obligation_outside_vocabulary"
	FrameFailureEmphasisNeedsRanking  FrameFailureDetail = "emphasis_requires_ranking_obligation"
	FrameFailureAxisUndischarged      FrameFailureDetail = "axis_undischarged"
	FrameFailureOperandKindUnset      FrameFailureDetail = "operand_kind_unset"
	FrameFailureOperandNoVariant      FrameFailureDetail = "operand_no_variant_set"
	FrameFailureOperandMultiVariant   FrameFailureDetail = "operand_multiple_variants_set"
	FrameFailureOperandKindMismatch   FrameFailureDetail = "operand_disagrees_with_kind"
	FrameFailureOperandNoTerms        FrameFailureDetail = "operand_no_terms"
	FrameFailureOperandNoAnchor       FrameFailureDetail = "operand_no_anchor_terms"
	FrameFailureOperandMemberKind     FrameFailureDetail = "operand_member_kind_invalid"
)

// ValidateFramePhaseA1 evaluates the SYNTACTIC invariants over the fields
// the model EMITTED, and returns the FIRST failure in table order.
//
// It runs BEFORE normalization, so it must not read Obligations or the
// derived Shape -- law L4, asserted structurally by the spec table above
// rather than only by review.
func ValidateFramePhaseA1(frame QuestionFrame) (FrameValidationFailure, bool) {
	expression := frame.SubjectExpression

	// I1 -- exactly one variant pointer, and it is the one Kind names.
	if failure, bad := checkI1(expression); bad {
		return failure, true
	}
	// I17 -- an organization-scope COUNT must say what it counts.
	if failure, bad := checkI17(frame); bad {
		return failure, true
	}
	// I2 -- explicit_set operand COUNT. Operand well-formedness is I19,
	// checked below, and reported as i19 rather than as i2: a malformed
	// operand is an operand defect, and folding it into i2 would make
	// "the comparison had one operand" and "the second operand was
	// malformed" the same telemetry value.
	if failure, bad := checkI2(expression); bad {
		return failure, true
	}
	// I3 -- named_subject terms.
	if failure, bad := checkI3(expression); bad {
		return failure, true
	}
	// I19 -- each operand is a well-formed discriminated union whose
	// variant satisfies I3 or I5.
	if failure, bad := checkI19(expression); bad {
		return failure, true
	}
	// I4 -- discovered_kind MemberKind.
	if failure, bad := checkI4(expression); bad {
		return failure, true
	}
	// I5 -- children_of_scope anchor + MemberKind.
	if failure, bad := checkI5(expression); bad {
		return failure, true
	}
	// I6 -- grouped_members kinds, both valid and DIFFERENT.
	if failure, bad := checkI6(expression); bad {
		return failure, true
	}
	// I7 -- compare implies explicit_set.
	if failure, bad := checkI7(frame); bad {
		return failure, true
	}
	// I8 -- a trend/change goal implies a non-current Temporal.
	if failure, bad := checkI8(frame); bad {
		return failure, true
	}
	// I9 -- a count goal implies a set-valued Kind.
	if failure, bad := checkI9(frame); bad {
		return failure, true
	}
	// I15 -- the goal set is non-empty after sanitization. LAST in A1
	// order, per §13.5.2's table, and that placement is deliberate: a
	// frame that is malformed in a more specific way should report the
	// specific invariant, not "no goals".
	if failure, bad := checkI15(frame); bad {
		return failure, true
	}
	return FrameValidationFailure{}, false
}

// ValidateFramePhaseA2 evaluates the invariants that read DERIVED values,
// AFTER normalization, and returns the FIRST failure in table order.
//
// emittedShape is the sampler's own Shape, for I18. An empty emitted Shape
// is not a divergence -- see ShapeAgreement.
func ValidateFramePhaseA2(frame QuestionFrame, emittedShape InvestigationShape) (FrameValidationFailure, bool) {
	// I10 -- obligations non-empty and in-vocabulary.
	if len(frame.Obligations) == 0 {
		return FrameValidationFailure{Invariant: FrameInvariantI10, Phase: FrameValidationPhaseA2, Detail: FrameFailureNoObligations}, true
	}
	for _, obligation := range frame.Obligations {
		if !ValidAnswerObligation(obligation) {
			return FrameValidationFailure{Invariant: FrameInvariantI10, Phase: FrameValidationPhaseA2, Detail: FrameFailureObligationInvalid}, true
		}
	}
	// I14 -- emphasis with no ordering to emphasize is malformed, not a
	// silent no-op.
	//
	// I14 NAMES Goals AS WELL AS Emphasis, and that is not decoration:
	// `ranking` is discharged only by Goals containing rank_or_survey
	// (table 1), so the ONE permitted repair for an I14 failure is to ADD
	// that goal -- which the repair bound allows only because the
	// violated invariant names the field. The frozen text permitted the
	// repair while its own I14 did not name Goals, a self-contradiction
	// round 3 (finding 4) caught. It is closed HERE, in the invariant,
	// and in the spec table's Reads list, rather than in a ledger.
	if len(frame.Emphasis) > 0 && !frame.HasObligation(ObligationRanking) {
		return FrameValidationFailure{Invariant: FrameInvariantI14, Phase: FrameValidationPhaseA2, Detail: FrameFailureEmphasisNeedsRanking}, true
	}
	// I18 -- the derived Shape wins; a divergence is RECORDED, never a
	// rejection. It is listed as an invariant so it has a telemetry
	// value; the frozen text carried it as prose only, which is why its
	// failures were unobservable.
	//
	// Returning no failure here is the correct behaviour and the reason
	// is worth stating: I18's contract is "or the divergence is recorded
	// and the derived value wins". Rejecting a frame whose sampler
	// disagreed with the server would refuse frames the server has
	// already decided it can answer.
	_, _ = ShapeAgreement(emittedShape, frame.SubjectExpression)
	// I16 -- every axis the frame SETS is discharged (law L2, per-frame).
	//
	// THIS IS THE INVARIANT THAT CATCHES A DROPPED DRIVER OBLIGATION, NOT
	// I10. Round 2's P2-4 corrected the design on exactly this point: I10
	// checks only non-emptiness and vocabulary validity, so a frame whose
	// explain_drivers goal lost its principal_drivers obligation still
	// carries {state, health, evidence, coverage} and passes I10
	// cleanly. What actually catches it is I16 -- the explain_drivers
	// goal axis is no longer discharged.
	if discharge, undischarged := UndischargedAxis(frame); undischarged {
		_ = discharge
		return FrameValidationFailure{Invariant: FrameInvariantI16, Phase: FrameValidationPhaseA2, Detail: FrameFailureAxisUndischarged}, true
	}
	return FrameValidationFailure{}, false
}

func checkI1(expression SubjectExpression) (FrameValidationFailure, bool) {
	fail := func(detail FrameFailureDetail) (FrameValidationFailure, bool) {
		return FrameValidationFailure{Invariant: FrameInvariantI1, Phase: FrameValidationPhaseA1, Detail: detail}, true
	}
	if expression.Kind == "" || !ValidSubjectExpressionKind(expression.Kind) {
		return fail(FrameFailureKindUnset)
	}
	set := 0
	var named SubjectExpressionKind
	if expression.Named != nil {
		set++
		named = SubjectExpressionNamed
	}
	if expression.Explicit != nil {
		set++
		named = SubjectExpressionExplicitSet
	}
	if expression.Discovered != nil {
		set++
		named = SubjectExpressionDiscoveredKind
	}
	if expression.Scoped != nil {
		set++
		named = SubjectExpressionChildrenOfScope
	}
	if expression.Grouped != nil {
		set++
		named = SubjectExpressionGroupedMembers
	}
	if expression.Org != nil {
		set++
		named = SubjectExpressionOrganizationScope
	}
	switch {
	case set == 0:
		return fail(FrameFailureNoVariant)
	case set > 1:
		return fail(FrameFailureMultipleVariants)
	case named != expression.Kind:
		return fail(FrameFailureVariantKindMismatch)
	}
	return FrameValidationFailure{}, false
}

func checkI17(frame QuestionFrame) (FrameValidationFailure, bool) {
	if frame.SubjectExpression.Kind != SubjectExpressionOrganizationScope {
		return FrameValidationFailure{}, false
	}
	org := frame.SubjectExpression.Org
	// OPTIONAL IS NOT UNVALIDATED, and the two are easy to conflate.
	//
	// MemberKind is optional for a non-counting goal, where the org itself
	// is the subject and there is nothing to enumerate. But "may be
	// absent" is not "may be anything": a SUPPLIED value must still be a
	// member of the closed subject-kind vocabulary. Checking it only on
	// the counting path let `organization_scope{member_kind:"squad"}` with
	// `Goals=[assess_state]` pass BOTH phases, and `MemberKind()` then
	// handed `("squad", true)` to the retrieval seam -- which the seam-7
	// slice will call on frames it assumes were validated. Found by
	// adversarial review; the optionality is the exemption, the
	// vocabulary is not.
	if org != nil && org.MemberKind != nil && !contractsv1.ValidContextFabricSubjectKind(*org.MemberKind) {
		return FrameValidationFailure{Invariant: FrameInvariantI17, Phase: FrameValidationPhaseA1, Detail: FrameFailureMemberKindInvalid}, true
	}
	if !frame.HasGoal(GoalCountOrAggregate) {
		return FrameValidationFailure{}, false
	}
	if org == nil || org.MemberKind == nil {
		return FrameValidationFailure{Invariant: FrameInvariantI17, Phase: FrameValidationPhaseA1, Detail: FrameFailureOrgCountNeedsMember}, true
	}
	return FrameValidationFailure{}, false
}

func checkI2(expression SubjectExpression) (FrameValidationFailure, bool) {
	if expression.Kind != SubjectExpressionExplicitSet || expression.Explicit == nil {
		return FrameValidationFailure{}, false
	}
	if len(expression.Explicit.Operands) < 2 {
		return FrameValidationFailure{Invariant: FrameInvariantI2, Phase: FrameValidationPhaseA1, Detail: FrameFailureTooFewOperands}, true
	}
	return FrameValidationFailure{}, false
}

func checkI3(expression SubjectExpression) (FrameValidationFailure, bool) {
	if expression.Kind != SubjectExpressionNamed || expression.Named == nil {
		return FrameValidationFailure{}, false
	}
	return namedTermsFailure(expression.Named, FrameInvariantI3, FrameFailureNoTerms, FrameFailureBlankTerm)
}

// namedTermsFailure is I3's rule, factored so I19 can apply the SAME rule
// to a named operand rather than a paraphrase of it. §13.5.2 says a named
// operand "satisfies I3"; sharing the predicate is what makes that
// literally true instead of approximately true.
func namedTermsFailure(named *NamedSubjectExpression, invariant FrameInvariant, empty, blank FrameFailureDetail) (FrameValidationFailure, bool) {
	if named == nil || len(named.Terms) == 0 {
		return FrameValidationFailure{Invariant: invariant, Phase: FrameValidationPhaseA1, Detail: empty}, true
	}
	for _, term := range named.Terms {
		if strings.TrimSpace(term) == "" {
			return FrameValidationFailure{Invariant: invariant, Phase: FrameValidationPhaseA1, Detail: blank}, true
		}
	}
	return FrameValidationFailure{}, false
}

func checkI19(expression SubjectExpression) (FrameValidationFailure, bool) {
	if expression.Kind != SubjectExpressionExplicitSet || expression.Explicit == nil {
		return FrameValidationFailure{}, false
	}
	fail := func(detail FrameFailureDetail) (FrameValidationFailure, bool) {
		return FrameValidationFailure{Invariant: FrameInvariantI19, Phase: FrameValidationPhaseA1, Detail: detail}, true
	}
	for _, operand := range expression.Explicit.Operands {
		if operand.Kind == "" || !ValidSubjectOperandKind(operand.Kind) {
			return fail(FrameFailureOperandKindUnset)
		}
		set := 0
		var named SubjectOperandKind
		if operand.Named != nil {
			set++
			named = SubjectOperandNamed
		}
		if operand.Scoped != nil {
			set++
			named = SubjectOperandScoped
		}
		switch {
		case set == 0:
			return fail(FrameFailureOperandNoVariant)
		case set > 1:
			return fail(FrameFailureOperandMultiVariant)
		case named != operand.Kind:
			return fail(FrameFailureOperandKindMismatch)
		}
		// The variant's own invariant, applied THROUGH I19 and reported
		// as i19. §13.5.2: "a named operand satisfies I3, a scoped
		// operand satisfies I5"; oracle O12(b) pins the reporting --
		// "a scoped operand with empty AnchorTerms fails I5 through I19;
		// the failed invariant is recorded as i19".
		switch operand.Kind {
		case SubjectOperandNamed:
			if _, bad := namedTermsFailure(operand.Named, FrameInvariantI19, FrameFailureOperandNoTerms, FrameFailureOperandNoTerms); bad {
				return fail(FrameFailureOperandNoTerms)
			}
		case SubjectOperandScoped:
			if len(operand.Scoped.AnchorTerms) == 0 {
				return fail(FrameFailureOperandNoAnchor)
			}
			for _, term := range operand.Scoped.AnchorTerms {
				if strings.TrimSpace(term) == "" {
					return fail(FrameFailureOperandNoAnchor)
				}
			}
			if !contractsv1.ValidContextFabricSubjectKind(operand.Scoped.MemberKind) {
				return fail(FrameFailureOperandMemberKind)
			}
		}
	}
	return FrameValidationFailure{}, false
}

func checkI4(expression SubjectExpression) (FrameValidationFailure, bool) {
	if expression.Kind != SubjectExpressionDiscoveredKind || expression.Discovered == nil {
		return FrameValidationFailure{}, false
	}
	if expression.Discovered.MemberKind == "" {
		return FrameValidationFailure{Invariant: FrameInvariantI4, Phase: FrameValidationPhaseA1, Detail: FrameFailureMemberKindUnset}, true
	}
	if !contractsv1.ValidContextFabricSubjectKind(expression.Discovered.MemberKind) {
		return FrameValidationFailure{Invariant: FrameInvariantI4, Phase: FrameValidationPhaseA1, Detail: FrameFailureMemberKindInvalid}, true
	}
	return FrameValidationFailure{}, false
}

func checkI5(expression SubjectExpression) (FrameValidationFailure, bool) {
	if expression.Kind != SubjectExpressionChildrenOfScope || expression.Scoped == nil {
		return FrameValidationFailure{}, false
	}
	fail := func(detail FrameFailureDetail) (FrameValidationFailure, bool) {
		return FrameValidationFailure{Invariant: FrameInvariantI5, Phase: FrameValidationPhaseA1, Detail: detail}, true
	}
	if len(expression.Scoped.AnchorTerms) == 0 {
		return fail(FrameFailureNoAnchorTerms)
	}
	for _, term := range expression.Scoped.AnchorTerms {
		if strings.TrimSpace(term) == "" {
			return fail(FrameFailureNoAnchorTerms)
		}
	}
	if expression.Scoped.MemberKind == "" {
		return fail(FrameFailureMemberKindUnset)
	}
	if !contractsv1.ValidContextFabricSubjectKind(expression.Scoped.MemberKind) {
		return fail(FrameFailureMemberKindInvalid)
	}
	return FrameValidationFailure{}, false
}

func checkI6(expression SubjectExpression) (FrameValidationFailure, bool) {
	if expression.Kind != SubjectExpressionGroupedMembers || expression.Grouped == nil {
		return FrameValidationFailure{}, false
	}
	fail := func(detail FrameFailureDetail) (FrameValidationFailure, bool) {
		return FrameValidationFailure{Invariant: FrameInvariantI6, Phase: FrameValidationPhaseA1, Detail: detail}, true
	}
	grouped := expression.Grouped
	if grouped.GroupKind == "" {
		return fail(FrameFailureGroupKindUnset)
	}
	if !contractsv1.ValidContextFabricSubjectKind(grouped.GroupKind) {
		return fail(FrameFailureGroupKindInvalid)
	}
	if grouped.MemberKind == "" {
		return fail(FrameFailureMemberKindUnset)
	}
	if !contractsv1.ValidContextFabricSubjectKind(grouped.MemberKind) {
		return fail(FrameFailureMemberKindInvalid)
	}
	// GroupKind != MemberKind. Grouping a kind by itself is not a
	// grouping, and the engine ALREADY refuses a plan in that state
	// ("groups %q members by their own kind") -- so a frame that permits
	// it produces a plan the contract rejects downstream. Catching it
	// here is what turns a late refusal into an early, repairable one.
	if grouped.GroupKind == grouped.MemberKind {
		return fail(FrameFailureGroupEqualsMember)
	}
	return FrameValidationFailure{}, false
}

func checkI7(frame QuestionFrame) (FrameValidationFailure, bool) {
	if !frame.HasGoal(GoalCompare) {
		return FrameValidationFailure{}, false
	}
	if frame.SubjectExpression.Kind != SubjectExpressionExplicitSet {
		return FrameValidationFailure{Invariant: FrameInvariantI7, Phase: FrameValidationPhaseA1, Detail: FrameFailureCompareNeedsSet}, true
	}
	return FrameValidationFailure{}, false
}

func checkI8(frame QuestionFrame) (FrameValidationFailure, bool) {
	if !frame.HasAnyGoal(GoalDescribeTrend, GoalExplainChange) {
		return FrameValidationFailure{}, false
	}
	switch frame.Temporal {
	case TemporalIntentBoundedWindow, TemporalIntentPeriodComparison, TemporalIntentTimeSeries:
		return FrameValidationFailure{}, false
	default:
		// Includes TemporalIntentCurrent and the unset value. An unset
		// Temporal normalizes to `current`, and `current` is exactly
		// what I8 forbids for a trend goal, so both reach here.
		return FrameValidationFailure{Invariant: FrameInvariantI8, Phase: FrameValidationPhaseA1, Detail: FrameFailureTrendNeedsTemporal}, true
	}
}

func checkI9(frame QuestionFrame) (FrameValidationFailure, bool) {
	if !frame.HasGoal(GoalCountOrAggregate) {
		return FrameValidationFailure{}, false
	}
	switch frame.SubjectExpression.Kind {
	case SubjectExpressionChildrenOfScope,
		SubjectExpressionDiscoveredKind,
		SubjectExpressionGroupedMembers,
		SubjectExpressionOrganizationScope:
		return FrameValidationFailure{}, false
	default:
		// A count over a single named subject is not a count.
		return FrameValidationFailure{Invariant: FrameInvariantI9, Phase: FrameValidationPhaseA1, Detail: FrameFailureCountNeedsSetKind}, true
	}
}

func checkI15(frame QuestionFrame) (FrameValidationFailure, bool) {
	if len(frame.Goals) == 0 {
		// An empty goal set is a FAILURE, never a default. Round 2's
		// P1-7: the old "unset goal defaults to assess_state" rule
		// silently turned "which teams are struggling?" into a status
		// question, losing the ranking operation with no repair,
		// clarification or refusal.
		return FrameValidationFailure{Invariant: FrameInvariantI15, Phase: FrameValidationPhaseA1, Detail: FrameFailureNoGoals}, true
	}
	// MEMBERSHIP, not merely non-emptiness -- and the asymmetry with the
	// design's prose is deliberate rather than an over-reach.
	//
	// §13.2.1 says an unknown goal string is DROPPED at the SANITIZATION
	// boundary, never an error, and §13.5.2 states I15 as "Goals is
	// non-empty AFTER SANITIZATION". Both are about a frame that HAS been
	// sanitized. This function validates frames it did not build, and it
	// cannot distinguish "the caller sanitized and this is a real member"
	// from "the caller skipped sanitization". Trusting the caller is what
	// made an unrecognized goal invisible in three places at once: it
	// contributes no obligations (a map miss in table 1), it contributes
	// no axis discharge (a map miss in goalDischarge, so I16 cannot see
	// the axis it should have failed on), and it reaches
	// FrameValidationEventFrom verbatim -- which put ARBITRARY MODEL TEXT
	// into the `proposed_goals` log field, defeating the closed-vocabulary
	// rule and the no-free-text telemetry rule in one step.
	//
	// I10 is the design's own precedent for the shape of this fix: it
	// reads "Obligations is non-empty AND every member is in the closed
	// vocabulary". I15 is the same invariant for the goal axis and now
	// says the same thing.
	//
	// In production this fires almost never: the emission path sanitizes
	// first, so an unknown goal is dropped and only the all-unknown case
	// reaches here, as empty. What it catches is a caller that skipped
	// sanitization -- which is a bug the validator now NAMES instead of
	// swallowing.
	for _, goal := range frame.Goals {
		if !ValidInvestigationGoal(goal) {
			return FrameValidationFailure{Invariant: FrameInvariantI15, Phase: FrameValidationPhaseA1, Detail: FrameFailureGoalOutsideVocabulary}, true
		}
	}
	return FrameValidationFailure{}, false
}

// subjectOperandWellFormed is I19's per-operand predicate, exposed so the
// repair bound can ask the question I19 asks.
//
// IT EXISTS BECAUSE THE BOUND NEEDS TO TELL "correct the operand the
// server proved inconsistent" FROM "rewrite an operand that was already
// fine". Adversarial review round 4 found the gap: I2's condition is
// `len(Operands) >= 2`, a COUNT, and the bound let an I2 repair change an
// EXISTING, well-formed operand from `named_subject("team a")` into
// `children_of_scope(anchor "team a", member project)` -- preserving the
// term string, and so slipping past the pointer rule, while turning "how
// is team A doing" into "how are team A's projects doing".
//
// Sharing the predicate rather than restating it is the point: a second
// copy of "what makes an operand well-formed" is the parallel authority
// law L6 bans, and it is how the outer union's rules drifted from the
// operand's in the first place.
func subjectOperandWellFormed(operand SubjectOperand) bool {
	if operand.Kind == "" || !ValidSubjectOperandKind(operand.Kind) {
		return false
	}
	set := 0
	var named SubjectOperandKind
	if operand.Named != nil {
		set++
		named = SubjectOperandNamed
	}
	if operand.Scoped != nil {
		set++
		named = SubjectOperandScoped
	}
	if set != 1 || named != operand.Kind {
		return false
	}
	switch operand.Kind {
	case SubjectOperandNamed:
		if _, bad := namedTermsFailure(operand.Named, FrameInvariantI19, FrameFailureOperandNoTerms, FrameFailureOperandNoTerms); bad {
			return false
		}
	case SubjectOperandScoped:
		if len(operand.Scoped.AnchorTerms) == 0 {
			return false
		}
		for _, term := range operand.Scoped.AnchorTerms {
			if strings.TrimSpace(term) == "" {
				return false
			}
		}
		if !contractsv1.ValidContextFabricSubjectKind(operand.Scoped.MemberKind) {
			return false
		}
	}
	return true
}
