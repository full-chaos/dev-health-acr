package contextfabric

import (
	"sort"
	"strings"

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

// SynthesisStatusOverrideNoMatchKeptUnaskedFactKinds: the model said no_match,
// a subject is committed, and the only rows read for it are of kinds the
// question did not ask for, so the no_match is kept. From and To are both
// no_match; UnaskedFactKinds names the skipped kinds.
const SynthesisStatusOverrideNoMatchKeptUnaskedFactKinds SynthesisStatusOverrideReason = "no_match_kept_unasked_fact_kinds"

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

// committedFactRowRead reports whether an available or stale row of an asked
// kind was read for a committed subject, and names (sorted, deduplicated) the
// kinds of the rows it skipped because no requirement asked for them.
func committedFactRowRead(committed []SubjectRef, facts CanonicalFactBundle, asked map[FactKind]bool) (bool, []string) {
	matched := false
	skipped := map[string]bool{}
	for _, fact := range facts.Facts {
		if fact.SourceState != SourceAvailable && fact.SourceState != SourceStale {
			continue
		}
		isCommitted := false
		for _, subject := range committed {
			if fact.Subject == subject {
				isCommitted = true
				break
			}
		}
		if !isCommitted {
			continue
		}
		if len(asked) > 0 && !asked[fact.Kind] {
			skipped[string(fact.Kind)] = true
			continue
		}
		matched = true
	}
	kinds := make([]string, 0, len(skipped))
	for kind := range skipped {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return matched, kinds
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
	factRowMatched, unaskedKinds := committedFactRowRead(committed, facts, askedFactKinds(result, graph, read))
	switch {
	case membersServed:
		floor = InvestigationPartial
	case terminal:
	case factRowMatched:
		reason = SynthesisStatusOverrideNoMatchOverReadFacts
	case len(unaskedKinds) > 0:
		return &SynthesisStatusOverrideOutcome{
			From: result.Status, To: result.Status, Reason: SynthesisStatusOverrideNoMatchKeptUnaskedFactKinds,
			CommittedCount: len(committed), UnaskedFactKinds: strings.Join(unaskedKinds, ","),
		}
	default:
		return nil
	}
	outcome := &SynthesisStatusOverrideOutcome{
		From: result.Status, To: floor, Reason: reason, CommittedCount: len(committed), UnaskedFactKinds: strings.Join(unaskedKinds, ","),
	}
	floorNoMatchTo(result, floor)
	return outcome
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
