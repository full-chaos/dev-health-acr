package contextfabric

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ProjectNarrowingResolver is the optional graph capability that resolves a
// request's raw scope.project_ids to canonical project.v2 identities through
// the project nodes (CHAOS-12229, lead ruling Q2 (a): no contract change).
// A graph reader without it leaves a requested project narrowing unresolved,
// and the narrowing keeps the unverified path: admitted, disclosed
// (read_population_unverified) and floored to partial. It is optional so a
// reader that cannot resolve degrades to that path instead of failing.
type ProjectNarrowingResolver interface {
	ResolveProjectNarrowing(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, projectIDs []string) (*contractsv1.ContextFabricProjectNarrowing, error)
}

// ProjectNarrowingOutcome is the closed vocabulary of one investigation's
// project narrowing resolution.
type ProjectNarrowingOutcome string

const (
	// ProjectNarrowingResolved: every requested id resolved to exactly one
	// identity; stated membership decides every node.
	ProjectNarrowingResolved ProjectNarrowingOutcome = "resolved"
	// ProjectNarrowingIncomplete: at least one requested id resolved to no
	// identity or to more than one; a stated non-member is still admitted
	// and disclosed, never denied on a guess.
	ProjectNarrowingIncomplete ProjectNarrowingOutcome = "incomplete"
	// ProjectNarrowingResolverAbsent: the graph reader cannot resolve; the
	// narrowing stays unverified.
	ProjectNarrowingResolverAbsent ProjectNarrowingOutcome = "resolver_absent"
	// ProjectNarrowingFailed: the resolution read failed; the narrowing stays
	// unverified. Nothing is widened.
	ProjectNarrowingFailed ProjectNarrowingOutcome = "failed"
)

// ProjectNarrowingOutcomeVocabulary is the closed set the resolution line's
// outcome field takes.
func ProjectNarrowingOutcomeVocabulary() []string {
	return []string{string(ProjectNarrowingResolved), string(ProjectNarrowingIncomplete), string(ProjectNarrowingResolverAbsent), string(ProjectNarrowingFailed)}
}

// ProjectNarrowingResolutionEvent is one investigation's resolution of its
// requested project narrowing: counts only, never ids.
type ProjectNarrowingResolutionEvent struct {
	Outcome            ProjectNarrowingOutcome
	RequestedIDs       int
	ResolvedIdentities int
}

// resolveRequestedProjectNarrowing resolves a requested project narrowing
// once per investigation, before any graph read, and returns the scope every
// read of this investigation uses (CHAOS-12229). A scope with no project ids,
// one with a requested "*" (it narrows nothing), or one already resolved is
// returned unchanged. A failed or absent resolver leaves the scope
// unresolved, which is the unverified path, and the line says so.
func (e *Engine) resolveRequestedProjectNarrowing(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, scope RequestedScope) RequestedScope {
	if scope.ProjectNarrowing != nil {
		return scope
	}
	requested := 0
	for _, id := range scope.ProjectIDs {
		switch strings.TrimSpace(id) {
		case "":
		case "*":
			return scope
		default:
			requested++
		}
	}
	if requested == 0 {
		return scope
	}
	event := ProjectNarrowingResolutionEvent{RequestedIDs: requested}
	resolver, ok := e.graph.(ProjectNarrowingResolver)
	switch {
	case !ok:
		event.Outcome = ProjectNarrowingResolverAbsent
	default:
		narrowing, err := resolver.ResolveProjectNarrowing(ctx, principal, binding, scope.ProjectIDs)
		switch {
		case err != nil || narrowing == nil:
			event.Outcome = ProjectNarrowingFailed
		default:
			scope.ProjectNarrowing = narrowing
			event.ResolvedIdentities = len(narrowing.Identities)
			event.Outcome = ProjectNarrowingIncomplete
			if narrowing.Complete {
				event.Outcome = ProjectNarrowingResolved
			}
		}
	}
	if e.telemetry != nil {
		e.telemetry.RecordProjectNarrowingResolution(ctx, principal, event)
	}
	return scope
}
