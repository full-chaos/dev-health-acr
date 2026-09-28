package contextfabric

import (
	"context"
	"fmt"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// FactRootRefusalRecorder is the optional, loud side of the fact-root
// re-check (CHAOS-7080). A refusal here means resolution committed a subject
// the caller's grant does not admit -- which the shared predicate is built to
// make impossible -- so it is written at Warn, apart from the Info decision
// line every re-check writes (surface fact_root_recheck).
type FactRootRefusalRecorder interface {
	RecordFactRootRefused(ctx context.Context, principal storage.Principal, decision StoredResultAuthorization, refused int)
}

// recheckCommittedRoots re-decides the turn's committed root subjects, live,
// for a repository-restricted caller, before the fact registry (which
// authorizes no subject: it checks only the organization id) reads facts for
// them. It is the stored-result gate's own decision (the shared predicate,
// over the caller's graph, with each project's live ownership reach).
//
// A refused (denied or absent) root is removed from the resolution -- from
// Committed and from Candidates -- so the turn continues on its admitted
// roots, or, with none left, ends on the ordinary zero-subject terminal: the
// same shape a resolver refusal gives, never a status code that would signal
// the subject exists. The refusal itself is loud: an Info decision line and a
// Warn line.
//
// Unrestricted and universal ("*") callers are not re-checked: the shared
// predicate applies no repository check to them, so their turns are
// unchanged. A decision that cannot be taken fails closed.
func (e *Engine) recheckCommittedRoots(ctx context.Context, principal storage.Principal, resolution SubjectResolution) (SubjectResolution, error) {
	if len(resolution.Committed) == 0 || classifyStoredResultPrincipalScope(principal) != StoredResultScopeRestricted {
		return resolution, nil
	}
	var all InvestigationResult
	all.SubjectResolution.Committed = resolution.Committed
	decision := e.storedResultGate.decide(ctx, principal, all, StoredResultSurfaceFactRootRecheck)
	if e.telemetry != nil {
		e.telemetry.RecordStoredResultAuthorization(ctx, principal, decision)
	}
	switch decision.Decision {
	case StoredResultAdmitted:
		return resolution, nil
	case StoredResultDenied:
	default:
		if decision.Err != nil {
			return resolution, fmt.Errorf("%w: fact root authorization %s: %w", ErrUnavailable, decision.Reason, decision.Err)
		}
		return resolution, fmt.Errorf("%w: fact root authorization %s", ErrUnavailable, decision.Reason)
	}

	// Which roots: one decision per root (a turn commits a handful).
	refused := map[string]struct{}{}
	for _, root := range resolution.Committed {
		var one InvestigationResult
		one.SubjectResolution.Committed = []SubjectRef{root}
		switch per := e.storedResultGate.decide(ctx, principal, one, StoredResultSurfaceFactRootRecheck); per.Decision {
		case StoredResultAdmitted:
		case StoredResultDenied:
			refused[SubjectMapKey(root)] = struct{}{}
		default:
			if per.Err != nil {
				return resolution, fmt.Errorf("%w: fact root authorization %s: %w", ErrUnavailable, per.Reason, per.Err)
			}
			return resolution, fmt.Errorf("%w: fact root authorization %s", ErrUnavailable, per.Reason)
		}
	}
	if len(refused) == 0 {
		// The aggregate refused and no single root did: fail closed rather
		// than serve on an inconsistent decision.
		return resolution, fmt.Errorf("%w: fact root authorization inconsistent (reason %s)", ErrUnavailable, decision.Reason)
	}
	if recorder, ok := e.telemetry.(FactRootRefusalRecorder); ok {
		recorder.RecordFactRootRefused(ctx, principal, decision, len(refused))
	}
	isRefused := func(subject SubjectRef) bool {
		_, hit := refused[SubjectMapKey(subject)]
		return hit
	}
	filtered := resolution
	filtered.Committed = slices.DeleteFunc(slices.Clone(resolution.Committed), isRefused)
	filtered.Candidates = slices.DeleteFunc(slices.Clone(resolution.Candidates), func(candidate SubjectCandidate) bool {
		return isRefused(candidate.Subject)
	})
	return filtered, nil
}
