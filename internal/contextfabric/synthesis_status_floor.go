package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A model no_match never overrides a committed subject that the service has
// already acted on. no_match means nothing to answer with; when a subject is
// committed and the service holds a cohort outcome or a canonical fact row for
// it, the status is the service's: the model may lower confidence and add
// limitations, but it cannot turn that state into no_match.

const synthesisNarrativeWithheldLimitation = contractsv1.ContextFabricSynthesisNarrativeWithheldLimitation

// SynthesisStatusOverrideNoMatchOverCohortOutcome: the model said no_match while
// the service served cohort members or held a degrading cohort coverage terminal.
const SynthesisStatusOverrideNoMatchOverCohortOutcome SynthesisStatusOverrideReason = "no_match_over_cohort_outcome"

// SynthesisStatusOverrideNoMatchOverReadFacts: the model said no_match while
// a canonical fact row of a kind the question asked for was read for a
// committed subject.
const SynthesisStatusOverrideNoMatchOverReadFacts SynthesisStatusOverrideReason = "no_match_over_read_facts"

func cohortTerminalCoverageCode(code contractsv1.ContextFabricCoverageDetailCode) bool {
	switch code {
	case contractsv1.ContextFabricCoverageDetailKindCensusTruncated,
		contractsv1.ContextFabricCoverageDetailGraphWalkCutBeforeMember,
		contractsv1.ContextFabricCoverageDetailGraphProjectDeploymentsUnlinked,
		contractsv1.ContextFabricCoverageDetailWorkItemRepositoryUnlinked,
		contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
		contractsv1.ContextFabricCoverageDetailGraphExactNameCandidatesTruncated:
		return true
	}
	return false
}

// askedFactKinds is the set of fact kinds the question asked for: the
// interpretation's requirements, the graph's and the ones the fact read ran with. Empty means the question named
// no kind, and then any read row of a committed subject is relevant.
func askedFactKinds(result *InvestigationResult, graph GraphContext, read []FactRequirement) map[FactKind]bool {
	asked := map[FactKind]bool{}
	for _, requirement := range result.Interpretation.FactRequirements {
		asked[requirement.Kind] = true
	}
	for _, requirement := range graph.FactRequirements {
		asked[requirement.Kind] = true
	}
	for _, requirement := range read {
		asked[requirement.Kind] = true
	}
	return asked
}

func committedFactRowRead(committed []SubjectRef, facts CanonicalFactBundle, asked map[FactKind]bool) bool {
	for _, fact := range facts.Facts {
		if fact.SourceState != SourceAvailable && fact.SourceState != SourceStale {
			continue
		}
		if len(asked) > 0 && !asked[fact.Kind] {
			continue
		}
		for _, subject := range committed {
			if fact.Subject == subject {
				return true
			}
		}
	}
	return false
}

// applyServerStatusFloor rewrites a model no_match to the server's status when
// a subject is committed and the service holds a deterministic outcome. It
// never promotes to complete, never touches the resolution, drivers or claims,
// and is idempotent (the status is no longer no_match afterwards).
func applyServerStatusFloor(result *InvestigationResult, graph GraphContext, facts CanonicalFactBundle, read []FactRequirement) *SynthesisStatusOverrideOutcome {
	if result == nil || result.Status != InvestigationNoMatch || result.RefusalBasis != "" {
		return nil
	}
	committed := result.SubjectResolution.Committed
	if len(committed) == 0 {
		committed = graph.Resolution.Committed
	}
	if len(committed) == 0 {
		return nil
	}
	membersServed := graph.Cohort != nil && len(graph.Cohort.Members) > 0
	terminal := false
	for _, detail := range graph.Coverage.Details {
		if detail.Degrading && cohortTerminalCoverageCode(detail.Code) {
			terminal = true
			break
		}
	}
	reason := SynthesisStatusOverrideNoMatchOverCohortOutcome
	floor := InvestigationDegraded
	switch {
	case membersServed:
		floor = InvestigationPartial
	case terminal:
	case committedFactRowRead(committed, facts, askedFactKinds(result, graph, read)):
		reason = SynthesisStatusOverrideNoMatchOverReadFacts
	default:
		return nil
	}
	outcome := &SynthesisStatusOverrideOutcome{
		From: result.Status, To: floor, Reason: reason, CommittedCount: len(committed),
	}
	floorNoMatchTo(result, floor)
	return outcome
}

const truncatedPopulationNoMemberLimitation = "The population for this question was truncated and no member of it was served, so this answer shows none of it."

// applyPopulationOutcomeStatusFloor floors a no_match whose own outcome rows
// say the retrieved population was truncated. The row exists only after
// synthesis, so this runs at the serving point every path passes, not beside
// applyServerStatusFloor. Truncation with served members is already floored
// there to partial; this covers the truncated population that carried no
// members, which is degraded: the service read a population it could not carry.
func applyPopulationOutcomeStatusFloor(result *InvestigationResult) {
	if result == nil || result.Status != InvestigationNoMatch || result.RefusalBasis != "" {
		return
	}
	for _, row := range result.Completeness.Outcomes {
		if row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult &&
			row.Outcome == contractsv1.ContextFabricRequirementNarrowed &&
			row.CauseCoverage == contractsv1.ContextFabricCoverageDetailPopulationTruncated {
			floorNoMatchTo(result, InvestigationDegraded)
			composed, displaced := appendBoundedLimitations(result.Limitations, []string{truncatedPopulationNoMemberLimitation})
			result.Limitations = composed
			result.LimitationsDisplaced += displaced
			return
		}
	}
}

// floorNoMatchTo is the one status swap every no_match floor uses: it sets the
// status, recomposes the two prose fields from it, and discloses that the
// narrative was withheld.
func floorNoMatchTo(result *InvestigationResult, floor InvestigationStatus) {
	result.Status = floor
	result.DirectJudgment = composeDirectJudgmentFrom(result.Status, result.Drivers, result.SubjectResolution)
	result.DeterministicAnswer = composeDeterministicAnswerFrom(result.Status, result.Drivers, result.ClaimedFacts, result.SubjectResolution)
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{synthesisNarrativeWithheldLimitation})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	result.Coverage.Partial = true
}
