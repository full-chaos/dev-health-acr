package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const workItemMembershipRationale = "Work items are members of the resolved project within the authorized scope."

// discoverWorkItemTuple reads S1 under the response owner's existing lease
// lifetime. It never reads the graph or expands the content subject set.
func (e *Engine) discoverWorkItemTuple(ctx context.Context, principal storage.Principal, request InvestigationRequest, resolution SubjectResolution, plan *AnswerPlan, filter workItemMemberFilter) (GraphContext, *WorkItemTupleCensus, error) {
	graph := GraphContext{Resolution: resolution, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, EvidenceRefIDs: []string{}, FactRequirements: []FactRequirement{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}
	digest, err := WorkItemAuthorizationDigest(principal, request.RequestedScope.RepositorySlugs)
	if err != nil {
		return graph, nil, err
	}
	census := &WorkItemTupleCensus{Version: WorkItemTupleCensusVersion, State: WorkItemMembershipCensusUnmeasured, RequestedRepositoryScope: append([]string{}, request.RequestedScope.RepositorySlugs...), AuthorizationDigest: digest, memberFilter: filter}
	if e.workItemMembership == nil {
		return graph, census, nil
	}
	lease, membership, readErr := e.workItemMembership.BeginWorkItemMembership(ctx, principal, WorkItemMembershipRequest{Anchor: WorkItemMembershipAnchor{Subject: resolution.Committed[0]}, RequestedRepositoryScope: append([]string{}, request.RequestedScope.RepositorySlugs...), PlanMaxMembers: plan.Budget.MaxMembers, RequestMaxMembers: request.Options.MaxCohortMembers, Status: filter.Status, TimeColumn: filter.timeColumn(), TimeStart: filter.Start, TimeEnd: filter.End})
	if lease != nil {
		owner, ok := WorkItemResponseOwnerFromContext(ctx)
		if !ok {
			lease.Release()
			return graph, census, fmt.Errorf("work-item dispatch response owner unavailable")
		}
		if err := owner.Retain(lease); err != nil {
			return graph, census, err
		}
	}
	if ctx.Err() != nil {
		return graph, census, ctx.Err()
	}
	if readErr != nil || lease == nil || !membership.Census.PopulationMeasured || membership.Census.State == WorkItemMembershipCensusUnmeasured {
		return graph, census, nil
	}
	// A filtered read never measures the denied partition: its count would be
	// the denied items' count for one status, a distribution the unfiltered
	// read does not give. The filtered answer states one fixed exclusion
	// instead (workItemStatusDeniedExclusion).
	if gap, ok := workItemAuthorizationGapOf(membership.Census); ok && !filter.Active() {
		census.gap = &gap
		if gap.NoneAuthorized() {
			// Members exist and none are authorized: the answer is a
			// disclosure, not an empty project and not a zero count.
			return graph, census, nil
		}
	}
	census.State = membership.Census.State
	census.Value = membership.Census.AuthorizedPopulation
	if census.State == WorkItemMembershipCensusFloor {
		census.Value = WorkItemMembershipCensusLimit
	}
	members := slices.Clone(membership.Members)
	slices.SortFunc(members, func(a, b WorkItemMembershipMember) int {
		if a.CanonicalID < b.CanonicalID {
			return -1
		}
		if a.CanonicalID > b.CanonicalID {
			return 1
		}
		return 0
	})
	limit := workItemTupleSelectionCap(plan.Budget.MaxMembers, request.Options.MaxCohortMembers)
	if len(members) > limit {
		members = members[:limit]
	}
	cohort := &Cohort{Kind: SubjectWorkItem, Rationale: workItemMembershipRationale, Members: []CohortMember{}, Complete: census.State == WorkItemMembershipCensusExact && census.Value == len(members), Truncated: census.Value > len(members)}
	for index, member := range members {
		subject := SubjectRef{Kind: SubjectWorkItem, CanonicalID: member.CanonicalID, Label: member.WorkItemID}
		if subject.Label == "" {
			subject.Label = member.CanonicalID
		}
		ref, ok := canonicalWorkItemEvidenceRef(subject)
		if !ok {
			return graph, nil, fmt.Errorf("work-item membership identity invalid")
		}
		cohort.Members = append(cohort.Members, CohortMember{Subject: subject, Rank: index + 1, InclusionReasons: []string{workItemMembershipRationale}, EvidenceRefIDs: []string{ref}})
		graph.EvidenceRefIDs = append(graph.EvidenceRefIDs, ref)
	}
	census.Retained = len(cohort.Members)
	if ValidateWorkItemTupleCensus(census) != WorkItemTupleCensusReadAvailable {
		return graph, nil, fmt.Errorf("work-item membership census invalid")
	}
	graph.Cohort = cohort
	graph.CohortPopulation = census.Value
	return graph, census, nil
}

func workItemTupleSubjects(cohort *Cohort) []SubjectRef {
	subjects := []SubjectRef{}
	if cohort != nil {
		for _, member := range cohort.Members {
			subjects = append(subjects, member.Subject)
		}
	}
	return subjects
}

func workItemTupleFactRequirements(subjects []SubjectRef) []FactRequirement {
	return []FactRequirement{{Kind: FactStatus, Subjects: slices.Clone(subjects)}, {Kind: FactWork, Subjects: slices.Clone(subjects)}}
}

// restrictWorkItemTupleCandidate keeps the committed project as resolution
// metadata, while evidence remains exclusively member-derived.
func restrictWorkItemTupleCandidate(resolution SubjectResolution) SubjectResolution {
	kept := copySubjectResolutionForRetry(resolution)
	kept.Candidates = []SubjectCandidate{}
	anchor := kept.Committed[0]
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind == anchor.Kind && candidate.Subject.CanonicalID == anchor.CanonicalID {
			candidate.EvidenceRefIDs = []string{}
			kept.Candidates = append(kept.Candidates, candidate)
			break
		}
	}
	return kept
}

func workItemTupleCardinality(census *WorkItemTupleCensus) MembershipCardinality {
	if census == nil || census.State == WorkItemMembershipCensusUnmeasured {
		return MembershipCardinality{}
	}
	return MembershipCardinality{Resolved: true, Kind: SubjectWorkItem, Served: census.Value, Declared: census.Value, PopulationIncomplete: census.State == WorkItemMembershipCensusFloor}
}

func workItemTupleSelectionCap(planCap, requestCap int) int {
	limit := WorkItemMembershipServeLimit
	for _, cap := range []int{planCap, requestCap} {
		if cap > 0 && cap < limit {
			limit = cap
		}
	}
	return limit
}

func (e *Engine) workItemTupleNarrowing(ctx context.Context, principal storage.Principal, plan *AnswerPlan, requestCap int, census *WorkItemTupleCensus) {
	limit := workItemTupleSelectionCap(plan.Budget.MaxMembers, requestCap)
	if plan.Budget.MaxMembers > limit {
		e.recordPlanNarrowingStep(plan, PlanNarrowing{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: plan.Budget.MaxMembers, After: limit})
		e.recordPlanNarrowing(ctx, principal, PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingCardinality, plan.Budget.MaxMembers, limit, false, false, "", ""))
	}
	if census != nil && census.Value > census.Retained {
		e.recordPlanNarrowingStep(plan, PlanNarrowing{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Basis: contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical, Before: census.Value, After: census.Retained})
		e.recordPlanNarrowing(ctx, principal, PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingCardinality, census.Value, census.Retained, false, false, "", ""))
	}
}

func finalWorkItemTupleCensus(census *WorkItemTupleCensus, cohort *Cohort) *WorkItemTupleCensus {
	if census == nil {
		return nil
	}
	final := *census
	final.Retained = cohortMemberCount(cohort)
	return &final
}

func restrictWorkItemTupleEvidence(result InvestigationResult) InvestigationResult {
	allowed := map[string]bool{}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			if ref, ok := canonicalWorkItemEvidenceRef(member.Subject); ok {
				allowed[ref] = true
			}
		}
	}
	for index := range result.SubjectResolution.Candidates {
		refs := []string{}
		for _, ref := range result.SubjectResolution.Candidates[index].EvidenceRefIDs {
			if allowed[ref] {
				refs = append(refs, ref)
			}
		}
		result.SubjectResolution.Candidates[index].EvidenceRefIDs = refs
	}
	labels := map[string]string{}
	for ref, label := range result.EvidenceRefLabels {
		if allowed[ref] {
			labels[ref] = label
		}
	}
	result.EvidenceRefLabels = labels
	return result
}

// Stage C already proved the project anchor under current authorization.
// Its status is not the question's subject and member-only content cannot
// supply an anchor-status affirmation. Other families retain that gate.
func applyCommitAffirmationForWorkItemTuple(result *InvestigationResult, census *WorkItemTupleCensus, inputs affirmationInputs) []CommitAffirmationOutcome {
	if census != nil {
		return nil
	}
	return applyCommitAffirmation(result, inputs)
}

func validateWorkItemTupleFactRequest(request CanonicalFactRequest) error {
	members := workItemTupleSubjects(request.Cohort)
	if len(members) == 0 || len(request.Subjects) != len(members) || len(request.Requirements) != 2 {
		return fmt.Errorf("%w: work-item tuple requires retained fact subjects", ErrNoInvestigationSubjects)
	}
	keys := map[string]bool{}
	for _, subject := range members {
		if _, ok := canonicalWorkItemEvidenceRef(subject); !ok {
			return fmt.Errorf("work-item tuple fact subject invalid")
		}
		keys[subject.CanonicalID] = true
	}
	for _, subject := range request.Subjects {
		if subject.Kind != SubjectWorkItem || !keys[subject.CanonicalID] {
			return fmt.Errorf("work-item tuple fact subject is outside retained members")
		}
	}
	kinds := map[FactKind]bool{}
	for _, requirement := range request.Requirements {
		if requirement.Kind != FactStatus && requirement.Kind != FactWork {
			return fmt.Errorf("work-item tuple fact kind invalid")
		}
		kinds[requirement.Kind] = true
		if len(requirement.Subjects) != len(members) {
			return fmt.Errorf("%w: work-item tuple requirement subjects must be explicit", ErrNoInvestigationSubjects)
		}
		for _, subject := range requirement.Subjects {
			if subject.Kind != SubjectWorkItem || !keys[subject.CanonicalID] {
				return fmt.Errorf("work-item tuple requirement is outside retained members")
			}
		}
	}
	if len(kinds) != 2 {
		return fmt.Errorf("work-item tuple requires status and work content")
	}
	return nil
}

func applyWorkItemTitles(cohort *Cohort, facts []CanonicalFact) {
	if cohort == nil {
		return
	}
	titles := map[string]string{}
	for _, fact := range facts {
		if fact.Kind == FactWork && fact.Subject.Kind == SubjectWorkItem {
			if title := fact.Fields["title"].String; title != nil && *title != "" {
				titles[fact.Subject.CanonicalID] = strings.TrimSpace(*title)
			}
		}
	}
	for index := range cohort.Members {
		if title := titles[cohort.Members[index].Subject.CanonicalID]; title != "" {
			label := []rune(title)
			if len(label) > contractsv1.ContextFabricSubjectRefLabelMaxLength {
				label = label[:contractsv1.ContextFabricSubjectRefLabelMaxLength]
			}
			cohort.Members[index].Subject.Label = strings.TrimSpace(string(label))
		}
	}
}

// workItemStatusFilterDisclosure states the filter beside a served member
// set: a current-status read, not a period, and not completion or readiness.
func workItemStatusFilterDisclosure(status string) string {
	return contractsv1.ContextFabricWorkItemMemberFilterLimitationPrefix + "whose current status is " + status + "; status is read as of now, over no period, and is not completion or readiness."
}

// workItemStatusNoMatchDisclosure names the empty result. Zero matches is a
// count of matching items, not a statement that the project is healthy.
func workItemStatusNoMatchDisclosure(status string) string {
	return contractsv1.ContextFabricWorkItemNoMatchLimitationPrefix + "currently has status " + status + contractsv1.ContextFabricWorkItemNoMatchLimitationSuffix
}

// workItemMemberFilter is what the member read applied: a status of the closed
// set, and/or a half-open window on one bound time field. The zero value reads
// every member.
type workItemMemberFilter struct {
	Status   string
	TimeRole MemberTimeRole
	Start    time.Time
	End      time.Time
}

func (f workItemMemberFilter) hasWindow() bool { return f.TimeRole != "" }

// Active reports whether the read is filtered at all.
func (f workItemMemberFilter) Active() bool { return f.Status != "" || f.hasWindow() }

func (f workItemMemberFilter) timeColumn() string {
	column, _ := f.TimeRole.WorkItemTimeColumn()
	return column
}

// workItemWindowFilterDisclosure states the window beside a served member set.
// The period filters one time field; every other fact is as of now.
func workItemWindowFilterDisclosure(f workItemMemberFilter) string {
	field := map[MemberTimeRole]string{MemberTimeRoleCreated: "created", MemberTimeRoleCompleted: "completed", MemberTimeRoleUpdated: "last updated"}[f.TimeRole]
	return contractsv1.ContextFabricWorkItemMemberFilterLimitationPrefix + field + " from " + f.Start.UTC().Format(workItemWindowTimeLayout) + " to " + f.End.UTC().Format(workItemWindowTimeLayout) + " (the " + f.timeColumn() + " field); their status and every other fact is as of now, not as of the period."
}

// workItemMemberFilterNoMatchDisclosure names the empty result of a filter that
// includes a window. Zero matches is a count of matches, not a statement that
// the project is healthy.
func workItemMemberFilterNoMatchDisclosure(f workItemMemberFilter) string {
	field := map[MemberTimeRole]string{MemberTimeRoleCreated: "created", MemberTimeRoleCompleted: "completed", MemberTimeRoleUpdated: "last updated"}[f.TimeRole]
	with := ""
	if f.Status != "" {
		with = " and a current status of " + f.Status
	}
	return contractsv1.ContextFabricWorkItemNoMatchLimitationPrefix + "was " + field + " in that period" + with + contractsv1.ContextFabricWorkItemNoMatchLimitationSuffix
}

// withWorkItemMemberFilterLimitations appends the filter disclosures, and the
// named empty result when the measured population is exactly zero. Nothing is
// added for an unfiltered read.
func withWorkItemMemberFilterLimitations(result InvestigationResult, f workItemMemberFilter, census *WorkItemTupleCensus) InvestigationResult {
	if !f.Active() {
		return result
	}
	var additions []string
	if f.Status != "" {
		additions = append(additions, workItemStatusFilterDisclosure(f.Status))
	}
	if f.hasWindow() {
		additions = append(additions, workItemWindowFilterDisclosure(f))
	}
	additions = append(additions, workItemStatusDeniedExclusion)
	if census != nil && census.State == WorkItemMembershipCensusExact && census.Value == 0 && census.gap == nil {
		if f.hasWindow() {
			additions = append(additions, workItemMemberFilterNoMatchDisclosure(f))
		} else {
			additions = append(additions, workItemStatusNoMatchDisclosure(f.Status))
		}
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, additions)
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}

// workItemStatusDeniedExclusion is the fixed statement a filtered read makes
// about work items outside the principal's scope. It is the same words for
// every outcome, so it cannot tell the caller how many denied items hold a
// given status.
const workItemStatusDeniedExclusion = contractsv1.ContextFabricWorkItemDeniedScopeExclusionLimitation

// workItemWindowTimeLayout names an instant to the microsecond, the precision
// the member read binds.
const workItemWindowTimeLayout = "2006-01-02T15:04:05.000000Z"
