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

// factPlanRequirement is one requested fact family, reduced to the only two
// things planning is allowed to consider: which family, and whether the
// requirement scoped itself to particular subjects.
//
// Deliberately NOT a FactRequirement: that type also carries Parameters,
// which are provider query inputs and none of planning's business.
type factPlanRequirement struct {
	Kind     FactKind
	Subjects []SubjectRef
}

// factPlanInput is the ONLY thing the planner is given, and it is
// prose-free by construction (CHAOS-3783 codex round-1 F1).
//
// CanonicalFactRequest carries the full InterpretedQuestion -- shape,
// RequestedJudgment, SubjectTerms, ComparisonTerms, ClarificationReason.
// All of that is model-authored text. An earlier version of this planner
// took the whole request and simply did not read those fields, which made
// "a model cannot prune a provider by phrasing" a property of the current
// function body rather than of the interface: any later edit could reach
// for request.Question with no signature change, and a behavioral test
// written against today's code cannot fail for an edit it was never written
// against.
//
// This type removes the option. Prose is not merely unread, it is not
// present, so the fail-open guarantee is carried by the type system and
// survives authors who never read this comment. newFactPlanInput at the
// ReadFacts boundary is the single place the narrowing happens.
type factPlanInput struct {
	// Subjects is the investigation-wide subject set, already collapsed
	// from request subjects or cohort members. Only Kind is ever read;
	// CanonicalID and Label ride along solely so the planner can hand back
	// the narrowed list the caller will query with.
	Subjects     []SubjectRef
	Requirements []factPlanRequirement
}

// newFactPlanInput is the boundary that strips a CanonicalFactRequest down
// to what planning may see. It is the only function that touches both
// types, so the set of fields planning can reach is auditable in one place.
func newFactPlanInput(request CanonicalFactRequest) factPlanInput {
	requirements := make([]factPlanRequirement, 0, len(request.Requirements))
	for _, requirement := range request.Requirements {
		requirements = append(requirements, factPlanRequirement{
			Kind: requirement.Kind, Subjects: requirement.Subjects,
		})
	}
	return factPlanInput{
		Subjects:     investigationScopeSubjects(request),
		Requirements: requirements,
	}
}

// factPlanEntry is one capability's planned read for one investigation.
//
// Subjects is the narrowed subject list the provider will actually be asked
// about; it is empty exactly when Pruned is true. Reason is always non-empty
// when Pruned or Narrowed is true, because a coverage observation in any
// non-available state must carry a reason (ContextFabricSourceObservation's
// contract) -- that requirement is what makes the empty-states rule hold
// here: a skipped provider is explained absence, never silent absence.
type factPlanEntry struct {
	Kind     FactKind
	Subjects []SubjectRef
	Pruned   bool
	Narrowed bool
	Reason   string
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
// already validates -- and, per factPlanInput's doc comment, nothing else
// is even reachable from here.
//
// # Why an empty intersection is a proof, not a guess
//
// A capability that supports none of the resolved subject kinds could not
// have contributed one usable fact even if it were run: mergeFactProviderResult
// rejects any fact whose subject is outside the investigation set,
// buildFactQuery only ever asks about subjects from that set, and each
// provider filters on its own ID column, which no subject of an unsupported
// kind matches. So the pruned read is not "probably useless", it is
// "provably empty". This is what the issue's fail-open constraint asks for
// -- prune only on confident irrelevance -- and it is why no confidence
// threshold or scoring appears anywhere below.
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
// The returned slice is in the same order as the input requirements, so the
// caller's coverage output stays deterministic.
func planFactReads(input factPlanInput, capabilities map[FactKind]FactCapability) []factPlanEntry {
	plan := make([]factPlanEntry, 0, len(input.Requirements))
	for _, requirement := range input.Requirements {
		capability, registered := capabilities[requirement.Kind]
		if !registered {
			// Not this planner's decision: ReadFacts already reports an
			// unregistered kind as SourceUnconfigured, which is a different
			// and more specific statement than "pruned" (nothing is
			// configured to answer this at all, for any subject). Passing it
			// through unchanged keeps that distinction intact.
			plan = append(plan, factPlanEntry{Kind: requirement.Kind})
			continue
		}
		subjects := requirement.Subjects
		if len(subjects) == 0 {
			subjects = input.Subjects
		}
		supported, unsupportedKinds := partitionBySupportedSubjectKind(subjects, capability.SupportedSubjectKinds)
		switch {
		case len(subjects) == 0:
			// Leave the existing "requires at least one discovered subject"
			// error to buildFactQuery rather than reporting it as a prune.
			// An investigation with no subjects at all is a different
			// failure from a capability that does not fit the subjects, and
			// collapsing the two would hide it.
			plan = append(plan, factPlanEntry{Kind: requirement.Kind})
		case len(supported) == 0:
			plan = append(plan, factPlanEntry{
				Kind:   requirement.Kind,
				Pruned: true,
				Reason: prunedReason(capability, unsupportedKinds),
			})
		case len(supported) < len(subjects):
			plan = append(plan, factPlanEntry{
				Kind:     requirement.Kind,
				Subjects: supported,
				Narrowed: true,
				Reason:   narrowedReason(capability, unsupportedKinds, len(subjects)-len(supported)),
			})
		default:
			plan = append(plan, factPlanEntry{Kind: requirement.Kind, Subjects: supported})
		}
	}
	return plan
}

// investigationScopeSubjects is the investigation-wide subject set: the
// request's own subjects, else the cohort's members. It is the single
// definition shared by newFactPlanInput and buildFactQuery, because the
// planner narrows the very list buildFactQuery goes on to validate and the
// two must not diverge.
// investigationScopeSubjectSet is the SAME scope as investigationScopeSubjects,
// keyed for membership tests. It is derived from that function rather than
// rebuilt, so the planner's notion of scope and the registry's cannot drift
// (codex round-6 F1).
//
// They previously did drift, and it mattered. The registry keyed its
// allowed-subject map on the UNION of request.Subjects and the cohort's
// members while the planner applied request.Subjects ELSE cohort as a
// FALLBACK. For a request naming both -- say request.Subjects=[repo_api] with
// a cohort of [project_titan] -- the union accepted project_titan as in
// scope even though the planner had scoped it out, so an explicit
// requirement naming it was pruned (or, for a project-capable capability,
// actually QUERIED) instead of being rejected as out of scope. One
// derivation, used everywhere, is the only fix that stays fixed.
func investigationScopeSubjectSet(request CanonicalFactRequest) map[string]SubjectRef {
	subjects := investigationScopeSubjects(request)
	scope := make(map[string]SubjectRef, len(subjects))
	for _, subject := range subjects {
		scope[canonicalFactSubjectKey(subject)] = subject
	}
	return scope
}

func investigationScopeSubjects(request CanonicalFactRequest) []SubjectRef {
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

// factQuerySubjects mirrors the planner's subject precedence exactly: the
// requirement's own subjects, else the investigation-wide set.
func factQuerySubjects(request CanonicalFactRequest, requirement FactRequirement) []SubjectRef {
	if len(requirement.Subjects) > 0 {
		return requirement.Subjects
	}
	return investigationScopeSubjects(request)
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

// withNarrowingNote prefixes a narrowed capability's planner note onto a
// coverage reason, whatever that reason turned out to be (CHAOS-3783 codex
// round-1 F2).
//
// It has to apply on EVERY path a narrowed capability can take, not just the
// success path. A narrowed provider that then fails still had its subject
// list cut by the planner, and if only the failure reason survives, the
// record that subjects were dropped is silently lost -- which is exactly the
// unexplained absence the empty-states rule forbids. The two facts are
// independent and the observation must carry both.
func withNarrowingNote(planned factPlanEntry, reason string) string {
	if !planned.Narrowed {
		return reason
	}
	return strings.TrimSpace(planned.Reason + " " + reason)
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
