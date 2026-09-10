package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupReadCompletionScope and groupReadRequirementKind are the two domain
// values that select the rows a group-rooted read can serve: a requirement
// completing over EACH GROUP, satisfied by READING rather than by computing or
// by the answer contract.
//
// Both are derived from the domain's own vocabulary constants rather than
// restated as literals. The plan carries these coordinates as wire strings, so
// a literal here would be a second spelling of a value the derivation owns --
// and it would keep compiling, and silently match nothing, the day either
// vocabulary member is renamed.
var (
	groupReadCompletionScope = string(CompletionScopeEachGroup)
	groupReadRequirementKind = string(ObligationKindRead)
)

// groupReadOutcome is what the group stage decided, so the caller can act on
// it and the operator can read it without inferring anything from counts.
type groupReadOutcome struct {
	// Refused reports that the group axis was not read at all, and Reason
	// says why. A refusal is a product outcome, not an error: the turn goes
	// on to answer flat.
	Refused bool
	Reason  GroupReadRefusal
	// Proposed is how many groups the grouping produced, BEFORE any bound or
	// authorization narrowed them. It is the denominator, and it is reported
	// even when nothing was read, because "251 groups were proposed and none
	// could be read" and "no group was proposed" are different states.
	Proposed int
	// Admitted are the group identities the authorizer let through. They are
	// the only roots a group fact request may carry.
	Admitted []SubjectRef
	// Denied is how many proposed groups the authorizer refused. They stay
	// in the published group list, unread -- a group the principal may not
	// see must still be COUNTED, or "2 of 3 teams" quietly becomes a
	// complete-looking "2 of 2".
	Denied int
	// Read reports that a group-rooted fact request was actually issued.
	// Distinguishes "the read happened and returned nothing" from "no read
	// happened", which no count downstream can tell apart.
	Read bool
}

// GroupReadRefusal is the closed vocabulary of reasons the group axis was not
// read. It is a vocabulary rather than a free string because it reaches a
// telemetry field consumers group on.
type GroupReadRefusal string

const (
	// GroupReadRefusalNone is the absence of a refusal.
	GroupReadRefusalNone GroupReadRefusal = ""
	// GroupReadRefusalOverContractBound is a proposed group list larger than
	// the published contract can carry. It is decided BEFORE any group I/O,
	// and the list is never sliced to fit: a silently truncated group list is
	// a denominator quietly reduced to fit the answer, which answers a
	// question nobody asked.
	GroupReadRefusalOverContractBound GroupReadRefusal = "over_contract_bound"
	// GroupReadRefusalNoGroupAdmitted is an authorizer that admitted none of
	// the proposed groups. No provider request is issued at all -- reading
	// zero subjects is not a cheaper read, it is a read that should not
	// happen.
	GroupReadRefusalNoGroupAdmitted GroupReadRefusal = "no_group_admitted"
	// GroupReadRefusalAuthorizationUnavailable is an authorizer that could
	// not answer. Fail closed: an unauthorized read is worse than an
	// unserved requirement.
	GroupReadRefusalAuthorizationUnavailable GroupReadRefusal = "authorization_unavailable"
	// GroupReadRefusalNoReadRequirement is a grouped plan whose `each_group`
	// rows are all computed or given, so no fact kind can serve them and no
	// read is owed.
	GroupReadRefusalNoReadRequirement GroupReadRefusal = "no_read_requirement"
	// GroupReadRefusalMetadataConflict is two reads that disagree about one
	// fact kind's version or watermark. Neither read is the authority on the
	// other's opaque metadata, and choosing between them would invent an
	// ordering no producer declared -- so the group contribution is dropped
	// and the member evidence, which is complete and internally consistent,
	// is what the turn serves.
	GroupReadRefusalMetadataConflict GroupReadRefusal = "metadata_conflict"
)

// canonicalGroupReadRefusals is the emitter's own membership table, so a
// value outside the vocabulary reaches a log field as a named unknown rather
// than as free text.
var canonicalGroupReadRefusals = map[GroupReadRefusal]GroupReadRefusal{
	GroupReadRefusalNone:                     GroupReadRefusalNone,
	GroupReadRefusalOverContractBound:        GroupReadRefusalOverContractBound,
	GroupReadRefusalNoGroupAdmitted:          GroupReadRefusalNoGroupAdmitted,
	GroupReadRefusalAuthorizationUnavailable: GroupReadRefusalAuthorizationUnavailable,
	GroupReadRefusalNoReadRequirement:        GroupReadRefusalNoReadRequirement,
	GroupReadRefusalMetadataConflict:         GroupReadRefusalMetadataConflict,
}

// ValidGroupReadRefusal reports membership in the closed vocabulary.
func ValidGroupReadRefusal(value GroupReadRefusal) bool {
	_, member := canonicalGroupReadRefusals[value]
	return member
}

// groupReadRequirements is the fact kinds a group-rooted read must ask for:
// the distinct kinds of the plan's SERVED `read`/`each_group` rows, and
// nothing else.
//
// Derived from the plan's own requirement rows rather than from a hand list,
// because the rows are the authority on what this answer owes. A hand list
// would be a second authority that stops agreeing the first time the
// derivation changes -- and would silently ask providers for kinds this
// question never declared.
func groupReadRequirements(plan AnswerPlan) []FactRequirement {
	seen := make(map[FactKind]struct{}, len(plan.Requirements))
	requirements := make([]FactRequirement, 0, len(plan.Requirements))
	for _, row := range plan.Requirements {
		if row.Scope != groupReadCompletionScope || row.Kind != groupReadRequirementKind {
			continue
		}
		for _, kind := range row.FactKinds {
			factKind := FactKind(kind)
			if factKind == "" {
				continue
			}
			if _, known := seen[factKind]; known {
				continue
			}
			seen[factKind] = struct{}{}
			requirements = append(requirements, FactRequirement{Kind: factKind})
		}
	}
	return requirements
}

// authorizeCohortGroups asks whether this principal may see each constructed
// group, and returns the ones admitted.
//
// It asks the SAME question, of the SAME port, in the same shape the answer
// reuse recheck already asks it (reuseAuthorizationStillHolds): resolve the
// subjects by canonical-id hint and keep what comes back committed. There is
// no separate permission verdict in this system to consult -- a subject the
// principal cannot see does not resolve -- so a second mechanism here would be
// a second authority for one decision.
//
// It runs on the CONSTRUCTED group identities, after grouping, which is the
// first moment those identities exist: a member's owning group is read off
// that member's own facts.
func (e *Engine) authorizeCohortGroups(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpretation InterpretedQuestion, binding ResolvedGraphBinding, groups []contractsv1.ContextFabricCohortGroup) ([]SubjectRef, error) {
	hints := make([]SubjectHint, 0, len(groups))
	for _, group := range groups {
		hints = append(hints, SubjectHint{
			Kind:   group.Subject.Kind,
			ID:     group.Subject.CanonicalID,
			Label:  group.Subject.Label,
			Source: string(hintsource.CohortGroupAuthorization),
		})
	}
	authorizationRequest := request
	authorizationRequest.Options = reuseRecheckOptions
	authorizationRequest.RequestedScope.SubjectHints = hints
	// NO FRAME, no confirmed kind, no anchor -- for the same reason the reuse
	// recheck supplies none. This call asks only "may this principal see
	// these identities". Supplying the turn's frame would hint the pool
	// toward kinds this question's MEMBERS are about, which is not what is
	// being authorized here.
	resolution, _, _, _, err := e.graph.ResolveSubjects(ctx, principal, authorizationRequest, interpretation, binding, nil, nil, nil, "")
	if err != nil {
		return nil, err
	}
	// Admitted by IDENTITY, not by count. A resolution that returned the
	// right NUMBER of subjects and the wrong ones would pass a count check
	// and then read facts about a group nobody asked for.
	proposed := make(map[string]SubjectRef, len(groups))
	for _, group := range groups {
		proposed[group.Subject.CanonicalID] = group.Subject
	}
	admitted := make([]SubjectRef, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, committed := range resolution.Committed {
		subject, wasProposed := proposed[committed.CanonicalID]
		if !wasProposed || subject.Kind != committed.Kind {
			// The resolver returned something this turn did not ask about.
			// Silently reading facts for it would make the authorization
			// step a widening rather than a filter.
			continue
		}
		if _, duplicate := seen[committed.CanonicalID]; duplicate {
			continue
		}
		seen[committed.CanonicalID] = struct{}{}
		admitted = append(admitted, subject)
	}
	return admitted, nil
}

// readAdmittedGroupFacts issues the ONE group-rooted fact request this turn is
// allowed, and returns what the group stage decided along with the bundle.
//
// The order is bound, then authorize, then read, and it is load-bearing at
// every step. The bound is checked before any I/O because a bound enforced
// after the read has already paid the cost it exists to avoid. Authorization
// precedes the read because a group the principal may not see must never reach
// a provider. And there is exactly ONE read for the whole admitted set rather
// than one per group, because a per-group fan-out is a different cost profile
// than this design accepts.
func (e *Engine) readAdmittedGroupFacts(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpretation InterpretedQuestion, binding ResolvedGraphBinding, plan AnswerPlan, cohort *Cohort, window *contractsv1.ContextFabricEffectiveEvidenceWindow) (CanonicalFactBundle, groupReadOutcome, error) {
	outcome := groupReadOutcome{Proposed: len(cohort.Groups)}
	if outcome.Proposed == 0 {
		return CanonicalFactBundle{}, outcome, nil
	}

	// THE BOUND, BEFORE ANY I/O. Never sliced: an answer about the first 250
	// of 251 groups is an answer to a question nobody asked, and the caller
	// has no way to tell it from a complete one.
	if outcome.Proposed > contractsv1.ContextFabricCohortGroupsMaxCount {
		outcome.Refused, outcome.Reason = true, GroupReadRefusalOverContractBound
		return CanonicalFactBundle{}, outcome, nil
	}

	requirements := groupReadRequirements(plan)
	if len(requirements) == 0 {
		outcome.Refused, outcome.Reason = true, GroupReadRefusalNoReadRequirement
		return CanonicalFactBundle{}, outcome, nil
	}

	admitted, err := e.authorizeCohortGroups(ctx, principal, request, interpretation, binding, cohort.Groups)
	if err != nil {
		// FAIL CLOSED. An authorizer that could not answer is not an
		// authorizer that said yes.
		outcome.Refused, outcome.Reason = true, GroupReadRefusalAuthorizationUnavailable
		return CanonicalFactBundle{}, outcome, nil
	}
	outcome.Admitted = admitted
	outcome.Denied = outcome.Proposed - len(admitted)
	if len(admitted) == 0 {
		outcome.Refused, outcome.Reason = true, GroupReadRefusalNoGroupAdmitted
		return CanonicalFactBundle{}, outcome, nil
	}

	// Rooted on the admitted GROUPS and nothing else. Cohort is deliberately
	// nil: the cohort describes the MEMBER population, and passing it here
	// would let a provider widen a group read back onto the members, which is
	// the projection this whole stage exists to remove.
	bundle, err := e.facts.ReadFacts(ctx, principal, CanonicalFactRequest{
		Question:     factReadQuestion(interpretation, window),
		Subjects:     admitted,
		Requirements: requirements,
	})
	outcome.Read = true
	if err != nil {
		return bundle, outcome, err
	}
	return bundle, outcome, nil
}

// CohortGroupReadEvent is the ONE line that makes the group stage's whole
// decision graph readable from the trace: what was proposed before anything
// narrowed it, what the authorizer admitted and denied, whether a provider was
// actually asked, and -- when it was not -- the named reason.
//
// Every field is here because its absence would make two different states
// indistinguishable. Proposed against Admitted separates "the principal may
// see one of three teams" from "only one team exists". Denied is carried
// explicitly rather than left to subtraction so a future field cannot make the
// arithmetic wrong silently. Read separates a read that returned nothing from
// a read that never happened -- FactsReturned=0 is true of both, and they are
// opposite diagnoses. ContractBound travels with the count it bounds so a
// reader of one line can tell a refusal at the edge from a refusal far past
// it, without knowing this build's constant.
type CohortGroupReadEvent struct {
	Family        QuestionFamily
	GroupKind     SubjectKind
	Proposed      int
	Admitted      int
	Denied        int
	Read          bool
	Refused       bool
	Refusal       GroupReadRefusal
	FactsReturned int
	ContractBound int
}

// recordCohortGroupRead emits the group stage's decision, on EVERY grouped
// turn that reached the stage -- served, refused, or admitted-nothing.
//
// Unconditional on purpose. A line emitted only on the interesting path makes
// silence ambiguous between "this did not happen" and "this build no longer
// emits it", and the group read is exactly the decision an operator needs to
// be able to count.
func (e *Engine) recordCohortGroupRead(ctx context.Context, principal storage.Principal, event CohortGroupReadEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordCohortGroupRead(ctx, principal, event)
}

// mergeGroupBundle folds the group read's bundle into the turn's, and reports
// whether the two can be composed at all.
//
// EVERY carrier the bundle has must survive composition, not just the facts.
// A group fact whose provider version never reached the turn's version map is
// evidence with no provenance -- and the failure would be silent, because the
// facts themselves look complete. The three carriers below are exactly the
// ones a second read can contribute that the first also has an opinion about.
//
// Conflicting opaque metadata is REFUSED rather than resolved. Neither read is
// the authority on the other's version string, and picking one -- newest, last
// writer, the group read because it ran second -- would be inventing an
// ordering that no producer declared. The turn keeps the member evidence,
// which is complete and internally consistent, and discloses that the group
// read could not be composed.
func mergeGroupBundle(into *CanonicalFactBundle, group CanonicalFactBundle, orgID string) (conflicted bool) {
	// Checked BEFORE anything is written, so a refusal leaves the turn's
	// bundle exactly as it was rather than half-merged.
	for kind, version := range group.Versions {
		if existing, known := into.Versions[kind]; known && existing != version {
			return true
		}
	}
	for kind, watermark := range group.Watermarks {
		if existing, known := into.Watermarks[kind]; known && existing != watermark {
			return true
		}
	}

	into.Facts = append(into.Facts, group.Facts...)
	into.Coverage = MergeCoverage(orgID, into.Coverage, group.Coverage)
	if into.Versions == nil {
		into.Versions = make(map[FactKind]string, len(group.Versions))
	}
	for kind, version := range group.Versions {
		into.Versions[kind] = version
	}
	if into.Watermarks == nil {
		into.Watermarks = make(map[FactKind]string, len(group.Watermarks))
	}
	for kind, watermark := range group.Watermarks {
		into.Watermarks[kind] = watermark
	}
	// COARSEST, not the group read's own. An answer is only as precise as
	// its least precise source, and the group read is now one of the
	// sources this answer is built from -- so a day-grain group read must
	// coarsen an instant-grain member read, never the other way round.
	into.TemporalGrain = coarsestGrain(into.TemporalGrain, group.TemporalGrain)
	return false
}

// GroupReadCoverageStateEvent is ONE read's observation of ONE source, emitted
// BEFORE the two reads' coverage is folded together.
//
// It exists because the fold is lossy in a way nothing downstream can undo.
// Both reads request the same fact kinds, so both report coverage under the
// same `canonical_fact:<kind>` source names, and MergeCoverage keeps the WORST
// state per name. That is the conservative direction and it is the right
// default for the served answer -- but it means a group gap ERASES the member
// read's `available`, and a reader of the merged coverage cannot tell "neither
// population had health data" from "the members had it and the groups did
// not". Those are different answers to different questions.
//
// The served document is not widened to carry both: doing that needs a new
// coverage-detail code or a widened field allowance, and both are contract
// tokens. This line is the trace-side answer to the same question, and a log
// line is not a token.
//
// Read is the discriminator the whole event exists for -- `member` or `group`
// -- and it is why one line per observation is emitted rather than one line
// carrying a summary of both: a consumer aggregating coverage by state needs
// each observation to stand alone with its own values.
type GroupReadCoverageStateEvent struct {
	Family    QuestionFamily
	GroupKind SubjectKind
	// Read is which of the turn's two reads made this observation.
	Read GroupReadArm
	// Source is the coverage source name, and State the state that read
	// observed for it -- the pair the fold collapses.
	Source string
	State  SourceState
}

// GroupReadArm names which read an observation came from. Closed, because it
// reaches a telemetry field consumers group on.
type GroupReadArm string

const (
	// GroupReadArmMember is the turn's first read, rooted on the cohort's
	// members.
	GroupReadArmMember GroupReadArm = "member"
	// GroupReadArmGroup is the second read, rooted on the admitted group
	// identities.
	GroupReadArmGroup GroupReadArm = "group"
)

var canonicalGroupReadArms = map[GroupReadArm]GroupReadArm{
	GroupReadArmMember: GroupReadArmMember,
	GroupReadArmGroup:  GroupReadArmGroup,
}

// ValidGroupReadArm reports membership in the closed vocabulary.
func ValidGroupReadArm(value GroupReadArm) bool {
	_, member := canonicalGroupReadArms[value]
	return member
}

// recordGroupReadCoverageStates emits both reads' per-source states, in a
// deterministic order, before the fold that collapses them.
//
// Emitted for EVERY observation of both reads, not only the ones that differ.
// A line emitted only on disagreement would make silence mean both "the two
// reads agreed" and "this build stopped emitting", and it would also deny a
// reader the baseline they need to interpret the disagreements that do appear.
func (e *Engine) recordGroupReadCoverageStates(ctx context.Context, principal storage.Principal, family QuestionFamily, groupKind SubjectKind, member, group Coverage) {
	if e.telemetry == nil {
		return
	}
	emit := func(arm GroupReadArm, coverage Coverage) {
		for _, observation := range coverage.Sources {
			e.telemetry.RecordGroupReadCoverageState(ctx, principal, GroupReadCoverageStateEvent{
				Family: family, GroupKind: groupKind, Read: arm,
				Source: observation.Source, State: observation.State,
			})
		}
	}
	emit(GroupReadArmMember, member)
	emit(GroupReadArmGroup, group)
}

// CohortMemberAllowanceEvent reports how many cohort members this turn's item
// budget actually admits, and whether that number was CLAMPED rather than
// computed.
//
// The allowance is `MaxItems - SynthesisHeadroom`, and a grouped plan reserves
// a headroom of twenty. So every grouped turn whose budget is at or below that
// headroom gets an allowance of ONE -- not because one member is what the
// budget affords, but because the subtraction went to zero or below and the
// floor caught it. Stage 2's set cover then reduces the cohort to one member
// per group, and a reader of the answer sees a single project under each team
// with nothing anywhere saying why.
//
// Every field is here because its absence leaves two states looking alike.
// MaxItems and Headroom together are the only way to tell "this budget is
// genuinely small" from "this budget is large and the reserve ate it".
// Clamped separates a computed allowance from the floor. Groups explains why
// MembersAfter does not simply equal Allowance -- the set cover keeps one
// member per group, so a cohort narrowed to an allowance of one still carries
// as many members as it has groups.
type CohortMemberAllowanceEvent struct {
	Family    QuestionFamily
	GroupKind SubjectKind
	// MaxItems and Headroom are the two inputs to the subtraction.
	MaxItems int
	Headroom int
	// Allowance is what the plan will narrow to, and Clamped reports that
	// the subtraction produced less than one and the floor supplied this
	// value instead.
	Allowance int
	Clamped   bool
	// Groups, MembersBefore and MembersAfter are what the allowance did to
	// this particular cohort.
	Groups        int
	MembersBefore int
	MembersAfter  int
}

// recordCohortMemberAllowance emits the allowance decision on EVERY turn that
// has a cohort, narrowed or not.
//
// Unconditional on purpose: the clamp happens while computing the allowance,
// not while applying it, so a turn whose cohort was already small enough not
// to narrow was clamped just as hard as one that was cut -- and emitting only
// on narrowing would hide exactly the turns where the reader wonders why the
// answer is so thin.
func (e *Engine) recordCohortMemberAllowance(ctx context.Context, principal storage.Principal, event CohortMemberAllowanceEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordCohortMemberAllowance(ctx, principal, event)
}

// cohortMemberAllowanceClamped reports whether the plan's member allowance was
// supplied by the floor rather than by the subtraction.
//
// It mirrors PlanAnswer's own arithmetic rather than reading a flag, because
// no flag exists: the plan carries the RESULT, and "one member because the
// budget affords one" and "one member because the reserve consumed the budget"
// are the same number there. This is the one place the difference is
// recoverable, and it is recovered from the plan's own published inputs.
func cohortMemberAllowanceClamped(budget contractsv1.ContextFabricAnswerPlanBudget) bool {
	return budget.MaxItems > 0 && budget.MaxItems-budget.SynthesisHeadroom < 1
}
