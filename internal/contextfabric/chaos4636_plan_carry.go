package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4636: carrying the PLAN across turns (extends CHAOS-4387).
//
// WHAT IS CARRIED, and the one thing that is emphatically not. The family,
// the group kind and the declared narrowing basis carry. The MEMBER LIST does
// not, ever. Carrying members would carry an AUTHORIZATION DECISION, and
// North Star check 18 forbids that outright: authorization is re-checked live
// every turn and is never conversation memory. Membership re-resolves from
// the graph on turn 2 exactly as it did on turn 1.
//
// WHAT MEMBERSHIP COMPARABILITY ACTUALLY RESTS ON, stated as the dependency
// it is rather than claimed as a property this slice provides. Turn 2
// re-discovers the cohort, and the cap fires against whatever order discovery
// produces. Before CHAOS-4630 that order was Go map iteration -- so two turns
// could retain DIFFERENT members of the same graph, and no amount of carrying
// would have fixed it. CHAOS-4630 (merged as this branch's own parent,
// f9d9688c) sorts hopWalk's materialized nodes on a total key, so the order is
// now stable and the same prefix is selected on both turns given the same
// graph. That is what makes a narrowed cohort comparable across turns, and it
// is a property of 4630, not of this file.
//
// The guarantee is still bounded, and saying so is the point: it holds for
// the same graph epoch. A rebuild between turns can legitimately change
// membership, which is why the taint gate below refuses a carrier from a
// different epoch instead of carrying across one.
//
// WHY ONE HOP, not the five-deep chain walk the window carry does. A window
// is a COMMITMENT -- confirmed once and legitimately inherited through many
// later turns, which is why resolveCarriedWindow walks a chain looking for
// the nearest confirmation. A family is a READING of the question just asked.
// The turn it continues is the previous one; a family from four turns back is
// not evidence about this question, and treating it as such would make a
// stale reading progressively harder to escape. The bound is declared here
// rather than discovered later.

// PlanCarryOutcome is the closed, content-safe vocabulary
// RecordPlanCarry-shaped telemetry reports. Never free text -- the same
// discipline WindowCarryOutcome holds.
type PlanCarryOutcome string

const (
	// PlanCarryNotAttempted is the zero value. Carry is attempted only when
	// this turn resolved no family of its own, following the "once per
	// non-zero signal" convention every other counted decision here uses.
	PlanCarryNotAttempted PlanCarryOutcome = ""
	// PlanCarryHit: a prior turn's family was found and is now this turn's.
	PlanCarryHit PlanCarryOutcome = "hit"
	// PlanCarryMissNoReference: this request named no prior result at all.
	PlanCarryMissNoReference PlanCarryOutcome = "miss_no_reference"
	// PlanCarryMissUnloadable: every referenced result failed to load.
	PlanCarryMissUnloadable PlanCarryOutcome = "miss_unloadable"
	// PlanCarryMissStaleGraphEpoch: a carrier loaded but failed the
	// CHAOS-3898 ingress taint gate -- its graph epoch is absent or differs
	// from this investigation's binding. The SAME fail-closed check
	// resolvePriorSubjectHints and resolveCarriedWindow both apply, reused
	// rather than re-implemented.
	PlanCarryMissStaleGraphEpoch PlanCarryOutcome = "miss_stale_graph_epoch"
	// PlanCarryMissNoPlan: every reachable, taint-gate-passing prior result
	// carried no plan, or carried one whose family was itself unclassified.
	// Carrying "we could not classify it" forward would spread a
	// non-classification rather than a classification.
	PlanCarryMissNoPlan PlanCarryOutcome = "miss_no_plan"
	// PlanCarryMissConflictingPlans: two directly-referenced prior results
	// carried genuinely DIFFERENT families. The receipt fields validate
	// independently of one another, so one request can legitimately name two
	// prior results; picking whichever loaded first would silently answer
	// under an arbitrary one of two real, disagreeing readings. A genuine
	// conflict fails closed, exactly like every other carry ambiguity.
	PlanCarryMissConflictingPlans PlanCarryOutcome = "miss_conflicting_plans"
)

// planCarryResult is resolveCarriedPlan's return shape.
type planCarryResult struct {
	// Family and GroupKind are the carried reading. Both empty unless
	// Outcome == PlanCarryHit.
	Family    QuestionFamily
	GroupKind SubjectKind
	// NarrowingBasis is the order the earlier turn declared it would narrow
	// on, carried so two turns of one conversation narrow the same way. A
	// follow-up that silently changed basis would make the two answers
	// incomparable while looking like a refinement.
	NarrowingBasis contractsv1.ContextFabricNarrowingBasis
	SourceResultID string
	Outcome        PlanCarryOutcome
}

// resolveCarriedPlan looks one hop back for a family to continue.
// preloaded is every prior result the subject-hint resolution ALREADY fetched
// and taint-gated this turn. Reading a plan out of one costs nothing; only an
// id that is not in it is fetched. That matters because the common follow-up
// turn names exactly the result whose plan it wants to continue, so the carry
// should not double the store reads on the hot path.
func (e *Engine) resolveCarriedPlan(ctx context.Context, principal storage.Principal, request InvestigationRequest, validatedSubjectReceipts []BoundSubjectReceipt, binding ResolvedGraphBinding, preloaded map[string]InvestigationResult) planCarryResult {
	if e.results == nil {
		return planCarryResult{Outcome: PlanCarryMissNoReference}
	}
	// The SAME reference set the window carry seeds its walk from --
	// validated subject receipts, never the raw request field, which is the
	// codex R1 P1 fix carryReferencedResultIDs' own doc comment records: an
	// unmatched receipt must not be able to seed a carry.
	referenced := carryReferencedResultIDs(request, validatedSubjectReceipts)
	if len(referenced) == 0 {
		return planCarryResult{Outcome: PlanCarryMissNoReference}
	}
	visited := make(map[string]struct{}, len(referenced))
	var sawUnloadable, sawStaleEpoch, sawResult bool
	var hits []planCarryResult
	for _, resultID := range referenced {
		if ctx.Err() != nil {
			return planCarryResult{Outcome: PlanCarryMissUnloadable}
		}
		if _, seen := visited[resultID]; seen {
			continue
		}
		visited[resultID] = struct{}{}
		prior, cached := preloaded[resultID]
		if !cached {
			fetched, err := e.results.Get(ctx, principal, resultID)
			if err != nil {
				sawUnloadable = true
				continue
			}
			// Same CHAOS-3898 §2.2 ingress taint gate every other carrier
			// check applies. A preloaded entry has already passed it -- see
			// resolvePriorSubjectHints, which only ever stores a result in
			// that map after the gate -- so it is not re-checked here, and
			// it is not skipped here either.
			if fetched.GraphEpoch == nil || *fetched.GraphEpoch != binding.Epoch {
				sawStaleEpoch = true
				continue
			}
			prior = fetched.Result
		}
		sawResult = true
		carried := carriablePlan(prior)
		if carried == nil {
			continue
		}
		hits = append(hits, planCarryResult{
			Family:         carried.Family,
			GroupKind:      carried.GroupKind,
			NarrowingBasis: carried.Budget.NarrowingBasis,
			SourceResultID: prior.ResultID,
			Outcome:        PlanCarryHit,
		})
	}
	switch {
	case len(hits) == 1:
		return hits[0]
	case len(hits) > 1:
		// Agreement on the FAMILY is what matters; two carriers that read
		// the question the same way are not a conflict even if one of them
		// also named a group kind. Disagreement on the family is.
		first := hits[0]
		for _, hit := range hits[1:] {
			if hit.Family != first.Family || hit.GroupKind != first.GroupKind {
				return planCarryResult{Outcome: PlanCarryMissConflictingPlans}
			}
		}
		return first
	case sawResult:
		return planCarryResult{Outcome: PlanCarryMissNoPlan}
	case sawStaleEpoch:
		return planCarryResult{Outcome: PlanCarryMissStaleGraphEpoch}
	case sawUnloadable:
		return planCarryResult{Outcome: PlanCarryMissUnloadable}
	}
	return planCarryResult{Outcome: PlanCarryMissNoReference}
}

// carriablePlan reports whether a prior result's plan is worth carrying.
//
// An `unclassified` family is deliberately NOT carriable. It is the
// refuse-to-guess member and today's behaviour unchanged; propagating it
// forward would spread a non-classification through a conversation while
// looking like a decision, and the next turn is entitled to its own attempt.
func carriablePlan(prior InvestigationResult) *contractsv1.ContextFabricAnswerPlan {
	if prior.AnswerPlan == nil {
		return nil
	}
	if prior.AnswerPlan.Family == "" || prior.AnswerPlan.Family == QuestionFamilyUnclassified {
		return nil
	}
	return prior.AnswerPlan
}

// applyCarriedPlan overlays a carried reading onto this turn's family
// outcome, and returns whether it did.
//
// It applies ONLY when this turn classified nothing of its own. A carried
// family never overrides a family the model resolved for THIS question: the
// caller may genuinely have changed subject, and a conversation that could
// not be steered out of its opening reading would be worse than one with no
// memory at all.
func applyCarriedPlan(outcome QuestionFamilyOutcome, carry planCarryResult) (QuestionFamilyOutcome, bool) {
	if carry.Outcome != PlanCarryHit {
		return outcome, false
	}
	if outcome.Family != "" && outcome.Family != QuestionFamilyUnclassified {
		return outcome, false
	}
	outcome.Family = carry.Family
	outcome.Source = QuestionFamilySourceCarried
	// The group kind rides on the winning sample, which is where PlanAnswer
	// reads it from -- so a carried grouped reading rebuilds the same group
	// axis rather than becoming a grouped family with no axis to group by.
	outcome.WinningSample.GroupKind = carry.GroupKind
	// SEAM 7 (CHAOS-4736): neither table decided this answer -- a prior turn
	// did -- so the outcome must not keep reporting the routing decision made
	// before the carry. The family-resolution EVENT was already emitted by
	// then and cannot be amended; see FamilyRouteCarried's own doc comment
	// for the limit that leaves.
	outcome.Route = FamilyRouteDecision{
		Family: carry.Family, Source: FamilyRoutePrecedence,
		Disposition: FamilyRouteCarried,
	}
	return outcome, true
}
