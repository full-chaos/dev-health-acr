package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const workItemMembershipRationale = "Work items are members of the resolved project within the authorized scope."

// workItemMembershipRationaleFor is the cohort's inclusion rationale for
// members read on an anchor of this kind.
func workItemMembershipRationaleFor(anchor SubjectKind) string {
	if anchor == SubjectRepository {
		return contractsv1.ContextFabricWorkItemRepositoryMembershipRationale
	}
	return workItemMembershipRationale
}

// workItemMemberReason is one member's inclusion reason. On a repository
// anchor it names the tier of the link that reached the member; a member linked
// only by the heuristic tier is never presented as natively linked.
func workItemMemberReason(anchor SubjectKind, member WorkItemMembershipMember) string {
	if anchor == SubjectRepository {
		return contractsv1.ContextFabricWorkItemRepositoryMembershipReason(member.LinkTier)
	}
	return workItemMembershipRationale
}

// beginWorkItemMembership opens the membership read of the anchor's kind: the
// S1 read of a project, or the graph walk of a repository.
func (e *Engine) beginWorkItemMembership(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, request WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
	if request.Anchor.Subject.Kind == SubjectRepository {
		return e.treeWorkItemMembership.Begin(ctx, principal, binding, scope, request)
	}
	if e.workItemMembership == nil {
		return nil, WorkItemMembershipResult{}, errors.New("work item membership is not wired")
	}
	return e.workItemMembership.BeginWorkItemMembership(ctx, principal, request)
}

// discoverWorkItemTuple reads S1 under the response owner's existing lease
// lifetime. It never reads the graph or expands the content subject set.
func (e *Engine) discoverWorkItemTuple(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding, resolution SubjectResolution, plan *AnswerPlan, filter workItemMemberFilter) (GraphContext, *WorkItemTupleCensus, error) {
	graph := GraphContext{Resolution: resolution, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, EvidenceRefIDs: []string{}, FactRequirements: []FactRequirement{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}
	digest, err := WorkItemAuthorizationDigest(principal, request.RequestedScope.RepositorySlugs)
	if err != nil {
		return graph, nil, err
	}
	census := &WorkItemTupleCensus{Version: WorkItemTupleCensusVersion, State: WorkItemMembershipCensusUnmeasured, RequestedRepositoryScope: append([]string{}, request.RequestedScope.RepositorySlugs...), AuthorizationDigest: digest, memberFilter: filter, measuredNow: true}
	anchorKind := resolution.Committed[0].Kind
	if anchorKind != SubjectRepository && e.workItemMembership == nil {
		return graph, census, nil
	}
	var membership WorkItemMembershipResult
	measured := false
	restricted := false
	if anchorKind == SubjectRepository {
		restricted = workItemRepositoryRestricted(principal, request.RequestedScope.RepositorySlugs)
		// Every repository answer states where its links come from, whatever
		// the read reaches: the reading is replaced by the measured one below.
		census.repository = &repositoryWorkItemReading{Outcome: RepositoryWorkItemWalkReadFailed}
		// One decision line per walk, on every path out of this function.
		defer func() {
			e.recordRepositoryWorkItemWalk(ctx, principal, membership, measured, cohortMemberCount(graph.Cohort), filter.Active(), restricted)
		}()
	}
	lease, read, readErr := e.beginWorkItemMembership(ctx, principal, request.RequestedScope, binding, WorkItemMembershipRequest{Anchor: WorkItemMembershipAnchor{Subject: resolution.Committed[0]}, RequestedRepositoryScope: append([]string{}, request.RequestedScope.RepositorySlugs...), RequestMaxMembers: request.Options.MaxCohortMembers, Status: filter.Status, TimeColumn: filter.timeColumn(), TimeStart: filter.Start, TimeEnd: filter.End})
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
	membership = read
	if readErr != nil || lease == nil || !membership.Census.PopulationMeasured || membership.Census.State == WorkItemMembershipCensusUnmeasured {
		return graph, census, nil
	}
	measured = true
	// A filtered read never measures the denied partition: its count would be
	// the denied items' count for one status, a distribution the unfiltered
	// read does not give. The filtered answer states one fixed exclusion
	// instead (workItemStatusDeniedExclusion).
	//
	// A repository-restricted caller with no member is served the neutral
	// exclusion, not the measured partition, so the answer does not tell an
	// unlinked repository from a hidden one.
	neutralDenial := anchorKind == SubjectRepository && restricted && len(membership.Members) == 0
	if gap, ok := workItemAuthorizationGapOf(membership.Census, anchorKind); ok && !filter.Active() && !neutralDenial {
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
	limit := workItemTupleSelectionCap(0, request.Options.MaxCohortMembers)
	census.listCut = contractsv1.ContextFabricWorkItemListCutServer
	if requested := request.Options.MaxCohortMembers; requested > 0 && requested < WorkItemMembershipServeLimit {
		census.listCut = contractsv1.ContextFabricWorkItemListCutRequest
	}
	if len(members) > limit {
		members = members[:limit]
	}
	// A repository read can be partial without being a floor: a filter that
	// could not read every walked member, or a walk cut at its read bound. The
	// census stays exact over what was read (a floor's value is pinned to the
	// census limit), and the cohort is not complete.
	partial := anchorKind == SubjectRepository && census.State == WorkItemMembershipCensusExact && membership.Census.PopulationIncomplete
	census.incomplete = partial
	rationale := workItemMembershipRationaleFor(anchorKind)
	cohort := &Cohort{Kind: SubjectWorkItem, Rationale: rationale, Members: []CohortMember{}, Complete: census.State == WorkItemMembershipCensusExact && census.Value == len(members) && !partial, Truncated: census.Value > len(members)}
	heuristic := 0
	for index, member := range members {
		subject := SubjectRef{Kind: SubjectWorkItem, CanonicalID: member.CanonicalID, Label: member.WorkItemID}
		if subject.Label == "" {
			subject.Label = member.CanonicalID
		}
		ref, ok := canonicalWorkItemEvidenceRef(subject)
		if !ok {
			return graph, nil, fmt.Errorf("work-item membership identity invalid")
		}
		if anchorKind == SubjectRepository && !treeLinkTierStrongerThanHeuristic(member.LinkTier) {
			heuristic++
		}
		if anchorKind == SubjectRepository {
			if census.linkTier == nil {
				census.linkTier = map[string]string{}
			}
			census.linkTier[member.CanonicalID] = member.LinkTier
		}
		cohort.Members = append(cohort.Members, CohortMember{Subject: subject, Rank: index + 1, InclusionReasons: []string{workItemMemberReason(anchorKind, member)}, EvidenceRefIDs: []string{ref}})
		graph.EvidenceRefIDs = append(graph.EvidenceRefIDs, ref)
	}
	if anchorKind == SubjectRepository {
		census.repository = &repositoryWorkItemReading{
			Outcome:      repositoryWorkItemWalkOutcome(membership.Census, true, len(cohort.Members), restricted),
			PullRequests: membership.Census.RepositoryPullRequests, LinkedIssues: membership.Census.RepositoryLinkedIssues,
			Heuristic: heuristic, Cut: membership.Census.PopulationIncomplete || membership.Census.State == WorkItemMembershipCensusFloor,
			LowerTierCut: membership.Census.LowerTierCut,
		}
	}
	census.Retained = len(cohort.Members)
	if ValidateWorkItemTupleCensus(census) != WorkItemTupleCensusReadAvailable {
		return graph, nil, fmt.Errorf("work-item membership census invalid")
	}
	// The list is served up to the request cap. The fact reads cover all of it
	// (titles label every listed member); what the model reads and writes about
	// is bounded by the item ceiling: stage 2 narrows the synthesis input to the
	// plan.Budget.MaxMembers members chosen strongest link first (a repository walk) and then by canonical id, and the full
	// list is put back on the served result (ServeWorkItemTupleCensus).
	census.walkList = cohort
	census.anchorKind = anchorKind
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
	return MembershipCardinality{Resolved: true, Kind: SubjectWorkItem, Served: census.Value, Declared: census.Value, PopulationIncomplete: census.State == WorkItemMembershipCensusFloor || census.incomplete}
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
	// The member budget of the plan bounds what the model reads (a synthesis_input
	// step, recorded when it narrows), never the listed members: the list is
	// bounded by the request's cap and the serve limit, and a cut there is this
	// step, in the order the walk kept members in.
	if census != nil && census.Value > census.Retained {
		basis := census.walkBasis()
		e.recordPlanNarrowingStep(plan, PlanNarrowing{Stage: contractsv1.ContextFabricPlanNarrowingCardinality, Basis: basis, Before: census.Value, After: census.Retained})
		event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingCardinality, census.Value, census.Retained, false, false, "", basis)
		event.Basis = basis
		e.recordPlanNarrowing(ctx, principal, event)
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
	return workItemNoMatchDisclosure(SubjectProject, "currently has status "+status)
}

// workItemNoMatchDisclosure names the empty result for the anchor the members
// were read on.
func workItemNoMatchDisclosure(anchor SubjectKind, body string) string {
	if anchor == SubjectRepository {
		return contractsv1.ContextFabricWorkItemRepositoryNoMatchLimitationPrefix + body + contractsv1.ContextFabricWorkItemRepositoryNoMatchLimitationSuffix
	}
	return contractsv1.ContextFabricWorkItemNoMatchLimitationPrefix + body + contractsv1.ContextFabricWorkItemNoMatchLimitationSuffix
}

// workItemMemberFilter is what the member read applied: a status of the closed
// set, and/or a half-open window on one bound time field. The zero value reads
// every member.
type workItemMemberFilter struct {
	// AnchorKind is the kind of the subject the members were read on; the
	// zero value is a project.
	AnchorKind SubjectKind
	Status     string
	TimeRole   MemberTimeRole
	Start      time.Time
	End        time.Time
	// WindowNotApplied marks a current read served although the request
	// committed a window: the membership is as of now.
	WindowNotApplied bool
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
	return workItemNoMatchDisclosure(f.AnchorKind, "was "+field+" in that period"+with)
}

// withWorkItemMemberFilterLimitations appends the filter disclosures, and the
// named empty result when the measured population is exactly zero. Nothing is
// added for an unfiltered read.
func withWorkItemMemberFilterLimitations(result InvestigationResult, f workItemMemberFilter, census *WorkItemTupleCensus) InvestigationResult {
	if !f.Active() {
		if !f.WindowNotApplied {
			return result
		}
		// An unfiltered read: only the period statement is added; the
		// authorization gap keeps its own disclosure.
		composed, displaced := appendBoundedLimitations(result.Limitations, []string{contractsv1.ContextFabricWorkItemWindowNotAppliedLimitation})
		result.Limitations = composed
		result.LimitationsDisplaced += displaced
		return result
	}
	var additions []string
	if f.Status != "" {
		additions = append(additions, workItemStatusFilterDisclosure(f.Status))
	}
	if f.hasWindow() {
		additions = append(additions, workItemWindowFilterDisclosure(f))
	}
	if f.WindowNotApplied {
		additions = append(additions, contractsv1.ContextFabricWorkItemWindowNotAppliedLimitation)
	}
	additions = append(additions, workItemStatusDeniedExclusion)
	if census != nil && census.State == WorkItemMembershipCensusExact && census.Value == 0 && census.gap == nil && !census.incomplete {
		if f.hasWindow() {
			additions = append(additions, workItemMemberFilterNoMatchDisclosure(f))
		} else {
			additions = append(additions, workItemNoMatchDisclosure(f.AnchorKind, "currently has status "+f.Status))
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

// workItemWindowReadBounds widens a window to the microsecond the read binds:
// the start rounds down and the end rounds up, so the read never narrows the
// window the answer speaks for and a sub-microsecond window cannot collapse to
// an empty interval.
func workItemWindowReadBounds(start, end time.Time) (time.Time, time.Time) {
	start = start.UTC().Truncate(time.Microsecond)
	end = end.UTC()
	if rounded := end.Truncate(time.Microsecond); rounded.Before(end) {
		end = rounded.Add(time.Microsecond)
	} else {
		end = rounded
	}
	if !start.Before(end) {
		end = start.Add(time.Microsecond)
	}
	return start, end
}

// recordRepositoryWorkItemWalk emits the decision line of one walk: the
// outcome and the counts behind it, and no name or id.
func (e *Engine) recordRepositoryWorkItemWalk(ctx context.Context, principal storage.Principal, membership WorkItemMembershipResult, measured bool, members int, filtered, restricted bool) {
	if e.telemetry == nil {
		return
	}
	event := RepositoryWorkItemWalkEvent{
		Outcome:  repositoryWorkItemWalkOutcome(membership.Census, measured, members, restricted),
		Filtered: filtered, Restricted: restricted, Measured: measured, UnmeasuredReason: membership.Census.UnmeasuredReason,
	}
	if measured {
		event.PullRequests, event.LinkedIssues = membership.Census.RepositoryPullRequests, membership.Census.RepositoryLinkedIssues
		event.LowerTierCut = membership.Census.LowerTierCut
		event.Members, event.Population = members, membership.Census.AuthorizedPopulation
		event.Truncated = membership.Census.PopulationIncomplete || membership.Census.State == WorkItemMembershipCensusFloor || members < event.Population
		if !filtered {
			event.Denied = membership.Census.DeniedPopulation
		}
	}
	e.telemetry.RecordRepositoryWorkItemWalk(ctx, principal, event)
}
