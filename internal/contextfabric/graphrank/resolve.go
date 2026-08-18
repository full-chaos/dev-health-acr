package graphrank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
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
	// VectorMarginCommitThreshold is CHAOS-3829's per-embedder-identity
	// calibrated M (falkorgraph's retrieval_policy.go RetrievalPolicy.
	// VectorMarginCommitThreshold): the vector-arm top-1/top-2 similarity
	// margin ResolveFromMergedCandidates' commit-path carve-out requires
	// before it will auto-commit a corroborated-but-otherwise-ambiguous
	// top-1. Zero means "uncalibrated for this identity" -- the SAME
	// zero-means-unchanged convention OverFetchMultiplier/EfRuntime already
	// use -- and disables the carve-out entirely, byte-identical to
	// pre-CHAOS-3829 behavior. A backend with no calibrated identity (or no
	// vector retrieval at all) leaves this at its zero value.
	VectorMarginCommitThreshold float64
	// CalibratedTopK is CHAOS-3829 codex r5 K1's (accepted) companion to
	// VectorMarginCommitThreshold: the lexical/vector search depth
	// CalibrateMarginFromReport's oracle measured corroboration AT (pinned
	// 20 for the shipped identity, RetrievalPolicy.CalibratedTopK). Zero
	// means "uncalibrated", the same convention every other calibrated field
	// on this struct uses, and -- together with MaxResultsCap below --
	// disables the carve-out just as surely as
	// VectorMarginCommitThreshold==0 does; see
	// ResolveFromMergedCandidates' effectiveSearchLimit/calibratedTopK
	// envelope for why both bounds are required.
	CalibratedTopK int
	// MaxResultsCap is the backend's own configured per-call result-row cap
	// (falkorgraph: a.config.MaxResults, ACR_CONTEXT_FABRIC_FALKOR_MAX_RESULTS)
	// -- CHAOS-3829 codex r5 K2's (accepted) fix input. ResolveSubjects uses
	// it to compute the REAL, cap-clamped per-call returned-row bound
	// (effectiveSearchLimit) that ResolveFromMergedCandidates' commit-path
	// carve-out gates on, rather than trusting the caller's nominal
	// request.Options.MaxSubjectCandidates alone -- see
	// ResolveFromMergedCandidates' own doc comment (codex r5 K2) for the
	// hazard a mismatched cap otherwise opens. Zero (or <=0) means "no known
	// cap": effectiveSearchLimit then falls back to the request-side value
	// unclamped, which a backend with no such cap (or one that never
	// truncates below the request) can safely leave as the zero value.
	MaxResultsCap int
	// CommitGatePolicy is CHAOS-3857's sweep/measurement override for
	// ResolveFromMergedCandidatesWithGate's three commit-gate thresholds
	// (LoneFloor/TopFloor/TopGap). The ZERO VALUE means "not overridden":
	// ResolveSubjects falls back to graphrank.DefaultCommitGatePolicy()
	// (the calibrated production thresholds), NOT to a zero-threshold
	// policy that would auto-commit everything -- see the call site below.
	// This is a DIFFERENT zero-value convention than
	// VectorMarginCommitThreshold/CalibratedTopK above (whose zero means
	// "carve-out disabled"), because a zero commit-gate floor is never a
	// meaningful "off" state the way a zero margin/topK is; it would be
	// the most PERMISSIVE possible policy, the opposite of what an unset
	// override should mean. A backend with no override leaves this at its
	// zero value and gets exactly today's calibrated behavior.
	CommitGatePolicy CommitGatePolicy
	// RawSignalObserver (CHAOS-3858, measurement-only) is an optional,
	// nil-by-default hook: when set, ResolveSubjects reports the raw
	// pre-remap signal (CandidateNode.VectorSimilarity,
	// LexicalMatchedTerms/LexicalTermCount) for every ACCEPTED candidate --
	// i.e. AFTER NodeCandidate's own authorization/acceptance check, never
	// before, so this cannot become the kind of pre-authorization existence
	// oracle vectorArmSimilarity's own doc comment (mergeSearchResults)
	// warns about: it only ever describes a candidate the caller's own
	// result already contains. No production caller sets this -- every real
	// deployment leaves it nil, and a nil observer is never invoked, so
	// this has no effect on any resolution decision. Exists to let a
	// measurement harness (never a production consumer) compare the raw
	// signal against the remapped Confidence without touching any commit
	// gate.
	RawSignalObserver RawSignalObserver
	// AliasLookup (CHAOS-3884, optional) is a COMPLETE, keyed identity-claimant
	// read: given this resolution's own subject terms, return every subject
	// (any isAliasLookupScopedKind kind -- slice 1: repository, project,
	// team, see that registry's own doc comment for why work_item is
	// deliberately excluded) whose canonical label OR alias/provider-alias
	// set contains any of terms (normalized via NormalizeAliasTerm), keyed
	// by which ORIGINAL term (as passed in, not normalized) it was found
	// for. Unlike Search, this MUST NOT be a ranked/truncatable relevance
	// search -- see the design doc's Option C for the completeness argument
	// (a bounded full-population enumeration over the org's
	// identity-bearing source data, matched in Go, existence-checked
	// against the graph before ever being returned here). Every returned
	// CandidateNode's Mechanism MUST be set to the key class that matched
	// (MatchExact for a label hit, MatchAlias for a bare-name/native-key
	// hit, MatchProviderKey for a provider-variant hit) -- NodeCandidate
	// trusts this AS DECLARED for a lookup-sourced node rather than
	// re-deriving it from attribute text.
	//
	// FromKeyedIdentityLookup MUST be true for every returned CandidateNode
	// UNLESS this SAME call also had at least one claimant fail its own
	// existence check (graph-missing) -- decision 1 (team-lead amendment,
	// 2026-08-17, settled): when that happens, EVERY survivor of the call
	// must have FromKeyedIdentityLookup false instead, not just complete
	// (below). This is not optional hardening: identityCollision
	// (resolution.go) counts the CANDIDATE set, not the table set
	// completeness is proven over, so a claimant that silently vanished
	// from that count would otherwise let a surviving sibling's
	// confidence=1 identity-trust bump (NodeCandidate's identityTrusted,
	// gated on FromKeyedIdentityLookup alone, independent of complete)
	// clear LoneFloor/TopFloor/the CHAOS-3829 rescue on a claim this call
	// never actually proved unique -- complete=false ALONE only ever
	// disabled the one dedicated fast-path switch case, not those three
	// other sites. See falkorgraph's own AliasLookup closure
	// (reader.go) for the reference implementation of this rule.
	//
	// complete=false whenever this resolution's identity view could not be
	// guaranteed complete: a per-call row budget was exceeded, the read
	// timed out, a source-table claimant could not be confirmed present in
	// the graph (existence-check failure folds into incompleteness HERE,
	// rather than as a separately-threaded flag), or this resolution's time
	// axis is non-current (temporal authority stays with the graph; a
	// historical-axis question simply never gets this mechanism -- the
	// implementation decides this via whatever temporal state it already
	// captures for Search/SearchQuestion, exactly the same closure-capture
	// shape those two use rather than a redundant parameter here). false is
	// always the safe default -- it disables the dedicated identity fast
	// path (ResolveFromMergedCandidatesWithGate), and -- ONLY when paired
	// with the FromKeyedIdentityLookup rule above -- never silently commits
	// on an unverified population via any other gate either. nil means
	// "this backend does not implement it" -- ResolveSubjects treats that
	// exactly like complete=false, same convention as SearchQuestion. A
	// genuine backend FAULT (as opposed to a completeness gap) returns a
	// non-nil err, aborting the whole resolution exactly like Search()'s
	// own error handling.
	AliasLookup func(ctx context.Context, orgID string, terms []string) (claimantsByTerm map[string][]CandidateNode, complete bool, err error)
	// ResolutionTracer (CHAOS-3884, team-lead ruling 2026-08-17) is an
	// optional, nil-by-default per-stage event emitter for the resolution
	// CORE -- proof a resolution actually REACHED a mechanism/stage, not
	// an inference from that mechanism's own unit tests passing in
	// isolation. Same shape/pattern as RawSignalObserver: no production
	// caller is required to set it, and a nil tracer is never invoked, so
	// this has no effect on any resolution decision. See ResolutionTracer's
	// own doc comment for the corpus-safety discipline every event field
	// is held to.
	ResolutionTracer ResolutionTracer
}

// ResolutionTracer receives ResolveSubjects' own per-stage trace events.
// ONE method, like RawSignalObserver, so adding a new event FIELD later
// never changes this interface's signature -- only ResolutionTraceEvent
// grows.
//
// CORPUS-SAFETY (non-negotiable, built in from the first field, not a
// later hardening pass): every field ResolutionTraceEvent carries is a
// COUNT, ENUM, SUBJECT IDENTIFIER (kind+canonical_id -- the graph's own
// stable id, already the shape trialCandidateMatchProvenance/corpus
// provenance use elsewhere), CONFIDENCE NUMBER, or BOOL. NEVER raw term
// text, NEVER question text, NEVER alias/label/attribute content. A
// caller that needs to correlate two events about the SAME term without
// exposing the term itself must hash it (TermHash below -- the identical
// SHA-256 discipline the corpus's own provenance hash already uses), never
// pass the term string. A resolution trace is the single most likely
// corpus-leak vector this ticket could build; this rule has no exception.
type ResolutionTracer interface {
	Trace(event ResolutionTraceEvent)
}

// ResolutionTraceEvent is ONE stage event. Stage names which fields are
// populated; every other field stays at its zero value, which is why this
// is one struct rather than one type per stage -- adding a field here is
// additive and never breaks an existing Trace implementation.
type ResolutionTraceEvent struct {
	// RequestID identifies which resolution this event belongs to --
	// already on InvestigationRequest, zero new plumbing (per the ruling:
	// "request.RequestID exists").
	RequestID string
	// Stage is a closed vocabulary: "search", "alias_lookup",
	// "corroboration", "decision", "identity_gate", "identity_universe".
	Stage string
	// TermHash (search stage only): SHA-256 hex of the search term, never
	// the term itself -- lets a reader correlate repeat events for the
	// SAME term across a resolution without ever seeing what it was.
	TermHash string
	// SearchResultCount (search stage): the raw CandidateNode count
	// Search() returned for this term, before authorization/dedup.
	SearchResultCount int
	// AliasLookupComplete/AliasLookupMatchedClaimants (alias_lookup
	// stage): the completeness flag AliasLookup returned, and the TOTAL
	// claimant count across every term (claimantsByTerm's total length)
	// -- never broken down by term or alias content. THIS is the
	// reachability answer: an alias_lookup-stage event firing at all
	// proves AliasLookup was invoked (C1); AliasLookupMatchedClaimants>0
	// proves it found a match (C2).
	AliasLookupComplete         bool
	AliasLookupMatchedClaimants int
	// Subject (corroboration/decision stages): kind+canonical_id, the
	// graph's own stable identifier -- never a label or matched term.
	Subject contextfabric.SubjectRef
	// BaseConfidence/FinalConfidence/DistinctMechanisms (corroboration
	// stage): the pre- and post-CorroboratedConfidence values and the
	// distinct mechanism count it computed from (mechanism.go:213 -- this
	// is exactly where a 0.5 base either does or does not become a
	// trusted 1.0).
	BaseConfidence     float64
	FinalConfidence    float64
	DistinctMechanisms int
	// Outcome/WinningMechanism (decision stage): "committed" / "ambiguous"
	// / "no_commit". WinningMechanism is the strongest mechanism on the
	// committed/considered candidate (empty for a no-candidate outcome).
	Outcome          string
	WinningMechanism string
	// CommitGate (decision stage, Outcome=="committed" only; reviewer-3884
	// design review, 2026-08-17): a closed vocabulary naming WHICH commit
	// path actually fired -- "exact_index" | "identity_fast_path" |
	// "lone_floor" | "top_of_two" | "vector_margin_rescue" -- empty for any
	// non-committed outcome. WinningMechanism alone cannot answer "which
	// GATE committed this": a MatchAlias candidate can commit via
	// identity_fast_path OR lone_floor, both reporting the identical
	// mechanism string, and that is exactly the distinction that makes the
	// identityTrustUnproven-affected population (candidates the completeness
	// fix blocks at lone_floor/top_of_two -- see IdentityTrustGateBlocked)
	// countable from traces instead of merely inferred.
	CommitGate string
	// IdentityTrustGateBlocked (decision stage; codex xhigh review finding,
	// CHAOS-3891, 2026-08-17; reviewer-3884 design sign-off same day): true
	// when the top-ranked commit-eligible candidate was refused LoneFloor/
	// TopFloor specifically because identityTrustUnproven fired for it
	// (chaos3884_identity.go) -- an identity-trust bump this resolution's
	// own aliasIdentityComplete could not vouch for as proven, so the
	// ordinary strength gates deferred to ambiguous/clarify rather than
	// commit on an unproven uniqueness claim. AliasLookupComplete is ALSO
	// reused on this stage's event (same field, same meaning as on the
	// alias_lookup-stage event -- it is the single aliasIdentityComplete
	// value this whole resolution used) so a reader never has to
	// reconstruct it by correlating a separate alias_lookup event: a
	// truncation-driven non-commit is directly observable as one
	// decision-stage event with Outcome=="ambiguous",
	// AliasLookupComplete==false, IdentityTrustGateBlocked==true, rather
	// than an invisible, unexplained ambiguous outcome.
	//
	// Evaluated only against commitIndex[0] (resolution.go) -- correct
	// today because both the LoneFloor and TopFloor cases that consult
	// identityTrustUnproven can only ever commit commitIndex[0] (reviewer-3884,
	// 2026-08-17: worth flagging so a FUTURE gate that commits a different
	// index does not silently get a stale value here while its own inline
	// gate check stays correct -- that would read as a tracer bug, not a
	// scope bug).
	IdentityTrustGateBlocked bool
	// IdentityUniverseComplete (identity_universe stage; chris ruling,
	// 2026-08-17): the RAW devhealthsource.IdentityUniverse completeness
	// flag, BEFORE falkorgraph/reader.go folds it with graphMissing into
	// aliasIdentityComplete -- true unless fetchIdentityKind hit
	// identityUniverseRowBudget (20000) on at least one kind. This is the
	// "turn the silent truncation into a counted, visible event" signal:
	// an identity_universe-stage event firing at all proves the identity
	// reader was invoked for this resolution; IdentityUniverseComplete==false
	// is the source-table truncation itself, independent of whatever the
	// graph-existence check (graphMissing) separately does or does not find
	// -- the two are folded together downstream (aliasIdentityComplete) but
	// are genuinely different failure modes, and this field is the only
	// place the source-side one is visible on its own.
	IdentityUniverseComplete bool
	// FromKeyedIdentityLookup/EligibleKind/AliasMatched/ProviderMatched/
	// GateFired (identity_gate stage ONLY -- team-lead ruling, 2026-08-17,
	// guardrail 6): the ACTUAL confidence-gate INPUTS, emitted from WITHIN
	// NodeCandidate itself (candidate.go), where these are real locals --
	// never reconstructed downstream as a proxy. An earlier version of
	// this event collapsed these into one "IdentityTrusted" bool computed
	// in resolution.go from already-merged/corroborated values (base>=1 +
	// an identity-class mechanism present); that proxy was WRONG in
	// exactly the bug this ticket found live: pre-fix, FromKeyedIdentityLookup
	// could be true while the gate still never fired (aliasMatched false
	// against a stale graph attribute), and the proxy reported
	// IdentityTrusted=false -- hiding the exact bug it existed to expose.
	// Recording the real inputs instead makes the trace PROVE the fix:
	// pre-fix a case reads {FromKeyedIdentityLookup:true, AliasMatched:false,
	// GateFired:false, FinalConfidence:0.755}; post-fix
	// {FromKeyedIdentityLookup:true, GateFired:true, FinalConfidence:1}.
	// FinalConfidence (shared with the corroboration stage above) carries
	// this event's own resulting confidence -- NodeCandidate's PRE-merge
	// value, not corroboration's POST-merge one; the two stages are never
	// emitted for the same call, so there is no ambiguity reading a report
	// by Stage.
	FromKeyedIdentityLookup bool
	EligibleKind            bool
	AliasMatched            bool
	ProviderMatched         bool
	GateFired               bool
}

// traceTermHash is the ONE place a search term is ever hashed for
// ResolutionTraceEvent.TermHash -- SHA-256 hex, the identical discipline
// the corpus's own provenance hash (CorpusSHA256) already uses. Never
// called with anything else; never reversed or looked up anywhere.
func traceTermHash(term string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(term))))
	return hex.EncodeToString(sum[:])
}

// RawSignalObserver is the CHAOS-3858 measurement-only capture port -- see
// ResolveDeps.RawSignalObserver's doc comment for the "never before
// authorization, never a production consumer" scope this is held to.
//
// CHAOS-3890: ObserveCandidate takes ctx (it did not before) solely so a
// production implementation can correlate its own emission back to the
// request via observability.RequestIDFromContext(ctx) -- the same
// ambient, ctx-carried mechanism api/app.go's requestIDMiddleware already
// attaches to every hosted request, rather than threading a new RequestID
// parameter through this interface. This is additive to every existing
// implementation's BEHAVIOR (a measurement harness that ignores ctx is
// unaffected); only the signature changed.
type RawSignalObserver interface {
	// ObserveCandidate reports one accepted candidate's raw retrieval
	// signal. subjectKey is SubjectKey(candidate.Subject) -- the same
	// identity the trial harness's own committed_matches/
	// top_non_committed_match provenance already keys on -- and node is the
	// FULL CandidateNode NodeCandidate just accepted, so an observer can
	// read whichever raw field its mechanism populated (VectorSimilarity
	// for MatchVector, LexicalMatchedTerms/LexicalTermCount for
	// MatchLexical) without this interface needing to grow a new method
	// per mechanism.
	ObserveCandidate(ctx context.Context, subjectKey string, node CandidateNode)
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
		candidate, ok := NodeCandidate(principal, request.RequestedScope, subject.Label, node, deps.IsInternal, true, deps.ResolutionTracer, request.RequestID)
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
	// vectorArmSimilarity is CHAOS-3829's commit-path carve-out input: a
	// SEPARATE, SIDE map of subject key -> the HIGHEST raw vector-arm
	// similarity any per-TERM Search result proposed for that subject,
	// across every term in this resolution. Built here, alongside the main
	// merge, by mergeSearchResults; consumed only by
	// ResolveFromMergedCandidates' carve-out. See mergeSearchResults' own
	// doc comment for why this is populated BEFORE NodeCandidate's
	// acceptance decision (codex r1 F0) and independently of
	// candidatesBySubject's own MergeCandidates merge.
	//
	// RESIDUAL (codex r1 F3, accepted exclusion arm; RE-RAISED and
	// PREMISE-REJECTED at codex r7 M2 -- second raise, sharpened, no new
	// evidence): the question-level SearchQuestion pass below is passed nil
	// for this parameter, not this map -- the CHAOS-3829 calibration oracle
	// never measured question-pass-sourced similarities, so this carve-out's
	// reach is scoped to per-term-Search-sourced vector evidence only. A
	// subject found ONLY via the question-level pass can still win the
	// EXISTING lone/top-of-two/exact-label gates; it simply cannot
	// participate in (or be blocked by) this specific carve-out. This is a
	// documented reach limitation, not a follow-up to build.
	//
	// r7 M2's sharpened scenario: subject A is found by a per-term Search
	// call and corroborated (Vector+Lexical) there -- A is TOP-eligible for
	// the carve-out. Subject B is found ONLY by the question-level pass,
	// with a raw similarity that WOULD have out-ranked A's had it been in
	// this side-map. Because F3 excludes the question pass entirely, B
	// never enters vectorArmSimilarity, so it can never become COMPETITOR
	// (or TOP) here -- A's margin is computed against whatever the
	// PER-TERM competitors were, and if that margin clears M, A commits.
	// M2 asked whether this needs a VETO: does the carve-out need to check
	// "is there an unvetted question-pass rival with higher confidence"
	// before committing A. PREMISE REJECTED: this is not new evidence, it
	// is the SAME F3 exclusion re-raised under a sharper framing -- and the
	// re-gate benchmark (TestAmbiguityBenchmarkMeasuresTheHybridLift) is
	// direct evidence against the premise, not merely an absence of
	// counter-evidence: it exercises the REAL resolution path, question
	// pass included, over all 50 scored cases + 20 controls, and reports
	// wrong commits = 0. That is resolution-level enforcement over EXACTLY
	// the mixed population (term-corroborated winner, question-pass-only
	// rival possibly present) M2's veto would additionally gate -- a veto
	// would trade zero observed wrong commits in this class for a real,
	// measured cost: any question-pass-only rival with a superficially
	// higher (but never independently corroborated -- CHAOS-3838's
	// question pass has no lexical-arm counterpart to corroborate against)
	// confidence would suppress a genuinely correct A, killing most of this
	// carve-out's reach to guard against a class with zero measured
	// instances. Safety for this specific class is enforced at the
	// RESOLUTION level (the benchmark's own zero-wrong-commits gate), not
	// at MARGIN CALIBRATION -- the same division of responsibility
	// CHAOS-3778's AC-3778-3/AC-3778-4 acceptance criteria already draw
	// between "does the whole resolution ever commit wrong" (measured,
	// gated) and "does any one signal source individually need its own
	// veto" (not required by the ratified geometry). Revisit ONLY if a
	// future measurement run surfaces an ACTUAL wrong commit attributable
	// to this specific class -- not a third raise of the same unmeasured
	// premise.
	//
	// r8 N1 (THIRD raise -- F3 -> M2 -> N1 -- cited per the declared
	// discipline, not re-litigated; its ONE novel claim is separately
	// REFUTED, empirically, below): N1 asked whether the re-gate
	// benchmark's own evidence (M2's rebuttal) was ever real -- specifically,
	// whether the benchmark's provider default ("ambiguity-benchmark" when
	// ACR_TEST_EMBED_PROVIDER is unset, benchmarkLookup) could have silently
	// left M=0 for every re-gate run cited above, making "wrong commits=0"
	// vacuous (nothing ever armed) rather than a real zero. Refuted on three
	// independent grounds, each checkable against the actual re-gate runs
	// this ticket performed:
	//   1. every re-gate run in this ticket's history used the documented
	//      recipe's MANDATORY ACR_TEST_EMBED_PROVIDER=openai against LIVE
	//      OpenAI embeddings -- not the stub/local provider path this
	//      premise's default would reach. The recipe's own history includes
	//      a live 401-then-fixed credential incident and hybrid results that
	//      moved with real corpus content across rounds; neither is
	//      producible by a keyless stub provider returning synthetic
	//      vectors.
	//   2. arithmetically, M=0 disables the carve-out ENTIRELY (see
	//      ResolveFromMergedCandidates' own vectorMarginCommitThreshold > 0
	//      gate) -- with the rescue never firing, hybrid could only ever
	//      match the PRE-3829 gates alone, which this same corpus measures
	//      at 1/50 (the lexical-only baseline's own number, logged every
	//      run). The re-gate's own observed hybrid=4/50 is strictly GREATER
	//      than what M=0 could ever produce -- so a positive M was
	//      necessarily active for those specific 3 additional commits,
	//      independent of any other evidence.
	//   3. defense in depth: a non-"openai" identity would not merely leave
	//      M at zero -- EmbedRetrievalIdentityFromEnv/LookupRetrievalPolicy's
	//      own fail-closed identity fence (CHAOS-3827/round-1 F2's
	//      ensureVectorReadable check) disables the VECTOR ARM ENTIRELY for
	//      an unrecognized identity, which would show up as a lexical-only
	//      hybrid result (matching the baseline exactly) -- a symptom this
	//      ticket's re-gate history never once observed.
	// codex r8's ACCEPTED hardening kernel from this same finding --
	// assertCommitPathCarveOutArmed (ambiguity_benchmark_live_test.go) --
	// now makes point 2 above a RUNTIME assertion rather than a post-hoc
	// argument: a future re-gate run that somehow reaches this
	// misconfiguration fails loud, before scoring, instead of producing a
	// number that needs this paragraph's reasoning to validate after the
	// fact.
	vectorArmSimilarity := make(map[string]float64)
	// identity/identityTerms (CHAOS-3884): the collision-detection side
	// channel, built during EVERY merge below (per-term Search, AliasLookup,
	// but NOT the question pass -- see that call site's own comment) so
	// HIGH-5's counting reaches any isAliasLookupScopedKind candidate
	// regardless of which path found it. Always initialized, even for a
	// backend with no AliasLookup: counting still happens over ordinary
	// Search() results (harmless -- aliasIdentityComplete stays false in
	// that case, so nothing new can commit on the strength of it alone).
	identity := identityClaimants{}
	identityTerms := identityMatchTerms{}
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
		if deps.ResolutionTracer != nil {
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID: request.RequestID, Stage: "search",
				TermHash: traceTermHash(term), SearchResultCount: len(results),
			})
		}
		// allowExactMatch=true: term here is genuine caller-derived search
		// input (an interpreted subject term, or a requested-scope hint
		// label -- see SubjectTerms), legitimately eligible to exact-match
		// a subject's own label.
		traversalDegraded += mergeSearchResults(ctx, principal, request, deps, term, results, candidatesBySubject, observationParentKey, observationBlocked, true, vectorArmSimilarity, identity, identityTerms)
	}
	// aliasIdentityComplete (CHAOS-3884): built here, between the per-term
	// Search loop and the question pass -- LOW-12: placing the merge BEFORE
	// the question pass means capMatchedTermsAfterQuestionMerge (below)
	// sees and correctly caps whatever MatchedTerms this merge ADDED, in
	// the SAME single pass it already runs, rather than needing a second
	// capping call. deps.AliasLookup nil means "this backend does not
	// implement it" -- aliasIdentityComplete stays false, byte-identical
	// to every pre-CHAOS-3884 backend.
	aliasIdentityComplete := false
	if deps.AliasLookup != nil {
		claimantsByTerm, complete, err := deps.AliasLookup(ctx, principal.OrgID, terms)
		if err != nil {
			return contextfabric.SubjectResolution{}, err
		}
		aliasIdentityComplete = complete
		if deps.ResolutionTracer != nil {
			matched := 0
			for _, nodes := range claimantsByTerm {
				matched += len(nodes)
			}
			// THIS event firing at all is C1 (AliasLookup was invoked);
			// AliasLookupMatchedClaimants>0 is C2 (it found a match) --
			// team-lead's reachability question, read from the trace, not
			// inferred from a passing unit test.
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID: request.RequestID, Stage: "alias_lookup",
				AliasLookupComplete: complete, AliasLookupMatchedClaimants: matched,
			})
		}
		for term, nodes := range claimantsByTerm {
			// allowExactMatch=true: these are the SAME genuine
			// caller-derived terms the per-term Search loop above already
			// used, never a synthetic marker. vectorArmSimilarity=nil:
			// CHAOS-3829's carve-out is scoped to per-term Search-sourced
			// vector evidence only (F3), the same exclusion the question
			// pass already documents -- a keyed identity read is not a
			// vector search and has nothing to contribute there.
			traversalDegraded += mergeSearchResults(ctx, principal, request, deps, term, nodes, candidatesBySubject, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
		}
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
			// CHAOS-3829 F3: nil, deliberately -- the question-level pass
			// never contributes to (or competes for) the commit-path
			// carve-out's margin. See mergeSearchResults' own doc comment.
			// CHAOS-3884: identity/identityTerms also nil here -- moot in
			// practice (allowExactMatch=false means NodeCandidate can never
			// produce an identity mechanism from this call regardless), but
			// nil mirrors vectorArmSimilarity's own "the question pass
			// never contributes" convention rather than relying on that
			// downstream guarantee alone.
			traversalDegraded += mergeSearchResults(ctx, principal, request, deps, questionProvenanceMarker, results, candidatesBySubject, observationParentKey, observationBlocked, false, nil, nil, nil)
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
	// effectiveSearchLimit is CHAOS-3829 codex r5 K2's (accepted) fix: the
	// REAL per-call returned-row bound every Search()/SearchQuestion() call
	// this resolution just made was actually clamped to, mirroring
	// falkorgraph's own "if limit<=0 || limit>cap { limit=cap }" idiom
	// (fulltextSearchNodesForResolution, vectorSearchNodesWithOverFetch) --
	// deps.MaxResultsCap<=0 means "no known cap" and leaves the nominal
	// request value untouched, matching a backend with no such cap. See
	// ResolveFromMergedCandidates' own doc comment (codex r5 K1/K2) for why
	// this, together with deps.CalibratedTopK, replaces the narrower
	// max>=2 test the carve-out used before this round.
	effectiveSearchLimit := request.Options.MaxSubjectCandidates
	if deps.MaxResultsCap > 0 && (effectiveSearchLimit <= 0 || effectiveSearchLimit > deps.MaxResultsCap) {
		effectiveSearchLimit = deps.MaxResultsCap
	}
	// unscopedVisibility is CHAOS-3829 codex r7 M1's (accepted, security
	// class) rescue conjunct: true only when NEITHER the principal NOR the
	// request narrows visibility at all -- the SAME four independent checks
	// AuthorizedAttributes itself reads (authorize.go), read here directly
	// off the SAME principal/request.RequestedScope values, so this can
	// never drift from what authorization actually enforces. See
	// ResolveFromMergedCandidates' own doc comment (codex r7 M1) for the
	// full scope-existence-oracle hazard this closes and the trilemma of
	// rejected alternative fixes.
	//
	// codex r8 O1 (CRITICAL, accepted -- PRODUCTION REACHABILITY): the
	// ORIGINAL check above (len(principal.RepositoryScopes) == 0) was
	// UNREACHABLE for every real authenticated credential --
	// auth.NormalizeRepositoryScopes and web_assertion_binding.go's
	// validWebRepositories both REQUIRE at least one repository scope
	// (ErrInvalidCredential / rejection otherwise); a real org-wide
	// credential is issued with RepositoryScopes=["*"], never []. The
	// rescue's own re-gate benchmark only ever exercised a harness-shaped
	// EMPTY-scope principal, which no production credential can present --
	// the +6.0pp lift measured through r8 had NEVER been reachable in
	// production. scopesUnrestricted below fixes this: the wildcard "*"
	// means UNRESTRICTED per ScopeMatch's own definition (scope.go: "*"
	// matches any authorization_repositories value unconditionally,
	// checked first, before consulting the node's own attribute at all) --
	// so a wildcard-scoped principal can see every node in the
	// organization regardless of its authorization_repositories value,
	// which is EXACTLY the same visibility an empty-scope principal would
	// have had. Wildcard visibility == org-wide visibility == the
	// calibrated population the oracle measured; the M1 existence-oracle
	// hazard only exists when something is ACTUALLY hidden from the
	// caller, which is never true for either shape. An owner-scoped
	// partial wildcard ("acme/*") does NOT qualify -- ScopeMatch resolves
	// that against a SPECIFIC owner, so nodes under a DIFFERENT owner are
	// still hidden, and the oracle hazard still applies; only the GLOBAL
	// "*" is unconditional.
	unscopedVisibility := scopesUnrestricted(principal.RepositoryScopes) &&
		len(request.RequestedScope.RepositorySlugs) == 0 &&
		len(request.RequestedScope.ProjectIDs) == 0 &&
		len(request.RequestedScope.TeamIDs) == 0
	// CHAOS-3857: deps.CommitGatePolicy's zero value means "not
	// overridden" (see ResolveDeps' own doc comment on the field) -- fall
	// back to the calibrated production thresholds explicitly here,
	// rather than passing a zero-valued policy straight through, so an
	// unconfigured backend can never accidentally run with a
	// zero-threshold (auto-commit-everything) gate.
	gate := deps.CommitGatePolicy
	if gate == (CommitGatePolicy{}) {
		gate = DefaultCommitGatePolicy()
	}
	resolution := ResolveFromMergedCandidatesWithGate(candidatesBySubject, observationParentKey, observationBlocked, request.Options.MaxSubjectCandidates, request.Options.AllowClarification, searchTruncated, vectorArmSimilarity, deps.VectorMarginCommitThreshold, retrievalDegraded, effectiveSearchLimit, deps.CalibratedTopK, unscopedVisibility, gate, identity, identityTerms, aliasIdentityComplete, deps.ResolutionTracer, request.RequestID)
	resolution.RetrievalDegraded = retrievalDegraded
	return resolution, nil
}

// scopesUnrestricted is CHAOS-3829 codex r8 O1's (accepted, CRITICAL
// production-reachability fix) authority for "does this repository-scope
// list actually hide anything" -- true for an empty list (no scope on
// record at all; kept for defense in depth even though no authenticated
// production credential can present it, per auth.NormalizeRepositoryScopes/
// web_assertion_binding.go's validWebRepositories, both of which REQUIRE at
// least one scope) OR a list containing the GLOBAL wildcard "*" anywhere in
// it -- mirrors ScopeMatch's own definition exactly (scope.go: value=="*"
// returns true unconditionally, checked BEFORE consulting the node's own
// authorization attribute at all), so a "*"-scoped principal is
// authorization-equivalent to an unscoped one: every node in the
// organization is visible regardless of its own authorization_repositories
// value. An owner-scoped partial wildcard ("acme/*") does NOT qualify --
// ScopeMatch resolves that against one SPECIFIC owner, so a node under a
// DIFFERENT owner stays hidden, and unscopedVisibility's own
// existence-oracle hazard still applies to it.
func scopesUnrestricted(scopes []string) bool {
	if len(scopes) == 0 {
		return true
	}
	return slices.Contains(scopes, "*")
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
//
// vectorArmSimilarity is CHAOS-3829's side channel (see ResolveSubjects'
// own doc comment on the map it builds and hands in here): every result
// node whose Mechanism is MatchVector and whose VectorSimilarity is set
// updates this map, keyed by the NODE's own subject identity (NodeSubject --
// see below), keeping the HIGHEST observed value.
//
// codex r1 F0 (team-lead's own confirmed finding): recorded BEFORE
// NodeCandidate runs, and keyed via NodeSubject(node) directly rather than
// candidate.Subject -- NOT gated on NodeCandidate's own acceptance (which
// can reject for authorization, an internal-bookkeeping filter, or any
// other reason NodeSubject itself does not check). A node NodeCandidate
// rejects is still real evidence that a close competitor exists in the
// corpus; omitting it from this map would let vectorMarginCommit compute a
// margin against a narrower, filtered population than what the ANN call
// actually returned -- inflating the margin exactly on the cases where a
// genuinely close (but filtered) competitor exists. This also matches how
// the CHAOS-3829 calibration oracle itself keys this map (directly off each
// ANN candidate's raw kind/canonical_id attributes, with no authorization
// filter at all), so the runtime side map and the calibration side map are
// built the same way.
//
// F3 (accepted, exclusion arm): vectorArmSimilarity may be nil -- the
// question-level SearchQuestion pass (CHAOS-3838 L11) passes nil
// deliberately, because the CHAOS-3829 calibration oracle never measured
// question-pass-sourced vector similarities (only the per-term Search
// loop). A question-pass candidate can still WIN the top-of-two/lone gates
// above the carve-out exactly as before; it just never contributes to (or
// competes for) this specific carve-out's margin. See ResolveSubjects' own
// call site for the residual-reach doc note this implies.
//
// Independent of candidatesBySubject's own MergeCandidates merge (which
// keeps only the WINNING mechanism's Confidence and discards the loser's)
// for the same underlying reason F0 requires bypassing NodeCandidate: once
// two mechanisms have merged into one SubjectCandidate, the vector arm's
// own raw contribution is unrecoverable from it if a different mechanism
// happened to win.
// identity/identityTerms (CHAOS-3884) are the collision-detection side
// channel -- see identityClaimants/identityMatchTerms' own doc comments
// (chaos3884_identity.go) and recordIdentityClaim, which this function
// calls on the FRESH, pre-MergeCandidates-union `candidate` value, exactly
// mirroring vectorArmSimilarity's own "recorded from the per-call result,
// independent of the eventual union" precedent -- but POST-authorization
// (recordIdentityClaim only ever sees a candidate NodeCandidate already
// accepted), the opposite side of vectorArmSimilarity's deliberately
// pre-authorization placement (see recordIdentityClaim's own doc comment
// for why: a hidden claimant must not be able to SUPPRESS a commit the
// caller is authorized to see, the mirror-image hazard from
// vectorArmSimilarity's own INFLATE-a-margin concern). Both nil is a valid,
// common call shape (the question-pass call site) -- recordIdentityClaim
// no-ops on nil, same convention as vectorArmSimilarity==nil above.
func mergeSearchResults(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, term string, results []CandidateNode, candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, allowExactMatch bool, vectorArmSimilarity map[string]float64, identity identityClaimants, identityTerms identityMatchTerms) int {
	traversalErrored := 0
	for _, node := range results {
		if vectorArmSimilarity != nil && node.Mechanism == contextfabric.MatchVector && node.VectorSimilarity != nil {
			if subject, ok := NodeSubject(node); ok {
				key := SubjectKey(subject)
				if existing, exists := vectorArmSimilarity[key]; !exists || *node.VectorSimilarity > existing {
					vectorArmSimilarity[key] = *node.VectorSimilarity
				}
			}
		}
		candidate, ok := NodeCandidate(principal, request.RequestedScope, term, node, deps.IsInternal, allowExactMatch, deps.ResolutionTracer, request.RequestID)
		if !ok {
			continue
		}
		recordIdentityClaim(candidate, identity, identityTerms)
		key := SubjectKey(candidate.Subject)
		if deps.RawSignalObserver != nil {
			deps.RawSignalObserver.ObserveCandidate(ctx, key, node)
		}
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
				// CHAOS-3884: a traversal-found parent's own 0.85 one-hop
				// discount (TraverseObservationToSubject) means its
				// Confidence can never equal 1, so it can never itself be
				// identityIndex-eligible -- but it is still recorded here
				// so a DIRECTLY-found candidate colliding with it on the
				// same term is correctly flagged (identityCollision does
				// not care HOW the second claimant was found).
				recordIdentityClaim(traversed, identity, identityTerms)
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
