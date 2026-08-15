package graphrank

import (
	"context"
	"errors"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// questionProvenanceMarker is the BOUNDED provenance CHAOS-3838's
// question-level SearchQuestion pass records as a candidate's MatchedTerms
// entry (and folds into its ReceiptID derivation), in place of the raw
// question text (codex round-1 P1, fix A).
//
// The raw question is caller-supplied free text with no length bound --
// unlike an interpretation's extracted SubjectTerms, which are short
// phrases -- and contractsv1's SubjectCandidate.Validate() rejects any
// MatchedTerms entry over 512 characters (matchedTermLength,
// contextFabricWriteBounds, internal/contracts/v1/validate_context_fabric_result.go).
// That bound is unexported and therefore cannot be referenced directly from
// here; this literal is deliberately far under it, and
// TestResolveSubjects_SearchQuestionOversizedQuestionStaysContractValid
// cross-checks against the REAL exported Validate() (never a mirrored
// numeric constant), so a future contract tightening trips a test here
// rather than silently reintroducing the P1.
//
// Bracket-wrapped and lowercase so it cannot plausibly collide with a real
// extracted subject term (SubjectTerms are short bare phrases -- "auth
// service", "PR 52" -- never bracket-wrapped). A coincidental collision
// would just dedupe under MergeCandidates' UniqueSorted union, which is
// harmless, not a correctness bug -- this is a legibility choice, not a
// uniqueness guarantee this package enforces.
const questionProvenanceMarker = "[full question]"

// matchedTermsCap mirrors contractsv1's own unexported MatchedTerms entry
// count bound (matchedTerms:32, contextFabricWriteBounds) -- see
// questionProvenanceMarker's doc comment for why it is mirrored rather than
// referenced, and which test cross-checks it against the real Validate().
const matchedTermsCap = 32

// capMatchedTermsAfterQuestionMerge enforces matchedTermsCap on every
// candidate the question-level pass touched (codex round-1 P1, fix A,
// second half). A candidate already carrying matchedTermsCap real,
// user-meaningful extracted terms overflows by exactly one once
// questionProvenanceMarker unions in; the marker -- synthetic, not
// something a caller typed -- is the entry dropped to restore the bound,
// never a real term.
//
// Walks the WHOLE map rather than tracking which keys the question pass
// touched: cheap at resolution scale (at most a few dozen candidates), and
// simpler than threading a touched-set through mergeSearchResults for a
// property that is a pure function of each candidate's own final
// MatchedTerms. A candidate that already exceeded the cap from real terms
// ALONE (a pre-existing, this-ticket-unrelated gap: mergeSearchResults'
// shared per-term path has never capped MatchedTerms) is left as-is here --
// this function's job is only to keep a PREVIOUSLY-valid candidate valid
// after the question marker unions in, not to retrofit a bound onto
// per-term merging this ticket did not touch.
func capMatchedTermsAfterQuestionMerge(candidatesBySubject map[string]contextfabric.SubjectCandidate) {
	for key, candidate := range candidatesBySubject {
		if len(candidate.MatchedTerms) <= matchedTermsCap {
			continue
		}
		trimmed := make([]string, 0, len(candidate.MatchedTerms))
		for _, term := range candidate.MatchedTerms {
			if term == questionProvenanceMarker {
				continue
			}
			trimmed = append(trimmed, term)
		}
		candidate.MatchedTerms = trimmed
		candidatesBySubject[key] = candidate
	}
}

// ResolveDeps carries the backend I/O ResolveSubjects needs. Every function
// field is required.
type ResolveDeps struct {
	// ExactHint looks up a subject by exact canonical identity, scoped to
	// the calling organization. ok=false with a nil error means "not
	// found" -- a safe non-match, not an error.
	ExactHint func(ctx context.Context, subject contextfabric.SubjectRef) (node CandidateNode, ok bool, err error)
	// Search performs bounded node-scoped hybrid search for term.
	//
	// degraded reports that a retrieval MECHANISM was unavailable for this
	// call -- e.g. CHAOS-3778's vector step timed out, errored, or was fenced
	// off -- so the candidate set may be narrower than a healthy run would
	// produce. It is distinct from truncated: truncated means "there were
	// more results than the budget could show", degraded means "one way of
	// finding results did not run at all". A backend with no optional
	// mechanism always reports false.
	//
	// truncated reports whether the backend's own bound on this result set
	// (e.g. a server-side row LIMIT) means genuinely competing candidates
	// could have been left out before ResolveSubjects ever saw them -- see
	// ResolveFromMergedCandidates' searchTruncated parameter for what that
	// means for auto-commit eligibility. A backend with no such bound (or
	// one that fetched a generous superset with no risk of missing a real
	// competitor) always reports false.
	Search func(ctx context.Context, term string, limit int) (candidates []CandidateNode, truncated bool, degraded bool, err error)
	// Traverse implements observation-to-entity traversal for a matched
	// document/episode node -- see TraverseObservationToSubject, which a
	// backend's own Traverse implementation should call with its own
	// GetNodeEdges/GetNode-equivalent I/O bound in, forwarding allowExactMatch
	// unchanged (codex round-2 P1) -- see NodeCandidate's doc comment for what
	// it gates and why.
	Traverse func(ctx context.Context, term string, observation CandidateNode, allowExactMatch bool) (contextfabric.SubjectCandidate, ObservationTraversal)
	// IsInternal reports whether subject is one of the backend's own
	// bookkeeping nodes (see NodeCandidate's isInternal parameter).
	IsInternal func(contextfabric.SubjectRef) bool
	// TraversalDegraded optionally reports how many Traverse calls in this
	// ResolveSubjects call ended in ObservationTraversalErrored. May be nil.
	TraversalDegraded func(ctx context.Context, orgID string, count int)
	// SearchQuestion optionally runs ONE additional retrieval pass over the
	// full interpreted QUESTION text, rather than one extracted subject
	// term (CHAOS-3838 / spec L11 -- the "union" read-side lever). Nil
	// means this backend has no such capability (or a configured mechanism
	// declined -- e.g. vector retrieval is off), and ResolveSubjects treats
	// that exactly like "found nothing", never as an error.
	//
	// Called AT MOST ONCE per ResolveSubjects call, after every per-term
	// Search call has already run -- never once per term -- which is what
	// keeps this to the ticket's stated budget of one extra provider call
	// per resolution rather than one per term. Its result merges into the
	// SAME candidatesBySubject map, through the SAME NodeCandidate/
	// MergeCandidates/traversal path every per-term Search result does, so
	// a subject the question-level pass finds competes and corroborates
	// identically to one a term-level pass finds -- see mergeSearchResults.
	SearchQuestion func(ctx context.Context, question string, limit int) (candidates []CandidateNode, truncated bool, degraded bool, err error)
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
		// allowExactMatch=true: subject.Label here is the caller's own
		// explicit hint label, legitimately eligible to exact-match --
		// unlike CHAOS-3838's question-provenance marker below, this is
		// genuine caller-supplied identity, and this whole branch already
		// forces Confidence/MatchExact explicitly regardless.
		candidate, ok := NodeCandidate(principal, request.RequestedScope, subject.Label, node, deps.IsInternal, true)
		if !ok {
			continue
		}
		candidate.Confidence = 1
		candidate.State = contextfabric.ResolutionCommitted
		candidate.MatchReasons = []string{"Exact canonical subject hint matched the organization graph."}
		// An exact canonical hint IS the exact mechanism, whatever the
		// ExactHint implementation did or did not declare on the node.
		candidate.MatchMechanisms = MergeMechanisms(
			candidate.MatchMechanisms,
			[]contextfabric.MatchMechanism{contextfabric.MatchExact},
		)
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
	// retrievalDegraded is true if ANY of this invocation's Search() calls
	// reported that a retrieval mechanism was unavailable (codex round-1 F4).
	// Like searchTruncated it is a property of the WHOLE resolution, not of
	// any one candidate -- a mechanism that failed for one term leaves the
	// resolution as a whole narrower than it should have been.
	retrievalDegraded := false
	for _, term := range terms {
		results, truncated, degraded, err := deps.Search(ctx, term, request.Options.MaxSubjectCandidates)
		if err != nil {
			return contextfabric.SubjectResolution{}, err
		}
		if truncated {
			searchTruncated = true
		}
		if degraded {
			retrievalDegraded = true
		}
		// allowExactMatch=true: term here is genuine caller-derived search
		// input (an interpreted subject term, or a requested-scope hint
		// label -- see SubjectTerms), legitimately eligible to exact-match
		// a subject's own label.
		traversalDegraded += mergeSearchResults(ctx, principal, request, deps, term, results, candidatesBySubject, observationParentKey, observationBlocked, true)
	}
	// CHAOS-3838 (spec L11): the question-level pass runs AT MOST ONCE,
	// AFTER every per-term pass above, never interleaved with it. Ordering
	// matters for determinism, not just budget: MergeCandidates' winner/
	// loser choice is order-sensitive on an exact confidence TIE (the
	// earlier-processed candidate wins ties), so running this after the
	// term loop means every per-term-only resolution keeps byte-identical
	// output to before this ticket, and the question pass can only ever
	// ADD subjects or lose a tie to one a term already found -- never
	// silently reorder which of two tied term-level finds "wins".
	if deps.SearchQuestion != nil {
		question := strings.TrimSpace(request.Question)
		if question != "" {
			results, truncated, degraded, err := deps.SearchQuestion(ctx, question, request.Options.MaxSubjectCandidates)
			if err != nil {
				return contextfabric.SubjectResolution{}, err
			}
			if truncated {
				searchTruncated = true
			}
			if degraded {
				retrievalDegraded = true
			}
			// codex round-1 P1 (fix A): mergeSearchResults' term parameter
			// becomes MatchedTerms/ReceiptID provenance (NodeCandidate) --
			// the raw QUESTION, unlike an extracted subject term, is
			// caller-supplied free text with no length bound, and
			// contractsv1's SubjectCandidate.Validate() rejects any
			// MatchedTerms entry over 512 chars. Passing the raw question
			// here made a >512-char question produce an INVALID resolution
			// (a 500 downstream). questionProvenanceMarker is the bounded,
			// contract-safe substitute -- see its own doc comment.
			//
			// allowExactMatch=false (codex round-2 P1): questionProvenanceMarker
			// is a SYNTHETIC provenance literal, not caller-typed text -- a
			// subject that happened to be labeled the same literal string
			// must never win an exact-match promotion (confidence 1.0,
			// MatchExact) from it. Every question-path candidate's
			// confidence and mechanism come ONLY from the vector similarity
			// band (node.Relevance/node.Mechanism), by construction --
			// see NodeCandidate's doc comment.
			traversalDegraded += mergeSearchResults(ctx, principal, request, deps, questionProvenanceMarker, results, candidatesBySubject, observationParentKey, observationBlocked, false)
			// codex round-1 P1, second half: a candidate already at the
			// 32-entry MatchedTerms cap from real per-term finds would
			// overflow to 33 once the marker above unioned in. The marker
			// -- synthetic provenance, not something a caller typed -- is
			// exactly the one entry that must give way; every real,
			// user-meaningful extracted term survives.
			capMatchedTermsAfterQuestionMerge(candidatesBySubject)
		}
	}
	if traversalDegraded > 0 && deps.TraversalDegraded != nil {
		deps.TraversalDegraded(ctx, principal.OrgID, traversalDegraded)
	}
	resolution := ResolveFromMergedCandidates(candidatesBySubject, observationParentKey, observationBlocked, request.Options.MaxSubjectCandidates, request.Options.AllowClarification, searchTruncated)
	resolution.RetrievalDegraded = retrievalDegraded
	return resolution, nil
}

// mergeSearchResults is the ONE per-node ingestion path shared by
// ResolveSubjects' per-term Search loop and its single question-level
// SearchQuestion call (CHAOS-3838): convert each result node to a
// SubjectCandidate, merge it into candidatesBySubject (CHAOS-3778's
// mechanism-preserving MergeCandidates, never a plain confidence
// comparison), and -- for an observation node (document/episode) -- walk
// traversal back to whichever canonical entity it is attached to and merge
// that too. Extracted verbatim from ResolveSubjects' original per-term loop
// body so the term path and the question path CANNOT independently drift on
// how a found node becomes a candidate; the only difference between the two
// callers is which term/results pair they hand in.
//
// term is the provenance label recorded on every SubjectCandidate this call
// produces (NodeCandidate's ReceiptID derivation, MatchedTerms, and its own
// "does this equal the node's own name/label" exact-match check) -- the
// per-term loop passes the extracted subject term; the question-level call
// passes the full question text, which will essentially never equal a
// node's own name, so it correctly never claims the MatchExact bump on its
// own.
//
// Returns the count of ObservationTraversalErrored outcomes this call
// produced, for the caller to fold into ResolveSubjects' own running total
// (TraversalDegraded's single aggregate report covers both passes).
// allowExactMatch is threaded straight through to every NodeCandidate/
// Traverse call this function makes (codex round-2 P1) -- see
// NodeCandidate's doc comment for what it gates. Both this function's
// callers in ResolveSubjects document why they pass the value they do.
func mergeSearchResults(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, term string, results []CandidateNode, candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, allowExactMatch bool) int {
	traversalErrored := 0
	for _, node := range results {
		candidate, ok := NodeCandidate(principal, request.RequestedScope, term, node, deps.IsInternal, allowExactMatch)
		if !ok {
			continue
		}
		key := SubjectKey(candidate.Subject)
		// CHAOS-3778: MergeCandidates replaces a plain "keep the higher
		// confidence" here. The higher-confidence finding still supplies
		// the spine and the base confidence, so nothing about a
		// single-mechanism candidate changes; what is new is that the
		// loser's MECHANISMS survive instead of being discarded, which is
		// the whole signal the corroborated band reads (see
		// MergeCandidates and CorroboratedConfidence).
		if current, exists := candidatesBySubject[key]; exists {
			candidatesBySubject[key] = MergeCandidates(current, candidate)
		} else {
			candidatesBySubject[key] = candidate
		}
		// Observation-to-entity traversal: a hybrid match on a document or
		// episode node means the term appeared in text *about* some
		// canonical entity, not necessarily that the caller is asking about
		// the document/episode itself. Walk back to whichever entity that
		// observation is attached to and propose it as an additional
		// candidate (never a replacement -- a caller may genuinely mean the
		// document or episode).
		if IsObservationSubjectKind(candidate.Subject.Kind) {
			traversed, outcome := deps.Traverse(ctx, term, node, allowExactMatch)
			switch outcome {
			case ObservationParentFound:
				observationBlocked[key] = true
				traversedKey := SubjectKey(traversed.Subject)
				observationParentKey[key] = traversedKey
				// Same merge rule as the direct-hit path above: a parent
				// that BOTH a direct search and a traversal proposed must
				// keep both mechanisms, because that pairing is exactly
				// what the corroborated band is meant to reward.
				if current, exists := candidatesBySubject[traversedKey]; exists {
					candidatesBySubject[traversedKey] = MergeCandidates(current, traversed)
				} else {
					candidatesBySubject[traversedKey] = traversed
				}
			case ObservationTraversalErrored:
				observationBlocked[key] = true
				traversalErrored++
			case ObservationNoParent:
				// Confirmed: no parent. Leave eligible.
			}
		}
	}
	return traversalErrored
}
