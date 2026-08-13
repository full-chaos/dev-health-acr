package contextfabric

import (
	"fmt"
	"sort"
	"strings"
)

// Closed reason codes for a planner decision (CHAOS-3783). A decision is
// recorded in Coverage as a free-text Reason -- the contract has no
// structured field for it -- so the code is a fixed prefix rather than
// prose, and tests, operators, and downstream surfaces match on the prefix
// instead of parsing a sentence. Never build a reason string by hand; go
// through prunedReason/narrowedReason so the prefix cannot drift.
const (
	// FactPruneReasonSubjectKindUnsupported is the ONLY reason this planner
	// prunes a whole capability today. It means no subject this
	// investigation resolved has a kind the capability declares in
	// SupportedSubjectKinds, so the capability could not have produced a
	// single admissible fact -- see planFactReads for why that is a proof
	// and not a heuristic.
	FactPruneReasonSubjectKindUnsupported = "pruned:subject_kind_unsupported"
	// FactNarrowReasonSubjectKindUnsupported marks the partial case: the
	// capability DID run, but only against the subset of subjects whose
	// kind it supports. The dropped subjects are absence too, and coverage
	// source names must be unique (ContextFabricCoverage.Validate), so this
	// cannot be a second Coverage entry -- it is prefixed onto the
	// capability's own observation instead.
	FactNarrowReasonSubjectKindUnsupported = "narrowed:subject_kind_unsupported"
)

// factPlanEntry is one capability's planned read for one investigation.
//
// Subjects is the narrowed subject list the provider will actually be asked
// about; it is empty exactly when Pruned is true. Reason is always non-empty
// when Pruned or Narrowed is true, because a coverage observation in any
// non-available state must carry a reason (ContextFabricSourceObservation's
// contract) -- that requirement is what makes the empty-states rule hold
// here: a skipped provider is explained absence, never silent absence.
type factPlanEntry struct {
	Requirement FactRequirement
	Subjects    []SubjectRef
	Pruned      bool
	Narrowed    bool
	Reason      string
}

// planFactReads decides, after interpretation and before any fan-out, which
// canonical fact capabilities this investigation can possibly be answered by
// and which subjects each one should be asked about.
//
// # What the decision is made from
//
// Exactly one signal: FactCapability.SupportedSubjectKinds, which every
// provider declares in its own code (see devhealthfacts.newCapability), set
// against the kinds of the subjects the GRAPH resolved. The interpretation
// contributes only the fact KIND, chosen from a closed enum the contract
// already validates.
//
// That split is the point. The model names a family; code decides whether
// that family can apply to these subjects. No part of this planner reads
// question text, requested_judgment, subject terms, investigation shape, or
// clarification state, so a model cannot prune a provider by phrasing --
// there is no path from prose to a pruning decision. See
// docs/design/context-fabric-fact-planning.md for why a model-selected
// judgment-category gate was considered and rejected.
//
// # Why an empty intersection is a proof, not a guess
//
// A capability that supports none of the resolved subject kinds could not
// have contributed one usable fact even if it were run: mergeFactProviderResult
// rejects any fact whose subject is outside the investigation set, and
// buildFactQuery only ever asks about subjects from that set. So the pruned
// read is not "probably useless", it is "provably empty". This is what the
// issue's fail-open constraint asks for -- prune only on confident
// irrelevance -- and it is why no confidence threshold or scoring appears
// anywhere below.
//
// # What it fails open on
//
// Everything else. A requirement naming its own Subjects is honored as
// given. A capability that supports even one resolved subject runs. An
// ambiguous or low-confidence interpretation widens the requirement union
// and prunes nothing extra, because none of that reaches this function. An
// unregistered kind is not this function's business and keeps its existing
// SourceUnconfigured path in ReadFacts.
//
// # Why this replaced a hard failure
//
// Before CHAOS-3783, buildFactQuery returned an error for the first subject
// whose kind a capability did not support, and ReadFacts turned that error
// into a whole-bundle failure. One inapplicable fact family did not cost
// latency -- it failed the entire investigation. That was reachable without
// any model mistake: falkorgraph merges FactHealth and FactWorkload for
// every discovered cohort, graphrank resolves a cohort of kind project for
// a question naming "project", and neither capability supports project. So
// "which projects are behind" could not be answered at all. Pruning is what
// makes a wide requirement union survivable; the smaller fact bundle is the
// second-order win, not the first.
//
// The returned slice is in the same order as request.Requirements, so the
// caller's coverage output stays deterministic.
func planFactReads(request CanonicalFactRequest, capabilities map[FactKind]FactCapability) []factPlanEntry {
	plan := make([]factPlanEntry, 0, len(request.Requirements))
	for _, requirement := range request.Requirements {
		capability, registered := capabilities[requirement.Kind]
		if !registered {
			// Not this planner's decision: ReadFacts already reports an
			// unregistered kind as SourceUnconfigured, which is a different
			// and more specific statement than "pruned" (nothing is
			// configured to answer this at all, for any subject). Passing it
			// through unchanged keeps that distinction intact.
			plan = append(plan, factPlanEntry{Requirement: requirement})
			continue
		}
		subjects := factQuerySubjects(request, requirement)
		supported, unsupportedKinds := partitionBySupportedSubjectKind(subjects, capability.SupportedSubjectKinds)
		switch {
		case len(subjects) == 0:
			// Leave the existing "requires at least one discovered subject"
			// error to buildFactQuery rather than reporting it as a prune.
			// An investigation with no subjects at all is a different
			// failure from a capability that does not fit the subjects, and
			// collapsing the two would hide it.
			plan = append(plan, factPlanEntry{Requirement: requirement})
		case len(supported) == 0:
			plan = append(plan, factPlanEntry{
				Requirement: requirement,
				Pruned:      true,
				Reason:      prunedReason(capability, unsupportedKinds),
			})
		case len(supported) < len(subjects):
			plan = append(plan, factPlanEntry{
				Requirement: requirement,
				Subjects:    supported,
				Narrowed:    true,
				Reason:      narrowedReason(capability, unsupportedKinds, len(subjects)-len(supported)),
			})
		default:
			plan = append(plan, factPlanEntry{Requirement: requirement, Subjects: supported})
		}
	}
	return plan
}

// factQuerySubjects mirrors buildFactQuery's subject precedence exactly:
// the requirement's own subjects, else the request's, else the cohort's.
// The two must not diverge -- the planner narrows the very list
// buildFactQuery will go on to validate -- so this is the single definition
// and buildFactQuery calls it too.
func factQuerySubjects(request CanonicalFactRequest, requirement FactRequirement) []SubjectRef {
	if len(requirement.Subjects) > 0 {
		return requirement.Subjects
	}
	if len(request.Subjects) > 0 {
		return request.Subjects
	}
	if request.Cohort == nil {
		return nil
	}
	subjects := make([]SubjectRef, 0, len(request.Cohort.Members))
	for _, member := range request.Cohort.Members {
		subjects = append(subjects, member.Subject)
	}
	return subjects
}

// partitionBySupportedSubjectKind splits subjects into those the capability
// declares it can answer for and the distinct kinds of those it cannot. The
// unsupported KINDS (not the subjects) are what the reason reports:
// canonical IDs and labels are investigation content and have no business in
// a coverage reason that is stored, replayed, and read by operators, while
// the kind vocabulary is closed and content-free.
func partitionBySupportedSubjectKind(subjects []SubjectRef, supportedKinds []SubjectKind) ([]SubjectRef, []SubjectKind) {
	supported := make([]SubjectRef, 0, len(subjects))
	unsupportedSeen := make(map[SubjectKind]struct{})
	for _, subject := range subjects {
		if supportsSubjectKind(supportedKinds, subject.Kind) {
			supported = append(supported, subject)
			continue
		}
		unsupportedSeen[subject.Kind] = struct{}{}
	}
	unsupported := make([]SubjectKind, 0, len(unsupportedSeen))
	for kind := range unsupportedSeen {
		unsupported = append(unsupported, kind)
	}
	sort.Slice(unsupported, func(i, j int) bool { return unsupported[i] < unsupported[j] })
	return supported, unsupported
}

func prunedReason(capability FactCapability, unsupported []SubjectKind) string {
	return fmt.Sprintf(
		"%s: no resolved subject has a kind this capability supports (resolved: %s; supported: %s)",
		FactPruneReasonSubjectKindUnsupported, joinSubjectKinds(unsupported), joinSubjectKinds(capability.SupportedSubjectKinds),
	)
}

func narrowedReason(capability FactCapability, unsupported []SubjectKind, dropped int) string {
	return fmt.Sprintf(
		"%s: %d subject(s) were not queried because this capability does not support their kind (skipped: %s; supported: %s)",
		FactNarrowReasonSubjectKindUnsupported, dropped, joinSubjectKinds(unsupported), joinSubjectKinds(capability.SupportedSubjectKinds),
	)
}

func joinSubjectKinds(kinds []SubjectKind) string {
	if len(kinds) == 0 {
		return "none"
	}
	values := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		values = append(values, string(kind))
	}
	return strings.Join(values, ",")
}
