package contextfabric

import (
	"context"

	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrFactRootNotAuthorized (CHAOS-7080) is the refusal of a fact read whose
// committed root subject the caller's grant does not admit. It wraps
// ErrNoInvestigationSubjects, so the investigation route classifies it as the
// named ACR invariant breach (500, non-retryable, class no_subjects): reaching
// it means resolution committed a subject the caller may not see, which the
// shared predicate is built to make impossible.
var ErrFactRootNotAuthorized = fmt.Errorf("%w: committed root subject failed the fact-read authorization re-check", ErrNoInvestigationSubjects)

// recheckCommittedRoots re-decides every committed root subject, live, for a
// repository-restricted caller, before the fact registry (which authorizes
// no subject: it checks only the organization id) reads facts for it. It is
// the stored-result gate's own decision (the shared predicate, over the
// caller's graph, with each project's live ownership reach), so a root is
// admitted here exactly when a stored answer naming it would be served.
//
// Unrestricted and universal ("*") callers are not re-checked: the shared
// predicate applies no repository check to them, so their reads are
// unchanged. A decision that cannot be taken fails closed.
func (e *Engine) recheckCommittedRoots(ctx context.Context, principal storage.Principal, roots []SubjectRef) error {
	if len(roots) == 0 || classifyStoredResultPrincipalScope(principal) != StoredResultScopeRestricted {
		return nil
	}
	var result InvestigationResult
	result.SubjectResolution.Committed = roots
	decision := e.storedResultGate.decide(ctx, principal, result, StoredResultSurfacePriorResult)
	switch decision.Decision {
	case StoredResultAdmitted:
		return nil
	case StoredResultDenied:
		return fmt.Errorf("%w (reason %s, denied %d, absent %d, organization mismatch %d)", ErrFactRootNotAuthorized, decision.Reason, decision.DeniedCount, decision.AbsentCount, decision.OrganizationMismatchCount)
	default:
		if decision.Err != nil {
			return fmt.Errorf("%w: fact root authorization %s: %w", ErrUnavailable, decision.Reason, decision.Err)
		}
		return fmt.Errorf("%w: fact root authorization %s", ErrUnavailable, decision.Reason)
	}
}
