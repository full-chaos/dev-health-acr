package v1

// THE TERMINAL-FORM CONTENT PREDICATE.
//
// A result is SUPPORTED when it carries something that came from a read and
// can be traced: at least one claimed fact AND at least one citable evidence
// ref. DeterministicAnswer -- the service's own answer sentence -- is
// required on every supported result. On a result that is NOT supported it
// may be empty: there is nothing read behind an answer sentence there, and the
// document's disclosure (Limitations, Warnings, Coverage, the completeness
// block, a refusal basis, a clarification prompt) is what it carries instead.
//
// Every input is a STRUCTURED field the producer already ships. Nothing here
// reads prose to decide whether prose is allowed.
//
// WHY EACH CONJUNCT:
//
//   - The claimed-fact count is read from the completeness block
//     (ClaimedFactsCount), the producer's own count, not re-counted from the
//     ClaimedFacts array: one authority per fact. The two can never disagree
//     on a valid document -- validateCompleteness refuses one that does --
//     so re-counting would only add a second authority to keep in step.
//   - The evidence conjunct stops a claimed fact that nothing can trace from
//     licensing an answer: a claim a consumer cannot follow to its evidence is
//     not support. ClaimedFacts and EvidenceRefIDs are independent fields (a
//     claimed fact carries no evidence refs of its own), so both are needed.
//
// WHAT IS NOT AN INPUT, and why:
//
//   - Status. A degraded or clarification_required result that read facts is
//     supported and keeps its answer; degradation is orthogonal to having
//     something to say. Reading Status instead would turn `degraded` into a
//     content-free form by fiat.
//   - Limitations and Warnings. They are service-authored disclosure and are
//     allowed at every status; they neither license nor suppress an answer.
//   - RefusalBasis. A refused result is never supported, but not because this
//     predicate reads the basis: validateCompleteness already refuses a basis
//     alongside any claimed fact, so a refused result has a zero count.
//
// THE PUBLISHED SCHEMAS STATE THE SAME RULE OVER THE DATA THEY CAN SEE.
// A JSON Schema cannot compare a number to an array's length, so the schemas
// key the rule off `claimed_facts` and `evidence_ref_ids` -- both present in
// the document -- rather than off the count. validateCompleteness already
// refuses any document whose count and array disagree, so on every document
// the contract admits the two readings are the same number and the verdicts
// coincide; a disagreeing document is refused here for the census rule, which
// the schema cannot express at all (both directions executed in the domain
// table). Go keeps reading the COUNT so this file has one authority for it.
//
// This file only WIDENS the contract: a result that validated before still
// validates. Complete and partial results keep requiring the answer sentence
// (they already require a non-blank DirectJudgment, so a content-free one is
// invalid anyway), and a stored row written before the completeness block
// existed keeps the rule it was written under.
func ContextFabricResultSupported(r ContextFabricInvestigationResult) bool {
	return r.Completeness.ClaimedFactsCount > 0 && len(r.EvidenceRefIDs) > 0
}

// contextFabricDeterministicAnswerMayBeEmpty reports whether this result may
// carry an empty DeterministicAnswer. It is the ONLY place the answer
// sentence's lower bound is decided; validateAgainstSchemaVersion reads it,
// and the published JSON Schemas state the same rule as a conditional.
func contextFabricDeterministicAnswerMayBeEmpty(r ContextFabricInvestigationResult) bool {
	// No completeness block: a legacy stored row (the write path requires the
	// block). It was written under "always required" and keeps that rule.
	if r.Completeness.IsZero() {
		return false
	}
	switch r.Status {
	case ContextFabricInvestigationComplete, ContextFabricInvestigationPartial:
		return false
	}
	return !ContextFabricResultSupported(r)
}

// contextFabricDeterministicAnswerMinLength is the lower bound
// validateAgainstSchemaVersion applies to DeterministicAnswer.
func contextFabricDeterministicAnswerMinLength(r ContextFabricInvestigationResult) int {
	if contextFabricDeterministicAnswerMayBeEmpty(r) {
		return 0
	}
	return 1
}
