package graphrank

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A work item a hint names under a requested repository scope is in that scope
// when it is linked to a pull request of a repository the scope names: the
// entity tree (Repository <> Pull request <> Issue <> Project) relates an issue
// to a repository only through its linked pull requests, never through the
// issue's own repository column (CHAOS-12706). So the hint is tested as the
// census tests its satisfiers (census_link_scope.go): AuthorizedThroughLink for
// the caller's grants and every other part of the request, and the keyed link
// read for the repository part. A failed, cut or unwired read admits nothing,
// as it admits nothing to the census.
//
// The answer reuse recheck keeps the issue's own node test: a stored answer's
// subject that the census admitted through its link is not re-admitted by the
// recheck, so the stored answer is not reused and the fresh turn's census reads
// the link again (TestAReuseRecheckDoesNotCarryALinkAdmission).

// hintThroughLink reports whether a hint is tested through the link: a work
// item hint under a requested repository scope, from any source but the answer
// reuse recheck.
func hintThroughLink(request contextfabric.InvestigationRequest, kind contextfabric.SubjectKind, source string) bool {
	return kind == contextfabric.SubjectWorkItem && len(request.RequestedScope.RepositorySlugs) > 0 &&
		hintsource.Source(source) != hintsource.AnswerReuseAuthorizationRecheck
}

// hintLinks is the keyed link read of the request's repository scope for the
// hints of one resolution, made when the first work item hint needs it. What it
// decided for an id is kept, and a failed or cut read fails every later hint, as
// it fails every later call of a census round.
type hintLinks struct {
	built bool
	scope *linkScope
}

func (h *hintLinks) read(deps ResolveDeps, scope contextfabric.RequestedScope) *linkScope {
	if !h.built {
		h.built, h.scope = true, newLinkScope(deps, scope)
	}
	return h.scope
}

// hintAdmission is what one hinted node gives under the request.
type hintAdmission struct {
	candidate contextfabric.SubjectCandidate
	// admitted: the node is a candidate the request may hold.
	admitted bool
	// authorized: the caller's grants and the request admit the node itself,
	// for the authorization-drop count. A node the link read leaves out is
	// outside the scope, not unauthorized.
	authorized bool
	// tier is the strongest link that put a work item hint in the requested
	// repository scope; "" for a hint not tested through the link.
	tier string
}

// admitHint tests one hinted node: through the link for a work item under a
// requested repository scope (hintThroughLink), and against the whole requested
// scope otherwise, as before.
func admitHint(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, links *hintLinks, kind contextfabric.SubjectKind, source, term string, node CandidateNode) hintAdmission {
	if !hintThroughLink(request, kind, source) {
		candidate, admitted := NodeCandidate(principal, request.RequestedScope, term, node, deps.IsInternal, true, deps.ResolutionTracer, request.RequestID)
		return hintAdmission{candidate: candidate, admitted: admitted, authorized: AuthorizedAttributes(principal, request.RequestedScope, node.Attributes)}
	}
	issueScope := issueScopeOfLink(request.RequestedScope)
	out := hintAdmission{authorized: AuthorizedThroughLink(principal, request.RequestedScope, node.Attributes)}
	candidate, admitted := NodeCandidate(principal, issueScope, term, node, deps.IsInternal, true, deps.ResolutionTracer, request.RequestID)
	if !admitted {
		return out
	}
	tiers, err := links.read(deps, request.RequestedScope).linked(ctx, []string{candidate.Subject.CanonicalID})
	tier := tiers[candidate.Subject.CanonicalID]
	if err != nil || tier == "" {
		return out
	}
	out.candidate, out.admitted, out.tier = candidate, true, tier
	return out
}

// recordHintLink records a work item hint the link read admitted, as the census
// records a satisfier it kept: the call's fact read then reads the item under
// the scope, and the answer states the scope and a link tier that is not native.
func recordHintLink(ctx context.Context, admission hintAdmission) {
	if admission.tier == "" {
		return
	}
	contextfabric.RecordWorkItemCensusRepositoryScope(ctx)
	contextfabric.RecordWorkItemCensusLinkedSatisfier(ctx, admission.candidate.Subject.CanonicalID, admission.tier)
}
