package graphrank

import (
	"context"
	"errors"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The wire digest vocabulary is closed (validCommitGate): this commit is an
// evidence-census commit, and the handle-explicit marker rides on the trace.
const commitGateExplicitHandleCensus = "evidence_census"

// commitExplicitHandleSubjects resolves a caller-explicit subject_handle that
// the ordinary resolution did not commit. A handle is a bare value (a PR
// number), so it names one subject only inside one repository: it is
// resolved only when exactly one repository is committed, as a keyed census
// over (handle, that repository) that must return exactly one satisfier,
// which is then re-read from the graph and re-authorized. Any other shape
// leaves the resolution unchanged.
func commitExplicitHandleSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet) contextfabric.SubjectResolution {
	if contextfabric.OffersOnlyResolution(ctx) || len(request.SubjectHandles) == 0 || deps.CensusFunc == nil || deps.HandleGrammarChecker == nil || deps.ExactHint == nil {
		return resolution
	}
	var anchor *contextfabric.SubjectRef
	repositories := 0
	for i := range resolution.Committed {
		if resolution.Committed[i].Kind == contextfabric.SubjectRepository {
			repositories++
			anchor = &resolution.Committed[i]
		}
	}
	if repositories > 1 {
		return demoteAmbiguousRepositoryAnchors(request, deps, resolution, bases, digests)
	}
	if anchor == nil {
		return resolution
	}
	for _, handle := range request.SubjectHandles {
		if _, ok := deps.HandleGrammarChecker(handle.Kind, handle.PatternID, handle.Value); !ok {
			continue
		}
		if !KindHasAnchorFK(handle.Kind, anchor.Kind) || committedHasKind(resolution.Committed, handle.Kind) {
			continue
		}
		satisfier, ok := explicitHandleSatisfier(ctx, principal, request, deps, handle.Kind, handle.Value, *anchor)
		if !ok {
			continue
		}
		candidate, ok := explicitHandleCandidate(ctx, principal, request, deps, satisfier)
		if !ok {
			continue
		}
		resolution.Committed = append(resolution.Committed, candidate.Subject)
		resolution.Candidates = append(resolution.Candidates, candidate)
		bases.Record(candidate.Subject, contextfabric.CommitBasisStatistical)
		digests.Record(candidate.Subject, contextfabric.CommitDecisionDigest{CommitGate: commitGateExplicitHandleCensus})
	}
	return resolution
}

func committedHasKind(committed []contextfabric.SubjectRef, kind contextfabric.SubjectKind) bool {
	for _, subject := range committed {
		if subject.Kind == kind {
			return true
		}
	}
	return false
}

func explicitHandleSatisfier(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, kind contextfabric.SubjectKind, value string, anchor contextfabric.SubjectRef) (contextfabric.SubjectRef, bool) {
	roundCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	outcome, err := deps.CensusFunc(roundCtx, principal.OrgID, kind, value, true, anchor.Kind, anchor.CanonicalID, true)
	if err != nil {
		if !errors.Is(err, ErrCensusAnchorUnsupported) {
			emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: kind}, "refused", false, censusCommitErrorReason, true)
		}
		return contextfabric.SubjectRef{}, false
	}
	if outcome.Count != 1 || outcome.ClosureMismatch || outcome.SatisfierSetClosureMismatch || strings.TrimSpace(outcome.SatisfierCanonicalID) == "" {
		return contextfabric.SubjectRef{}, false
	}
	return contextfabric.SubjectRef{Kind: kind, CanonicalID: outcome.SatisfierCanonicalID}, true
}

func explicitHandleCandidate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, subject contextfabric.SubjectRef) (contextfabric.SubjectCandidate, bool) {
	graphCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	node, exists, err := deps.ExactHint(graphCtx, subject)
	if err != nil || !exists {
		reason := string(ReasonGraphMissingSatisfier)
		if err != nil {
			reason = censusCommitErrorReason
		}
		emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, subject, "refused", false, reason, true)
		return contextfabric.SubjectCandidate{}, false
	}
	if subject.Label == "" {
		subject.Label = subject.CanonicalID
	}
	candidate, ok := NodeCandidate(principal, request.RequestedScope, subject.Label, node, deps.IsInternal, true, deps.ResolutionTracer, request.RequestID)
	if !ok {
		emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, subject, "refused", true, "", true)
		return contextfabric.SubjectCandidate{}, false
	}
	candidate.Confidence = 1
	candidate.State = contextfabric.ResolutionCommitted
	candidate.MatchReasons = []string{"Explicit subject handle matched exactly one record in the committed repository."}
	candidate.MatchMechanisms = MergeMechanisms(candidate.MatchMechanisms, []contextfabric.MatchMechanism{contextfabric.MatchExact})
	emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, candidate.Subject, "merged", true, "", true)
	return candidate, true
}

// demoteAmbiguousRepositoryAnchors handles a valid explicit handle that needs
// a repository to name one subject when more than one repository committed.
// Guessing one would answer about the wrong pull request, so the repositories
// leave Committed, stay in Candidates as ambiguous, and the prompt names them.
func demoteAmbiguousRepositoryAnchors(request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet) contextfabric.SubjectResolution {
	if !request.Options.AllowClarification {
		return resolution
	}
	needsAnchor := false
	for _, handle := range request.SubjectHandles {
		if _, ok := deps.HandleGrammarChecker(handle.Kind, handle.PatternID, handle.Value); ok && KindHasAnchorFK(handle.Kind, contextfabric.SubjectRepository) && !committedHasKind(resolution.Committed, handle.Kind) {
			needsAnchor = true
		}
	}
	if !needsAnchor {
		return resolution
	}
	kept := make([]contextfabric.SubjectRef, 0, len(resolution.Committed))
	labels := make([]string, 0, len(resolution.Committed))
	demoted := map[string]bool{}
	for _, subject := range resolution.Committed {
		if subject.Kind != contextfabric.SubjectRepository {
			kept = append(kept, subject)
			continue
		}
		demoted[SubjectKey(subject)] = true
		delete(bases, contextfabric.SubjectMapKey(subject))
		delete(digests, contextfabric.SubjectMapKey(subject))
		label := subject.Label
		if label == "" {
			label = subject.CanonicalID
		}
		labels = append(labels, label)
	}
	for i := range resolution.Candidates {
		if demoted[SubjectKey(resolution.Candidates[i].Subject)] {
			resolution.Candidates[i].State = contextfabric.ResolutionAmbiguous
		}
	}
	resolution.Committed = kept
	resolution.ClarificationPrompt = "Which repository holds the pull request you named: " + strings.Join(labels, ", ") + "?"
	return resolution
}
