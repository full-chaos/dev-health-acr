package graphrank

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12557, lead rulings (a)-(d): a handle named beside a committed team, and
// several handles named beside a committed repository, are answered or said, on
// CHAOS-12523's stated-kind gate. A turn that states the handle's kind owes it; one
// that states no kind only mentions it, and the anchor's answer stands.

// teamOwnedRepositoryLimit bounds the repositories one team's read takes: the
// census runs once per repository. A team past it is not read, and the cut is
// logged.
const teamOwnedRepositoryLimit = 25

// handleBesideTeam answers for one handle named beside exactly one committed team.
// Team = ownership only: a pull request or a CI run is read in the repositories the
// team owns now, one repository-anchored census each; a work item, which no
// repository column holds, across what the caller can read, as beside a
// repository. A read that fails or is cut changes nothing.
func handleBesideTeam(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, handle contractsv1.ContextFabricRequestedHandle, claim handleClaim, anchored bool) (contextfabric.SubjectResolution, *BoundHandle) {
	var team contextfabric.SubjectRef
	teams := 0
	for _, subject := range resolution.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			teams++
			team = subject
		}
	}
	if teams != 1 {
		return resolution, nil
	}
	var census namedCensus
	if anchored {
		var ok bool
		if census, ok = teamHandleCensus(ctx, principal, request, deps, handle, team); !ok {
			return resolution, nil
		}
	} else {
		census = namedHandleCensus(ctx, principal, request, deps, handle.Kind, handle.Value, contextfabric.SubjectRef{}, false)
	}
	var candidate contextfabric.SubjectCandidate
	ok := census.satisfier.CanonicalID != ""
	if ok {
		candidate, ok = explicitHandleCandidate(ctx, principal, request, deps, census.satisfier)
	}
	if !ok {
		return unresolvedNamedHandle(ctx, principal, request, deps, resolution, bases, digests, handle, claim, anchored, census, contextfabric.SubjectTeam)
	}
	return commitNamedSatisfier(resolution, bases, digests, handle.Kind, candidate, team), nil
}

// teamHandleCensus counts a repository-keyed handle in the repositories the team
// owns: one census anchored on each, the counts added and the satisfiers joined.
// ok is false when the team's repositories could not be read whole, past
// teamOwnedRepositoryLimit included (logged): nothing is decided then. A census
// that fails or does not close leaves the result incomplete, as for a repository.
func teamHandleCensus(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, handle contractsv1.ContextFabricRequestedHandle, team contextfabric.SubjectRef) (namedCensus, bool) {
	if deps.TeamOwnedRepositories == nil {
		return namedCensus{}, false
	}
	readCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	owned, complete, err := deps.TeamOwnedRepositories(readCtx, team, teamOwnedRepositoryLimit)
	cancel()
	switch {
	case err != nil:
		slog.WarnContext(ctx, "context fabric census: the repositories of the team named beside a handle could not be read",
			"request_id", contextfabric.SanitizeLogAttr(request.RequestID), "kind", contextfabric.SanitizeLogAttr(string(handle.Kind)))
		return namedCensus{}, false
	case !complete:
		slog.InfoContext(ctx, "context fabric census: the team named beside a handle owns more repositories than one read takes, so the handle was not read",
			"request_id", contextfabric.SanitizeLogAttr(request.RequestID), "kind", contextfabric.SanitizeLogAttr(string(handle.Kind)),
			"repositories_read", len(owned), "limit", teamOwnedRepositoryLimit)
		return namedCensus{}, false
	}
	explicit := len(request.SubjectHandles) > 0
	total := CensusOutcome{}
	var ids []string
	for _, repository := range owned {
		outcome, err := handleCensusOutcome(ctx, principal, deps, handle.Kind, handle.Value, repository, true)
		if err != nil {
			emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: handle.Kind}, "refused", false, censusCommitErrorReason, explicit)
			return namedCensus{}, true
		}
		if outcome.ClosureMismatch || outcome.SatisfierSetClosureMismatch {
			emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: handle.Kind}, "refused", false, "", explicit)
			return namedCensus{outcome: total}, true
		}
		total.Count += outcome.Count
		if total.CensusReadAt.IsZero() || outcome.CensusReadAt.Before(total.CensusReadAt) {
			total.CensusReadAt = outcome.CensusReadAt
		}
		switch {
		case outcome.Count == 1 && outcome.SatisfierCanonicalID != "":
			ids = append(ids, outcome.SatisfierCanonicalID)
		case outcome.Count > 1:
			ids = append(ids, outcome.SatisfierCanonicalIDs...)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	total.SatisfierSetClosureMismatch = len(ids) != total.Count
	if total.Count == 1 && len(ids) == 1 {
		total.SatisfierCanonicalID = ids[0]
		return namedCensus{satisfier: contextfabric.SubjectRef{Kind: handle.Kind, CanonicalID: ids[0]}, outcome: total, complete: true}, true
	}
	total.SatisfierCanonicalIDs = ids
	emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: handle.Kind}, "refused", false, "", explicit)
	return namedCensus{outcome: total, complete: true}, true
}

// severalHandlesBesideRepository answers a turn that names several handles of one
// kind beside one committed repository (H2). A turn that states the kind owes them:
// each is counted inside the repository, the repository leaves the answer, and the
// satisfiers found are offered, with the prompt the explicit rule uses for several
// handles. A complete zero among them is said in the census's own scope. A turn
// that states no kind keeps the repository's answer, with a sentence that it does
// not answer for the handles: nothing was read, so nothing more is claimed. A
// census that cannot read changes nothing.
func severalHandlesBesideRepository(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, handles []contractsv1.ContextFabricRequestedHandle, claim handleClaim) contextfabric.SubjectResolution {
	if claim == handleMentioned {
		resolution.UnansweredHandlesLimitation = contractsv1.ContextFabricSeveralHandlesUnansweredLimitation
		return resolution
	}
	kind := handles[0].Kind
	anchored := KindHasAnchorFK(kind, contextfabric.SubjectRepository)
	var anchor contextfabric.SubjectRef
	for _, subject := range resolution.Committed {
		if subject.Kind == contextfabric.SubjectRepository {
			anchor = subject
		}
	}
	if !anchored {
		anchor = contextfabric.SubjectRef{}
	}
	var ids []string
	absent := false
	for _, handle := range handles {
		if _, ok := deps.HandleGrammarChecker(handle.Kind, handle.PatternID, handle.Value); !ok {
			return resolution
		}
		outcome, err := handleCensusOutcome(ctx, principal, deps, handle.Kind, handle.Value, anchor, anchored)
		if err != nil || outcome.ClosureMismatch || outcome.SatisfierSetClosureMismatch {
			return resolution
		}
		switch {
		case outcome.Count == 0:
			absent = true
		case outcome.Count == 1 && outcome.SatisfierCanonicalID != "":
			ids = append(ids, outcome.SatisfierCanonicalID)
		default:
			ids = append(ids, outcome.SatisfierCanonicalIDs...)
		}
	}
	values := make([]string, 0, len(handles))
	for _, handle := range handles {
		values = append(values, handle.Value)
	}
	resolution = demoteRepositoryAnchors(request, resolution, bases, digests, kind, "Several "+handleKindLabel(kind)+" identifiers were named ("+strings.Join(values, ", ")+"); ask about one at a time")
	if absent {
		resolution.CensusLimitation = censusAbsenceLimitation(principal, request, KindAttestation{Kind: kind, Complete: true, AnchorApplied: anchored, HandleApplied: true})
	}
	if listed := handleSatisfierCandidates(ctx, principal, request, deps, kind, ids, resolution.Candidates); len(listed) > 0 {
		resolution.Candidates = append(listed, resolution.Candidates...)
	}
	return resolution
}

// handleSatisfierCandidates reads the satisfiers a census named as candidates to
// offer: each re-read from the graph and re-authorized, ambiguous, none already in
// pool. A satisfier the graph does not hold, or the caller may not read, is not
// offered.
func handleSatisfierCandidates(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, kind contextfabric.SubjectKind, ids []string, pool []contextfabric.SubjectCandidate) []contextfabric.SubjectCandidate {
	if deps.ExactHint == nil {
		return nil
	}
	held := map[string]bool{}
	for _, candidate := range pool {
		held[SubjectKey(candidate.Subject)] = true
	}
	graphCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	limit := min(request.Options.MaxSubjectCandidates, censusListingMaxReads)
	var listed []contextfabric.SubjectCandidate
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		if len(listed) >= limit {
			break
		}
		subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: id}
		if held[SubjectKey(subject)] {
			continue
		}
		node, exists, err := deps.ExactHint(graphCtx, subject)
		if err != nil || !exists {
			continue
		}
		candidate, admitted := NodeCandidate(principal, request.RequestedScope, id, node, deps.IsInternal, false, deps.ResolutionTracer, request.RequestID)
		if !admitted {
			continue
		}
		candidate.State = contextfabric.ResolutionAmbiguous
		listed = append(listed, candidate)
	}
	return listed
}
