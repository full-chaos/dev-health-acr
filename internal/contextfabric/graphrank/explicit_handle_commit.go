package graphrank

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The wire digest vocabulary is closed (validCommitGate): this commit is an
// evidence-census commit, and the handle-explicit marker rides on the trace.
const commitGateExplicitHandleCensus = contractsv1.CommitGateEvidenceCensus

var errCensusPanicked = errors.New("census panicked")

// handleClaim is how far a turn holds a committed repository to a handle it names.
type handleClaim int

const (
	// handleNotNamed: the turn names no handle this rule can speak to.
	handleNotNamed handleClaim = iota
	// handleOwed: the caller sent the handle, or the turn states its kind, so the
	// answer is about that subject. A repository must not answer in its place.
	handleOwed
	// handleMentioned: the turn states no kind, so the handle may only be mentioned.
	// A proved subject joins the repository, and a proved absence is said, but the
	// repository's answer stands.
	handleMentioned
)

// namedHandles is what the turn names beside what it committed. Only a named or an
// absent frame can claim a handle: in a cohort or a grouped question a handle is an
// example, and committing it would change what the answer is about.
func namedHandles(request contextfabric.InvestigationRequest, committed []contextfabric.SubjectRef, frame *contextfabric.QuestionFrame) ([]contractsv1.ContextFabricRequestedHandle, handleClaim) {
	if len(request.SubjectHandles) > 0 {
		return request.SubjectHandles, handleOwed
	}
	bound := BindHandles(request.Question)
	if len(bound) != 1 {
		return nil, handleNotNamed
	}
	handle := []contractsv1.ContextFabricRequestedHandle{{Kind: bound[0].Kind, PatternID: bound[0].Grammar, Value: bound[0].Value}}
	if turnStatesKind(request, frame, bound[0].Kind) {
		return handle, handleOwed
	}
	if committedScopeAnchorShadowsHandle(request.Question, committed, frame) {
		return handle, handleMentioned
	}
	return nil, handleNotNamed
}

// turnStatesKind reports a turn that says its answer is about a subject of kind: the
// frame's expected kind, which the model's requested kind fills, or a lone
// expected_kinds. The two must not disagree.
func turnStatesKind(request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, kind contextfabric.SubjectKind) bool {
	var framed *contextfabric.SubjectKind
	if frame != nil {
		if frame.SubjectExpression.Kind != contextfabric.SubjectExpressionNamed {
			return false
		}
		if named := frame.SubjectExpression.Named; named != nil {
			framed = named.ExpectedKind
		}
	}
	if framed != nil {
		return *framed == kind
	}
	return len(request.ExpectedKinds) == 1 && request.ExpectedKinds[0] == kind
}

// commitNamedHandle answers for the handle a turn names beside a committed
// repository (CHAOS-12523). A pull request or a CI run is a bare value, so it names
// one subject only inside one repository: its census is keyed on (handle, that
// repository), and a zero says nothing of any other repository. A work item has no
// repository column, so its census reads all the caller can read; a key names one
// item, and the commit makes no claim about a link to the repository. Either way
// exactly one satisfier is re-read from the graph and re-authorized. Exactly one
// handle is honored; several are a clarification.
//
// A handle the turn owes that cannot be resolved this way must not leave the
// repository committed: an answer about the repository is not an answer about the
// pull request the caller named. The repository then becomes a candidate and the
// caller is asked, never guessed for. A handle that is only mentioned leaves the
// repository's answer alone.
//
// The returned handle is one no option may be offered for.
func commitNamedHandle(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet) (contextfabric.SubjectResolution, *BoundHandle) {
	if contextfabric.OffersOnlyResolution(ctx) || deps.CensusFunc == nil || deps.HandleGrammarChecker == nil || deps.ExactHint == nil {
		return resolution, nil
	}
	handles, claim := namedHandles(request, resolution.Committed, frame)
	if claim == handleNotNamed {
		return resolution, nil
	}
	if len(handles) > 1 {
		// One subject per request: several handles cannot each be anchored
		// on the one repository, so the caller is asked to send one.
		for _, h := range handles {
			if _, ok := deps.HandleGrammarChecker(h.Kind, h.PatternID, h.Value); ok && KindHasAnchorFK(h.Kind, contextfabric.SubjectRepository) {
				return demoteRepositoryAnchors(request, resolution, bases, digests, h.Kind, "Several subject handles were sent; send one handle at a time"), nil
			}
		}
		return resolution, nil
	}
	handle := handles[0]
	if _, ok := deps.HandleGrammarChecker(handle.Kind, handle.PatternID, handle.Value); !ok {
		return resolution, nil
	}
	anchored := KindHasAnchorFK(handle.Kind, contextfabric.SubjectRepository)
	var anchor contextfabric.SubjectRef
	repositories := 0
	for _, subject := range resolution.Committed {
		if subject.Kind == contextfabric.SubjectRepository {
			repositories++
			anchor = subject
		}
	}
	switch {
	case repositories == 0:
		return resolution, nil
	case !anchored:
		anchor = contextfabric.SubjectRef{}
	case repositories > 1 && claim == handleMentioned:
		return resolution, nil
	case repositories > 1:
		return demoteRepositoryAnchors(request, resolution, bases, digests, handle.Kind, "Which repository holds the "+handleKindLabel(handle.Kind)+" "+handle.Value), nil
	case committedMatchesHandle(resolution.Committed, handle.Kind, handle.Value, anchor):
		return dropOtherSubjectsOfKind(resolution, bases, digests, handle.Kind, handle.Value, anchor), nil
	}
	census := namedHandleCensus(ctx, principal, request, deps, handle.Kind, handle.Value, anchor, anchored)
	var candidate contextfabric.SubjectCandidate
	ok := census.satisfier.CanonicalID != ""
	if ok {
		candidate, ok = explicitHandleCandidate(ctx, principal, request, deps, census.satisfier)
	}
	if !ok {
		return unresolvedNamedHandle(ctx, principal, request, deps, resolution, bases, digests, handle, claim, anchored, census)
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
		digest.IdentityLookup = prior.IdentityLookup
	}
	resolution.Committed = append(kept, candidate.Subject)
	resolution.Candidates = append(resolution.Candidates, candidate)
	bases.Record(candidate.Subject, contextfabric.CommitBasisStatistical)
	digests.Record(candidate.Subject, digest)
	return resolution, nil
}

// unresolvedNamedHandle ends a turn whose handle did not resolve to one subject. A
// complete zero is stated in the census's own scope. A mentioned handle changes
// nothing else. An owed handle takes the repository out of the answer, and a complete
// count of several is listed.
func unresolvedNamedHandle(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, handle contractsv1.ContextFabricRequestedHandle, claim handleClaim, anchored bool, census namedCensus) (contextfabric.SubjectResolution, *BoundHandle) {
	ka := KindAttestation{
		Kind: handle.Kind, Complete: true, Count: census.outcome.Count, HandleApplied: true, AnchorApplied: anchored,
		SatisfierCanonicalID: census.outcome.SatisfierCanonicalID, SatisfierCanonicalIDs: census.outcome.SatisfierCanonicalIDs,
	}
	// A free-text turn whose census could not read, by an error or by not closing,
	// keeps today's answer (lead ruling on #1920, the same side as CHAOS-12463
	// ruling 3): a gate that cannot read changes nothing. The refusal is already
	// traced on evidence_census_commit. A handle the caller sent keeps the explicit
	// rule's demotion below, as before this change. A committed subject of the
	// handle's kind that is not the handle still goes: the question names another
	// one, so it is not what the caller asked about (codex r2 on #1920). A kind with
	// no graph extractor (a work item) cannot be told apart here, so it stays.
	if !census.complete && len(request.SubjectHandles) == 0 {
		if _, extractable := handleGraphExtractors[handle.Kind]; extractable {
			return dropOtherSubjectsOfKind(resolution, bases, digests, handle.Kind, handle.Value, contextfabric.SubjectRef{}), nil
		}
		return resolution, nil
	}
	absent := census.complete && census.outcome.Count == 0
	if claim == handleMentioned {
		if absent {
			resolution.CensusLimitation = censusAbsenceLimitation(principal, request, ka)
		}
		return resolution, nil
	}
	resolution = demoteRepositoryAnchors(request, resolution, bases, digests, handle.Kind, "The "+handleKindLabel(handle.Kind)+" "+handle.Value+" could not be matched to exactly one record in the repository")
	if absent {
		resolution.CensusLimitation = censusAbsenceLimitation(principal, request, ka)
		return resolution, &BoundHandle{Kind: handle.Kind, Value: handle.Value}
	}
	if !census.complete || !questionBindsHandle(request.Question, handle) {
		return resolution, nil
	}
	attestation := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{ka}}
	resolution, undeciding := listCensusSatisfiers(ctx, principal, request, deps, attestation, resolution)
	// Several satisfiers on show are the choice to offer, ahead of the repository the
	// turn just demoted, so the prompt is theirs. The search may already hold them, and
	// the listing then reorders nothing.
	satisfies := func(candidate contextfabric.SubjectCandidate) bool {
		return slices.Contains(census.outcome.SatisfierCanonicalIDs, candidate.Subject.CanonicalID)
	}
	if slices.ContainsFunc(resolution.Candidates, satisfies) {
		first := slices.DeleteFunc(slices.Clone(resolution.Candidates), func(c contextfabric.SubjectCandidate) bool { return !satisfies(c) })
		resolution.Candidates = append(first, slices.DeleteFunc(slices.Clone(resolution.Candidates), satisfies)...)
		if request.Options.AllowClarification {
			resolution.ClarificationPrompt = ClarificationPrompt(resolution.Candidates)
		}
	}
	return resolution, undeciding
}

func questionBindsHandle(question string, handle contractsv1.ContextFabricRequestedHandle) bool {
	bound := BindHandles(question)
	return len(bound) == 1 && bound[0].Kind == handle.Kind && bound[0].Value == handle.Value
}

// committedMatchesHandle reports whether a committed subject of kind carries
// the handle's value AND lives in the anchor repository. Both are read from
// its canonical id; the same number in another repository is another subject.
func committedMatchesHandle(committed []contextfabric.SubjectRef, kind contextfabric.SubjectKind, value string, anchor contextfabric.SubjectRef) bool {
	extractor, ok := handleGraphExtractors[kind]
	if !ok {
		return false
	}
	anchorRepo := strings.TrimPrefix(anchor.CanonicalID, "repository:")
	for _, subject := range committed {
		if subject.Kind != kind {
			continue
		}
		got, ok := extractor.extract(subject.CanonicalID)
		repo, repoOK := committedSubjectRepository(kind, subject.CanonicalID)
		if ok && got == value && repoOK && repo == anchorRepo {
			return true
		}
	}
	return false
}

func committedSubjectRepository(kind contextfabric.SubjectKind, canonicalID string) (string, bool) {
	switch kind {
	case contextfabric.SubjectPullRequest:
		parts := strings.Split(canonicalID, ":")
		if len(parts) != 3 || parts[1] == "" {
			return "", false
		}
		return parts[1], true
	case contractsv1.ContextFabricSubjectCIRun:
		segments, ok := identity.Segments(identity.KindCIPipelineRun, canonicalID)
		if !ok || len(segments) != 2 || segments[0] == "" {
			return "", false
		}
		return segments[0], true
	}
	return "", false
}

func handleKindLabel(kind contextfabric.SubjectKind) string {
	switch kind {
	case contextfabric.SubjectPullRequest:
		return "pull request"
	case contractsv1.ContextFabricSubjectCIRun:
		return "CI run"
	case contextfabric.SubjectWorkItem:
		return "work item"
	default:
		return string(kind)
	}
}

// namedCensus is what the keyed census said of a handle. A satisfier is set when it
// named exactly one. complete is false when it failed or did not close.
type namedCensus struct {
	satisfier contextfabric.SubjectRef
	outcome   CensusOutcome
	complete  bool
}

func namedHandleCensus(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, kind contextfabric.SubjectKind, value string, anchor contextfabric.SubjectRef, anchored bool) namedCensus {
	roundCtx, cancel := context.WithTimeout(contextfabric.ScopeCensusToCaller(ctx, principal), evidenceRoundDeadline)
	defer cancel()
	explicit := len(request.SubjectHandles) > 0
	var outcome CensusOutcome
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errCensusPanicked
			}
		}()
		outcome, err = grantEnforced(deps.CensusFunc)(roundCtx, principal.OrgID, kind, value, true, anchor.Kind, anchor.CanonicalID, anchored)
	}()
	if err != nil {
		if !errors.Is(err, ErrCensusAnchorUnsupported) {
			emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: kind}, "refused", false, censusCommitErrorReason, explicit)
		}
		return namedCensus{}
	}
	complete := !outcome.ClosureMismatch && !outcome.SatisfierSetClosureMismatch
	if outcome.Count != 1 || !complete || strings.TrimSpace(outcome.SatisfierCanonicalID) == "" {
		emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, contextfabric.SubjectRef{Kind: kind}, "refused", false, "", explicit)
		return namedCensus{outcome: outcome, complete: complete}
	}
	return namedCensus{satisfier: contextfabric.SubjectRef{Kind: kind, CanonicalID: outcome.SatisfierCanonicalID}, outcome: outcome, complete: complete}
}

func explicitHandleCandidate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, subject contextfabric.SubjectRef) (contextfabric.SubjectCandidate, bool) {
	graphCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	explicit := len(request.SubjectHandles) > 0
	node, exists, err := deps.ExactHint(graphCtx, subject)
	if err != nil || !exists {
		reason := string(ReasonGraphMissingSatisfier)
		if err != nil {
			reason = censusCommitErrorReason
		}
		emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, subject, "refused", false, reason, explicit)
		return contextfabric.SubjectCandidate{}, false
	}
	if subject.Label == "" {
		subject.Label = subject.CanonicalID
	}
	candidate, ok := NodeCandidate(principal, request.RequestedScope, subject.Label, node, deps.IsInternal, true, deps.ResolutionTracer, request.RequestID)
	if !ok {
		emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, subject, "refused", true, "", explicit)
		return contextfabric.SubjectCandidate{}, false
	}
	candidate.Confidence = 1
	candidate.State = contextfabric.ResolutionCommitted
	candidate.MatchReasons = []string{"The named handle matched exactly one record."}
	candidate.MatchMechanisms = MergeMechanisms(candidate.MatchMechanisms, []contextfabric.MatchMechanism{contextfabric.MatchExact})
	emitEvidenceCensusCommit(deps.ResolutionTracer, request.RequestID, candidate.Subject, "merged", true, "", explicit)
	return candidate, true
}

// demoteRepositoryAnchors moves every committed repository, and every
// committed subject of the handle's kind (an answer about another such
// subject is not an answer about the one named), out of Committed: they stay
// in Candidates as ambiguous and the prompt names the repositories.
func demoteRepositoryAnchors(request contextfabric.InvestigationRequest, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, handleKind contextfabric.SubjectKind, question string) contextfabric.SubjectResolution {
	kept := make([]contextfabric.SubjectRef, 0, len(resolution.Committed))
	labels := make([]string, 0, len(resolution.Committed))
	demoted := map[string]bool{}
	for _, subject := range resolution.Committed {
		if subject.Kind != contextfabric.SubjectRepository && subject.Kind != handleKind {
			kept = append(kept, subject)
			continue
		}
		demoted[SubjectKey(subject)] = true
		delete(bases, contextfabric.SubjectMapKey(subject))
		delete(digests, contextfabric.SubjectMapKey(subject))
		if subject.Kind != contextfabric.SubjectRepository {
			continue
		}
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

// dropOtherSubjectsOfKind keeps only the committed subject that is the handle's
// own and moves every other committed subject of that kind to ambiguous
// candidates: evidence about another such subject must not enter an answer
// about the one the caller named.
func dropOtherSubjectsOfKind(resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, kind contextfabric.SubjectKind, value string, anchor contextfabric.SubjectRef) contextfabric.SubjectResolution {
	kept := make([]contextfabric.SubjectRef, 0, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		if subject.Kind != kind || committedMatchesHandle([]contextfabric.SubjectRef{subject}, kind, value, anchor) {
			kept = append(kept, subject)
			continue
		}
		delete(bases, contextfabric.SubjectMapKey(subject))
		delete(digests, contextfabric.SubjectMapKey(subject))
		for i := range resolution.Candidates {
			if resolution.Candidates[i].Subject == subject {
				resolution.Candidates[i].State = contextfabric.ResolutionAmbiguous
			}
		}
	}
	resolution.Committed = kept
	return resolution
}
