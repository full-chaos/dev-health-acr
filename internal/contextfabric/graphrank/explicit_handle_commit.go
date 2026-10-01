package graphrank

import (
	"context"
	"errors"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The wire digest vocabulary is closed (validCommitGate): this commit is an
// evidence-census commit, and the handle-explicit marker rides on the trace.
const commitGateExplicitHandleCensus = "evidence_census"

var errCensusPanicked = errors.New("census panicked")

// commitExplicitHandleSubjects resolves a caller-explicit subject_handle. A
// handle is a bare value (a PR number), so it names one subject only inside
// one repository: it resolves only when exactly one repository is committed,
// as a keyed census over (handle, that repository) that must return exactly
// one satisfier, which is then re-read from the graph and re-authorized.
// Exactly one handle is honored; several are a clarification.
//
// A handle that cannot be resolved this way must not leave the repository
// committed: an answer about the repository is not an answer about the
// pull request the caller named. The repository then becomes a candidate and
// the caller is asked, never guessed for.
func commitExplicitHandleSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet) contextfabric.SubjectResolution {
	if contextfabric.OffersOnlyResolution(ctx) || len(request.SubjectHandles) == 0 || deps.CensusFunc == nil || deps.HandleGrammarChecker == nil || deps.ExactHint == nil {
		return resolution
	}
	if len(request.SubjectHandles) > 1 {
		// One subject per request: several handles cannot each be anchored
		// on the one repository, so the caller is asked to send one.
		for _, h := range request.SubjectHandles {
			if _, ok := deps.HandleGrammarChecker(h.Kind, h.PatternID, h.Value); ok && KindHasAnchorFK(h.Kind, contextfabric.SubjectRepository) {
				return demoteRepositoryAnchors(request, resolution, bases, digests, "Several subject handles were sent; send one handle at a time")
			}
		}
		return resolution
	}
	handle := request.SubjectHandles[0]
	if _, ok := deps.HandleGrammarChecker(handle.Kind, handle.PatternID, handle.Value); !ok || !KindHasAnchorFK(handle.Kind, contextfabric.SubjectRepository) {
		return resolution
	}
	if committedMatchesHandle(resolution.Committed, handle.Kind, handle.Value) {
		return resolution
	}
	var anchor contextfabric.SubjectRef
	repositories := 0
	for _, subject := range resolution.Committed {
		if subject.Kind == contextfabric.SubjectRepository {
			repositories++
			anchor = subject
		}
	}
	switch repositories {
	case 0:
		return resolution
	case 1:
	default:
		return demoteRepositoryAnchors(request, resolution, bases, digests, "Which repository holds the "+handleKindLabel(handle.Kind)+" "+handle.Value)
	}
	satisfier, ok := explicitHandleSatisfier(ctx, principal, request, deps, handle.Kind, handle.Value, anchor)
	var candidate contextfabric.SubjectCandidate
	if ok {
		candidate, ok = explicitHandleCandidate(ctx, principal, request, deps, satisfier)
	}
	if !ok {
		return demoteRepositoryAnchors(request, resolution, bases, digests, "The "+handleKindLabel(handle.Kind)+" "+handle.Value+" could not be matched to exactly one record in the repository")
	}
	kept := make([]contextfabric.SubjectRef, 0, len(resolution.Committed)+1)
	for _, subject := range resolution.Committed {
		if subject.Kind == handle.Kind {
			delete(bases, contextfabric.SubjectMapKey(subject))
			delete(digests, contextfabric.SubjectMapKey(subject))
			for i := range resolution.Candidates {
				if resolution.Candidates[i].Subject == subject {
					resolution.Candidates[i].State = contextfabric.ResolutionAmbiguous
				}
			}
			continue
		}
		kept = append(kept, subject)
	}
	digest := contextfabric.CommitDecisionDigest{CommitGate: commitGateExplicitHandleCensus}
	if prior := digests.For(anchor); prior.CommitGate != "" {
		digest.SearchTruncated, digest.AliasLookupComplete = prior.SearchTruncated, prior.AliasLookupComplete
	}
	resolution.Committed = append(kept, candidate.Subject)
	resolution.Candidates = append(resolution.Candidates, candidate)
	bases.Record(candidate.Subject, contextfabric.CommitBasisStatistical)
	digests.Record(candidate.Subject, digest)
	return resolution
}

// committedMatchesHandle reports whether a committed subject of kind already
// carries the handle's value, read from its canonical id by the same
// extractor the handle offers use. A committed subject of that kind with a
// different value does not satisfy the handle.
func committedMatchesHandle(committed []contextfabric.SubjectRef, kind contextfabric.SubjectKind, value string) bool {
	extractor, ok := handleGraphExtractors[kind]
	if !ok {
		return false
	}
	for _, subject := range committed {
		if subject.Kind != kind {
			continue
		}
		if got, ok := extractor.extract(subject.CanonicalID); ok && got == value {
			return true
		}
	}
	return false
}

func handleKindLabel(kind contextfabric.SubjectKind) string {
	switch kind {
	case contextfabric.SubjectPullRequest:
		return "pull request"
	case contractsv1.ContextFabricSubjectCIRun:
		return "CI run"
	default:
		return string(kind)
	}
}

func explicitHandleSatisfier(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, kind contextfabric.SubjectKind, value string, anchor contextfabric.SubjectRef) (contextfabric.SubjectRef, bool) {
	roundCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	var outcome CensusOutcome
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errCensusPanicked
			}
		}()
		outcome, err = deps.CensusFunc(roundCtx, principal.OrgID, kind, value, true, anchor.Kind, anchor.CanonicalID, true)
	}()
	if err != nil {
		if !errors.Is(err, ErrCensusAnchorUnsupported) {
			emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: kind}, "refused", false, censusCommitErrorReason, true)
		}
		return contextfabric.SubjectRef{}, false
	}
	if outcome.Count != 1 || outcome.ClosureMismatch || outcome.SatisfierSetClosureMismatch || strings.TrimSpace(outcome.SatisfierCanonicalID) == "" {
		emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: kind}, "refused", false, "", true)
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

// demoteRepositoryAnchors moves every committed repository out of Committed:
// they stay in Candidates as ambiguous and the prompt names them.
func demoteRepositoryAnchors(request contextfabric.InvestigationRequest, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, question string) contextfabric.SubjectResolution {
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
	if request.Options.AllowClarification {
		resolution.ClarificationPrompt = question + " (" + strings.Join(labels, ", ") + ")?"
	}
	return resolution
}
