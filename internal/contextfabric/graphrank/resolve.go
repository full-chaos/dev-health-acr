package graphrank

import (
	"context"
	"errors"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ResolveDeps carries the backend I/O ResolveSubjects needs. Every function
// field is required.
type ResolveDeps struct {
	// ExactHint looks up a subject by exact canonical identity, scoped to
	// the calling organization. ok=false with a nil error means "not
	// found" -- a safe non-match, not an error.
	ExactHint func(ctx context.Context, subject contextfabric.SubjectRef) (node CandidateNode, ok bool, err error)
	// Search performs bounded node-scoped hybrid search for term.
	// truncated reports whether the backend's own bound on this result set
	// (e.g. a server-side row LIMIT) means genuinely competing candidates
	// could have been left out before ResolveSubjects ever saw them -- see
	// ResolveFromMergedCandidates' searchTruncated parameter for what that
	// means for auto-commit eligibility. A backend with no such bound (or
	// one that fetched a generous superset with no risk of missing a real
	// competitor) always reports false.
	Search func(ctx context.Context, term string, limit int) (candidates []CandidateNode, truncated bool, err error)
	// Traverse implements observation-to-entity traversal for a matched
	// document/episode node -- see TraverseObservationToSubject, which a
	// backend's own Traverse implementation should call with its own
	// GetNodeEdges/GetNode-equivalent I/O bound in.
	Traverse func(ctx context.Context, term string, observation CandidateNode) (contextfabric.SubjectCandidate, ObservationTraversal)
	// IsInternal reports whether subject is one of the backend's own
	// bookkeeping nodes (see NodeCandidate's isInternal parameter).
	IsInternal func(contextfabric.SubjectRef) bool
	// TraversalDegraded optionally reports how many Traverse calls in this
	// ResolveSubjects call ended in ObservationTraversalErrored. May be nil.
	TraversalDegraded func(ctx context.Context, orgID string, count int)
}

// ResolveSubjects resolves the committed/candidate subjects for an
// investigation request: exact caller-supplied hints first (short-circuiting
// on any caller-explicit one that resolves), then hybrid search over the
// interpreted subject terms with observation-to-entity traversal, merged and
// ranked by ResolveFromMergedCandidates. Ported from
// zepgraph.(*Adapter).ResolveSubjects's body, deduplicated so every graph
// backend shares the exact same resolution/ranking/ambiguity behavior.
func ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, deps ResolveDeps) (contextfabric.SubjectResolution, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.SubjectResolution{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.SubjectResolution{}, err
	}
	terms := SubjectTerms(request, interpreted)
	candidatesBySubject := make(map[string]contextfabric.SubjectCandidate)
	// callerSourced marks which resolved subjects came from a
	// caller-explicit hint -- any SubjectHint.Source other than
	// "prior_subject_receipt". A caller-explicit hint is an authoritative,
	// direct ask and keeps the short-circuit/truncation-priority behavior
	// below; a receipt-derived hint is not -- it is Engine's best guess at
	// what a conversational reference bound to previously, and the current
	// question may name a different subject entirely.
	callerSourced := make(map[string]bool)
	for _, hint := range request.RequestedScope.SubjectHints {
		if strings.TrimSpace(hint.ID) == "" || hint.Kind == "" {
			continue
		}
		subject := contextfabric.SubjectRef{Kind: hint.Kind, CanonicalID: strings.TrimSpace(hint.ID), Label: strings.TrimSpace(hint.Label)}
		if subject.Label == "" {
			subject.Label = subject.CanonicalID
		}
		if strings.TrimSpace(hint.Source) != "prior_subject_receipt" {
			callerSourced[SubjectKey(subject)] = true
		}
		node, ok, err := deps.ExactHint(ctx, subject)
		if err != nil {
			return contextfabric.SubjectResolution{}, err
		}
		if !ok {
			continue
		}
		candidate, ok := NodeCandidate(principal, request.RequestedScope, subject.Label, node, deps.IsInternal)
		if !ok {
			continue
		}
		candidate.Confidence = 1
		candidate.State = contextfabric.ResolutionCommitted
		candidate.MatchReasons = []string{"Exact canonical subject hint matched the organization graph."}
		candidatesBySubject[SubjectKey(candidate.Subject)] = candidate
	}
	// A caller-explicit hint that resolved is authoritative and
	// short-circuits here. A receipt-only resolution (candidatesBySubject
	// may be non-empty, but nothing in it came from a caller-explicit hint)
	// must NOT short-circuit -- it falls through to hybrid search below,
	// which merges into the same map, so a conversational follow-up naming
	// a different subject than the one a prior receipt bound can still be
	// found and compete on its own terms.
	if AnyCallerSourced(candidatesBySubject, callerSourced) {
		return FinalizeExactResolution(candidatesBySubject, callerSourced, request.Options.MaxSubjectCandidates), nil
	}
	// observationParentKey maps an observation (document/episode) subject
	// key to its found canonical parent's subject key -- only set when
	// traversal actually found one. observationBlocked marks an observation
	// subject key as NOT auto-commit-eligible, which is true both when a
	// parent was found (the parent should get to compete instead) and when
	// traversal errored (an unresolved traversal must fail toward
	// ambiguity, never toward treating the observation as if it were
	// confirmed parentless). traversalDegraded counts errored outcomes for
	// the optional telemetry report below.
	observationParentKey := make(map[string]string)
	observationBlocked := make(map[string]bool)
	traversalDegraded := 0
	// searchTruncated is true if ANY of this invocation's Search() calls
	// (one per entry in terms) reported truncation -- a subject found by an
	// UNtruncated call for one term can still merge over/replace a
	// truncated call's own (deliberately floor-capped, see falkorgraph's
	// fulltextSearchNodes) entry for the same subject a few lines below,
	// silently erasing the fact that truncation happened at all. Tracking
	// it here, independently of any one candidate's own data, is what lets
	// ResolveFromMergedCandidates treat truncation as a property of the
	// WHOLE resolution (Codex round 3 review of the D11/AC-3778-0 fix).
	searchTruncated := false
	for _, term := range terms {
		results, truncated, err := deps.Search(ctx, term, request.Options.MaxSubjectCandidates)
		if err != nil {
			return contextfabric.SubjectResolution{}, err
		}
		if truncated {
			searchTruncated = true
		}
		for _, node := range results {
			candidate, ok := NodeCandidate(principal, request.RequestedScope, term, node, deps.IsInternal)
			if !ok {
				continue
			}
			key := SubjectKey(candidate.Subject)
			if current, exists := candidatesBySubject[key]; !exists || candidate.Confidence > current.Confidence {
				candidatesBySubject[key] = candidate
			}
			// Observation-to-entity traversal: a hybrid match on a document
			// or episode node means the term appeared in text *about* some
			// canonical entity, not necessarily that the caller is asking
			// about the document/episode itself. Walk back to whichever
			// entity that observation is attached to and propose it as an
			// additional candidate (never a replacement -- a caller may
			// genuinely mean the document or episode).
			if IsObservationSubjectKind(candidate.Subject.Kind) {
				traversed, outcome := deps.Traverse(ctx, term, node)
				switch outcome {
				case ObservationParentFound:
					observationBlocked[key] = true
					traversedKey := SubjectKey(traversed.Subject)
					observationParentKey[key] = traversedKey
					if current, exists := candidatesBySubject[traversedKey]; !exists || traversed.Confidence > current.Confidence {
						candidatesBySubject[traversedKey] = traversed
					}
				case ObservationTraversalErrored:
					observationBlocked[key] = true
					traversalDegraded++
				case ObservationNoParent:
					// Confirmed: no parent. Leave eligible.
				}
			}
		}
	}
	if traversalDegraded > 0 && deps.TraversalDegraded != nil {
		deps.TraversalDegraded(ctx, principal.OrgID, traversalDegraded)
	}
	return ResolveFromMergedCandidates(candidatesBySubject, observationParentKey, observationBlocked, request.Options.MaxSubjectCandidates, request.Options.AllowClarification, searchTruncated), nil
}
