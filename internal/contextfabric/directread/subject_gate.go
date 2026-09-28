package directread

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// MaxSubjectsPerRequest bounds one gate decision. A direct read that names
// more subjects is refused whole, before any graph read.
const MaxSubjectsPerRequest = 256

// Authorization is one gate decision and everything a trace needs to rebuild
// it. It carries counts and kinds only, never ids or labels.
type Authorization struct {
	PrincipalClass       PrincipalClass
	RepositoryScopeCount int
	Decision             Decision
	Reason               Reason
	// SubjectCount is the number of distinct requested subjects.
	SubjectCount              int
	AdmittedCount             int
	DeniedCount               int
	AbsentCount               int
	OwnershipUnprovenCount    int
	OrganizationMismatchCount int
	InvalidCount              int
	// RefusedKinds is the sorted set of known subject kinds that were not
	// admitted.
	RefusedKinds []string
	// Outcomes holds one entry per distinct requested subject, in first-seen
	// order. Label is dropped: a caller-supplied label never flows on.
	Outcomes []GatedSubject
	// Err is the plane failure behind an unavailable decision.
	Err error
}

// GatedSubject is one subject's outcome.
type GatedSubject struct {
	Subject contextfabric.SubjectRef
	Outcome SubjectOutcome
}

// ErrorClass names Err for the trace, or "" when there is none.
func (a Authorization) ErrorClass() string {
	switch {
	case a.Err == nil:
		return ""
	case errors.Is(a.Err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(a.Err, context.Canceled):
		return "canceled"
	case errors.Is(a.Err, contextfabric.ErrUnavailable):
		return "dependency_unavailable"
	default:
		return "graph_error"
	}
}

// GraphAuthority is the graph side of the gate. falkorgraph.Adapter
// implements it. Every method reads the CALLER's own organization graph.
type GraphAuthority interface {
	ResolveInvestigationBinding(ctx context.Context, principal storage.Principal) (contextfabric.ResolvedGraphBinding, error)
	// AuthorizeStoredSubjects is the shared per-node decision
	// (graphrank.AuthorizeStoredSubjectNodes over the caller's graph).
	contextfabric.StoredSubjectAuthorizer
	// OwnershipReachedRepositories returns, per subject, the repository
	// slugs the subject reaches through ownership: a team's own
	// ownership-derived repository list (never the "*" wildcard), and for a
	// project the union of those lists over the teams its CURRENT
	// OWNED_BY_TEAM edges name. It is called with team and project subjects
	// only. A subject with no ownership reach gets an empty list.
	OwnershipReachedRepositories(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error)
}

// Recorder receives every gate decision. The API writes the event
// "context fabric direct read authorization" through it.
type Recorder interface {
	RecordDirectReadAuthorization(ctx context.Context, principal storage.Principal, decision Authorization)
}

// SubjectGate is the mandatory subject gate for every direct read.
type SubjectGate struct {
	graph    GraphAuthority
	recorder Recorder
}

// NewSubjectGate builds a gate. A nil graph is allowed and makes every
// decision that needs the graph unavailable (fail closed); a nil recorder
// records nothing.
func NewSubjectGate(graph GraphAuthority, recorder Recorder) *SubjectGate {
	gate := &SubjectGate{recorder: recorder}
	if !storage.IsNil(graph) {
		gate.graph = graph
	}
	return gate
}

// Authorize takes the live decision for requested, for this principal, now.
// It is called once per request; the returned AuthorizedSubjects is bound to
// the principal and holds only admitted subjects. It never returns an error:
// an unavailable decision is a Decision value, and its AuthorizedSubjects is
// empty.
func (g *SubjectGate) Authorize(ctx context.Context, principal storage.Principal, requested []contextfabric.SubjectRef) (AuthorizedSubjects, Authorization) {
	decision := g.decide(ctx, principal, requested)
	if g != nil && g.recorder != nil {
		g.recorder.RecordDirectReadAuthorization(ctx, principal, decision)
	}
	if decision.Decision != DecisionAdmitted && decision.Decision != DecisionPartial {
		return AuthorizedSubjects{}, decision
	}
	admitted := make([]contextfabric.SubjectRef, 0, decision.AdmittedCount)
	for _, gated := range decision.Outcomes {
		if gated.Outcome == SubjectAdmitted {
			admitted = append(admitted, gated.Subject)
		}
	}
	return issue(principal, admitted), decision
}

func (g *SubjectGate) decide(ctx context.Context, principal storage.Principal, requested []contextfabric.SubjectRef) Authorization {
	decision := Authorization{
		PrincipalClass:       ClassifyPrincipal(principal),
		RepositoryScopeCount: len(principal.RepositoryScopes),
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		decision.Decision, decision.Reason = DecisionDenied, ReasonPrincipalInvalid
		return finish(decision)
	}

	// Distinct subjects, first-seen order, label dropped.
	seen := map[string]struct{}{}
	for _, subject := range requested {
		ref := contextfabric.SubjectRef{Kind: subject.Kind, CanonicalID: strings.TrimSpace(subject.CanonicalID)}
		key := string(ref.Kind) + "\x00" + ref.CanonicalID
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		decision.Outcomes = append(decision.Outcomes, GatedSubject{Subject: ref})
	}
	decision.SubjectCount = len(decision.Outcomes)
	switch {
	case decision.SubjectCount == 0:
		decision.Decision, decision.Reason = DecisionDenied, ReasonNoSubjects
		return finish(decision)
	case decision.SubjectCount > MaxSubjectsPerRequest:
		decision.Decision, decision.Reason = DecisionDenied, ReasonSubjectLimit
		for index := range decision.Outcomes {
			decision.Outcomes[index].Outcome = SubjectInvalid
		}
		return finish(decision)
	}

	var graphIndexes []int
	for index, gated := range decision.Outcomes {
		switch {
		case gated.Subject.CanonicalID == "" || !contractsv1.ValidContextFabricSubjectKind(gated.Subject.Kind):
			decision.Outcomes[index].Outcome = SubjectInvalid
		case gated.Subject.Kind == contractsv1.ContextFabricSubjectOrganization:
			if contextfabric.OrganizationSubjectIsCallers(principal, gated.Subject) {
				decision.Outcomes[index].Outcome = SubjectAdmitted
			} else {
				decision.Outcomes[index].Outcome = SubjectOrganizationMismatch
			}
		default:
			graphIndexes = append(graphIndexes, index)
		}
	}

	if len(graphIndexes) > 0 {
		if g == nil || g.graph == nil {
			decision.Decision, decision.Reason = DecisionUnavailable, ReasonAuthorizerMissing
			return finish(decision)
		}
		graphSubjects := make([]contextfabric.SubjectRef, len(graphIndexes))
		for position, index := range graphIndexes {
			graphSubjects[position] = decision.Outcomes[index].Subject
		}
		binding, err := g.graph.ResolveInvestigationBinding(ctx, principal)
		var outcomes []contextfabric.StoredSubjectOutcome
		if err == nil {
			outcomes, err = g.graph.AuthorizeStoredSubjects(ctx, principal, binding, graphSubjects)
		}
		switch {
		case errors.Is(err, contextfabric.ErrGraphNotProjected):
			// A graph that was never projected holds no node: every graph
			// subject is absent. This is a refusal, not an unavailability.
			for _, index := range graphIndexes {
				decision.Outcomes[index].Outcome = SubjectAbsent
			}
			decision.Reason = ReasonGraphNotProjected
		case err != nil:
			decision.Decision, decision.Reason, decision.Err = DecisionUnavailable, ReasonGraphReadFailed, err
			return finish(decision)
		case len(outcomes) != len(graphSubjects):
			decision.Decision, decision.Reason = DecisionUnavailable, ReasonGraphReadFailed
			decision.Err = fmt.Errorf("subject authorizer returned %d outcomes for %d subjects", len(outcomes), len(graphSubjects))
			return finish(decision)
		default:
			var groupIndexes []int
			for position, outcome := range outcomes {
				index := graphIndexes[position]
				switch outcome {
				case contextfabric.StoredSubjectAdmitted:
					decision.Outcomes[index].Outcome = SubjectAdmitted
					if needsOwnershipReach(decision.PrincipalClass, decision.Outcomes[index].Subject.Kind) {
						groupIndexes = append(groupIndexes, index)
					}
				case contextfabric.StoredSubjectDenied:
					decision.Outcomes[index].Outcome = SubjectDenied
				default:
					decision.Outcomes[index].Outcome = SubjectAbsent
				}
			}
			if len(groupIndexes) > 0 {
				groups := make([]contextfabric.SubjectRef, len(groupIndexes))
				for position, index := range groupIndexes {
					groups[position] = decision.Outcomes[index].Subject
				}
				reached, err := g.graph.OwnershipReachedRepositories(ctx, principal, binding, groups)
				switch {
				case err != nil:
					decision.Decision, decision.Reason, decision.Err = DecisionUnavailable, ReasonGraphReadFailed, err
					return finish(decision)
				case len(reached) != len(groups):
					decision.Decision, decision.Reason = DecisionUnavailable, ReasonGraphReadFailed
					decision.Err = fmt.Errorf("ownership reach returned %d entries for %d subjects", len(reached), len(groups))
					return finish(decision)
				}
				for position, index := range groupIndexes {
					if !ownershipReachAdmits(principal, reached[position]) {
						decision.Outcomes[index].Outcome = SubjectOwnershipUnproven
					}
				}
			}
		}
	}
	return finish(decision)
}

// needsOwnershipReach: a team or project is admitted to a restricted caller
// only through a repository it reaches by ownership. An unrestricted or
// universal caller holds every repository, so the node check suffices.
func needsOwnershipReach(class PrincipalClass, kind contextfabric.SubjectKind) bool {
	return class == ClassRestricted && (kind == contractsv1.ContextFabricSubjectTeam || kind == contractsv1.ContextFabricSubjectProject)
}

// ownershipReachAdmits applies the SHARED predicate to the reached
// repositories, as the list-typed authorization_repositories attribute it
// already reads. An empty reach never admits: an empty list denies under the
// predicate's own convention, and the wildcard is never built here.
func ownershipReachAdmits(principal storage.Principal, reached []string) bool {
	if len(reached) == 0 {
		return false
	}
	attributes := map[string]interface{}{"authorization_repositories": append([]string(nil), reached...)}
	return graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, attributes)
}

// finish counts the outcomes and settles the decision and reason.
func finish(decision Authorization) Authorization {
	refused := map[string]struct{}{}
	for _, gated := range decision.Outcomes {
		switch gated.Outcome {
		case SubjectAdmitted:
			decision.AdmittedCount++
			continue
		case SubjectDenied:
			decision.DeniedCount++
		case SubjectAbsent:
			decision.AbsentCount++
		case SubjectOwnershipUnproven:
			decision.OwnershipUnprovenCount++
		case SubjectOrganizationMismatch:
			decision.OrganizationMismatchCount++
		case SubjectInvalid:
			decision.InvalidCount++
		default:
			// Undecided: only on an early unavailable/denied return.
			continue
		}
		if contractsv1.ValidContextFabricSubjectKind(gated.Subject.Kind) {
			refused[string(gated.Subject.Kind)] = struct{}{}
		}
	}
	decision.RefusedKinds = make([]string, 0, len(refused))
	for kind := range refused {
		decision.RefusedKinds = append(decision.RefusedKinds, kind)
	}
	sort.Strings(decision.RefusedKinds)
	if decision.Decision != "" {
		return decision
	}
	refusedCount := decision.SubjectCount - decision.AdmittedCount
	switch {
	case refusedCount == 0:
		decision.Decision, decision.Reason = DecisionAdmitted, ReasonSubjectsAdmitted
		return decision
	case decision.AdmittedCount == 0:
		decision.Decision = DecisionDenied
	default:
		decision.Decision = DecisionPartial
	}
	if decision.Reason == ReasonGraphNotProjected {
		return decision
	}
	switch {
	case decision.OrganizationMismatchCount > 0:
		decision.Reason = ReasonOrganizationMismatch
	case decision.DeniedCount > 0:
		decision.Reason = ReasonSubjectDenied
	case decision.OwnershipUnprovenCount > 0:
		decision.Reason = ReasonOwnershipUnproven
	case decision.AbsentCount > 0:
		decision.Reason = ReasonSubjectAbsent
	default:
		decision.Reason = ReasonSubjectInvalid
	}
	return decision
}
