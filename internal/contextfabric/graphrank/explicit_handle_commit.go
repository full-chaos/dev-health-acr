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
// example, and committing it would change what the answer is about. bound is the
// turn's handles after the work item key gate (CHAOS-12463): a key the caller does
// not hold names nothing.
func namedHandles(request contextfabric.InvestigationRequest, bound []BoundHandle, committed []contextfabric.SubjectRef, frame *contextfabric.QuestionFrame) ([]contractsv1.ContextFabricRequestedHandle, handleClaim) {
	if len(request.SubjectHandles) > 0 {
		return request.SubjectHandles, handleOwed
	}
	if len(bound) > 1 {
		return severalNamedHandles(request, bound, committed, frame)
	}
	if len(bound) != 1 {
		return nil, handleNotNamed
	}
	handle := []contractsv1.ContextFabricRequestedHandle{{Kind: bound[0].Kind, PatternID: bound[0].Grammar, Value: bound[0].Value}}
	if turnStatesKind(request, frame, bound[0].Kind) {
		return handle, handleOwed
	}
	if committedScopeAnchorShadowsHandle(request, bound, committed, frame) || committedTeamShadowsHandle(request, bound, committed, frame) {
		return handle, handleMentioned
	}
	return nil, handleNotNamed
}

// severalNamedHandles is what a turn that binds more than one handle names beside
// what it committed (CHAOS-12557): handles of one census kind beside one committed
// repository. A turn that states the kind owes them; one that states none only
// mentions them, so the repository's answer stands.
func severalNamedHandles(request contextfabric.InvestigationRequest, bound []BoundHandle, committed []contextfabric.SubjectRef, frame *contextfabric.QuestionFrame) ([]contractsv1.ContextFabricRequestedHandle, handleClaim) {
	kind := bound[0].Kind
	handles := make([]contractsv1.ContextFabricRequestedHandle, 0, len(bound))
	for _, b := range bound {
		if b.Kind != kind || !IsCensusKindRegistered(kind) {
			return nil, handleNotNamed
		}
		handles = append(handles, contractsv1.ContextFabricRequestedHandle{Kind: b.Kind, PatternID: b.Grammar, Value: b.Value})
	}
	repositories := 0
	for _, subject := range committed {
		if subject.Kind != contextfabric.SubjectRepository {
			return nil, handleNotNamed
		}
		repositories++
	}
	if repositories != 1 {
		return nil, handleNotNamed
	}
	if frame != nil && frame.SubjectExpression.Kind != contextfabric.SubjectExpressionNamed {
		return nil, handleNotNamed
	}
	if turnStatesKind(request, frame, kind) {
		return handles, handleOwed
	}
	// A turn that states another kind, the repository's included, is untouched,
	// as in CHAOS-12523, however it stated it (CHAOS-12631).
	if turnStatesAnotherKind(request, frame, kind) {
		return nil, handleNotNamed
	}
	return handles, handleMentioned
}

// committedTeamShadowsHandle is committedScopeAnchorShadowsHandle for a team
// (CHAOS-12557): one census-kind handle beside exactly one committed team, in a
// named frame that asks for no other kind. A team reaches a pull request or a CI
// run only through the repositories it owns, and a work item through no column,
// so the handle is read in what the team owns or across what the caller reads.
func committedTeamShadowsHandle(request contextfabric.InvestigationRequest, bound []BoundHandle, committed []contextfabric.SubjectRef, frame *contextfabric.QuestionFrame) bool {
	if len(bound) != 1 || !IsCensusKindRegistered(bound[0].Kind) || len(committed) != 1 || committed[0].Kind != contextfabric.SubjectTeam {
		return false
	}
	if frame != nil && frame.SubjectExpression.Kind != contextfabric.SubjectExpressionNamed {
		return false
	}
	// A turn that states a kind is decided by turnStatesKind; one that states
	// another, the team's included, is untouched (statedKind, CHAOS-12631).
	_, stated := statedKind(request, frame)
	return !stated
}

// statedKind is the kind a turn says its answer is about: the frame's expected
// kind, which the model's requested kind fills, or else a lone expected_kinds.
// The frame wins when both are present. A frame that is not a named one states
// no kind. It is the one reading of a stated kind: the owed test and every
// "states another kind" exit take it (CHAOS-12631).
func statedKind(request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame) (contextfabric.SubjectKind, bool) {
	if frame != nil {
		if frame.SubjectExpression.Kind != contextfabric.SubjectExpressionNamed {
			return "", false
		}
		if named := frame.SubjectExpression.Named; named != nil && named.ExpectedKind != nil {
			return *named.ExpectedKind, true
		}
	}
	if len(request.ExpectedKinds) == 1 {
		return request.ExpectedKinds[0], true
	}
	return "", false
}

// turnStatesKind reports a turn that says its answer is about a subject of kind.
func turnStatesKind(request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, kind contextfabric.SubjectKind) bool {
	stated, ok := statedKind(request, frame)
	return ok && stated == kind
}

// turnStatesAnotherKind reports a turn that says its answer is about a subject of
// a kind other than kind.
func turnStatesAnotherKind(request contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, kind contextfabric.SubjectKind) bool {
	stated, ok := statedKind(request, frame)
	return ok && stated != kind
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
	handles, claim := namedHandles(request, turnHandles(request, deps), resolution.Committed, frame)
	if claim == handleNotNamed {
		return resolution, nil
	}
	if len(handles) > 1 && len(request.SubjectHandles) == 0 {
		return severalHandlesBesideRepository(ctx, principal, request, deps, resolution, bases, digests, handles, claim), nil
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
	// The one named repository a work item may be linked to (ruling 3 below).
	named := anchor
	switch {
	case repositories == 0:
		return handleBesideTeam(ctx, principal, request, deps, resolution, bases, digests, handle, claim, anchored)
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
		return unresolvedNamedHandle(ctx, principal, request, deps, resolution, bases, digests, handle, claim, anchored, census, contextfabric.SubjectRepository)
	}
	resolution = commitNamedSatisfier(resolution, bases, digests, handle.Kind, candidate, anchor)
	if !anchored && repositories == 1 {
		resolution.NamedRepositoryLinkLimitation = namedRepositoryLinkLimitation(ctx, request, deps, named, candidate.Subject)
	}
	return resolution, nil
}

// commitNamedSatisfier commits the handle's one satisfier beside what the turn
// committed, and takes every other committed subject of the handle's kind out of
// the answer: it is about another subject. The digest carries the anchor's own
// search facts.
func commitNamedSatisfier(resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, kind contextfabric.SubjectKind, candidate contextfabric.SubjectCandidate, anchor contextfabric.SubjectRef) contextfabric.SubjectResolution {
	kept := make([]contextfabric.SubjectRef, 0, len(resolution.Committed)+1)
	for _, subject := range resolution.Committed {
		if subject.Kind == kind {
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
	return resolution
}

// namedRepositoryLinkLimitation says whether a pull request link joins a work
// item committed beside one named repository to that repository (CHAOS-12505,
// ruling 3 of the CHAOS-12523 rows). It asks the keyed link read, the one the
// scoped census asks, with the repository as the requested scope: the same rule,
// under the caller's grants, whatever the repository's size. A read that fails,
// is cut, or cannot start says so; it never reads as "no link".
func namedRepositoryLinkLimitation(ctx context.Context, request contextfabric.InvestigationRequest, deps ResolveDeps, repository, item contextfabric.SubjectRef) string {
	if deps.LinkScopedWorkItemsOf == nil || deps.ExactHint == nil {
		return contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation
	}
	readCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	node, exists, err := deps.ExactHint(readCtx, repository)
	if err != nil || !exists {
		return contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation
	}
	var slugs []string
	listed, _ := node.Attributes[authorizationRepositoriesAttr].([]string)
	for _, slug := range listed {
		if slug = strings.TrimSpace(slug); slug != "" && slug != "*" && !contractsv1.ContextFabricIsRepositoryAbsenceSentinel(slug) {
			slugs = append(slugs, slug)
		}
	}
	// A repository names one slug. A node that lists another could admit a link
	// into that one and say it of this repository (codex r1 on #1929), so it is
	// not read.
	if len(slugs) != 1 {
		return contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation
	}
	scope := request.RequestedScope
	scope.RepositorySlugs = slugs
	tiers, complete, err := deps.LinkScopedWorkItemsOf(readCtx, scope, []string{item.CanonicalID})
	if err != nil || !complete {
		return contractsv1.ContextFabricNamedRepositoryLinkUnreadLimitation
	}
	tier, linked := tiers[item.CanonicalID]
	if !linked {
		return contractsv1.ContextFabricNamedRepositoryNoLinkLimitation
	}
	return contractsv1.ContextFabricNamedRepositoryLinkedLimitation(tier)
}

// unresolvedNamedHandle ends a turn whose handle did not resolve to one subject. A
// complete zero is stated in the census's own scope. A mentioned handle changes
// nothing else. An owed handle takes the repository out of the answer, and a complete
// count of several is listed.
func unresolvedNamedHandle(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, handle contractsv1.ContextFabricRequestedHandle, claim handleClaim, anchored bool, census namedCensus, anchorKind contextfabric.SubjectKind) (contextfabric.SubjectResolution, *BoundHandle) {
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
	// one, so it is not what the caller asked about (codex r2 on #1920). Only a kind
	// with a repository key can be told apart here (committedSubjectRepository); a
	// work item cannot, so every committed work item stays (codex r3 on #1920).
	if !census.complete && len(request.SubjectHandles) == 0 {
		if anchored {
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
	where := "in the repository"
	if anchorKind == contextfabric.SubjectTeam {
		where = "in the repositories the team owns"
		if !anchored {
			where = "in what you can read"
		}
	}
	resolution = demoteAnchors(request, resolution, bases, digests, anchorKind, handle.Kind, "The "+handleKindLabel(handle.Kind)+" "+handle.Value+" could not be matched to exactly one record "+where)
	if absent {
		resolution.CensusLimitation = censusAbsenceLimitation(principal, request, ka)
		return resolution, &BoundHandle{Kind: handle.Kind, Value: handle.Value}
	}
	if !census.complete || !questionBindsHandle(turnHandles(request, deps), handle) {
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

// turnHandles is the turn's handles after the work item key gate, read once in
// ResolveSubjectsWithCommitBasis (CHAOS-12463). A direct call has no turn, so
// nothing read the gate, and the grammar alone answers.
func turnHandles(request contextfabric.InvestigationRequest, deps ResolveDeps) []BoundHandle {
	if deps.questionHandlesBound {
		return deps.questionHandles
	}
	return BindHandles(request.Question)
}

func questionBindsHandle(bound []BoundHandle, handle contractsv1.ContextFabricRequestedHandle) bool {
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
	explicit := len(request.SubjectHandles) > 0
	outcome, err := handleCensusOutcome(ctx, principal, deps, kind, value, anchor, anchored)
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

// handleCensusOutcome is one keyed census of a handle, inside the caller's grant,
// under the round's deadline, a panic read as an error. It traces nothing: its
// callers decide what the outcome was.
func handleCensusOutcome(ctx context.Context, principal storage.Principal, deps ResolveDeps, kind contextfabric.SubjectKind, value string, anchor contextfabric.SubjectRef, anchored bool) (outcome CensusOutcome, err error) {
	roundCtx, cancel := context.WithTimeout(contextfabric.ScopeCensusToCaller(ctx, principal), evidenceRoundDeadline)
	defer cancel()
	defer func() {
		if recover() != nil {
			err = errCensusPanicked
		}
	}()
	return grantEnforced(deps.CensusFunc)(roundCtx, principal.OrgID, kind, value, true, anchor.Kind, anchor.CanonicalID, anchored)
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
	return demoteAnchors(request, resolution, bases, digests, contextfabric.SubjectRepository, handleKind, question)
}

// demoteAnchors is demoteRepositoryAnchors for an anchor of anchorKind: a
// repository, or a team a handle was named beside (CHAOS-12557).
func demoteAnchors(request contextfabric.InvestigationRequest, resolution contextfabric.SubjectResolution, bases contextfabric.CommitBasisSet, digests contextfabric.CommitDecisionDigestSet, anchorKind, handleKind contextfabric.SubjectKind, question string) contextfabric.SubjectResolution {
	kept := make([]contextfabric.SubjectRef, 0, len(resolution.Committed))
	labels := make([]string, 0, len(resolution.Committed))
	demoted := map[string]bool{}
	for _, subject := range resolution.Committed {
		if subject.Kind != anchorKind && subject.Kind != handleKind {
			kept = append(kept, subject)
			continue
		}
		demoted[SubjectKey(subject)] = true
		delete(bases, contextfabric.SubjectMapKey(subject))
		delete(digests, contextfabric.SubjectMapKey(subject))
		if subject.Kind != anchorKind {
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
