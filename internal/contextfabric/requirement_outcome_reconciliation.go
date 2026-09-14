package contextfabric

import (
	"context"
	"errors"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Requirement-outcome reconciliation (CHAOS-5737): what the derivation
// PREDICTED for each requirement, set beside what assembly SERVED for it.
//
// THE GAP THIS CLOSES. The derivation calls a requirement served when a
// producer or a server step is declared for it. That is a prediction, made
// before retrieval. Assembly can still end the same requirement narrowed or
// unavailable: a member set that resolved nothing, a read that returned
// nothing, a budget cut. The served document carries both halves -- the
// planning seed and the assembled_result row -- but nothing on the trace said
// that the two disagree, so a requirement predicted served that ended
// unavailable was visible only by reading the stored document. And the wire
// cause `fact_pruned` is one code for two reasons (subject_kind_unsupported
// at derivation, computed_population_absent at assembly), so even the stored
// document could not say which one happened.
//
// WHAT THIS FILE DOES, over the FINAL served document only:
//
//  1. ReconcileRequirementOutcomes pairs every published plan requirement with
//     its assembled_result account and returns each pair whose outcome differs
//     from the prediction -- an OBSERVED transition with its cause, never a
//     silent flip.
//  2. requirementAssemblyReason names the reason below a collapsed wire cause.
//     The wire code is not changed here.
//  3. assertSatisfiedRequirementsAreServed refuses a document whose
//     assembled_result row says `satisfied` for a requirement that no served
//     evidence of its own kind and subject backs.
//
// WHAT IT DOES NOT DO. It never writes or rewrites an outcome row: the rows
// stay APPEND-only, and completeness is still derived from them by the one
// derivation that owns it. It does not decide what a requirement IS -- the
// published plan array is the input, for the reason
// SeedOutcomesFromPublishedPlanRequirements gives. And it does not touch a
// requirement that has no assembled_result row: that requirement was not
// evaluated at assembly, and whether it should have been is a question for the
// stage that owns it, not a transition this file can observe.

// RequirementPrediction is what the derivation predicted for one requirement.
// CLOSED, two members.
type RequirementPrediction string

const (
	// RequirementPredictedServed: the derivation named a server for the
	// requirement, so the planning seed reads `satisfied`.
	RequirementPredictedServed RequirementPrediction = "served"
	// RequirementPredictedUnavailable: the derivation named a closed reason
	// no server exists, so the planning seed reads `unavailable`.
	RequirementPredictedUnavailable RequirementPrediction = "unavailable"
)

// RequirementPredictionVocabulary returns the closed vocabulary in declared
// order.
func RequirementPredictionVocabulary() []RequirementPrediction {
	return []RequirementPrediction{RequirementPredictedServed, RequirementPredictedUnavailable}
}

// RequirementAssemblyReason names the reason assembly OBSERVED below a
// collapsed wire cause. CLOSED.
//
// Its members reuse the derivation's own reason tokens rather than minting new
// ones: a requirement whose member set did not resolve at assembly is
// unavailable for exactly the reason the derivation calls
// computed_population_absent, and a second token for the same fact would be a
// second authority for it.
//
// ONLY THE REASONS AN ASSEMBLY WRITER CAN PRODUCE ARE MEMBERS. `fact_pruned`
// reaches an assembled_result row from one writer only --
// unresolvedMemberSetOutcomeRow -- because a read requirement skips a pruned
// observation (evaluateReadRequirement) and a never-degrading code is never
// carried as a cause. The other reasons that map to a collapsed wire code
// (subject_kind_unsupported, no_declaring_producer, table_shape_undeclared)
// are decided at derivation time and travel on the plan requirement, where
// the transition reports them as PredictedReason.
type RequirementAssemblyReason string

const (
	// RequirementAssemblyReasonNone: the assembled row's own wire cause
	// already names its mechanism, so nothing below it needs splitting.
	RequirementAssemblyReasonNone RequirementAssemblyReason = "none"
	// RequirementAssemblyReasonComputedPopulationAbsent: a computed step that
	// runs over the resolved member set found no member set on the served
	// document. The wire code stays `fact_pruned`.
	RequirementAssemblyReasonComputedPopulationAbsent = RequirementAssemblyReason(RequirementReasonComputedPopulationAbsent)
)

// RequirementAssemblyReasonVocabulary returns the closed vocabulary in
// declared order.
func RequirementAssemblyReasonVocabulary() []RequirementAssemblyReason {
	return []RequirementAssemblyReason{RequirementAssemblyReasonNone, RequirementAssemblyReasonComputedPopulationAbsent}
}

// RequirementOutcomeTransition is ONE requirement whose assembled outcome
// differs from what the derivation predicted for it.
//
// Every field is a closed token or a count. Requirement, Obligation, Role and
// Subject are the requirement's own coordinate, copied from the published plan
// rather than re-derived.
type RequirementOutcomeTransition struct {
	Requirement string
	Obligation  string
	Role        string
	Subject     SubjectKind

	// Predicted is the derivation's prediction, and PredictedReason its closed
	// reason token when the prediction is unavailable (empty when served).
	// PredictedReason is what splits `fact_pruned` and `fact_unconfigured` at
	// derivation time.
	Predicted       RequirementPrediction
	PredictedReason RequirementUnavailableReason

	// AssembledOutcome is the outcome of the requirement's assembled_result
	// account, and the three Cause fields are that row's own wire causes.
	AssembledOutcome RequirementOutcome
	CauseCoverage    contractsv1.ContextFabricCoverageDetailCode
	CauseOverrun     contractsv1.ContextFabricBudgetOverrun
	CauseNarrowing   contractsv1.ContextFabricNarrowingBasis
	// AssemblyReason is the reason assembly observed below a collapsed wire
	// cause -- the split.
	AssemblyReason RequirementAssemblyReason

	// Served and Declared are the assembled row's own numbers.
	Served   int
	Declared int
	// ServedFactCount is how many claimed facts on the served document are of
	// this requirement's kind and keyed to its subject kind -- see
	// claimServesRequirement for what matches.
	ServedFactCount int
	// MemberSetResolved is whether the served document carries a member set,
	// the input every member-set step needs.
	MemberSetResolved bool
}

// RequirementOutcomeTransitionEvent is one transition as it reaches the trace:
// the transition plus its position among every transition of this request.
//
// Index is 1-based and Total is the request's transition count, the same on
// every line, so a reader can see that no line of the set is missing.
type RequirementOutcomeTransitionEvent struct {
	RequirementOutcomeTransition
	Index int
	Total int
}

// RequirementOutcomeTransitionLogMessage is the msg the transition line is
// emitted under.
const RequirementOutcomeTransitionLogMessage = "context fabric requirement outcome transition"

// ErrSatisfiedRequirementUnserved is the pinned failure for a served document
// whose assembled_result row says `satisfied` for a requirement that no
// served evidence of its kind and subject backs.
//
// A FAILURE, NOT A DOWNGRADE. Rewriting the row to something weaker would
// break the APPEND invariant and would hide the writer that claimed too much;
// a served document that states a satisfied requirement with nothing behind it
// is a server defect, and the caller receives it as one.
var ErrSatisfiedRequirementUnserved = errors.New("context fabric requirement is satisfied with no served evidence of its kind and subject")

// ReconcileRequirementOutcomes returns every published plan requirement whose
// assembled_result account differs from the derivation's prediction, in plan
// order.
//
// PURE: reads the document, mutates nothing.
//
// A requirement with no assembled_result row is skipped: nothing at assembly
// evaluated it, so there is no assembled outcome to set beside the prediction.
// A document with no plan returns nil.
func ReconcileRequirementOutcomes(result InvestigationResult) []RequirementOutcomeTransition {
	if result.AnswerPlan == nil || len(result.AnswerPlan.Requirements) == 0 {
		return nil
	}
	var transitions []RequirementOutcomeTransition
	for _, requirement := range result.AnswerPlan.Requirements {
		row, found := assembledRequirementAccount(result.Completeness.Outcomes, requirement.Requirement)
		if !found {
			continue
		}
		prediction, predictedOutcome := requirementPrediction(requirement)
		if row.Outcome == predictedOutcome {
			continue
		}
		transitions = append(transitions, RequirementOutcomeTransition{
			Requirement:       requirement.Requirement,
			Obligation:        requirement.Obligation,
			Role:              requirement.Role,
			Subject:           requirement.Subject,
			Predicted:         prediction,
			PredictedReason:   RequirementUnavailableReason(requirement.Unavailable),
			AssembledOutcome:  row.Outcome,
			CauseCoverage:     row.CauseCoverage,
			CauseOverrun:      row.CauseOverrun,
			CauseNarrowing:    row.CauseNarrowing,
			AssemblyReason:    requirementAssemblyReason(row, result),
			Served:            row.Served,
			Declared:          row.Declared,
			ServedFactCount:   servedRequirementFactCount(result, requirement),
			MemberSetResolved: memberSetResolved(result.Cohort),
		})
	}
	return transitions
}

// requirementPrediction maps a published plan requirement onto the prediction
// and onto the outcome a planning seed carries for that prediction.
//
// The outcome half is the SAME mapping planningStageOutcomeRow applies, so the
// comparison is against what the seed says rather than a second opinion.
func requirementPrediction(requirement contractsv1.ContextFabricPlanRequirement) (RequirementPrediction, RequirementOutcome) {
	if requirement.Served() {
		return RequirementPredictedServed, contractsv1.ContextFabricRequirementSatisfied
	}
	return RequirementPredictedUnavailable, contractsv1.ContextFabricRequirementUnavailable
}

// assembledRequirementAccount returns the assembled_result row that states
// what became of one requirement.
//
// More than one assembled_result row can carry the same identity: the read
// evaluator appends its row, and a later candidate reduction can append a
// narrowing for the same `state` requirement. The account is the row that
// loses the most, ranked the way the completeness derivation ranks outcomes
// (unavailable is absorbing, narrowed and not_attempted lower a set to partial,
// satisfied and not_applicable lose nothing); the first such row wins a tie, so
// the choice does not depend on anything but row order.
func assembledRequirementAccount(rows []RequirementOutcomeRow, identity string) (RequirementOutcomeRow, bool) {
	var account RequirementOutcomeRow
	found := false
	for _, row := range rows {
		if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult || row.Requirement != identity {
			continue
		}
		if !found || outcomeLossRank(row.Outcome) > outcomeLossRank(account.Outcome) {
			account = row
			found = true
		}
	}
	return account, found
}

// outcomeLossRank orders the outcome vocabulary by what the reader loses,
// matching DeriveContextFabricAnswerCompletenessState's own classes. TOTAL over
// the vocabulary; an unknown token ranks above everything, so it is never read
// as lossless.
func outcomeLossRank(outcome RequirementOutcome) int {
	switch outcome {
	case contractsv1.ContextFabricRequirementSatisfied, contractsv1.ContextFabricRequirementNotApplicable:
		return 0
	case contractsv1.ContextFabricRequirementNarrowed, contractsv1.ContextFabricRequirementNotAttempted:
		return 1
	case contractsv1.ContextFabricRequirementUnavailable:
		return 2
	}
	return 3
}

// requirementAssemblyReason names the reason assembly observed below the
// row's wire cause.
//
// It asks the SAME three questions the one writer of an assembled
// `fact_pruned` row asks before it writes one -- the outcome and code that
// writer sets, whether the obligation's step runs over the resolved member
// set, and whether the served document resolved a member set -- through the
// same predicates, so the reason and the row cannot come to describe different
// conditions.
func requirementAssemblyReason(row RequirementOutcomeRow, result InvestigationResult) RequirementAssemblyReason {
	if row.Outcome == contractsv1.ContextFabricRequirementUnavailable &&
		row.CauseCoverage == unavailableRequirementCause(RequirementReasonComputedPopulationAbsent) &&
		stepRunsOverResolvedMemberSet(AnswerObligation(row.Obligation)) &&
		!memberSetResolved(result.Cohort) {
		return RequirementAssemblyReasonComputedPopulationAbsent
	}
	return RequirementAssemblyReasonNone
}

// servedRequirementFactCount counts the claimed facts on the served document
// that serve one requirement.
func servedRequirementFactCount(result InvestigationResult, requirement contractsv1.ContextFabricPlanRequirement) int {
	count := 0
	for _, claim := range result.ClaimedFacts {
		if claimServesRequirement(claim, requirement) {
			count++
		}
	}
	return count
}

// claimServesRequirement reports whether one claimed fact is of a
// requirement's kind and keyed to its subject.
//
//   - `count`: the server-minted cardinality claim for the counted kind. That
//     claim's subject is the organization by design (cardinalityClaim), so the
//     kind being counted is matched on the claim's field, which names it.
//   - a READ requirement: a claim of one of its declared fact kinds about a
//     subject of its subject kind.
//   - any other COMPUTED requirement: a claim of one of the step's declared
//     input kinds about a subject of its subject kind.
//
// A claim about a DIFFERENT subject kind never matches, whatever its fact
// kind: a membership fact about a repository does not serve a count of teams.
func claimServesRequirement(claim ClaimedFact, requirement contractsv1.ContextFabricPlanRequirement) bool {
	if AnswerObligation(requirement.Obligation) == ObligationCount {
		return claim.Kind == contractsv1.ContextFabricFactCardinality &&
			claim.Field == cardinalityClaimField(requirement.Subject)
	}
	if claim.Subject.Kind != requirement.Subject {
		return false
	}
	if requirement.Kind == string(ObligationKindRead) {
		return factKindListed(claim.Kind, requirement.FactKinds)
	}
	return factKindListed(claim.Kind, requirement.InputFactKinds)
}

func factKindListed(kind FactKind, kinds []FactKind) bool {
	for _, listed := range kinds {
		if listed == kind {
			return true
		}
	}
	return false
}

// requirementEvidenceServed reports whether the served document carries
// evidence of a requirement's kind and subject.
//
// A matching claimed fact is evidence for any requirement. Two more records on
// the served document are evidence too, because each is what its writer's
// `satisfied` row is computed from and neither is guaranteed a claim:
//
//   - a member-set step: a resolved member set OF THE REQUIREMENT'S SUBJECT
//     KIND. The cardinality claim is dropped, never overflowed, at the claim
//     cap, while the row still states the count.
//   - a READ requirement: a canonical-fact observation of one of its declared
//     kinds that served (available or stale) -- the same observation the read
//     evaluator counted.
func requirementEvidenceServed(result InvestigationResult, requirement contractsv1.ContextFabricPlanRequirement) bool {
	if servedRequirementFactCount(result, requirement) > 0 {
		return true
	}
	if requirement.Kind == string(ObligationKindRead) {
		for _, observation := range result.Coverage.Sources {
			kind, ok := canonicalFactKindOf(observation.Source)
			if !ok || !factKindListed(kind, requirement.FactKinds) {
				continue
			}
			if observation.State == SourceAvailable || observation.State == SourceStale {
				return true
			}
		}
		return false
	}
	if stepRunsOverResolvedMemberSet(AnswerObligation(requirement.Obligation)) {
		return memberSetResolved(result.Cohort) && result.Cohort.Kind == requirement.Subject
	}
	return false
}

// AssertServedRequirementEvidence is the invariant: no assembled_result row
// says `satisfied` for a published requirement without served evidence of that
// requirement's kind and subject.
//
// EXPORTED, and there is exactly one of it. Every surface that SERVES a
// document owes this check -- the engine's own exits and the stored-read route
// that never reaches them -- and a second copy at the second surface is how the
// two come to disagree about what "served evidence" means.
//
// ASSEMBLED ROWS ONLY. A planning seed's `satisfied` is the derivation's
// prediction, not a claim about the served document -- it is what the
// transition above is compared against -- and treating it as a claim would
// refuse every answer whose prediction assembly has not yet evaluated.
//
// A row whose identity the plan does not publish is left to the join that
// owns plan/outcome agreement: without the requirement its kind and subject
// are unknown here, and guessing them is how a check starts to lie.
func AssertServedRequirementEvidence(result InvestigationResult) error {
	if result.AnswerPlan == nil {
		return nil
	}
	for _, requirement := range result.AnswerPlan.Requirements {
		for _, row := range result.Completeness.Outcomes {
			if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult ||
				row.Requirement != requirement.Requirement ||
				row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
				continue
			}
			if !requirementEvidenceServed(result, requirement) {
				return fmt.Errorf("%w: %s", ErrSatisfiedRequirementUnserved, requirement.Requirement)
			}
		}
	}
	return nil
}

// RequirementOutcomeTransitionEvents is what a served document's transitions
// look like on the trace: one event per transition, in plan order, each
// carrying its 1-based index and the request's own total.
//
// EXPORTED AND SHARED, for the reason AssertServedRequirementEvidence is: the
// engine and the stored-read route serve the same documents and must state the
// same lines about them. The numbering lives here so neither surface can
// number its own.
func RequirementOutcomeTransitionEvents(result InvestigationResult) []RequirementOutcomeTransitionEvent {
	transitions := ReconcileRequirementOutcomes(result)
	if len(transitions) == 0 {
		return nil
	}
	events := make([]RequirementOutcomeTransitionEvent, 0, len(transitions))
	for index, transition := range transitions {
		events = append(events, RequirementOutcomeTransitionEvent{
			RequirementOutcomeTransition: transition,
			Index:                        index + 1,
			Total:                        len(transitions),
		})
	}
	return events
}

// RequirementOutcomeTransitionLogArgs is the ONE construction of the transition
// line's fields, sanitized at this site.
//
// The engine's slog sink and the stored-read route both log through it, so a
// field added or renamed moves on both surfaces at once -- and the certified
// declaration describes one line rather than two that happen to agree today.
// The caller appends its own request-id attribute.
func RequirementOutcomeTransitionLogArgs(event RequirementOutcomeTransitionEvent, orgID string) []any {
	return []any{
		"org_id", SanitizeLogAttr(orgID),
		"requirement", SanitizeLogAttr(event.Requirement),
		"obligation", SanitizeLogAttr(event.Obligation),
		"role", SanitizeLogAttr(event.Role),
		"subject_kind", SanitizeLogAttr(string(event.Subject)),
		"predicted", SanitizeLogAttr(string(event.Predicted)),
		"predicted_reason", SanitizeLogAttr(transitionLineTokenOrNone(string(event.PredictedReason))),
		"assembled_outcome", SanitizeLogAttr(string(event.AssembledOutcome)),
		// The SPLIT: the reason assembly observed below the wire cause. The wire
		// code itself rides beside it, unchanged, as cause_coverage.
		"cause", SanitizeLogAttr(string(event.AssemblyReason)),
		"cause_coverage", SanitizeLogAttr(transitionLineTokenOrNone(string(event.CauseCoverage))),
		"cause_overrun", SanitizeLogAttr(transitionLineTokenOrNone(string(event.CauseOverrun))),
		"cause_narrowing", SanitizeLogAttr(transitionLineTokenOrNone(string(event.CauseNarrowing))),
		"served", SanitizeLogInt(int64(event.Served)),
		"declared", SanitizeLogInt(int64(event.Declared)),
		"served_fact_count", SanitizeLogInt(int64(event.ServedFactCount)),
		"member_set_resolved", event.MemberSetResolved,
		"index", SanitizeLogInt(int64(event.Index)),
		"total", SanitizeLogInt(int64(event.Total)),
	}
}

// recordRequirementOutcomeTransitions emits the served document's transitions
// through the engine's own telemetry.
func (e *Engine) recordRequirementOutcomeTransitions(ctx context.Context, principal storage.Principal, result InvestigationResult) {
	if e.telemetry == nil {
		return
	}
	for _, event := range RequirementOutcomeTransitionEvents(result) {
		e.telemetry.RecordRequirementOutcomeTransition(ctx, principal, event)
	}
}

// RequirementOutcomeTransitionLineVocabulary returns the closed vocabulary of
// one closed field on the transition line, for the event specification. Every
// list is DERIVED from the vocabulary that owns it, never retyped. An unknown
// key returns nil, which the specification reads as an open field.
func RequirementOutcomeTransitionLineVocabulary(key string) []string {
	switch key {
	case "predicted":
		return tokenStrings(RequirementPredictionVocabulary())
	case "predicted_reason":
		reasons := RequirementUnavailableReasonVocabulary()
		return append([]string{transitionLineNone}, tokenStrings(reasons[:])...)
	case "assembled_outcome":
		outcomes := contractsv1.ContextFabricPlanRequirementOutcomeVocabulary()
		return tokenStrings(outcomes[:])
	case "cause":
		return tokenStrings(RequirementAssemblyReasonVocabulary())
	}
	return nil
}

// transitionLineNone is the explicit absence token on the transition line: a
// field with nothing to report writes it rather than an empty string, so
// "nothing applied" cannot be confused with "the field was never set".
const transitionLineNone = "none"

// transitionLineTokenOrNone renders an optional closed token for the
// transition line.
func transitionLineTokenOrNone(value string) string {
	if value == "" {
		return transitionLineNone
	}
	return value
}
