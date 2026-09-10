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
