package graphrank

import (
	"context"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12467. A census that counted the subject a handle names used to end at
// the reorder: SurvivorsFirstOrder reads the ids, and on an empty pool it had
// nothing to reorder, so the turn said retrieval found no candidate. This step
// lists what the census proved, as candidates a caller can confirm.
//
// IT CANNOT COMMIT. It runs after the commit decision, appends ambiguous
// candidates, and never re-runs that decision. It reads a kind only when the
// handle was applied to it: an anchor-only census counts a population, not a named
// subject. Every id still passes the node gate, as a merge does.
//
// A count it cannot list is stated, never dropped: see the sentences in
// contracts/v1/context_fabric_census_listing_limitation.go, and the rule in
// docs/design/context-fabric-grant-scoped-census.md.
//
// The ids stay out of every trace (slice B doc §4): the line carries counts.

// bareUUID is lower-case text that is a UUID and nothing else.
var bareUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// censusListingMaxReads bounds the keyed graph reads of one listing, as
// censusNarrowingMaxSatisfiers bounds a narrowing's.
const censusListingMaxReads = censusNarrowingMaxSatisfiers

// The closed vocabularies of the evidence_census_listing line.
const (
	censusListingListed       = "listed"
	censusListingPartlyListed = "partly_listed"
	censusListingUnlisted     = "unlisted"

	censusListingOverBudget         = "over_budget"
	censusListingClosureMismatch    = "closure_mismatch"
	censusListingNarrowingUndecided = "narrowing_undecided"
	censusListingBudgetBelowTwo     = "budget_below_two"
	censusListingRefused            = "refused"
)

// CensusListingOutcomeVocabulary is the closed list of listing outcomes.
func CensusListingOutcomeVocabulary() []string {
	return []string{censusListingListed, censusListingPartlyListed, censusListingUnlisted}
}

// CensusListingReasonVocabulary is the closed list of reasons a count went
// unlisted, "" when it did not.
func CensusListingReasonVocabulary() []string {
	return []string{"", censusListingOverBudget, censusListingClosureMismatch, censusListingNarrowingUndecided, censusListingBudgetBelowTwo, censusListingRefused}
}

// handleAttestation is the one kind the question's handle was applied to: a round
// applies a handle to its own kind and to no other.
func handleAttestation(attestation Attestation) (KindAttestation, bool) {
	for _, ka := range attestation.Kinds {
		if ka.HandleApplied {
			return ka, true
		}
	}
	return KindAttestation{}, false
}

// listableSatisfiers is what a round's kind can list and the count it speaks for.
// A reason says why it holds no ids it may trust. A repository narrowing that
// refused every satisfier keeps its own sentence, so it speaks for nothing.
func listableSatisfiers(ka KindAttestation, request contextfabric.InvestigationRequest) (ids []string, count int, reason string) {
	switch {
	case ka.ClosureMismatch || ka.SatisfierSetClosureMismatch:
		return nil, ka.Count, censusListingClosureMismatch
	case ka.Narrowing.outcome == narrowTruncated || ka.Narrowing.outcome == narrowReadFailed:
		return nil, ka.Count, censusListingNarrowingUndecided
	case ka.Narrowing.outcome == narrowedToNone && len(request.RequestedScope.RepositorySlugs) > 0:
		return nil, 0, ""
	case ka.Narrowing.outcome == narrowedToMany:
		return ka.Narrowing.kept, len(ka.Narrowing.kept), ""
	case ka.Count > CensusSatisfierSetBudget:
		return nil, ka.Count, censusListingOverBudget
	case ka.Count == 1 && ka.SatisfierCanonicalID != "":
		return []string{ka.SatisfierCanonicalID}, 1, ""
	}
	return ka.SatisfierCanonicalIDs, ka.Count, ""
}

// listCensusSatisfiers lists the satisfiers of the question's handle. The pointer
// it returns is the handle when several subjects satisfy it: it cannot decide, so
// it is not offered (withoutUndecidingHandle).
func listCensusSatisfiers(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, attestation Attestation, resolution contextfabric.SubjectResolution) (contextfabric.SubjectResolution, *BoundHandle) {
	if !attestation.UnscopedVisibility || len(resolution.Committed) != 0 {
		return resolution, nil
	}
	bound := BindHandles(request.Question)
	if len(bound) != 1 {
		return resolution, nil
	}
	handle := bound[0]
	ka, ok := handleAttestation(attestation)
	if !ok || !ka.Complete || ka.Count < 1 {
		return resolution, nil
	}
	var undeciding *BoundHandle
	if ka.Count >= 2 {
		undeciding = &handle
	}
	ids, count, reason := listableSatisfiers(ka, request)
	if count < 1 {
		return resolution, undeciding
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	// What the census named, whether or not the budget lets it be read: a
	// satisfier the search already holds is shown, and the sentence must count it.
	named := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		named[SubjectKey(contextfabric.SubjectRef{Kind: ka.Kind, CanonicalID: id})] = struct{}{}
	}
	limit := min(request.Options.MaxSubjectCandidates, censusListingMaxReads)
	if reason == "" && count >= 2 && limit < 2 {
		ids, reason = nil, censusListingBudgetBelowTwo
	}
	satisfier := make(map[string]struct{}, len(ids))
	pooled := make(map[string]contextfabric.SubjectCandidate, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		pooled[SubjectKey(candidate.Subject)] = candidate
	}
	// A work item the round scoped by the link walk is in the requested repository
	// scope through its link: the gate tests the issue side of that rule, never
	// its own repository.
	scope := request.RequestedScope
	if linkScopedSatisfier(attestation, ka.Kind) {
		scope = issueScopeOfLink(scope)
	}
	term := request.Question[handle.SpanStart:handle.SpanEnd]
	graphCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	started := time.Now()
	reads, refused := 0, 0
	listed := make([]contextfabric.SubjectCandidate, 0, len(ids))
	fresh := 0
	for _, id := range ids {
		if len(listed) >= limit || reads >= limit {
			break
		}
		subject := contextfabric.SubjectRef{Kind: ka.Kind, CanonicalID: id}
		key := SubjectKey(subject)
		satisfier[key] = struct{}{}
		if candidate, held := pooled[key]; held {
			listed = append(listed, candidate)
			continue
		}
		if deps.ExactHint == nil {
			break
		}
		reads++
		node, exists, err := deps.ExactHint(graphCtx, subject)
		if err != nil || !exists {
			refused++
			continue
		}
		candidate, admitted := NodeCandidate(principal, scope, term, node, deps.IsInternal, false, deps.ResolutionTracer, request.RequestID)
		if !admitted {
			refused++
			continue
		}
		candidate.State = contextfabric.ResolutionAmbiguous
		candidate.Subject.Label = censusCandidateLabel(principal, handle, candidate.Subject, node.Attributes)
		listed = append(listed, candidate)
		fresh++
	}
	elapsed := time.Since(started)
	if fresh > 0 {
		resolution.Candidates = satisfiersFirst(listed, resolution.Candidates, satisfier, request.Options.MaxSubjectCandidates)
		if request.Options.AllowClarification {
			resolution.ClarificationPrompt = ClarificationPrompt(resolution.Candidates)
		}
	}
	shown := 0
	for _, candidate := range resolution.Candidates {
		if _, held := named[SubjectKey(candidate.Subject)]; held {
			shown++
		}
	}
	outcome := censusListingListed
	switch {
	case shown == 0:
		outcome = censusListingUnlisted
		if reason == "" {
			reason = censusListingRefused
		}
		// A lone work item a restricted caller cannot reach has its own sentence.
		if !(ka.Kind == contextfabric.SubjectWorkItem && count == 1 && grantScopedCensus(principal)) {
			resolution.CensusLimitation = contractsv1.ContextFabricCensusUnlistedLimitation(count)
		}
	case shown < count:
		outcome = censusListingPartlyListed
		resolution.CensusLimitation = contractsv1.ContextFabricCensusListedLimitation(count, shown)
	}
	if deps.ResolutionTracer != nil {
		deps.ResolutionTracer.Trace(ResolutionTraceEvent{
			RequestID: request.RequestID, Stage: "evidence_census_listing", CensusKind: ka.Kind, CensusCount: count,
			CensusListed: shown, CensusListingReads: reads, CensusListingRefused: refused, CensusListingElapsedMs: elapsed.Milliseconds(),
			CensusListingOutcome: outcome, CensusListingReason: reason,
		})
	}
	return resolution, undeciding
}

// satisfiersFirst puts what the census proved ahead of the pool, so a noisy pool
// cannot fill the budget, and cuts at the request's budget.
func satisfiersFirst(listed, pool []contextfabric.SubjectCandidate, satisfier map[string]struct{}, budget int) []contextfabric.SubjectCandidate {
	merged := make([]contextfabric.SubjectCandidate, 0, len(listed)+len(pool))
	merged = append(merged, listed...)
	held := maps.Clone(satisfier)
	for _, candidate := range pool {
		key := SubjectKey(candidate.Subject)
		if _, is := held[key]; is {
			continue
		}
		merged = append(merged, candidate)
	}
	return merged[:min(len(merged), budget)]
}

// withoutUndecidingHandle drops the option of a handle several subjects satisfy.
// A redeemed handle never reaches a commit, so the option would loop.
func withoutUndecidingHandle(offer contextfabric.StructureOfferMaterial, undeciding *BoundHandle) contextfabric.StructureOfferMaterial {
	if undeciding == nil {
		return offer
	}
	offer.HandleOptions = slices.DeleteFunc(slices.Clone(offer.HandleOptions), func(option contractsv1.ContextFabricHandleOption) bool {
		return option.Kind == undeciding.Kind && option.Value == undeciding.Value
	})
	return offer
}

// withoutVetoedHandles drops an option the request's own subject_handles would
// veto. A receipt must agree with them (resolveExplicitStructure), so redeeming
// any other value ends the follow-up in a conflict.
func withoutVetoedHandles(offer contextfabric.StructureOfferMaterial, explicit []contractsv1.ContextFabricRequestedHandle) contextfabric.StructureOfferMaterial {
	if len(explicit) == 0 {
		return offer
	}
	offer.HandleOptions = slices.DeleteFunc(slices.Clone(offer.HandleOptions), func(option contractsv1.ContextFabricHandleOption) bool {
		return !slices.ContainsFunc(explicit, func(handle contractsv1.ContextFabricRequestedHandle) bool {
			return handle.Kind == option.Kind && handle.Value == option.Value
		})
	})
	return offer
}

// censusCandidateLabel is the server's label for a candidate the census named. The
// node's own label is not trusted: some nodes carry their raw canonical id as a
// label (CHAOS-12468), and a shared title leaves the prompt to its id suffix cue.
func censusCandidateLabel(principal storage.Principal, handle BoundHandle, subject contextfabric.SubjectRef, attributes map[string]interface{}) string {
	label := handleOfferLabel(handle.Kind, handle.Value)
	if subject.Kind == contextfabric.SubjectWorkItem {
		if provider := workItemProvider(subject.CanonicalID); provider != "" {
			label += " (" + provider + ")"
		}
	} else if slug := grantedRepositorySlug(principal, attributes); slug != "" {
		label += " in " + slug
	}
	if title := strings.TrimSpace(subject.Label); title != "" && !rawSubjectLabel(subject, title) {
		label += ": " + title
	}
	bounded, _ := candidateOfferLabel(label)
	return bounded
}

// rawSubjectLabel reports a title that is an identifier: one that holds a canonical
// id of its kind, escaped or not, or that is a bare UUID. A real title that mentions
// a number, the kind's name, or a UUID among its words, stays.
func rawSubjectLabel(subject contextfabric.SubjectRef, title string) bool {
	plain := plainIdentifierText(title)
	return bareUUID.MatchString(plain) || holdsIDOfKind(plain, string(subject.Kind))
}

// holdsIDOfKind reports text with a canonical id in it, the subject's own or
// another's: the kind's prefix, and no space or end after it.
func holdsIDOfKind(plain, kind string) bool {
	for _, prefix := range []string{kind + ":", kind + ".v2:"} {
		if _, rest, found := strings.Cut(plain, prefix); found && rest != "" && !strings.HasPrefix(rest, " ") {
			return true
		}
	}
	return false
}

// plainIdentifierText is text with its percent escapes undone, in lower case: two
// rounds, so an id escaped twice still shows its prefix. A title with a bare percent
// sign stops at once, as it was written.
func plainIdentifierText(text string) string {
	for range 2 {
		decoded, err := url.PathUnescape(text)
		if err != nil || decoded == text {
			break
		}
		text = decoded
	}
	return strings.ToLower(text)
}

// grantedRepositorySlug is a repository of the node the caller may read. A node
// can list an old slug beside the one that admits it, so position decides nothing.
func grantedRepositorySlug(principal storage.Principal, attributes map[string]interface{}) string {
	scopes := principal.RepositoryScopes
	if scopesUnrestricted(scopes) {
		scopes = []string{"*"}
	}
	entries, _ := attributes[authorizationRepositoriesAttr].([]string)
	for _, slug := range slices.Sorted(slices.Values(entries)) {
		if auth.RepositoryAllowed(scopes, slug) {
			return slug
		}
	}
	return ""
}

// workItemProvider is the tracker of a work item, from its natural key.
func workItemProvider(canonicalID string) string {
	segments, ok := identity.Segments(identity.KindWorkItem, canonicalID)
	if !ok || len(segments) != 2 {
		return ""
	}
	provider, _, found := strings.Cut(segments[1], ":")
	if !found {
		return ""
	}
	return provider
}
