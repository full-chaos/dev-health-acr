package v1

import "sort"

// ContextFabricRejectedClause is the CLOSED vocabulary naming WHICH clause
// of a model-minted struct's own Validate() method rejected it.
//
// It answers a question no existing field can. A model-minted driver,
// claimed fact, or finding that fails its Validate() collapses into one
// generic error ("driver judgment violates v1 bounds") and, one layer out,
// into one rejection reason (driver_invalid / claim_invalid /
// finding_invalid). ContextFabricDriverJudgment.validate alone decides on
// twenty distinct clauses -- an identifier length, four separate closed
// vocabularies, a confidence range, six reference-shape rules, and three
// business rules -- and every one of them reports the same name. From the
// trace you cannot tell a title that ran two characters long from a
// category outside the vocabulary from a driver that cited no evidence at
// all, which are three different defects with three different owners.
//
// WHY THIS IS NOT ContextFabricModelFacingBounds. That registry is not a
// diagnosis vocabulary: every entry in it is a length/count bound the
// SYNTHESIS PROMPT is contractually required to state, asserted by
// genkitruntime.TestPromptsStateEveryModelFacingBound. Naming a clause
// there would oblige the prompt to state it, and a prompt change is a
// prompt VERSION change. This vocabulary therefore sits beside that
// registry rather than inside it: it names clauses for a rejection that
// already happened and obliges the prompt to say nothing.
//
// The two are complements, not alternatives, and a reader uses both: the
// clause names the FIELD and RULE that rejected, and violated_bound (where
// the clause is a registered maximum) names the bound value behind it.
//
// Every value is a fixed identifier chosen at the mirroring clause. NO
// value is derived from model output, field contents, subject labels, or
// any other corpus content -- the same content-safety rule
// contextfabric.SynthesisRejectionReason follows, for the same reason
// (CodeQL go/log-injection: these values reach a log field).
type ContextFabricRejectedClause string

const (
	// ContextFabricClauseNone is "no clause failed". It is what a VALID
	// value diagnoses to, and it is never a silent empty string: a caller
	// that reports a clause for a struct that actually validates is
	// reporting a measurement that did not happen, and must be able to see
	// that it did not.
	ContextFabricClauseNone ContextFabricRejectedClause = "none"

	// Driver judgment -- ContextFabricDriverJudgment.validate, statement 1
	// (one `||` expression; these are its left-to-right sub-clauses).
	ContextFabricClauseDriverIDLength          ContextFabricRejectedClause = "driver.driver_id_length"
	ContextFabricClauseDriverStanding          ContextFabricRejectedClause = "driver.standing_out_of_vocabulary"
	ContextFabricClauseDriverCategory          ContextFabricRejectedClause = "driver.category_out_of_vocabulary"
	ContextFabricClauseDriverTitle             ContextFabricRejectedClause = "driver.title_length"
	ContextFabricClauseDriverSummary           ContextFabricRejectedClause = "driver.summary_length"
	ContextFabricClauseDriverDerivation        ContextFabricRejectedClause = "driver.derivation_out_of_vocabulary"
	ContextFabricClauseDriverEpistemicStatus   ContextFabricRejectedClause = "driver.epistemic_status_out_of_vocabulary"
	ContextFabricClauseDriverConfidenceRange   ContextFabricRejectedClause = "driver.confidence_out_of_range"
	ContextFabricClauseDriverQualificationSize ContextFabricRejectedClause = "driver.qualification_length"

	// Driver judgment -- statement 2 (subject/path/evidence references).
	ContextFabricClauseDriverAffectedSubjectsBelowMinimum ContextFabricRejectedClause = "driver.affected_subjects_below_minimum"
	ContextFabricClauseDriverAffectedSubjectsAboveMaximum ContextFabricRejectedClause = "driver.affected_subjects_above_maximum"
	ContextFabricClauseDriverAffectedSubjectsShape        ContextFabricRejectedClause = "driver.affected_subjects_shape"
	ContextFabricClauseDriverPathIDsAboveMaximum          ContextFabricRejectedClause = "driver.path_ids_above_maximum"
	ContextFabricClauseDriverPathIDsShape                 ContextFabricRejectedClause = "driver.path_ids_shape"
	ContextFabricClauseDriverEvidenceRefIDsShape          ContextFabricRejectedClause = "driver.evidence_ref_ids_shape"

	// Driver judgment -- statement 3 (claimed-fact references).
	ContextFabricClauseDriverClaimedFactIDsAboveMaximum ContextFabricRejectedClause = "driver.claimed_fact_ids_above_maximum"
	ContextFabricClauseDriverClaimedFactIDsShape        ContextFabricRejectedClause = "driver.claimed_fact_ids_shape"

	// Driver judgment -- statements 4-6 (business rules).
	ContextFabricClauseDriverEvidenceClosureAbsent         ContextFabricRejectedClause = "driver.evidence_closure_absent"
	ContextFabricClauseDriverCategoryRequiresClaimedFact   ContextFabricRejectedClause = "driver.category_requires_claimed_fact"
	ContextFabricClauseDriverWithheldRequiresQualification ContextFabricRejectedClause = "driver.withheld_requires_qualification"

	// Claimed fact -- ContextFabricClaimedFact.Validate, statement 1 (one
	// `||` expression) then one statement per nested document.
	ContextFabricClauseClaimIDLength       ContextFabricRejectedClause = "claimed_fact.claim_id_length"
	ContextFabricClauseClaimKind           ContextFabricRejectedClause = "claimed_fact.kind_out_of_vocabulary"
	ContextFabricClauseClaimFieldLength    ContextFabricRejectedClause = "claimed_fact.field_length"
	ContextFabricClauseClaimFieldUntrimmed ContextFabricRejectedClause = "claimed_fact.field_not_trimmed"
	ContextFabricClauseClaimSubjectShape   ContextFabricRejectedClause = "claimed_fact.subject_shape"
	ContextFabricClauseClaimValueShape     ContextFabricRejectedClause = "claimed_fact.value_shape"
	ContextFabricClauseClaimRowsShape      ContextFabricRejectedClause = "claimed_fact.rows_shape"
	ContextFabricClauseClaimTableShape     ContextFabricRejectedClause = "claimed_fact.table_shape"
	// The TimeSeriesRows/TimeSeriesTable pair is the additive second table.
	// Distinct clauses rather than a reuse of the Rows pair
	// above, for the same reason the rejection-reason vocabulary keeps
	// claim_time_series_rows_model_authored distinct from
	// claim_rows_model_authored: a reader diagnosing a rejection must be
	// able to tell WHICH of the two table surfaces refused.
	ContextFabricClauseClaimTimeSeriesRowsShape  ContextFabricRejectedClause = "claimed_fact.time_series_rows_shape"
	ContextFabricClauseClaimTimeSeriesTableShape ContextFabricRejectedClause = "claimed_fact.time_series_table_shape"
	ContextFabricClauseClaimRowsCombinedShape    ContextFabricRejectedClause = "claimed_fact.rows_combined_shape"

	// Finding -- ContextFabricFinding.validate.
	ContextFabricClauseFindingIDLength                   ContextFabricRejectedClause = "finding.finding_id_length"
	ContextFabricClauseFindingKindLength                 ContextFabricRejectedClause = "finding.kind_length"
	ContextFabricClauseFindingSummaryLength              ContextFabricRejectedClause = "finding.summary_length"
	ContextFabricClauseFindingSubjectsAboveMaximum       ContextFabricRejectedClause = "finding.subjects_above_maximum"
	ContextFabricClauseFindingSubjectsShape              ContextFabricRejectedClause = "finding.subjects_shape"
	ContextFabricClauseFindingEvidenceRefIDsShape        ContextFabricRejectedClause = "finding.evidence_ref_ids_shape"
	ContextFabricClauseFindingClaimedFactIDsAboveMaximum ContextFabricRejectedClause = "finding.claimed_fact_ids_above_maximum"
	ContextFabricClauseFindingClaimedFactIDsShape        ContextFabricRejectedClause = "finding.claimed_fact_ids_shape"
	ContextFabricClauseFindingKindOutOfVocabulary        ContextFabricRejectedClause = "finding.kind_out_of_vocabulary"
	ContextFabricClauseFindingKindRequiresClaimedFact    ContextFabricRejectedClause = "finding.kind_requires_claimed_fact"
)

// canonicalContextFabricRejectedClauses maps each vocabulary member to
// ITSELF, and is the single enumeration behind both
// ValidContextFabricRejectedClause and ContextFabricRejectedClauseOf.
//
// Mapping a member to itself looks redundant and is not: every VALUE here
// is a package constant, so a lookup RETURNS a compile-time constant
// rather than the caller's own input. That is what makes "nothing derived
// from model output ever reaches a log field" a property the compiler and
// CodeQL can both see, instead of one that merely holds because a
// membership check happens to run first. See the identical table and
// reasoning in contextfabric.canonicalSynthesisRejectionReasons.
var canonicalContextFabricRejectedClauses = map[ContextFabricRejectedClause]ContextFabricRejectedClause{
	ContextFabricClauseNone:                                ContextFabricClauseNone,
	ContextFabricClauseDriverIDLength:                      ContextFabricClauseDriverIDLength,
	ContextFabricClauseDriverStanding:                      ContextFabricClauseDriverStanding,
	ContextFabricClauseDriverCategory:                      ContextFabricClauseDriverCategory,
	ContextFabricClauseDriverTitle:                         ContextFabricClauseDriverTitle,
	ContextFabricClauseDriverSummary:                       ContextFabricClauseDriverSummary,
	ContextFabricClauseDriverDerivation:                    ContextFabricClauseDriverDerivation,
	ContextFabricClauseDriverEpistemicStatus:               ContextFabricClauseDriverEpistemicStatus,
	ContextFabricClauseDriverConfidenceRange:               ContextFabricClauseDriverConfidenceRange,
	ContextFabricClauseDriverQualificationSize:             ContextFabricClauseDriverQualificationSize,
	ContextFabricClauseDriverAffectedSubjectsBelowMinimum:  ContextFabricClauseDriverAffectedSubjectsBelowMinimum,
	ContextFabricClauseDriverAffectedSubjectsAboveMaximum:  ContextFabricClauseDriverAffectedSubjectsAboveMaximum,
	ContextFabricClauseDriverAffectedSubjectsShape:         ContextFabricClauseDriverAffectedSubjectsShape,
	ContextFabricClauseDriverPathIDsAboveMaximum:           ContextFabricClauseDriverPathIDsAboveMaximum,
	ContextFabricClauseDriverPathIDsShape:                  ContextFabricClauseDriverPathIDsShape,
	ContextFabricClauseDriverEvidenceRefIDsShape:           ContextFabricClauseDriverEvidenceRefIDsShape,
	ContextFabricClauseDriverClaimedFactIDsAboveMaximum:    ContextFabricClauseDriverClaimedFactIDsAboveMaximum,
	ContextFabricClauseDriverClaimedFactIDsShape:           ContextFabricClauseDriverClaimedFactIDsShape,
	ContextFabricClauseDriverEvidenceClosureAbsent:         ContextFabricClauseDriverEvidenceClosureAbsent,
	ContextFabricClauseDriverCategoryRequiresClaimedFact:   ContextFabricClauseDriverCategoryRequiresClaimedFact,
	ContextFabricClauseDriverWithheldRequiresQualification: ContextFabricClauseDriverWithheldRequiresQualification,
	ContextFabricClauseClaimIDLength:                       ContextFabricClauseClaimIDLength,
	ContextFabricClauseClaimKind:                           ContextFabricClauseClaimKind,
	ContextFabricClauseClaimFieldLength:                    ContextFabricClauseClaimFieldLength,
	ContextFabricClauseClaimFieldUntrimmed:                 ContextFabricClauseClaimFieldUntrimmed,
	ContextFabricClauseClaimSubjectShape:                   ContextFabricClauseClaimSubjectShape,
	ContextFabricClauseClaimValueShape:                     ContextFabricClauseClaimValueShape,
	ContextFabricClauseClaimRowsShape:                      ContextFabricClauseClaimRowsShape,
	ContextFabricClauseClaimTableShape:                     ContextFabricClauseClaimTableShape,
	ContextFabricClauseClaimTimeSeriesRowsShape:            ContextFabricClauseClaimTimeSeriesRowsShape,
	ContextFabricClauseClaimTimeSeriesTableShape:           ContextFabricClauseClaimTimeSeriesTableShape,
	ContextFabricClauseClaimRowsCombinedShape:              ContextFabricClauseClaimRowsCombinedShape,
	ContextFabricClauseFindingIDLength:                     ContextFabricClauseFindingIDLength,
	ContextFabricClauseFindingKindLength:                   ContextFabricClauseFindingKindLength,
	ContextFabricClauseFindingSummaryLength:                ContextFabricClauseFindingSummaryLength,
	ContextFabricClauseFindingSubjectsAboveMaximum:         ContextFabricClauseFindingSubjectsAboveMaximum,
	ContextFabricClauseFindingSubjectsShape:                ContextFabricClauseFindingSubjectsShape,
	ContextFabricClauseFindingEvidenceRefIDsShape:          ContextFabricClauseFindingEvidenceRefIDsShape,
	ContextFabricClauseFindingClaimedFactIDsAboveMaximum:   ContextFabricClauseFindingClaimedFactIDsAboveMaximum,
	ContextFabricClauseFindingClaimedFactIDsShape:          ContextFabricClauseFindingClaimedFactIDsShape,
	ContextFabricClauseFindingKindOutOfVocabulary:          ContextFabricClauseFindingKindOutOfVocabulary,
	ContextFabricClauseFindingKindRequiresClaimedFact:      ContextFabricClauseFindingKindRequiresClaimedFact,
}

// ValidContextFabricRejectedClause reports whether clause is a member of
// the closed vocabulary.
func ValidContextFabricRejectedClause(clause ContextFabricRejectedClause) bool {
	_, ok := canonicalContextFabricRejectedClauses[clause]
	return ok
}

// ContextFabricRejectedClauseOf returns the PACKAGE CONSTANT equal to
// clause, or ContextFabricClauseNone when clause is not a vocabulary
// member. Callers that log the result log a compile-time constant, never
// their own input -- see canonicalContextFabricRejectedClauses.
func ContextFabricRejectedClauseOf(clause ContextFabricRejectedClause) ContextFabricRejectedClause {
	if canonical, ok := canonicalContextFabricRejectedClauses[clause]; ok {
		return canonical
	}
	return ContextFabricClauseNone
}

// ContextFabricRejectedClauses returns every vocabulary member, sorted, so
// a test can enumerate the vocabulary from its single producer instead of
// carrying a second hand-maintained list of the same names.
func ContextFabricRejectedClauses() []ContextFabricRejectedClause {
	clauses := make([]ContextFabricRejectedClause, 0, len(canonicalContextFabricRejectedClauses))
	for clause := range canonicalContextFabricRejectedClauses {
		clauses = append(clauses, clause)
	}
	sort.Slice(clauses, func(i, j int) bool { return clauses[i] < clauses[j] })
	return clauses
}
