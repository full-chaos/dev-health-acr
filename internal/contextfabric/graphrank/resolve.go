package graphrank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

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

// censusProvenanceMarker (CHAOS-3896 Slice C) is mergeCensusAttestedSatisfier's
// own synthetic MatchedTerms/ReceiptID provenance for a satisfier merged in
// from the source census's keyed graph-existence read -- the SAME
// "bracket-wrapped, never plausibly caller-typed" discipline
// questionProvenanceMarker documents, distinguished from it so a reader
// correlating MatchedTerms can tell a question-pass find from a
// census-recovered one.
const censusProvenanceMarker = "[census witness]"

// matchedTermsCap mirrors contractsv1's own unexported MatchedTerms entry
// count bound (matchedTerms:32, contextFabricWriteBounds) -- see
// questionProvenanceMarker's doc comment for why it is mirrored rather than
// referenced, and which test cross-checks it against the real Validate().
const matchedTermsCap = 32

// capMatchedTermsAfterMerge enforces matchedTermsCap on every candidate,
// dropping ONLY entries equal to marker -- a synthetic provenance literal
// never something a caller typed -- to restore the bound (codex round-1 P1,
// fix A, second half, generalized -- CHAOS-3896 Slice C codex xhigh review
// finding, confirmed and fixed). A candidate already carrying
// matchedTermsCap real, user-meaningful extracted terms overflows by
// exactly one once a synthetic marker unions in; the marker is the entry
// dropped to restore the bound, never a real term.
//
// Walks the WHOLE map rather than tracking which keys the merge touched:
// cheap at resolution scale (at most a few dozen candidates), and simpler
// than threading a touched-set through mergeSearchResults for a property
// that is a pure function of each candidate's own final MatchedTerms. A
// candidate that already exceeded the cap from real terms ALONE (a
// pre-existing, unrelated gap: mergeSearchResults' shared per-term path has
// never capped MatchedTerms) is left as-is here -- this function's job is
// only to keep a PREVIOUSLY-valid candidate valid after a synthetic marker
// unions in, not to retrofit a bound onto per-term merging this ticket did
// not touch.
//
// TWO callers, TWO distinct markers (codex xhigh review finding, CHAOS-3896
// Slice C, confirmed and fixed): questionProvenanceMarker after the
// question-level pass (unchanged, below), and censusProvenanceMarker after
// mergeCensusAttestedSatisfier's own merge
// (mergeCensusAttestedSatisfier, further down this file) -- an EARLIER
// version of this ticket added the census merge without ever re-capping
// for ITS OWN synthetic marker, so a candidate already sitting at exactly
// matchedTermsCap real terms overflowed to 33 once the census witness
// unioned in, and contractsv1's SubjectCandidate.Validate()
// (validate_context_fabric_result.go) rejected the WHOLE investigation
// result at engine.go's result.Validate() call -- a valid census recovery
// converted into a hard validation failure instead of a successful commit.
func capMatchedTermsAfterMerge(candidatesBySubject map[string]contextfabric.SubjectCandidate, marker string) {
	for key, candidate := range candidatesBySubject {
		if len(candidate.MatchedTerms) <= matchedTermsCap {
			continue
		}
		trimmed := make([]string, 0, len(candidate.MatchedTerms))
		for _, term := range candidate.MatchedTerms {
			if term == marker {
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
	// SubjectCandidatesAuthzDropped (CHAOS-3888) optionally reports how many
	// candidate nodes this ResolveSubjects call found -- via the caller's
	// own explicit SubjectHints (ExactHint) or via per-term/question hybrid
	// search (mergeSearchResults) -- and then excluded SPECIFICALLY because
	// AuthorizedAttributes denied them, as opposed to any other reason
	// NodeCandidate declines a node (not a valid subject, or an internal
	// bookkeeping node). Mirrors TraversalDegraded's exact convention: nil
	// means this backend does not report the signal, and the count is a
	// single aggregate over the whole call, not per-term.
	//
	// Before this ticket, an authorization-filtered candidate was
	// indistinguishable from one that simply never existed -- both
	// disappeared into the same `!ok { continue }`, so a user-facing
	// no_match was structurally identical whether nothing matched or
	// something did and authorization hid it. This is the seam that makes
	// that distinction observable, request-scoped, and content-safe (a
	// count only, never which candidate or why beyond "authorization").
	SubjectCandidatesAuthzDropped func(ctx context.Context, orgID string, count int)
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
	// SearchKind (CHAOS-4038) is an optional, kind-scoped SUPPLEMENTAL
	// retrieval call: the SAME term-search semantics as Search, additionally
	// constrained to return only nodes of exactly kind, at a small fixed
	// limit -- a coverage FLOOR, never a competing top-K. Nil means this
	// backend has no such capability, and ResolveSubjects skips the whole
	// coverage-floor pass, byte-identical to before this ticket -- the same
	// "not implemented" convention SearchQuestion/AliasLookup already use.
	//
	// Search/SearchQuestion share ONE cross-kind top-K budget
	// (request.Options.MaxSubjectCandidates): a genuinely-present but
	// comparatively lower-relevance-scored kind can be starved out of
	// candidatesBySubject entirely by a more common kind filling that same
	// budget. kindOfferMaterial (chaos3900_structure_offers.go) can then
	// never offer the starved kind, however it is written, because it is a
	// pure reducer over whatever pool it is handed -- CHAOS-4038's own
	// measurement (gen-trial-chaos3742_twoturn) found the oracle kind
	// missing from the pool in the overwhelming majority of positive-arm
	// expected_kind cases. applyKindCoverageFloor
	// (chaos4038_kind_coverage.go) calls this AFTER every other retrieval
	// pass has already run, ONLY for a kind in kindCoverageFloorKinds still
	// absent from candidatesBySubject at that point, and ONLY when
	// confirmedKind is nil (a request that already confirmed a kind has
	// nothing left to disambiguate on this axis) -- strictly additive,
	// exactly like SearchQuestion's own union contract: results merge
	// through the SAME NodeCandidate/MergeCandidates/mergeSearchResults path
	// every other pass uses, so a coverage-floor find competes and
	// corroborates identically to one an ordinary pass found, and can only
	// ever ADD a subject or lose an exact-confidence tie to one an earlier
	// pass already found (mergeSearchResults' own last-processed-loses-ties
	// convention).
	//
	// Scoped to kindCoverageFloorKinds only (never repository/project/team):
	// those three already get a COMPLETE, exact-term identity-universe read
	// via AliasLookup above, which a supplemental generic lexical query
	// would duplicate, not extend. A backend with no kind-scoped query path
	// can safely leave this nil.
	SearchKind func(ctx context.Context, term string, kind contextfabric.SubjectKind, limit int) (candidates []CandidateNode, truncated bool, degraded bool, err error)
	// VectorMechanismConfigured (CHAOS-4154) reports whether this deployment
	// has a LIVE vector-similarity mechanism at all (falkorgraph: a.embedder
	// != nil) -- distinct from VectorMarginCommitThreshold/CalibratedTopK
	// below, which describe whether the CHAOS-3829 margin RESCUE is
	// calibrated, not whether the mechanism runs. The confirmed-kind
	// truncation-scoping mechanism (chaos4154_confirmed_kind_scope.go) needs
	// this because SearchKind has no kind-scoped vector-arm counterpart --
	// kindScopedFulltextSearchNodes' own doc comment (falkorgraph) is
	// explicit that calibrating one blind, with no live corpus to validate
	// an over-fetch depth against, is exactly the kind of guess this repo's
	// CHAOS-3834/CHAOS-3829 calibration discipline rejects. So an exhaustive
	// per-term SearchKind pass can prove the LEXICAL channel complete, but
	// can only be trusted to prove the WHOLE candidate population complete
	// (sol-max's CHAOS-4154 ruling, amendment 2: "SearchQuestion() is also
	// candidate-producing... without one of [the three completeness
	// contracts], kindScopedComplete is a false proof") when this field is
	// false -- no live vector mechanism exists to have surfaced a same-kind
	// rival the lexical pass could not see. false (the zero value) is the
	// safe default for any backend that does not set this: it makes the
	// confirmed-kind-scoped bypass a permanent no-op rather than a false
	// completeness claim, exactly matching a backend with no vector arm at
	// all.
	VectorMechanismConfigured bool
	// ConfirmedKindVectorCensus (CHAOS-4155 Phase 1, SHADOW ONLY -- see
	// chaos4155_confirmed_kind_vector_scope.go's own doc comment for the
	// full design and why this is deliberately NOT the mechanism that lets
	// VectorMechanismConfigured==true stop forcing plan_incomplete) is an
	// optional exact, count-closed, kind-scoped vector completeness
	// census. nil is a safe default for any backend that does not
	// implement this, matching every other optional ResolveDeps hook's
	// nil convention -- attemptConfirmedKindVectorCensus (this file's own
	// call site) reports ConfirmedKindVectorScopeNotAttempted immediately
	// when it is nil. falkorgraph's own production implementation is
	// ALWAYS wired non-nil (codex R1 review, Medium, confirmed: an
	// earlier version of this comment implied the hook itself stays nil
	// absent the env var, which is not what reader.go does) -- it is the
	// FUNCTION BODY that degenerates to a zero-backend-call
	// ConfirmedKindVectorScopeNotAttempted whenever
	// ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS is
	// unset or no embedder is configured -- so "zero cost" here means no
	// query/embed calls, not a literally-skipped Go function call.
	//
	// Deliberately NO error return: a shadow-arm failure must never fail
	// the resolution it is only observing -- unlike SearchKind, whose
	// failure propagates because a truncated/failed READ genuinely
	// invalidates the population the caller is about to decide over, this
	// census decides NOTHING (see buildConfirmedKindScopedSnapshot's own
	// call site, chaos4154_confirmed_kind_scope.go: this outcome is never
	// consulted for scopeState). Any internal error (dependency failure,
	// count-closure mismatch, malformed row) is captured AS a returned
	// State value instead.
	ConfirmedKindVectorCensus func(ctx context.Context, kind contextfabric.SubjectKind, terms []string) ConfirmedKindVectorCensusOutcome
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
	// CensusFunc (CHAOS-3899, SHADOW ONLY -- design brief v5 §6 Slice A) is
	// the shadow evidence round's census execution dependency. nil by
	// default -- the same "not wired" convention every other optional
	// dependency here uses (RawSignalObserver, ResolutionTracer): a nil
	// CensusFunc means the shadow round NEVER RUNS, at zero cost, and every
	// existing/production caller that does not set this field gets
	// byte-identical behavior to before this ticket. See
	// RunShadowEvidenceRound's own doc comment for what runs when it is
	// set, and why the round can never influence `resolution` regardless.
	CensusFunc CensusFunc
	// HandleGrammarChecker (CHAOS-3972 P3) is contextfabric.Engine's own
	// offer-time grammar dependency, threaded through unchanged so
	// explicitHandleOfferMaterial (chaos3900_structure_offers.go) can
	// validate request.SubjectHandles the SAME way the production
	// composition root wires it for redemption-time re-verification (see
	// HandleGrammarChecker's own doc comment, internal/contextfabric/structure.go).
	// nil means an explicit subject_handle can never become an offer --
	// the SAME safe degradation HandleGrammarChecker's own doc comment
	// describes.
	HandleGrammarChecker contextfabric.HandleGrammarChecker
	// AnchorMembershipOffersEnabled (CHAOS-4042, team-lead ruling: ship
	// PR2's interim verifier DARK) gates whether anchorOfferMaterial may
	// ever mint a v2 (membership-verify) ambiguous-claimant AnchorOption.
	// false (every production deployment today) is byte-identical to
	// pre-CHAOS-4042 behavior -- no request can mint a v2 anchor offer.
	// PR3 landed the production verifier's remaining two pieces --
	// graphrank.VerifyAnchorClaimantMembership now reconciles against the
	// graph under the redemption request's own pinned binding AND
	// re-authorizes the selected claimant (see that function's own doc
	// comment) -- but this flag STILL stays false in every PR3 commit: the
	// flip to enabled-by-default is a separate, human-ratified
	// per-deployment decision (ACR_CONTEXT_FABRIC_ANCHOR_MEMBERSHIP_ENABLED),
	// never bundled into a code change. Tests that exercise the v2 offer
	// path set this to true directly, bypassing the env gate entirely --
	// see anchorOfferMaterial's own doc comment.
	AnchorMembershipOffersEnabled bool
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

// discardableDecisionTracer (CHAOS-4154, codex R2, Medium, confirmed) wraps
// a real ResolutionTracer for exactly one ResolveFromMergedCandidatesWithGateAndBasis
// call whose OWN resolution the caller may go on to discard. It holds back
// only the "decision"-stage event that call produces -- every other stage
// (corroboration, etc.) still reaches real immediately, unbuffered -- until
// keep() is called, which the caller does only when it actually retains
// this call's resolution. Never call Trace concurrently with itself or with
// keep(): this type has no synchronization, matching every other
// single-goroutine caller in this file.
//
// Why this exists: every OTHER "decision"-stage producer in this file
// traces unconditionally because the resolution it decides IS the one
// returned (see the CHAOS-3896 evidence-census re-decision, which
// unconditionally keeps whatever ResolveFromMergedCandidatesWithGateAndBasis
// returns). CHAOS-4154's own scoped re-decision is the first case in this
// file where that is NOT true -- it is discarded whenever it does not
// commit -- so tracing it unconditionally could leave a "decision" event
// as the LAST one describing a resolution that was never returned,
// contradicting the "last decision event describes the returned
// resolution" invariant readers of this trace (production and this
// ticket's own test helpers alike) rely on.
type discardableDecisionTracer struct {
	real ResolutionTracer
	// captured (CHAOS-4096: widened from *ResolutionTraceEvent to a slice)
	// holds back EVERY "decision"-stage event the wrapped call produces,
	// not just one -- the wrapped call can now emit one per committed
	// subject (a multi-subject commit), and a single-slot capture would
	// silently drop every one but the last when keep() replays it.
	captured          []ResolutionTraceEvent
	capturedRankedCut []ResolutionTraceEvent
}

// offersOnlyDecisionTracer (CHAOS-4234, codex round-1 finding 3) tags
// every "decision" stage event with OfferedUnderWindowGate=true before
// forwarding it, and passes every other stage through unchanged. Used
// ONLY at the offers-only pass's own first-pass decision call site
// (resolveSubjects, above) -- see that call site's own comment for why
// the tag matters: the resolution this decision belongs to is discarded
// unconditionally, so "Outcome=committed" on an untagged event would read
// as a real commit to anything consuming ResolutionTraceEvent without
// also cross-referencing which mode produced it.
type offersOnlyDecisionTracer struct{ real ResolutionTracer }

func (o offersOnlyDecisionTracer) Trace(event ResolutionTraceEvent) {
	if event.Stage == "decision" {
		event.OfferedUnderWindowGate = true
	}
	if o.real != nil {
		o.real.Trace(event)
	}
}

func (d *discardableDecisionTracer) Trace(event ResolutionTraceEvent) {
	switch event.Stage {
	case "decision":
		d.captured = append(d.captured, event)
		return
	case "ranked_cut":
		// CHAOS-4234: a scoped pass's own ranked_cut batch is discarded
		// with its decision -- otherwise a reader keeping the LAST batch
		// would read a discarded pass's ranks as the final ones.
		d.capturedRankedCut = append(d.capturedRankedCut, event)
		return
	}
	if d.real != nil {
		d.real.Trace(event)
	}
}

// keep forwards every held-back "decision" event (if any -- CHAOS-4096: one
// per committed subject on a multi-subject commit, not just one) to the
// real tracer -- call ONLY when the caller is retaining this call's
// resolution.
func (d *discardableDecisionTracer) keep() {
	if d.real == nil {
		return
	}
	for _, event := range d.capturedRankedCut {
		d.real.Trace(event)
	}
	for _, event := range d.captured {
		d.real.Trace(event)
	}
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
	// Stage is a closed vocabulary: "search", "search_question"
	// (CHAOS-4120), "confirmed_kind_rescue" (CHAOS-4132), "confirmed_kind_scope"
	// (CHAOS-4154), "alias_lookup", "corroboration", "decision",
	// "identity_gate", "identity_universe", "evidence_round", "evidence_probe"
	// (CHAOS-3899), "evidence_census_commit" (CHAOS-3896 Slice C),
	// "evidence_source_native", "evidence_source_native_probe" (CHAOS-3899
	// widening measurement, 2026-08-19), "slice_b_survivor_verdict"
	// (CHAOS-4088), "kind_offer" (CHAOS-4012 v20).
	Stage string
	// TermHash (search stage only): SHA-256 hex of the search term, never
	// the term itself -- lets a reader correlate repeat events for the
	// SAME term across a resolution without ever seeing what it was.
	TermHash string
	// SearchResultCount (search/search_question stages): the raw
	// CandidateNode count Search()/SearchQuestion() returned for this
	// call, before authorization/dedup.
	SearchResultCount int
	// Truncated (search/search_question stages, CHAOS-4120): THIS call's
	// own truncated return value, before it is folded into the
	// resolution-wide searchTruncated OR-accumulator (resolve.go). Before
	// this field, a per-term Search() truncation and the question-level
	// SearchQuestion() truncation were indistinguishable: both fed the
	// SAME resolution-wide flag, and only that pooled flag ever reached a
	// trace event (the decision-stage SearchTruncated above). A reader
	// could tell "some pass truncated" but never which one -- the exact
	// per-pass breakdown the CHAOS-4120 question-results decomposition
	// needed and could not get from the artifact. kind_coverage_floor's
	// own KindCoverageFloorTruncated below already carries this same
	// distinction for the coverage floor's SearchKind calls; this field
	// is the missing other two passes, read the identical way (per-event,
	// never resolution-wide).
	Truncated bool
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
	// SearchTruncated (decision stage; CHAOS-3897, 2026-08-17): the SAME
	// resolution-wide searchTruncated signal the commit switch
	// (resolution.go) itself reads, populated onto every decision-stage
	// event this function emits -- not reconstructed downstream. The
	// commit switch's `case searchTruncated` sits BEFORE LoneFloor/
	// TopFloor and short-circuits straight to ambiguous, so without this
	// field an ambiguous decision event with CommitGate=="" cannot tell a
	// reader whether the confidence gates ran and blocked (e.g.
	// IdentityTrustGateBlocked==true, or an ordinary below-floor/below-gap
	// miss) or never ran at all because truncation preempted them first.
	// SearchTruncated==true on an ambiguous event with CommitGate=="" is
	// the truncation-preempted case; SearchTruncated==false there means
	// the gates themselves declined to commit.
	SearchTruncated bool
	// CommitBasis (decision stage, CHAOS-4085): the recorded
	// contextfabric.CommitBasis for the committed subject -- "caller_canonical_id"
	// | "authoritative_identity" | "statistical" -- empty for any
	// non-committed outcome.
	//
	// This is the field that makes a wrong commit ATTRIBUTABLE FROM TRACE
	// ALONE, which the CHAOS-4085 investigation could not do: establishing
	// what had actually happened required reconstructing the commit from
	// captured model-exchange transcripts, because nothing in the trace said
	// which class of proof the commit stood on. CommitGate names which BRANCH
	// fired; CommitBasis names what that branch's decision was WORTH, and the
	// two are not derivable from each other -- exact_index and
	// identity_fast_path both commit at Confidence 1 with an identity-shaped
	// mechanism, and only one of them is a proof (see
	// contextfabric.CommitBasis).
	//
	// Together with TiedStatisticalTop below, a decision-stage event now
	// carries the complete answer to "why did this commit, and how much
	// should we have trusted it": gate, basis, mechanism, tie, truncation.
	CommitBasis string
	// TiedStatisticalTop (decision stage, CHAOS-4085): true when the top two
	// commit-eligible candidates held the SAME sub-1.0 confidence -- the tie
	// half of tiedStatisticalTopUnderTruncation's conjunct, emitted
	// independently of whether the refusal actually fired.
	//
	// Emitted on EVERY decision-stage event, committed or not, and
	// deliberately NOT conflated with the refusal itself: paired with the
	// SearchTruncated field immediately above, a reader can separate the
	// three populations that matter -- a tie WITH truncation, a tie WITHOUT
	// truncation (still rescuable, and still committing today), and
	// truncation without a tie (untouched by CHAOS-4085). A single "refused"
	// boolean would collapse the second and third into the first's absence
	// and make the rule's real reach uncountable.
	//
	// NECESSARY, NOT SUFFICIENT (codex xhigh retroactive review, MEDIUM 2 --
	// an earlier version of this comment claimed the conjunction below
	// simply WAS the refusal, which over-counts). An ambiguous event with
	// TiedStatisticalTop && SearchTruncated && CommitGate=="" is a
	// NECESSARY condition for the tied-rescue refusal, not a sufficient
	// one: this flag reports the TIE alone, independently of whether the
	// rescue was ever ELIGIBLE. The rescue additionally requires
	// unscopedVisibility, a valid CommitGatePolicy, a positive
	// vectorMarginCommitThreshold, non-degraded retrieval, and an
	// effectiveSearchLimit inside [2, calibratedTopK] (see the rescue's own
	// guard, resolution.go). A scoped principal alone -- or an invalid
	// gate, degraded retrieval, an uncalibrated threshold, or a
	// out-of-envelope search limit -- produces the identical conjunction
	// with the rescue never available at all.
	//
	// So a reader counting the refusal from trace gets an UPPER BOUND from
	// this conjunction, and must exclude the ineligible population by other
	// means (the scoped/degraded/uncalibrated dimensions are knowable from
	// deployment configuration and the request's own scope, none of which
	// this event carries). Reporting it as an exact count would overstate
	// the rule's reach.
	TiedStatisticalTop bool
	// SearchCandidateLimit (decision stage, CHAOS-4117): the NOMINAL
	// request.Options.MaxSubjectCandidates this resolution ran with -- the
	// same value threaded into deps.Search/deps.SearchQuestion as their
	// row limit (resolve.go) and into ResolveFromMergedCandidatesWithGate's
	// own `max` parameter. CHAOS-4117 raised the calibrated default from
	// 10 to 20 (falkorgraph.RetrievalPolicy.CalibratedTopK's ceiling); a
	// caller can still request any value the contract allows (1-50), so
	// this field is what makes "which candidate-limit regime produced this
	// decision" answerable from a run's own trace artifacts alone, rather
	// than requiring a reader to cross-reference the request payload (which
	// telemetry never retains) or assume every resolution shared one
	// deployment-wide default. Paired with SearchTruncated: a truncated
	// decision at limit=20 and one at limit=10 are the SAME symptom but
	// different regimes, and only this field tells them apart.
	SearchCandidateLimit int
	// KindCoverageFloorFired/KindCoverageMissingKinds/
	// KindCoverageFloorTruncated (stage=="kind_coverage_floor" ONLY,
	// CHAOS-4086) describe CHAOS-4038's kind-coverage floor: how many floor
	// kinds had ZERO representation in the candidate pool when the pass
	// began, whether the pass actually put a candidate of such a kind into
	// the pool, and whether any of its own SearchKind queries hit their row
	// limit.
	//
	// They are carried on their OWN stage and are ALWAYS ZERO on a decision
	// event. That is deliberate and load-bearing: this floor's truncation
	// and degradation are explicitly excluded from the commit gate's inputs
	// (see the call site in resolve.go), so presenting them beside
	// CommitGate/SearchTruncated -- which ARE gate inputs -- would invite
	// exactly the causal reading the resolver is built to avoid.
	//
	// "Fired" means ADDED SOMETHING (len(added) > 0), not "ran": a floor
	// can run over three missing kinds and find nothing, which is a
	// different finding from one that never ran, and MissingKinds>0 with
	// Fired==false is precisely how a reader tells them apart.
	KindCoverageFloorFired     bool
	KindCoverageMissingKinds   int
	KindCoverageFloorTruncated bool
	// KindCoverageMissingKindsList (stage=="kind_coverage_floor" ONLY,
	// CHAOS-4183 phase 2, team-lead ruling 2026-08-23) is
	// KindCoverageMissingKinds' own kind-IDENTITY twin -- the closed-
	// vocabulary contextfabric.SubjectKind values (as strings) the floor
	// found missing at its own pre-search snapshot, same corpus-safe
	// "kind values only, never a canonical id" discipline
	// KindOfferBoundaryKinds (below) already uses. Added because
	// MissingKinds' bare COUNT could not disambiguate a real CHAOS-4012
	// re-smoke finding: whether the floor searched for the SAME kind a
	// later analysis cares about, or a different one entirely, once more
	// than one floor kind could be in play for a single call.
	KindCoverageMissingKindsList []string
	// ConfirmedKindRescueFired/ConfirmedKindRescueResultCount/
	// ConfirmedKindRescueTruncated (stage=="confirmed_kind_rescue" ONLY,
	// CHAOS-4132) describe applyConfirmedKindRescue's own outcome: a
	// receipt-confirmed kind whose ordinary-search-filtered pool came up
	// EMPTY (its only route into a prior turn's pool was the CHAOS-4038
	// coverage floor, which a confirmed-kind call skips by design) gets a
	// small, bounded, kind-scoped supplemental SearchKind pass before
	// conceding to a guaranteed no_match. This event exists at all ONLY
	// when that rescue was actually attempted (confirmedKind != nil, the
	// filtered pool was empty, and deps.SearchKind != nil) -- its absence
	// on a confirmed-kind call means the rescue was never needed, ordinary
	// search already had the confirmed kind's candidates. Fired means the
	// rescue actually found something (ResultCount > 0), the SAME "added
	// something, not merely ran" convention KindCoverageFloorFired already
	// uses.
	//
	// Truncated is NOT purely observational the way KindCoverageFloorTruncated
	// is: unlike the coverage floor (which only ever adds ONE candidate
	// among many other kinds' candidates already in the pool), this rescue
	// is, when it fires, the SOLE source of the candidates the commit gate
	// is about to decide over. resolve.go folds this value into
	// searchTruncated for exactly that reason -- see its own call site
	// comment.
	ConfirmedKindRescueFired       bool
	ConfirmedKindRescueResultCount int
	ConfirmedKindRescueTruncated   bool
	// KindOfferExplicitHintCount/KindOfferDistinctKindCount/
	// KindOfferSuppressedByCardinality (stage=="kind_offer" ONLY,
	// CHAOS-4012 v20) describe kindOfferMaterial's own suppression check
	// (chaos3900_structure_offers.go): the number of valid, deduped,
	// caller-supplied ExpectedKinds hints; the number of distinct
	// structureOfferKinds-registered kinds actually collected (explicit
	// hints plus pool-derived, deduped) BEFORE the check runs; and whether
	// "explicitCount==0 && len(ranked)<2" fired, leaving KindOptions empty
	// for cardinality reasons rather than because no offerable kind was
	// present at all.
	//
	// Filed to resolve exactly the ambiguity CensusRan/EvidenceRoundEntered
	// (CHAOS-3899/CHAOS-4161) already resolve one layer over: an
	// expected_kind candidate can be genuinely IN the resolved pool
	// (including a CHAOS-4038 coverage-floor rescue, unioned in for offer
	// purposes at this call site) and still never reach KindOptions, because
	// a single-distinct-kind pool has, by kindOfferMaterial's own design,
	// "nothing to disambiguate." KindOfferDistinctKindCount is what tells a
	// reader "genuinely 0 offerable kinds" apart from "exactly 1 -- present,
	// still suppressed" (CHAOS-4012's own open question).
	//
	// UNLIKE KindCoverageFloorFired/ConfirmedKindRescueFired, this event is
	// UNCONDITIONAL: kindOfferMaterial runs on EVERY resolution (not gated
	// on a precondition the way the coverage floor and confirmed-kind
	// rescue are), so this stage fires every time deps.ResolutionTracer is
	// non-nil, corpus-wide, not only for the previously-flagged subset.
	KindOfferExplicitHintCount       int
	KindOfferDistinctKindCount       int
	KindOfferSuppressedByCardinality bool
	// KindOfferCandidateOfferCount/KindOfferOfferKind (stage=="kind_offer"
	// ONLY, CHAOS-4012 v22) describe candidateOfferMaterial's own
	// independent axis (chaos3900_structure_offers.go): the ranked-
	// candidate-list offer chris ruled for (2026-08-23) fires whenever
	// nothing committed and the pool is non-empty, regardless of
	// KindOfferSuppressedByCardinality -- the two axes are independent, so
	// this pair rides the SAME unconditional "kind_offer" event the three
	// fields above already use, rather than a separate stage.
	// KindOfferCandidateOfferCount is len(CandidateOptions) this call
	// minted (0 when the axis did not fire). KindOfferOfferKind is the
	// closed vocabulary "kind" | "candidate" | "both" | "" summarizing
	// which axis (or both, or neither) fired THIS call, so a reader never
	// has to cross-reference KindOfferSuppressedByCardinality and
	// KindOfferCandidateOfferCount by hand to answer that one question.
	KindOfferCandidateOfferCount int
	KindOfferOfferKind           string
	// KindOfferCandidateOfferLabelsNormalizedCount (stage=="kind_offer"
	// ONLY, CHAOS-4210) is the number of THIS call's CandidateOptions whose
	// Label candidateOfferMaterial had to bound-to-fit the v1 wire contract
	// ([1,200] runes, ContextFabricCandidateOption.Validate()) -- 0 the
	// overwhelming common case (a real title already fits). A candidate's
	// Subject.Label is a real, legitimate title (queryWorkItems' own empty
	// fallback already keeps it non-empty; nothing upstream bounds its
	// length), so an ordinary long title reaching this axis is exactly the
	// decision branch ext65 corpus case index 6 exposed: without this
	// counter, a normalized label is indistinguishable from an unmodified
	// one anywhere in the run's own artifacts (ledger #253 / CHAOS-4210).
	KindOfferCandidateOfferLabelsNormalizedCount int
	// KindOfferBoundaryKinds (stage=="kind_offer" ONLY, CHAOS-4012 v22,
	// team-lead ruling 2026-08-23) is call-boundary-scoped, unlike the
	// existing trace-wide ExpectedInPool (poolContainsKind reads the
	// "corroboration" stage, emitted for the FULL merged pool before final
	// truncation) -- see distinctCandidateKinds' own doc comment
	// (chaos3900_structure_offers.go) for why a candidate can corroborate
	// early and still be gone by the time this stage fires, and why that
	// gap matters for telling "candidate-list can fix this" apart from
	// "upstream truncation already lost it."
	//
	// CHAOS-4183 phase 3 (sol design consult, team-lead ratified
	// 2026-08-23): this field's own MEANING shifted -- it is now the
	// UNFILTERED pre-phase-3 reading (distinctCandidateKinds(kindOfferCandidates),
	// byte-identical to this field's own pre-phase-3 computation) PLUS,
	// ONLY when a stalled resolution's kind-only repair genuinely admitted
	// something, the repaired kind identities appended at the end, in the
	// SAME fixed closed-vocab order projectKindOfferKinds' own `after`
	// return uses. Codex CHAOS-4183 phase-3 review round 1, finding 1
	// (MEDIUM): an earlier version of this field used
	// subjectKindStrings(after) directly -- `after` is
	// distinctOfferableKinds' own STRUCTUREOFFERKINDS-FILTERED value (the
	// value that safely feeds kindOfferMaterial, which filters internally
	// anyway), so a committed resolution whose pool held a non-offerable
	// kind (e.g. document) would have reported FEWER kinds than this field
	// ever did pre-phase-3 -- violating "committed resolutions get the
	// pre-repair boundary unchanged" even though nothing was actually
	// repaired. The corrected computation (call site, below) starts from
	// the unfiltered reading and appends only the genuinely-new repaired
	// tail, so a committed or nothing-absent resolution reduces to the
	// unfiltered list verbatim -- byte-identical to pre-phase-3.
	// KindOfferBoundaryKindsBeforeRepair below carries the SAME unfiltered
	// pre-repair-only reading (distinctCandidateKinds(kindOfferCandidates))
	// unconditionally, so a v25-vs-v26 reader can still ask the pre-repair
	// question this field used to answer alone, even on a row this field
	// itself repaired. See projectKindOfferKinds' own doc comment for the
	// full repair mechanism -- candidate-list (candidateOfferMaterial) is
	// completely UNTOUCHED by this: it still ranks the SAME
	// kindOfferCandidates this field's own unfiltered half still reflects
	// verbatim.
	KindOfferBoundaryKinds []string
	// KindOfferBoundaryKindsBeforeRepair/KindOfferDistinctKindCountBeforeRepair/
	// KindOfferSuppressedByCardinalityBeforeRepair (CHAOS-4183 phase 3,
	// schema v26) are the PRE-repair twins of KindOfferBoundaryKinds/
	// KindOfferDistinctKindCount/KindOfferSuppressedByCardinality --
	// exactly what those three fields computed before this phase's
	// post-decision kind-only boundary completion existed.
	// BoundaryKindsBeforeRepair is UNFILTERED (distinctCandidateKinds'
	// own discipline, matching this field's pre-phase-3 behavior
	// verbatim); DistinctKindCountBeforeRepair/
	// SuppressedByCardinalityBeforeRepair come from calling
	// kindOfferMaterial over the PRE-repair kind list (`before`,
	// projectKindOfferKinds' own return) -- the IDENTICAL cardinality-
	// check logic the post-repair fields use, over the un-repaired input,
	// so the two readings can never drift apart by hand-duplicating that
	// check. Together with the post-repair fields, these are what let a
	// reader measure the repair's own effect directly from one event,
	// rather than needing a v25 artifact to diff against.
	KindOfferBoundaryKindsBeforeRepair           []string
	KindOfferDistinctKindCountBeforeRepair       int
	KindOfferSuppressedByCardinalityBeforeRepair bool
	// HandleOfferCountBeforeGraphSource/HandleOfferGraphDerivedCount/
	// HandleOfferGraphDerivedRejectedCount (CHAOS-4119, schema v27) ride
	// the SAME unconditional "kind_offer" stage every other axis on this
	// event already uses -- handleOfferMaterial, like
	// kindOfferMaterial/candidateOfferMaterial, runs on every resolution.
	// See handleOfferDiagnostics' own doc comment
	// (chaos3900_structure_offers.go) for what each field measures; this is
	// its trace-visible twin, mirrored through SlogResolutionTracer exactly
	// like KindOfferBoundaryKinds/CandidateOfferCount above.
	HandleOfferCountBeforeGraphSource    int
	HandleOfferGraphDerivedCount         int
	HandleOfferGraphDerivedRejectedCount int
	// OfferedUnderWindowGate (CHAOS-4234) is true when this resolution
	// ran in offers-only mode under the class-default window gate
	// (contextfabric.OffersOnlyResolution). Set on TWO stages, for two
	// different reasons -- both real events under offers-only mode, no
	// stage this resolution emits leaves it unset:
	//   - "kind_offer" (unconditional, every resolution): the trace-
	//     visible twin of the engine's RecordGatedOfferResolution
	//     telemetry, so a report row can tell "offers composed beside the
	//     window offer" apart from an ordinary decisive turn without
	//     cross-referencing the window canonicalization outcome by hand.
	//   - "decision" (offers-only mode ONLY, codex round-1 finding 3,
	//     offersOnlyDecisionTracer): a "decision" event's own
	//     Outcome=="committed" would otherwise read as a real commit --
	//     under this mode the engine discards resolution/commitBases/
	//     commitDigests unconditionally, so nothing this event describes
	//     ever reaches anywhere decisive. A reader of a "decision" event
	//     MUST check this field before trusting its CommitGate/Outcome.
	OfferedUnderWindowGate bool
	// Rank/Survived/CoverageBypass (stage=="ranked_cut" ONLY, CHAOS-4234)
	// describe ONE candidate's fate at ResolveFromMergedCandidatesWithGateAndBasis'
	// final MaxSubjectCandidates cut (resolution.go): Rank is its 1-based
	// position in the survivors-first ranked order the cut is taken over,
	// Survived is whether it stayed inside the cut (and so reached
	// resolution.Candidates and the offer builders' shared input). One
	// event per candidate, emitted in rank order, so Rank==1 marks the
	// start of a fresh batch -- a re-decision (census merge, scoped
	// commit) emits a fresh batch and a reader keeps the LAST one.
	// CoverageBypass is the companion emitted from resolve.go for a
	// CHAOS-4038 coverage-floor find that the cut dropped but
	// unionCandidatesForOffer still hands to the offer builders: Rank is 0
	// and Survived false on such an event, because the candidate reaches
	// the offer boundary WITHOUT surviving the cut. The harness's own
	// expected_subject_in_pool/expected_subject_rank/
	// expected_subject_at_offer_boundary row fields are derived from these
	// (Subject is the SAME graph canonical id the corroboration/decision
	// events already carry; the harness only ever writes booleans and
	// ranks to a report).
	Rank           int
	Survived       bool
	CoverageBypass bool
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
	// ShadowOutcome/ShadowReason/ShadowDIdentityHash/ShadowPreconditionUnproven/
	// ShadowUnscopedVisibility/ShadowNonCensusedSurvivor/
	// ShadowHandleGrammarBound/ShadowAnchorUniqueClaimant/ShadowKindsCensused
	// (evidence_round stage ONLY -- CHAOS-3899, design brief v5 §5/§6 Slice
	// A): the shadow evidence round's own per-resolution outcome, SUPPRESSED
	// from ever reaching a real commit-path decision this slice --
	// RunShadowEvidenceRound's Attestation, corpus-safe by construction
	// (ShadowDIdentityHash is D's SHA-256, never handle/anchor text; every
	// other field is a count/enum/bool). One evidence_round event fires per
	// call that reaches past the axis/scope gates -- the non-vacuity proof
	// that the round actually ran (brief §6/§7).
	ShadowOutcome              string
	ShadowReason               string
	ShadowDIdentityHash        string
	ShadowPreconditionUnproven bool
	ShadowUnscopedVisibility   bool
	ShadowNonCensusedSurvivor  bool
	ShadowHandleGrammarBound   bool
	ShadowAnchorUniqueClaimant bool
	// ShadowAnchorReceiptConfirmed (CHAOS-4042, sol-max ruling) mirrors
	// Attestation.AnchorReceiptConfirmed -- see that field's own doc
	// comment for why it is traced separately from
	// ShadowAnchorUniqueClaimant rather than folded into it.
	ShadowAnchorReceiptConfirmed bool
	ShadowKindsCensused          int
	// ShadowKindInsensitivityEvaluated/ShadowKindInsensitivityOutcome
	// (evidence_round stage ONLY, CHAOS-4039/sol-max ruling 2026-08-20,
	// adopted plan of record): whether the kind-insensitivity proof was
	// CONSULTED this round -- true only when the round reached a
	// would_commit/would_no_match outcome carrying an EXPLICIT
	// (non-receipt) kind hint -- and, when it was, its own
	// closed-vocabulary verdict ("commit_sound" | "no_match_sound" |
	// "kind_sensitive_outcome").
	//
	// HOW the verdict was obtained depends on the mode below, and the two
	// are NOT interchangeable (codex xhigh review round 3 follow-up --
	// this comment previously claimed kindInsensitivityProof itself always
	// ran, which stopped being true at CHAOS-4079):
	//   - "narrowed": kindInsensitivityProof (chaos3900_structure_offers.go)
	//     genuinely re-censused the PRE-narrowing kind set. Decision-bearing
	//     -- an unsound verdict demotes the round's own Outcome.
	//   - "observed_*": no narrowing was applied, so the pre-narrowing set
	//     IS the set already censused and the identical verdict is DERIVED
	//     arithmetically from this round's own KindAttestations
	//     (kindInsensitivityOutcomeFromRound) rather than re-read. Write-free
	//     and read-free: it issues no census call and demotes nothing.
	// Distinct from
	// ShadowOutcome==would_commit alone: that field cannot tell a reader
	// whether an inferred-tier commit was actually PROVEN kind-insensitive
	// (this proof ran and returned commit_sound) or merely reached that
	// outcome for an unrelated reason (the proof was never evaluated at
	// all, ShadowKindInsensitivityEvaluated==false) -- exactly the gap
	// CHAOS-4039's v4 measurement contract's kind_insensitivity_attested
	// classification needs a production-observed signal for, replacing
	// the prior generic-would_commit-is-good-enough proxy
	// (singleSatisfierVerified) sol-max's ruling found insufficient.
	ShadowKindInsensitivityEvaluated bool
	ShadowKindInsensitivityOutcome   string
	// ShadowKindInsensitivityMode (evidence_round stage ONLY, CHAOS-4079)
	// mirrors Attestation.KindInsensitivityMode: which explicit-kind
	// narrowing situation the two fields above were produced under, from
	// explicitKindNarrowingMode's closed vocabulary ("narrowed" |
	// "observed_no_overlap" | "observed_subsumed"), empty when the probe
	// was not evaluated.
	//
	// A consumer MUST read this alongside the two fields above rather than
	// treating "evaluated && commit_sound" as a uniform attestation. Only
	// "narrowed" means the verdict held across an actual change to the
	// census hypothesis set. An "observed_" mode means the census was
	// never narrowed, so the verdict shows the CENSUS was untouched by the
	// hint -- necessary but not sufficient for "the hint had no
	// influence", because request.ExpectedKinds still reaches explicit-
	// structure member stamping (contextfabric/structure.go) and kind-offer
	// ranking (kindOfferMaterial, this file), neither of which this proof
	// speaks for. The CHAOS-3742 two-turn harness gates its own
	// kind_insensitivity_attested classification on "narrowed" for exactly
	// that reason.
	ShadowKindInsensitivityMode string
	// CensusKind/CensusComplete/CensusCount/CensusReadAtUnix/CensusProtocol/
	// CensusClosureMismatch/CensusStatementCount/CensusRowsRead/
	// CensusHandleApplied/CensusAnchorApplied (evidence_probe stage ONLY,
	// CHAOS-3899): ONE per-kind census receipt -- brief §1.3(3), "Per-kind,
	// never aggregated across kinds". CensusReadAtUnix is the aggregate
	// statement's own now64() receipt as a Unix epoch (never zero for a
	// successfully executed census); CensusProtocol is always
	// "aggregate_first" per the brief's pin.
	CensusKind            contextfabric.SubjectKind
	CensusComplete        bool
	CensusCount           int
	CensusReadAtUnix      int64
	CensusProtocol        string
	CensusClosureMismatch bool
	CensusStatementCount  int
	CensusRowsRead        int
	CensusHandleApplied   bool
	CensusAnchorApplied   bool
	// GraphExistenceOK/CensusCommitReason (evidence_census_commit stage
	// ONLY, CHAOS-3896 Slice C, design brief v6 §1.4/§5): the keyed graph
	// existence read's own outcome for the ONE satisfier a decisive census
	// named -- Outcome (shared with corroboration/decision) is "merged"
	// (found, authorized, added to the candidate pool) or "refused"
	// (GraphExistenceOK=false: no node, graph_missing_satisfier;
	// GraphExistenceOK=true but still refused: found yet NodeCandidate
	// declined it -- unauthorized, invalid, or internal). CensusCommitReason
	// carries the closed-vocabulary DegradationReason (§4) ONLY for the
	// graph_missing_satisfier case; empty otherwise -- an authorization
	// decline has no dedicated reason token (brief §1.4: "unauthorized ->
	// no commit (and no oracle...)"), so leaving it empty is not a gap.
	// Whether the SUBSEQUENT re-decision call actually committed is a
	// SEPARATE, already-existing decision-stage event (CommitGate=="evidence_census")
	// -- this event only ever describes the graph read half.
	GraphExistenceOK   bool
	CensusCommitReason string
	// ShadowSourceNativeMatchCount/ShadowSourceNativeAnyResolved
	// (evidence_source_native stage ONLY, CHAOS-3899 WIDENING measurement --
	// chris-ratified pre-registered shadow measurement, 2026-08-19): the
	// source-native identifier grammar registry's own aggregate result for
	// this resolution -- total syntactic matches across every
	// sourceNativeGrammarRegistry entry, and whether AT LEAST ONE of them
	// resolved to a unique identity-universe claimant (BindSourceNativeHandles'
	// own completeness+uniqueness read, chaos3899_source_native_grammar.go).
	// SHADOW-ONLY: this stage's own event, like evidence_round/evidence_probe,
	// is never consumed by anything but a tracer -- see
	// BindSourceNativeHandles' doc comment for the structural guarantee.
	// One evidence_source_native event fires per resolution that reaches
	// past the round's own axis/scope gates (mirrors evidence_round's own
	// non-vacuity proof), independent of whether the ORIGINAL 3-entry
	// handleGrammarRegistry itself bound anything.
	ShadowSourceNativeMatchCount  int
	ShadowSourceNativeAnyResolved bool
	// ShadowSourceNativeGrammar/ShadowSourceNativeResolved/ShadowSourceNativeKind
	// (evidence_source_native_probe stage ONLY, CHAOS-3899 WIDENING
	// measurement): ONE per-match receipt -- mirrors evidence_probe's own
	// "per-kind, never aggregated" discipline, applied one level down to
	// "per grammar match". ShadowSourceNativeGrammar is the registry
	// entry's own FIXED name (safe to trace, never derived from question
	// text -- identical discipline to BoundHandle.Grammar); the matched
	// LITERAL TEXT itself (SourceNativeBind.Term) is in-process provenance
	// ONLY and is never placed on this or any other event field.
	// ShadowSourceNativeKind is populated only when ShadowSourceNativeResolved
	// is true (the resolved claimant's own kind, a closed enum -- same
	// discipline as CensusKind above).
	ShadowSourceNativeGrammar  string
	ShadowSourceNativeResolved bool
	ShadowSourceNativeKind     contextfabric.SubjectKind
	// SurvivorVerdict (slice_b_survivor_verdict stage ONLY, CHAOS-4088):
	// "neutral" or "eliminated" -- SurvivorsFirstOrder's own
	// candidateSurvivorVerdict for Subject (chaos3896_slice_b_presentation.go),
	// traced for the FIRST time by this field. Before it existed, a
	// candidate's post-hoc position in a reordered list could not be
	// attributed: a candidate sitting after the survivors could be
	// positively census-eliminated OR simply have ranked lower on its own
	// raw signal, and nothing distinguished the two from outside the
	// process. One event per candidate SurvivorsFirstOrder actually
	// classified (verdictNeutral included, not only eliminated -- the
	// silence-vs-neutral ambiguity elsewhere on this file is exactly what
	// evidence_round's own always-fires convention avoids, and this
	// mirrors it): absence of ANY slice_b_survivor_verdict event for a
	// resolution therefore means SurvivorsFirstOrder was never reached at
	// all (ReasonBudgetExhausted's own short-circuit, or the outer
	// deps.CensusFunc/stalled gate in resolve.go never opened), never that
	// every candidate happened to classify as neutral. TRACE-ONLY, per
	// team-lead's ruling on this ticket: nothing here feeds back into
	// resolution.Status/Committed/Candidates order -- SurvivorsFirstOrder's
	// own return value, computed identically whether or not a tracer is
	// wired, is what actually decides that. Ephemeral (this log line is
	// the only place the verdict is recorded) until CHAOS-4087 makes
	// commit-basis-shaped traces durable, at which point this signal is a
	// candidate for that same treatment.
	SurvivorVerdict string
	// PopulationBasis (decision stage ONLY, CHAOS-4154): closed vocabulary
	// naming WHICH candidate population a STATISTICAL commit (CommitBasis ==
	// "statistical") was actually decided over --
	// "resolution_wide_untruncated" (the ordinary, unscoped pool; this
	// resolution's own SearchTruncated, on this SAME event, was false),
	// "confirmed_kind_scoped_complete" (the isolated, exhaustively-proven-
	// complete confirmed-kind census chaos4154_confirmed_kind_scope.go
	// builds when the ordinary pool's own truncation would otherwise have
	// blocked an already-decisive confirmed-kind commit), or "none" (no
	// commit, a NON-statistical commit, OR the narrow CHAOS-3810
	// exact-label-survives-truncation carve-out -- that carve-out trusts
	// STRING EQUALITY, not any claim about population completeness, so it
	// deliberately makes no population-basis claim either). See
	// ResolveFromMergedCandidatesWithGateAndBasis's own populationBasis
	// local for the exact derivation. NEVER "resolution_wide_untruncated"
	// on an event whose own SearchTruncated is true -- see
	// TestResolveFromMergedCandidatesWithGateAndBasis_PopulationBasisInvariant's
	// "confirmed-kind-scoped commit" case.
	PopulationBasis string
	// ConfirmedKindScopeState/ConfirmedKindScopeCandidateCount (stage ==
	// "confirmed_kind_scope" ONLY, CHAOS-4154): describes
	// buildConfirmedKindScopedSnapshot's own outcome for this resolution --
	// see that function's doc comment (chaos4154_confirmed_kind_scope.go)
	// for the closed vocabulary ConfirmedKindScopeState carries
	// ("not_attempted" | "complete" | "truncated" | "failed" |
	// "plan_incomplete") and exactly what each means.
	// ConfirmedKindScopeCandidateCount is the isolated snapshot's own
	// candidate count (0 whenever State != "complete", since an incomplete
	// snapshot is never handed to the gate as a decision population).
	ConfirmedKindScopeState          string
	ConfirmedKindScopeCandidateCount int
	// AnchorOfferLabelsNormalizedCount (stage=="anchor_offer" ONLY,
	// CHAOS-4210) is the number of THIS call's AnchorOptions (either the V1
	// or V2 shape -- anchorOfferMaterial dispatches to exactly one) whose
	// Label anchorOfferLabel had to bound-to-fit the v1 wire contract
	// ([1,200] runes, ContextFabricAnchorOption.Validate()) -- 0 the
	// overwhelming common case. Mirrors
	// KindOfferCandidateOfferLabelsNormalizedCount's own doc comment (same
	// defect class, the anchor axis's own identity-universe Label instead
	// of the candidate axis's Subject.Label): a real display name has no
	// upstream length guard, so this is the SAME decision branch that must
	// be diagnosable from the run's own artifacts, not silently applied.
	// Rides its OWN "anchor_offer" stage (not "kind_offer") because
	// anchorOfferMaterial is called and traced independently in resolve.go,
	// not alongside kind/candidate/handle's shared call site.
	AnchorOfferLabelsNormalizedCount int
	// ConfirmedKindVectorScope* (stage == "confirmed_kind_scope" ONLY,
	// CHAOS-4155 Phase 1, SHADOW telemetry): describes
	// attemptConfirmedKindVectorCensus's own outcome -- see
	// chaos4155_confirmed_kind_vector_scope.go's doc comment for the closed
	// vocabulary ConfirmedKindVectorScopeState carries and why NONE of
	// these fields are ever consulted by any commit decision in this PR.
	// Populated only when ConfirmedKindScopeState=="plan_incomplete" (the
	// one case chaos4154_confirmed_kind_scope.go's own state machine
	// reaches this shadow arm from); every other stage-firing leaves these
	// at their zero value (State=="", read as "not attempted" by any
	// consumer, same convention as the CHAOS-4154 fields above).
	ConfirmedKindVectorScopeState              string
	ConfirmedKindVectorScopePopulationCount    int64
	ConfirmedKindVectorScopeEnumeratedCount    int64
	ConfirmedKindVectorScopeMalformedCount     int64
	ConfirmedKindVectorScopeQueryCount         int
	ConfirmedKindVectorScopeQueriesScored      int
	ConfirmedKindVectorScopeComparisonCount    int64
	ConfirmedKindVectorScopeRivalCountAboveTau int64
	ConfirmedKindVectorScopeSnapshotStable     bool
	ConfirmedKindVectorScopeDurationMS         int64
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
//
// CHAOS-3900 P1.C: also returns the structure-offer material this same
// resolution pass derives (expected_kind candidates from the SAME
// candidate pool -- see kindOfferMaterial, chaos3900_structure_offers.go).
// Every early-return error path returns a zero StructureOfferMaterial{};
// the exact-hint short-circuit below returns one too (a caller-supplied
// hint that resolved exactly is already decisive on subject identity, so
// there is nothing on this axis left to disambiguate).
// CHAOS-4085: the BASIS-DISCARDING wrapper. Kept at the original signature
// so this package's ~80 existing call sites are untouched; production goes
// through ResolveSubjectsWithCommitBasis. Discarding is safe by
// construction -- an absent basis reads back as CommitBasisUnknown, which
// IdentityProven reports false, which is the STRICT treatment.
func ResolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, deps ResolveDeps, confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, error) {
	resolution, offerMaterial, _, _, err := ResolveSubjectsWithCommitBasis(ctx, principal, request, interpreted, deps, confirmedKind, confirmedAnchor)
	return resolution, offerMaterial, err
}

// ResolveSubjectsWithCommitBasis is ResolveSubjects plus the CommitBasisSet
// describing WHICH CLASS OF PROOF stood behind each committed subject --
// the signal CHAOS-4085's post-synthesis affirmation gate needs and cannot
// reconstruct from the returned resolution alone (see
// contextfabric.CommitBasis for why the mechanism set and Confidence are
// not that signal).
//
// The set is threaded INTO the implementation as a map rather than returned
// up through it: a map is already a reference, so the body's existing
// eleven three-value returns stay exactly as they were and this change
// cannot silently alter any error path. Exactly three points write it, and
// each RESETS rather than merges -- see their call sites below.
//
// commitDigests (CHAOS-4087) is threaded identically, in lockstep with
// bases, at the SAME three write points -- see
// contextfabric.CommitDecisionDigest's own doc comment for why this is a
// wire-safe companion set rather than a widened CommitBasisSet.
func ResolveSubjectsWithCommitBasis(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, deps ResolveDeps, confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	bases := make(contextfabric.CommitBasisSet)
	digests := make(contextfabric.CommitDecisionDigestSet)
	resolution, offerMaterial, err := resolveSubjects(ctx, principal, request, interpreted, deps, confirmedKind, confirmedAnchor, bases, digests)
	if err != nil {
		// An error path commits nothing, so a basis (or digest) some
		// partial pass happened to record describes a resolution no
		// caller ever receives. Return empty sets rather than that debris.
		return resolution, offerMaterial, make(contextfabric.CommitBasisSet), make(contextfabric.CommitDecisionDigestSet), err
	}
	return resolution, offerMaterial, bases, digests, nil
}

func resolveSubjects(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, deps ResolveDeps, confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection, commitBases contextfabric.CommitBasisSet, commitDigests contextfabric.CommitDecisionDigestSet) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, err
	}
	terms := SubjectTerms(request, interpreted)
	// offersOnly (CHAOS-4234): the class-default window gate's offers-only
	// mode -- every commit MECHANISM below that only exists to reach a
	// commit (shadow evidence round/census, confirmed-kind truncation
	// scoping, survivors-first reordering) is skipped, because the engine
	// discards this call's resolution unconditionally and keeps only the
	// StructureOfferMaterial. Retrieval, ranking, the first-pass decision
	// (whose trace the harness reads) and every offer builder run exactly
	// as on a decisive turn. See contextfabric/chaos4234_offers_only.go.
	offersOnly := contextfabric.OffersOnlyResolution(ctx)
	candidatesBySubject := make(map[string]contextfabric.SubjectCandidate)
	// callerSourced marks which resolved subjects came from a
	// caller-explicit hint -- any SubjectHint.Source other than
	// "prior_subject_receipt". A caller-explicit hint is an authoritative,
	// direct ask and keeps the short-circuit/truncation-priority behavior
	// below; a receipt-derived hint is not -- it is Engine's best guess at
	// what a conversational reference bound to previously, and the current
	// question may name a different subject entirely.
	callerSourced := make(map[string]bool)
	// subjectCandidatesAuthzDropped (CHAOS-3888) aggregates every candidate
	// node this call found -- via an explicit SubjectHint below, or via
	// mergeSearchResults' per-term/question passes further down -- and
	// excluded specifically because AuthorizedAttributes denied it. Checked
	// independently of NodeCandidate's own !ok result (which also fires for
	// an invalid or internal-bookkeeping node, neither of which is an
	// authorization event) so this count never over-reports.
	subjectCandidatesAuthzDropped := 0
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
			return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, err
		}
		if !ok {
			continue
		}
		// allowExactMatch=true: subject.Label here is the caller's own
		// explicit hint label, legitimately eligible to exact-match --
		// unlike CHAOS-3838's question-provenance marker below, this is
		// genuine caller-supplied identity, and this whole branch already
		// forces Confidence/MatchExact explicitly regardless.
		// CHAOS-3888: checked before NodeCandidate, not derived from its
		// !ok result, so an invalid/internal-bookkeeping node (a different,
		// unrelated exclusion reason) never inflates this count.
		hintAuthorized := AuthorizedAttributes(principal, request.RequestedScope, node.Attributes)
		candidate, ok := NodeCandidate(principal, request.RequestedScope, subject.Label, node, deps.IsInternal, true, deps.ResolutionTracer, request.RequestID)
		if !ok {
			if !hintAuthorized {
				subjectCandidatesAuthzDropped++
			}
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
		// CHAOS-4085 commit-basis write site 1 of 3: the caller-hint short
		// circuit. Everything this exit commits was named by canonical id,
		// re-read from the graph by keyed lookup and re-authorized in the
		// hint loop above -- CommitBasisCallerCanonicalID for all of it.
		exactResolution, exactBases, exactDigests := FinalizeExactResolutionWithBasis(candidatesBySubject, callerSourced, request.Options.MaxSubjectCandidates)
		commitBases.ResetTo(exactBases)
		commitDigests.ResetTo(exactDigests)
		// CHAOS-4096: this short circuit never reaches
		// ResolveFromMergedCandidatesWithGateAndBasis -- the ONLY other
		// place a "decision" event fires -- so without this it committed
		// entirely invisibly to the trace, however many subjects it
		// resolved. One event per committed subject, same field shape and
		// same Stage/Outcome vocabulary that path uses; CommitGate names
		// THIS gate specifically (CommitGateCallerHintShortCircuit, the
		// SAME literal FinalizeExactResolutionWithBasis already stamped
		// onto the CommitDecisionDigest above). WinningMechanism is read
		// off the SAME candidatesBySubject entry the hint loop built (every
		// entry there carries MatchExact, caller-sourced or receipt-derived
		// alike -- see that loop's own comment). SearchTruncated/
		// AliasLookupComplete/TiedStatisticalTop stay their honest zero
		// value: no search ran and no identity trust gate fired on this
		// path, exactly as FinalizeExactResolutionWithBasis's own comment
		// already documents for the digest it stamps. SearchCandidateLimit
		// mirrors ResolveFromMergedCandidatesWithGateAndBasis's own decision
		// event (that field's own doc comment): the candidate-count bound
		// this call actually applied, Options.MaxSubjectCandidates, the SAME
		// value FinalizeExactResolutionWithBasis was called with two lines
		// up. PopulationBasis is "none": this exit never ranks or truncates
		// a searched candidate pool at all (FinalizeExactResolutionWithBasis
		// only reorders/truncates the ALREADY-KNOWN exact-hint set by class)
		// -- there is no ranked population for a statistical commit here to
		// name, the same "no claim to make" reasoning ResolutionTraceEvent.
		// PopulationBasis's own doc comment gives for a non-statistical
		// commit or no commit at all.
		//
		// CHAOS-4234 (codex R1 finding, confirmed): OfferedUnderWindowGate
		// mirrors offersOnlyDecisionTracer's own tag (this file, above) --
		// this short circuit bypasses that wrapper entirely (it emits
		// straight to deps.ResolutionTracer, never through firstPassTracer),
		// so under the offers-only pass (contextfabric.OffersOnlyResolution)
		// this event would otherwise read as an untagged real commit to any
		// consumer that has not also cross-referenced which mode produced
		// it -- exactly the hazard that wrapper exists to prevent on the
		// ordinary path. The engine's own discard of this call's resolution
		// under the gate (chaos4234_offers_only.go's own "layer 1") stays
		// the load-bearing safety property regardless; this tag is the
		// trace-side twin of it.
		if deps.ResolutionTracer != nil {
			for _, subject := range exactResolution.Committed {
				winningMechanism := ""
				if candidate, ok := candidatesBySubject[SubjectKey(subject)]; ok && len(candidate.MatchMechanisms) > 0 {
					winningMechanism = string(candidate.MatchMechanisms[0])
				}
				deps.ResolutionTracer.Trace(ResolutionTraceEvent{
					RequestID: request.RequestID, Stage: "decision", Subject: subject,
					Outcome: "committed", WinningMechanism: winningMechanism,
					CommitGate:             CommitGateCallerHintShortCircuit,
					CommitBasis:            string(exactBases.For(subject)),
					SearchCandidateLimit:   request.Options.MaxSubjectCandidates,
					PopulationBasis:        "none",
					OfferedUnderWindowGate: offersOnly,
				})
			}
		}
		return exactResolution, contextfabric.StructureOfferMaterial{}, nil
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
			return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, err
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
				// Truncated (CHAOS-4120): THIS term's own Search() truncation,
				// before the fold into the resolution-wide searchTruncated flag
				// just below -- see Truncated's own doc comment.
				Truncated: truncated,
			})
		}
		// allowExactMatch=true: term here is genuine caller-derived search
		// input (an interpreted subject term, or a requested-scope hint
		// label -- see SubjectTerms), legitimately eligible to exact-match
		// a subject's own label.
		termTraversalDegraded, termAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, results, candidatesBySubject, observationParentKey, observationBlocked, true, vectorArmSimilarity, identity, identityTerms)
		traversalDegraded += termTraversalDegraded
		subjectCandidatesAuthzDropped += termAuthzDropped
	}
	// aliasIdentityComplete (CHAOS-3884): built here, between the per-term
	// Search loop and the question pass -- LOW-12: placing the merge BEFORE
	// the question pass means capMatchedTermsAfterMerge (below, called with
	// questionProvenanceMarker) sees and correctly caps whatever
	// MatchedTerms this merge ADDED, in the SAME single pass it already
	// runs, rather than needing a second capping call. deps.AliasLookup nil
	// means "this backend does not implement it" -- aliasIdentityComplete
	// stays false, byte-identical
	// to every pre-CHAOS-3884 backend.
	aliasIdentityComplete := false
	// aliasClaimantsByTerm (CHAOS-3899, shadow-only) is deps.AliasLookup's
	// own claimantsByTerm, retained past the block below so the shadow
	// evidence round's anchor binding (BindAnchor) can reuse it rather than
	// re-querying -- see the shadow-round call near this function's return.
	// nil for a backend with no AliasLookup, the same "not implemented"
	// convention aliasIdentityComplete=false already carries.
	var aliasClaimantsByTerm map[string][]CandidateNode
	if deps.AliasLookup != nil {
		claimantsByTerm, complete, err := deps.AliasLookup(ctx, principal.OrgID, terms)
		if err != nil {
			return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, err
		}
		aliasIdentityComplete = complete
		aliasClaimantsByTerm = claimantsByTerm
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
			claimantTraversalDegraded, claimantAuthzDropped := mergeSearchResults(ctx, principal, request, deps, term, nodes, candidatesBySubject, observationParentKey, observationBlocked, true, nil, identity, identityTerms)
			traversalDegraded += claimantTraversalDegraded
			subjectCandidatesAuthzDropped += claimantAuthzDropped
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
				return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, err
			}
			if truncated {
				searchTruncated = true
			}
			if degraded {
				retrievalDegraded = true
			}
			// search_question (CHAOS-4120): a SEPARATE stage from "search"
			// above -- before this, the question-level pass traced NOTHING
			// of its own; its truncated/degraded outcome only ever reached
			// the pooled resolution-wide flags folded in just above, making
			// it indistinguishable from a per-term Search() truncation on
			// any trace a reader could inspect. This runs AT MOST ONCE per
			// resolution (deps.SearchQuestion != nil and a non-empty
			// question, this block's own guard), so one event suffices --
			// no per-term fan-out to worry about, unlike "search".
			if deps.ResolutionTracer != nil {
				deps.ResolutionTracer.Trace(ResolutionTraceEvent{
					RequestID: request.RequestID, Stage: "search_question",
					SearchResultCount: len(results), Truncated: truncated,
				})
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
			questionTraversalDegraded, questionAuthzDropped := mergeSearchResults(ctx, principal, request, deps, questionProvenanceMarker, results, candidatesBySubject, observationParentKey, observationBlocked, false, nil, nil, nil)
			traversalDegraded += questionTraversalDegraded
			subjectCandidatesAuthzDropped += questionAuthzDropped
			// codex round-1 P1, second half: a candidate already at the
			// 32-entry MatchedTerms cap from real per-term finds would
			// overflow to 33 once the marker above unioned in. The marker
			// -- synthetic provenance, not something a caller typed -- is
			// exactly the one entry that must give way; every real,
			// user-meaningful extracted term survives.
			capMatchedTermsAfterMerge(candidatesBySubject, questionProvenanceMarker)
		}
	}
	// CHAOS-4038: the kind-coverage floor runs LAST, strictly after every
	// ordinary retrieval pass above -- same ordering discipline the question
	// pass itself follows (a per-term or AliasLookup find always wins an
	// exact-confidence tie against a later pass's find of the SAME subject,
	// MergeCandidates' documented rule), and only when confirmedKind is nil:
	// a request that already confirmed a kind (CHAOS-3900 P1.D) has nothing
	// left to disambiguate on this axis, so spending extra kind-scoped calls
	// here would be pure waste -- filterCandidatesByConfirmedKind below would
	// discard their results regardless.
	//
	// coverageCandidates (codex CHAOS-4038 review round 1, finding 1) is
	// retained past this block so the kindOfferMaterial call site below can
	// union it into the offer's own input -- a coverage-floor find can
	// still be dropped from resolution.Candidates by
	// ResolveFromMergedCandidatesWithGate's final ranked-set truncation (a
	// small MaxSubjectCandidates plus a pool already full of
	// higher-confidence OTHER-kind candidates), which would otherwise
	// silently defeat this whole pass for exactly the resolutions it exists
	// to help. See applyKindCoverageFloor's own doc comment
	// (chaos4038_kind_coverage.go).
	//
	// coverageFloorDegraded is DELIBERATELY a separate variable from
	// retrievalDegraded, never merged into it before the commit-gate calls
	// below (codex CHAOS-4038 review round 2, finding 1): searchTruncated
	// and retrievalDegraded are gate INPUTS -- ResolveFromMergedCandidatesWithGate
	// reads them to decide whether an otherwise-decisive candidate is safe
	// to auto-commit. This coverage floor searches a kind that had ZERO
	// candidates in the pool; its own truncation/degradation says something
	// about THAT kind's own visibility, never about whether an
	// ALREADY-decisive candidate of a DIFFERENT kind remains safe to commit.
	// Folding it into the gate's inputs made an unrelated, previously-clean
	// commit fall to ambiguous purely because a missing-kind coverage query
	// happened to find more than kindCoverageQueryLimit rows -- directly
	// contradicting this pass's own "a coverage floor, never a competing
	// top-K" design intent (repro: TestResolveSubjects_SearchKindCoverageTruncationNeverBlocksAnUnrelatedCommit).
	// coverageFloorDegraded is instead OR'd into resolution.RetrievalDegraded
	// AFTER each gate call below, informationally -- it still reaches the
	// caller-visible signal, it just never influences the decision.
	// coverageFloorTruncated has no equivalent wire-facing field
	// (searchTruncated is gate-input only, see resolution.go), so it is
	// computed and then deliberately left unused beyond that.
	var coverageCandidates []contextfabric.SubjectCandidate
	var coverageFloorDegraded bool
	if confirmedKind == nil {
		// aliasLookupTrustworthy (codex CHAOS-4038 review round 1, finding 3):
		// the SAME two facts that already gate aliasIdentityComplete's own
		// meaning -- deps.AliasLookup actually ran for this resolution AND
		// reported complete=true. False (nil AliasLookup, or a historical/
		// row-budget/existence-check incompleteness) means repository/
		// project/team got NO complete identity-universe read this call, so
		// the coverage floor must widen to cover them too -- see
		// effectiveCoverageFloorKinds' own doc comment.
		aliasLookupTrustworthy := deps.AliasLookup != nil && aliasIdentityComplete
		added, coverageTraversalDegraded, coverageAuthzDropped, coverageTruncated, coverageDegraded, coverageMissingKinds, coverageMissingKindsList, coverageErr := applyKindCoverageFloor(ctx, principal, request, deps, terms, candidatesBySubject, observationParentKey, observationBlocked, identity, identityTerms, aliasLookupTrustworthy)
		if coverageErr != nil {
			return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, coverageErr
		}
		coverageCandidates = added
		coverageFloorDegraded = coverageDegraded
		traversalDegraded += coverageTraversalDegraded
		subjectCandidatesAuthzDropped += coverageAuthzDropped
		// CHAOS-4086: the floor's own OBSERVATION event, emitted on its own
		// stage rather than folded onto the decision event below.
		//
		// A SEPARATE STAGE IS THE POINT, not a convenience. The paragraph
		// above establishes that this pass's truncation and degradation say
		// something about a MISSING KIND's visibility and never about
		// whether an already-decisive candidate of a different kind is safe
		// to commit -- which is why coverageFloorDegraded is kept out of the
		// gate's inputs and coverageFloorTruncated was left unread
		// entirely. Attaching these three values to the decision event
		// would put them exactly where a reader infers gate inputs from,
		// re-creating by presentation the coupling the code is careful not
		// to have. On their own stage they are what they are: what the
		// floor went looking for, and what it found.
		//
		// This makes CHAOS-4038's floor effect readable from a run's own
		// artifacts for the first time -- before this, the pass could widen
		// or fail to widen a candidate pool and nothing downstream could
		// tell which had happened. Counts and booleans only: no kind name,
		// no term, no candidate identity.
		if deps.ResolutionTracer != nil {
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID: request.RequestID, Stage: "kind_coverage_floor",
				KindCoverageFloorFired:       len(added) > 0,
				KindCoverageMissingKinds:     coverageMissingKinds,
				KindCoverageFloorTruncated:   coverageTruncated,
				KindCoverageMissingKindsList: coverageMissingKindsList,
			})
		}
	}
	if traversalDegraded > 0 && deps.TraversalDegraded != nil {
		deps.TraversalDegraded(ctx, principal.OrgID, traversalDegraded)
	}
	// CHAOS-3888: same aggregate-report convention as TraversalDegraded
	// immediately above.
	if subjectCandidatesAuthzDropped > 0 && deps.SubjectCandidatesAuthzDropped != nil {
		deps.SubjectCandidatesAuthzDropped(ctx, principal.OrgID, subjectCandidatesAuthzDropped)
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
	// gateValid (CHAOS-3896 Slice C): computed here too, mirroring
	// ResolveFromMergedCandidatesWithGate's OWN internal gateValid --
	// resolve.go needs its own copy to decide whether the census's graph
	// read (real I/O, real cost) is even worth attempting, since an invalid
	// gate disables evidence_census inside that function regardless (see
	// this function's second call site below). gate.Validate() is a cheap,
	// pure check; computing it twice is simpler and safer than threading
	// resolution.go's private copy back out through a return value.
	gateValid := gate.Validate() == nil
	// CHAOS-3900 P1.D: narrows the hypothesis set to a CONFIRMED kind,
	// when the request's own receipts confirmed one -- design brief
	// §2.1's "the confirmed kind becomes the census scope." A nil
	// confirmedKind (every request that confirmed no kind -- the
	// overwhelming common case) makes this call a no-op returning
	// candidatesBySubject UNCHANGED, so pool composition below is
	// byte-identical to the pre-P1.D code path. See
	// ConfirmedExpectedKind's own doc comment (ports.go) for why
	// non-confirmed narrowing cannot reach this same call.
	candidatesBySubject = filterCandidatesByConfirmedKind(candidatesBySubject, confirmedKind)
	// CHAOS-4132: filterCandidatesByConfirmedKind can legitimately empty the
	// pool -- see applyConfirmedKindRescue's own doc comment for exactly
	// when and why (a confirmed kind whose only route into a PRIOR turn's
	// pool was the coverage floor, which THIS call skips by design just
	// above). A small, bounded, kind-scoped supplemental retrieval pass
	// rescues that case before conceding to a guaranteed no_match. Gated on
	// the pool being empty AFTER filtering (not merely "confirmedKind !=
	// nil") so the overwhelmingly common case -- ordinary search already
	// found the confirmed kind's candidates -- issues ZERO extra calls,
	// preserving CHAOS-3900 P1.D's own "nothing left to disambiguate"
	// optimization intact; see
	// TestResolveSubjects_ConfirmedKindRescueSkippedWhenPoolAlreadySatisfied.
	var confirmedKindRescueAttempted bool
	var confirmedKindRescueAdded []contextfabric.SubjectCandidate
	var confirmedKindRescueTruncated bool
	if confirmedKind != nil && len(candidatesBySubject) == 0 && deps.SearchKind != nil {
		confirmedKindRescueAttempted = true
		rescued, rescueTraversalDegraded, rescueAuthzDropped, rescueTruncated, rescueDegraded, rescueErr := applyConfirmedKindRescue(ctx, principal, request, deps, terms, candidatesBySubject, observationParentKey, observationBlocked, identity, identityTerms, confirmedKind.Kind)
		if rescueErr != nil {
			return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, rescueErr
		}
		confirmedKindRescueAdded = rescued
		confirmedKindRescueTruncated = rescueTruncated
		traversalDegraded += rescueTraversalDegraded
		subjectCandidatesAuthzDropped += rescueAuthzDropped
		// codex review round 1 (MEDIUM, confirmed): UNLIKE the coverage
		// floor's own coverageTruncated/coverageFloorDegraded -- which
		// describe a pass that only ever adds ONE candidate among many
		// OTHER kinds' candidates already in the pool, so an unrelated
		// commit must never see it -- this rescue is, when it fires, the
		// SOLE source of every candidate the gate is about to decide over
		// (candidatesBySubject was empty before it ran). Its own
		// truncated/degraded state is therefore NOT unrelated noise: a
		// truncated rescue call may have cut off a genuine rival candidate
		// of the SAME confirmed kind, exactly the risk searchTruncated
		// exists to gate on for ordinary search. Folding it in here (rather
		// than leaving it a purely observational, gate-blind signal) is
		// what keeps a same-shape non-exact commit from reading as
		// falsely confident; TestResolveSubjects_ConfirmedKindRescueTruncationBlocksALoneCandidateCommit
		// pins this, and
		// TestResolveSubjects_ConfirmedKindRescueExactMatchSurvivesItsOwnTruncation
		// pins that this does NOT reach the exact-label/identity-fast-path
		// tiers, which sit ahead of the searchTruncated check for every
		// caller (resolution.go) and are unaffected either way.
		searchTruncated = searchTruncated || rescueTruncated
		retrievalDegraded = retrievalDegraded || rescueDegraded
	}
	if confirmedKindRescueAttempted && deps.ResolutionTracer != nil {
		// CHAOS-4132: this event's own PRESENCE already tells a reader the
		// rescue was attempted (see ConfirmedKindRescueFired's own doc
		// comment); Truncated is carried here too so an operator can see
		// WHY a rescue that found something still deferred to ambiguous
		// rather than committing.
		deps.ResolutionTracer.Trace(ResolutionTraceEvent{
			RequestID: request.RequestID, Stage: "confirmed_kind_rescue",
			ConfirmedKindRescueFired:       len(confirmedKindRescueAdded) > 0,
			ConfirmedKindRescueResultCount: len(confirmedKindRescueAdded),
			ConfirmedKindRescueTruncated:   confirmedKindRescueTruncated,
		})
	}
	// CHAOS-4085 commit-basis write site 2 of 3: the ordinary commit
	// decision. ResetTo, not merge -- this is the first decision, and it
	// defines the whole basis set for it.
	//
	// CHAOS-4234 (codex round-1 finding 3, confirmed): firstPassTracer
	// tags this call's OWN "decision" stage event with OfferedUnderWindowGate
	// under offers-only mode -- the engine discards resolution/
	// commitBases/commitDigests for this SAME call unconditionally
	// (chaos4234_offers_only.go's gatedOfferMaterial), so an
	// "Outcome=committed" reading here never reaches anywhere decisive.
	// Without the tag a reader of the two-turn harness's own
	// finalDecisionEvents() (Turn1CommitGate/Outcome) cannot tell this
	// pass's own would-be commit apart from a real one. ranked_cut events
	// from this SAME call are left untouched -- they drive the offer
	// builders' own input under offers-only mode too, so they stay
	// accurate regardless of which mode produced them. See
	// offersOnlyDecisionTracer's own doc comment.
	firstPassTracer := deps.ResolutionTracer
	if offersOnly && firstPassTracer != nil {
		firstPassTracer = offersOnlyDecisionTracer{real: firstPassTracer}
	}
	resolution, firstPassBases, firstPassDigests := ResolveFromMergedCandidatesWithGateAndBasis(candidatesBySubject, observationParentKey, observationBlocked, request.Options.MaxSubjectCandidates, request.Options.AllowClarification, searchTruncated, vectorArmSimilarity, deps.VectorMarginCommitThreshold, retrievalDegraded, effectiveSearchLimit, deps.CalibratedTopK, unscopedVisibility, gate, identity, identityTerms, aliasIdentityComplete, firstPassTracer, request.RequestID, "", false)
	commitBases.ResetTo(firstPassBases)
	commitDigests.ResetTo(firstPassDigests)
	// coverageFloorDegraded (CHAOS-4038, codex review round 2 finding 1) is
	// OR'd in HERE, informationally, AFTER the gate already decided using
	// the un-widened retrievalDegraded -- see its own declaration above for
	// why it must never reach the gate call itself.
	resolution.RetrievalDegraded = retrievalDegraded || coverageFloorDegraded
	// CHAOS-4154: confirmed-kind truncation scoping. Reachable ONLY when
	// this resolution confirmed a kind, the resolution-wide searchTruncated
	// bit is true, and the ordinary (unscoped) decision above committed
	// nothing -- which, given resolution.go's switch ordering (searchTruncated
	// sits BEFORE LoneFloor/TopFloor, first-match wins), is EXACTLY the
	// population case 57 (see chaos4154_confirmed_kind_scope.go) names: the
	// truncation-preempt branch fired, so LoneFloor/TopFloor never even ran.
	// exact_index/identity_fast_path already had first refusal in the call
	// above -- if either had fired, resolution.Committed would be non-empty
	// and this block would not run.
	if !offersOnly && confirmedKind != nil && searchTruncated && len(resolution.Committed) == 0 {
		scopedPool, scopedObservationParentKey, scopedObservationBlocked, scopedIdentity, scopedIdentityTerms, scopeState, scopeTraversalDegraded, scopeAuthzDropped, scopeVectorCensus, scopeErr := buildConfirmedKindScopedSnapshot(ctx, principal, request, deps, terms, aliasClaimantsByTerm, aliasIdentityComplete, confirmedKind.Kind, effectiveSearchLimit)
		if scopeErr != nil {
			return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, scopeErr
		}
		if scopeTraversalDegraded > 0 && deps.TraversalDegraded != nil {
			deps.TraversalDegraded(ctx, principal.OrgID, scopeTraversalDegraded)
		}
		// codex R2 (Medium, confirmed): call ONLY deps.SubjectCandidatesAuthzDropped,
		// exactly like the pre-existing unscoped call site above (~line 1460)
		// -- a production SubjectCandidatesAuthzDropped implementation
		// (falkorgraph/reader.go) already forwards to
		// contextfabric.RecordSubjectCandidatesAuthzDropped itself; an
		// earlier version of this branch ALSO called it directly here,
		// double-counting every scoped-pass authz drop into the ctx-attached
		// recorder.
		if scopeAuthzDropped > 0 && deps.SubjectCandidatesAuthzDropped != nil {
			deps.SubjectCandidatesAuthzDropped(ctx, principal.OrgID, scopeAuthzDropped)
		}
		if deps.ResolutionTracer != nil {
			// ConfirmedKindScopeCandidateCount (codex review finding, LOW/
			// HIGH confidence, confirmed): 0 whenever scopeState !=
			// confirmedKindScopeComplete -- see that field's own doc
			// comment. buildConfirmedKindScopedSnapshot can return a
			// non-empty (but discarded) pool on a truncated/failed/
			// plan_incomplete outcome (a partial read still merges what it
			// found before the disqualifying signal arrives), and reporting
			// that count would both contradict the documented contract and
			// leak a size signal from a population the gate never sees.
			candidateCount := 0
			if scopeState == confirmedKindScopeComplete {
				candidateCount = len(scopedPool)
			}
			// CHAOS-4155 Phase 1: ConfirmedKindVectorScope* fields are the
			// shadow census's own outcome, carried on this SAME event --
			// see ConfirmedKindVectorCensusOutcome's own doc comment. Zero
			// value (State=="") on every scopeState other than
			// confirmedKindScopePlanIncomplete, which is exactly when
			// buildConfirmedKindScopedSnapshot's own switch invokes the
			// shadow arm at all.
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID: request.RequestID, Stage: "confirmed_kind_scope",
				ConfirmedKindScopeState:                    scopeState,
				ConfirmedKindScopeCandidateCount:           candidateCount,
				ConfirmedKindVectorScopeState:              scopeVectorCensus.State,
				ConfirmedKindVectorScopePopulationCount:    scopeVectorCensus.PopulationCount,
				ConfirmedKindVectorScopeEnumeratedCount:    scopeVectorCensus.EnumeratedCount,
				ConfirmedKindVectorScopeMalformedCount:     scopeVectorCensus.MalformedCount,
				ConfirmedKindVectorScopeQueryCount:         scopeVectorCensus.QueryCount,
				ConfirmedKindVectorScopeQueriesScored:      scopeVectorCensus.QueriesScored,
				ConfirmedKindVectorScopeComparisonCount:    scopeVectorCensus.ComparisonCount,
				ConfirmedKindVectorScopeRivalCountAboveTau: scopeVectorCensus.RivalCountAboveTau,
				ConfirmedKindVectorScopeSnapshotStable:     scopeVectorCensus.SnapshotStable,
				ConfirmedKindVectorScopeDurationMS:         scopeVectorCensus.DurationMS,
			})
		}
		// gateValid: the SAME conjunct every other commit path in this
		// resolution requires -- an invalid gate must disable this one too,
		// not just leave it to fire because the ordinary gates individually
		// declined (mirrors the evidence-census call site's own identical
		// guard below).
		if scopeState == confirmedKindScopeComplete && gateValid {
			// CHAOS-4085 commit-basis write site: the confirmed-kind-scoped
			// re-decision. ResetTo only when it actually commits (below) --
			// an ambiguous scoped re-decision must never discard the first
			// pass's own (unscoped) candidates/clarification prompt, which
			// is the caller-visible fallback sol's routing keeps: "evaluate
			// lone_floor and top_of_two from the scoped snapshot... [when
			// complete]; otherwise: fail closed as today." In practice the
			// FULL switch runs unmodified over the scoped population
			// (resolution.go), so exact_index/identity_fast_path can also
			// fire here, not only lone_floor/top_of_two -- deliberately: an
			// exact-label or genuinely-proven-unique identity match within
			// an isolated, proven-complete population is a STRONGER
			// commit-worthiness signal than a floor/gap comparison, never a
			// weaker one, so widening beyond sol's own two named tiers adds
			// no new risk (do-not-build's "do not change... veto semantics"
			// is honored: no gate's OWN threshold or check changes, only
			// which population it evaluates).
			//
			// searchTruncated=false: this call's own population is the
			// isolated, exhaustively-proven-complete census -- see
			// confirmedKindScopedBasis's own doc comment (resolution.go) for
			// why that, not this literal parameter, is what the decision
			// trace records.
			//
			// vectorArmSimilarity=nil, vectorMarginCommitThreshold=0,
			// calibratedTopK=0: SearchKind is lexical-only (nothing to feed
			// the CHAOS-3829 carve-out) and the carve-out is deliberately
			// disabled outright for this call -- sol's ratified routing
			// names only "evaluate lone_floor and top_of_two from the
			// scoped snapshot", not a NEW, unreviewed interaction between
			// two novel-population mechanisms.
			//
			// scopedIdentity/scopedIdentityTerms: buildConfirmedKindScopedSnapshot's
			// OWN fresh, scoped-only collision-detection maps (codex review
			// finding, Medium, confirmed) -- NEVER the caller's
			// whole-resolution identity/identityTerms here. See that
			// function's own doc comment for why reusing the shared maps
			// was wrong (an unrelated, possibly cross-kind unscoped
			// claimant could veto a scoped candidate, and mutation residue
			// leaked into the later evidence-census re-decision) and why a
			// genuine same-kind collision is still caught regardless (this
			// pass's own exhaustiveness finds it too).
			//
			// aliasIdentityComplete: the REAL value this resolution's own
			// AliasLookup call proved (not hardcoded) -- it is an honest,
			// independently-true fact about this org's identity universe,
			// unrelated to which population the gate is currently deciding
			// over. For confirmedKind.Kind outside isAliasLookupScopedKind
			// (work_item) identity_fast_path is structurally unreachable
			// regardless, so this can only ever let a genuinely-proven
			// unique identity claimant commit through the STRONGEST tier
			// rather than falling to lone_floor/top_of_two -- never the
			// reverse.
			// scopedDecisionTracer (codex R2, Medium, confirmed): every OTHER
			// decision-stage event producer in this file traces
			// unconditionally because the resolution it decides is always
			// the one returned (see the CHAOS-3896 evidence-census
			// re-decision just below, which unconditionally keeps whatever
			// it decides). This call is different -- it is DISCARDED
			// (resolution stays the first pass's) whenever
			// scopedResolution.Committed is empty -- so tracing its
			// "decision" event unconditionally could leave that event as
			// the LAST one a reader sees while the actual returned
			// resolution is the discarded call's sibling, not itself:
			// exactly the "last decision event describes the returned
			// resolution" invariant every consumer of this trace (including
			// this ticket's own lastDecisionEvent test helper) relies on,
			// broken. scopedDecisionTracer holds the ONE "decision" event
			// this call produces back from the real tracer until keep() is
			// called below, which happens only when this resolution is
			// actually retained; every other stage (corroboration, etc.)
			// still passes through immediately, unaffected. The
			// "confirmed_kind_scope" stage event traced above already
			// records this attempt's own completeness/candidate-count
			// regardless of outcome, so nothing about the attempt itself
			// becomes undiagnosable by holding back just the decision event.
			scopedDecisionTracer := &discardableDecisionTracer{real: deps.ResolutionTracer}
			scopedResolution, scopedBases, scopedDigests := ResolveFromMergedCandidatesWithGateAndBasis(
				scopedPool, scopedObservationParentKey, scopedObservationBlocked, request.Options.MaxSubjectCandidates,
				request.Options.AllowClarification, false, nil, 0, false, effectiveSearchLimit, 0,
				unscopedVisibility, gate, scopedIdentity, scopedIdentityTerms, aliasIdentityComplete,
				scopedDecisionTracer, request.RequestID, "", true,
			)
			if len(scopedResolution.Committed) > 0 {
				resolution = scopedResolution
				// ResetTo, not merge -- same CHAOS-4085/4087 discipline every
				// other re-decision call site in this function follows: this
				// call returns a WHOLLY FRESH resolution over an isolated
				// population, so the basis/digest sets must be replaced, not
				// merged with the first pass's.
				commitBases.ResetTo(scopedBases)
				commitDigests.ResetTo(scopedDigests)
				resolution.RetrievalDegraded = retrievalDegraded || coverageFloorDegraded
				scopedDecisionTracer.keep()
			}
		}
	}
	// CHAOS-3899 (design brief v5 §6 Slice A) runs the full evidence round
	// for measurement, strictly AFTER resolution's own COMMIT-GATE decision
	// is already final -- CHAOS-3896 Slice B (below) may reorder
	// resolution.Candidates and rebuild resolution.ClarificationPrompt from
	// the round's own Attestation, but resolution.Status/Committed and
	// which candidates are IN the list are exactly as
	// ResolveFromMergedCandidatesWithGate decided, never touched by
	// anything past this point (TestResolveSubjects_ShadowEvidenceRoundNeverChangesResolutionDecision
	// pins the decision half; TestResolveSubjects_SurvivorsFirstReorderNeverChangesMembership
	// pins the Slice B half). Gated on deps.CensusFunc != nil (nil is the
	// default -- zero cost, zero effect for every existing caller, the same
	// convention every other optional ResolveDeps dependency uses) AND
	// "stalled" (§0's own definition: nothing committed and searchTruncated)
	// -- the brief's own cost note ("per stalled resolution... committed
	// resolutions pay nothing").
	if !offersOnly && deps.CensusFunc != nil && len(resolution.Committed) == 0 && searchTruncated {
		attestation := runShadowEvidenceRoundForResolution(ctx, principal, request, interpreted, resolution, aliasClaimantsByTerm, aliasIdentityComplete, unscopedVisibility, deps, confirmedKind, confirmedAnchor)
		// CHAOS-3896 Slice C (design brief v6 §1.4): the round's Attestation
		// is now CONSUMED in the commit decision, not merely traced. When it
		// named exactly one satisfier (attestedSatisfier), prove that
		// satisfier exists as a keyed GRAPH node (fail-closed on absence --
		// graph_missing_satisfier) and merge it into the SAME candidate pool
		// ordinary search already built (mergeCensusAttestedSatisfier), then
		// re-run the commit decision with that evidence available to the
		// evidence_census rescue (resolution.go). gateValid gates this
		// exactly like every other commit path -- an invalid gate must
		// disable evidence_census too, and skipping the (real I/O, real
		// cost) graph read entirely when the gate is invalid is strictly
		// better than paying for a read whose result the gate would refuse
		// to use regardless.
		if gateValid {
			if attestedKey, merged := mergeCensusAttestedSatisfier(ctx, principal, request, deps, attestation, candidatesBySubject, observationParentKey, observationBlocked, identity, identityTerms); merged {
				// CHAOS-4085 commit-basis write site 3 of 3: the
				// evidence-census RE-DECISION. This call re-runs the entire
				// commit decision and returns a wholly fresh resolution, so
				// the basis set must be REPLACED, never merged: a merge
				// would leave a basis recorded for a subject the first pass
				// committed and this pass does not, and a stale proven
				// basis attached to a subject nothing committed is exactly
				// the failure mode this vocabulary exists to prevent.
				var censusBases contextfabric.CommitBasisSet
				var censusDigests contextfabric.CommitDecisionDigestSet
				resolution, censusBases, censusDigests = ResolveFromMergedCandidatesWithGateAndBasis(candidatesBySubject, observationParentKey, observationBlocked, request.Options.MaxSubjectCandidates, request.Options.AllowClarification, searchTruncated, vectorArmSimilarity, deps.VectorMarginCommitThreshold, retrievalDegraded, effectiveSearchLimit, deps.CalibratedTopK, unscopedVisibility, gate, identity, identityTerms, aliasIdentityComplete, deps.ResolutionTracer, request.RequestID, attestedKey, false)
				commitBases.ResetTo(censusBases)
				commitDigests.ResetTo(censusDigests)
				resolution.RetrievalDegraded = retrievalDegraded || coverageFloorDegraded
			}
		}
		// CHAOS-3896 Slice B: presentation only -- SurvivorsFirstOrder's own
		// doc comment is the contract (same membership, same length, order
		// only). Rebuilding ClarificationPrompt from the SAME (now
		// reordered) candidates preserves the existing "prompt built from
		// the retained candidate set" invariant (codex round-4 finding 1,
		// resolution.go) -- the prompt and resolution.Candidates never
		// diverge, only their SHARED order changes. Runs regardless of
		// whether Slice C just committed: SurvivorsFirstOrder never touches
		// resolution.Committed/Status (its own doc comment), so reordering
		// the candidate list around an already-committed subject is
		// harmless and keeps this call site's shape unconditional.
		resolution.Candidates = SurvivorsFirstOrder(resolution.Candidates, attestation, deps.ResolutionTracer, request.RequestID)
		if resolution.ClarificationPrompt != "" {
			resolution.ClarificationPrompt = ClarificationPrompt(resolution.Candidates)
		}
	}
	// CHAOS-3900 P1.C/P1.C': kindOfferMaterial is derived from the SAME
	// final candidate pool the resolution above committed to, after Slice
	// B's reordering (order does not affect its own distinct-kind
	// computation, but using the SAME resolution.Candidates the caller
	// sees keeps this call visibly tied to what was actually resolved, not
	// an earlier intermediate pool). anchorOfferMaterial/handleOfferMaterial
	// (P1.C', team-lead ruling) instead use aliasClaimantsByTerm/
	// aliasIdentityComplete/request.Question -- data computed UNCONDITIONALLY
	// above, before the gated shadow-evidence-round check, so a stall that
	// skips that round still gets a real StructureNeeds block (see this
	// file's own package doc comment, chaos3900_structure_offers.go).
	// CHAOS-3972 P3: kindOfferMaterial/handleOfferMaterial additionally take
	// request.ExpectedKinds/SubjectHandles -- the caller's own explicit
	// structure fields -- so a grammar/registry-valid explicit value
	// becomes a top-ranked, receipt-bound offer on THIS SAME response
	// (design brief §2.3's deterministic one-turn upgrade), merged with
	// whatever the pool/question text itself would have offered.
	// anchorOfferMaterial builds CALLER-VISIBLE offer material: every
	// candidate/option it computes must see only what principal is
	// authorized to see (CHAOS-4042 auth-gap closure), never the raw
	// aliasClaimantsByTerm truth BindAnchor and the shadow evidence round
	// above already consumed unfiltered. The raw map is ALSO passed
	// through (never used for option content) so anchorOfferMaterial can
	// detect and suppress a mixed-visibility ambiguous term -- see its own
	// doc comment.
	// CHAOS-4038 (codex review finding 1) / CHAOS-4012 (codex xhigh R2
	// review, 2026-08-23): kindOfferMaterial/candidateOfferMaterial's shared
	// input is resolution.Candidates UNIONED with coverageCandidates, never
	// resolution.Candidates alone -- a coverage-floor find that
	// ResolveFromMergedCandidatesWithGate's own final ranked-set truncation
	// dropped must still reach the offer, or this whole pass is silently
	// defeated for exactly the resolutions it exists to help. See
	// unionCandidatesForOffer's own doc comment (chaos3900_structure_
	// offers.go) for why this union must dedupe by subject, not merely
	// concatenate.
	kindOfferCandidates := unionCandidatesForOffer(resolution.Candidates, coverageCandidates)
	// CHAOS-4234: a coverage-floor find the final cut dropped still reaches
	// the offer builders through the union above -- emit its own
	// "ranked_cut" companion (CoverageBypass=true, Rank 0) so a reader of
	// resolution.go's per-candidate ranked_cut batch can compute "at the
	// offer boundary" as Survived||CoverageBypass without re-deriving
	// this union. See ResolutionTraceEvent.CoverageBypass' doc comment.
	if deps.ResolutionTracer != nil && len(coverageCandidates) > 0 {
		visible := make(map[string]bool, len(resolution.Candidates))
		for _, candidate := range resolution.Candidates {
			visible[SubjectKey(candidate.Subject)] = true
		}
		for _, candidate := range coverageCandidates {
			if visible[SubjectKey(candidate.Subject)] {
				continue
			}
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{RequestID: request.RequestID, Stage: "ranked_cut", Subject: candidate.Subject, CoverageBypass: true})
		}
	}
	// CHAOS-4183 phase 3 (sol design consult, team-lead ratified
	// 2026-08-23): projectKindOfferKinds' own POST-DECISION, KIND-ONLY
	// boundary completion for Shape A -- see its own doc comment for the
	// full mechanism and what it deliberately does NOT do.
	// candidatesBySubject is the FULL, pre-truncation merged pool (this
	// SAME function's own local map, built up throughout Search/
	// SearchQuestion/AliasLookup/the coverage floor above) -- a kind can
	// have real representation there and still never reach
	// kindOfferCandidates once ResolveFromMergedCandidatesWithGateAndBasis's
	// own MaxSubjectCandidates ranking/truncation has run.
	beforeKinds, afterKinds := projectKindOfferKinds(kindOfferCandidates, candidatesBySubject, len(resolution.Committed))
	// beforeOffer is discarded -- only beforeDiag's counts are kept, as the
	// PRE-repair telemetry twin below. Calling kindOfferMaterial twice
	// (once per kind list) keeps both diagnostics computed by the
	// IDENTICAL cardinality-check logic, rather than duplicating that
	// check by hand and risking the two readings drifting apart.
	_, beforeDiag := kindOfferMaterial(beforeKinds, request.ExpectedKinds)
	kindOffer, kindOfferDiag := kindOfferMaterial(afterKinds, request.ExpectedKinds)
	// CHAOS-4012: the SAME union kindOfferMaterial read (pre-repair) above
	// is also the read-only pool candidateOfferMaterial ranks over -- one
	// candidate pool, two independent offer axes, never two different
	// views of it. Deliberately kindOfferCandidates, NOT afterKinds/
	// projectKindOfferKinds' own output: phase 3 is a kind-identity-only
	// projection for the expected_kind axis alone -- the candidate-list
	// axis's own top-five ranking is completely untouched by this phase
	// (sol design consult's own explicit scope boundary).
	candidateOffer, candidateOfferDiag := candidateOfferMaterial(kindOfferCandidates, len(resolution.Committed))
	// CHAOS-4119: handleOfferMaterial now ALSO reads kindOfferCandidates --
	// the SAME final pool kindOfferMaterial/candidateOfferMaterial already
	// read (design brief precedent: "derived from the SAME final candidate
	// pool the resolution above committed to") -- so a ticket key/PR#/
	// CI-run# the resolution already found can be offered even when it was
	// never literally typed in the question. Computed here, before the
	// tracer block below, so its own diagnostics can ride the SAME
	// unconditional kind_offer event candidateOfferDiag already does.
	handleOffer, handleOfferDiag := handleOfferMaterial(request.Question, request.SubjectHandles, deps.HandleGrammarChecker, kindOfferCandidates)
	// CHAOS-4012 v22: offerKind/candidateOfferCount ride the SAME
	// unconditional "kind_offer" stage kind_offer's own fields already use
	// (team-lead ruling: same-change telemetry) -- offerKind is "kind",
	// "candidate", "both", or "" (closed vocabulary), so a reader can tell
	// which axis (or both, or neither) actually fired without cross-
	// referencing KindOfferSuppressedByCardinality and
	// CandidateOfferCount by hand.
	offerKind := ""
	switch {
	case !kindOfferDiag.SuppressedByCardinality && candidateOfferDiag.OfferKind == "candidate":
		offerKind = "both"
	case !kindOfferDiag.SuppressedByCardinality:
		offerKind = "kind"
	case candidateOfferDiag.OfferKind == "candidate":
		offerKind = "candidate"
	}
	// UNCONDITIONAL, unlike kind_coverage_floor/confirmed_kind_rescue above
	// -- kindOfferMaterial/candidateOfferMaterial both run on EVERY
	// resolution, not gated behind a "still missing" precondition, so this
	// stage fires every time a tracer is wired, corpus-wide. See
	// ResolutionTraceEvent's own "kind_offer" field doc comment.
	if deps.ResolutionTracer != nil {
		// boundaryKindsPostRepair (CHAOS-4183 phase 3): see
		// KindOfferBoundaryKinds' own doc comment for the full mechanism.
		// codex CHAOS-4183 phase-3 review round 2, finding 1 (LOW): an
		// earlier version unconditionally seeded this with append([]string{},
		// ...), so an empty pre-repair reading (distinctCandidateKinds
		// returns nil for an empty kindOfferCandidates) became a non-nil
		// []string{} whenever nothing was repaired -- observable as JSON
		// `[]` where the pre-phase-3 field always serialized `null`. Fixed:
		// only allocate/append when the repaired tail is genuinely
		// non-empty; otherwise this is distinctCandidateKinds' own return
		// value verbatim, nil included.
		boundaryKindsPostRepair := distinctCandidateKinds(kindOfferCandidates)
		if repairedTail := subjectKindStrings(afterKinds[len(beforeKinds):]); len(repairedTail) > 0 {
			boundaryKindsPostRepair = append(append([]string{}, boundaryKindsPostRepair...), repairedTail...)
		}
		deps.ResolutionTracer.Trace(ResolutionTraceEvent{
			RequestID: request.RequestID, Stage: "kind_offer",
			KindOfferExplicitHintCount:                   kindOfferDiag.ExplicitHintCount,
			KindOfferDistinctKindCount:                   kindOfferDiag.DistinctKindCount,
			KindOfferSuppressedByCardinality:             kindOfferDiag.SuppressedByCardinality,
			KindOfferCandidateOfferCount:                 candidateOfferDiag.CandidateOfferCount,
			KindOfferOfferKind:                           offerKind,
			KindOfferCandidateOfferLabelsNormalizedCount: candidateOfferDiag.LabelsNormalizedCount,
			OfferedUnderWindowGate:                       offersOnly,
			// CHAOS-4012 v22 (team-lead ruling, re-smoke follow-up): computed
			// only when a tracer is actually wired -- this is telemetry-only,
			// never consulted by kindOfferMaterial/candidateOfferMaterial
			// themselves, so it must not cost anything on the hot path when
			// no one is listening. See KindOfferBoundaryKinds' own doc
			// comment for what this distinguishes.
			//
			// CHAOS-4183 phase 3: KindOfferBoundaryKinds is now POST-repair --
			// BoundaryKindsBeforeRepair keeps the field's own pre-phase-3
			// computation verbatim (distinctCandidateKinds(kindOfferCandidates),
			// UNFILTERED). DistinctKindCountBeforeRepair/
			// SuppressedByCardinalityBeforeRepair come from beforeDiag above --
			// the SAME cardinality check kindOfferDiag itself uses, run over
			// the un-repaired input.
			//
			// codex CHAOS-4183 phase-3 review round 1, finding 1 (MEDIUM):
			// subjectKindStrings(afterKinds) alone is WRONG here -- afterKinds
			// is projectKindOfferKinds' own kindOfferMaterial-feed value,
			// filtered to structureOfferKinds (distinctOfferableKinds' own
			// contract), so a committed/no-repair-needed resolution would
			// report FEWER kinds than the pre-phase-3 field ever did (e.g. a
			// "document" kind silently dropped), breaking "committed
			// resolutions get the pre-repair boundary unchanged." The fix:
			// start from the SAME unfiltered distinctCandidateKinds reading
			// BoundaryKindsBeforeRepair uses, and append ONLY the tail
			// projectKindOfferKinds genuinely added beyond beforeKinds --
			// afterKinds always starts with beforeKinds verbatim (see that
			// function's own "after = append(after, before...)"), so the tail
			// is exactly the repaired kind identities, always disjoint from
			// the unfiltered list (a repaired kind is, by construction, absent
			// from beforeKinds' offerable subset, and beforeKinds' offerable
			// subset is exactly the offerable portion of the unfiltered
			// list). Committed or nothing-absent: this reduces to the
			// unfiltered list verbatim, byte-identical to pre-phase-3.
			KindOfferBoundaryKinds:                       boundaryKindsPostRepair,
			KindOfferBoundaryKindsBeforeRepair:           distinctCandidateKinds(kindOfferCandidates),
			KindOfferDistinctKindCountBeforeRepair:       beforeDiag.DistinctKindCount,
			KindOfferSuppressedByCardinalityBeforeRepair: beforeDiag.SuppressedByCardinality,
			HandleOfferCountBeforeGraphSource:            handleOfferDiag.CountBeforeGraphSource,
			HandleOfferGraphDerivedCount:                 handleOfferDiag.GraphDerivedCount,
			HandleOfferGraphDerivedRejectedCount:         handleOfferDiag.GraphDerivedRejectedCount,
		})
	}
	anchorOffer, anchorOfferDiag := anchorOfferMaterial(claimantsFromCandidateNodes(aliasClaimantsByTerm), claimantsFromCandidateNodes(authorizedClaimantNodes(principal, request.RequestedScope, aliasClaimantsByTerm)), aliasIdentityComplete, deps.AnchorMembershipOffersEnabled)
	// CHAOS-4210: unconditional, mirroring kind_offer's own "fires every
	// time a tracer is wired" discipline -- anchorOfferMaterial runs on
	// every resolution, not gated behind a "still missing" precondition.
	if deps.ResolutionTracer != nil {
		deps.ResolutionTracer.Trace(ResolutionTraceEvent{
			RequestID: request.RequestID, Stage: "anchor_offer",
			AnchorOfferLabelsNormalizedCount: anchorOfferDiag.LabelsNormalizedCount,
		})
	}
	offerMaterial := combineStructureOfferMaterial(
		kindOffer,
		candidateOffer,
		anchorOffer,
		handleOffer,
	)
	return resolution, offerMaterial, nil
}

// mergeCensusAttestedSatisfier implements design brief v6 §1.4's commit
// precondition: "censusComplete && |satisfiers| == 1 names one source row
// S. Committing requires S as a GRAPH node (payload, authorization)." ok is
// true only when a keyed, single-node graph read found S and NodeCandidate
// accepted it (authorized, valid, non-internal) -- the returned key is then
// SubjectKey(S) and candidatesBySubject already contains it, merged through
// the SAME mergeSearchResults path every ordinary search result uses (so a
// satisfier the search ALSO already found gets its mechanisms unioned via
// MergeCandidates rather than overwritten; one the search never returned at
// all is added fresh -- brief: "A satisfier the truncated search never
// returned is merged as an ordinary candidate from its graph node
// (NodeCandidate -> MergeCandidates) -- the round still RECOVERS
// truncated-away referents").
//
// censusCommitErrorReason (CHAOS-3896 Slice C, codex xhigh review finding,
// confirmed) is mergeCensusAttestedSatisfier's OWN CensusCommitReason value
// for a genuine deps.ExactHint backend fault -- deliberately DISTINCT from
// ReasonGraphMissingSatisfier (design brief §4's closed vocabulary, which
// names a CONFIRMED absence: "census named one satisfier, keyed graph read
// found no node"). A transient backend error proves nothing about whether
// the node exists; conflating the two made a production graph outage or
// deadline operationally indistinguishable, in the trace, from a genuine
// missing satisfier -- an operator triaging a spike in refused commits
// could not tell "the source of record disagrees with the census" from
// "the graph was unreachable". This is scoped to THIS trace field only
// (not added to graphrank.DegradationReason, the round's own §4 vocabulary
// RunShadowEvidenceRound produces -- that enum describes why the CENSUS
// ROUND itself degraded, not this separate, Slice-C-only commit-attempt
// event). The fail-closed BEHAVIOR is unchanged either way -- see this
// function's own doc comment for why a backend error still never commits.
const censusCommitErrorReason = "census_commit_error"

// ok is false, fail-closed, for every other outcome -- each traced as a
// dedicated evidence_census_commit event (never silent):
//   - the round did not name exactly one bridged satisfier (attestedSatisfier
//     itself untraced here -- it is a pure read of data RunShadowEvidenceRound
//     already traced via its own evidence_round/evidence_probe events);
//   - the keyed graph read (deps.ExactHint) found no node -- projection lag
//     or a tombstone, the CHAOS-3884 graphMissing precedent (R2: "the
//     phantom-commit scenario... is unconstructible: no graph census
//     exists, and the one graph read fails closed on absence"), reason
//     graph_missing_satisfier;
//   - deps.ExactHint itself errored -- reason censusCommitErrorReason
//     (above), NOT graph_missing_satisfier: this is "could not confirm",
//     not "confirmed absent";
//   - the node exists but is not authorized/valid/non-internal for this
//     principal+scope -- "unauthorized -> no commit (and no oracle:
//     unscopedVisibility already holds on this path)", brief §1.4. No
//     dedicated reason token exists for this case (§4's vocabulary has
//     none), so CensusCommitReason stays empty; GraphExistenceOK=true
//     alone distinguishes it from the two absence/error cases above.
func mergeCensusAttestedSatisfier(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, attestation Attestation, candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, identity identityClaimants, identityTerms identityMatchTerms) (string, bool) {
	kind, canonicalID, found := attestedSatisfier(attestation)
	if !found {
		return "", false
	}
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: canonicalID}
	// graphReadCtx (codex xhigh review finding, confirmed): the keyed
	// graph existence read gets its OWN bound from the SAME
	// evidenceRoundDeadline the census round itself already uses
	// (runShadowEvidenceRoundForResolution) -- design brief D4's round
	// budget ("...+ 3s wall") otherwise covered only the census read, not
	// this SECOND piece of I/O the round's own outcome now triggers. A
	// slow/hanging ExactHint can therefore still not extend a real
	// request's latency past this function's own bounded window, mirroring
	// evidenceRoundDeadline's own doc comment exactly.
	graphReadCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	// A genuine backend fault (err != nil) is deliberately folded into the
	// SAME fail-closed "refuse to commit" outcome as a confirmed absence
	// (exists == false), not propagated as a hard ResolveSubjects error:
	// this whole call is an ADDITIVE rescue attempt for a resolution the
	// ordinary gates already left merely ambiguous -- a transient hiccup on
	// the rescue's OWN extra I/O must not turn a legitimate "ambiguous,
	// please clarify" outcome into a 500-class failure. This mirrors
	// runShadowEvidenceRoundForResolution's own recover()-and-fall-back
	// discipline immediately above this call site, extended to the
	// synchronous error return this function (unlike a panic) can observe
	// directly. Every top-of-ResolveSubjects ExactHint call remains
	// unchanged and still propagates its own error hard -- that path serves
	// a caller-EXPLICIT hint, where silently discarding a real backend fault
	// would hide it from the very request that asked for it; this path
	// serves an INTERNAL rescue the caller never asked for by name. The
	// trace reason still distinguishes the two cases (censusCommitErrorReason
	// above), even though the fail-closed commit behavior does not.
	node, exists, err := deps.ExactHint(graphReadCtx, subject)
	if err != nil || !exists {
		reason := string(ReasonGraphMissingSatisfier)
		if err != nil {
			reason = censusCommitErrorReason
		}
		if deps.ResolutionTracer != nil {
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID: request.RequestID, Stage: "evidence_census_commit",
				Subject: subject, Outcome: "refused", GraphExistenceOK: false,
				CensusCommitReason: reason,
			})
		}
		return "", false
	}
	// accepted (codex xhigh review finding, confirmed): mirrors
	// NodeCandidate's own gating conditions (candidate.go) EXACTLY, so this
	// function knows -- BEFORE calling mergeSearchResults -- whether the
	// census-derived node itself will actually be accepted, rather than
	// inferring success from candidatesBySubject[key]'s mere presence
	// afterward. That inference was wrong whenever the subject was ALREADY
	// in the pool from ordinary search (the common case: census usually
	// names an already-pooled, merely-ambiguous candidate) -- the key would
	// stay present even if THIS node's own authorization/validity check
	// failed, silently reporting "merged" for a graph read that brief §1.4
	// requires to refuse ("unauthorized -> no commit"). Duplicated here
	// rather than threading a new return value through mergeSearchResults
	// (shared by three other call sites with a different, established
	// contract) -- see NodeCandidate's own doc comment for the exact
	// conditions being mirrored.
	accepted := AuthorizedAttributes(principal, request.RequestedScope, node.Attributes)
	if accepted {
		if nodeSubject, ok := NodeSubject(node); !ok || deps.IsInternal(nodeSubject) {
			accepted = false
		}
	}
	if !accepted {
		if deps.ResolutionTracer != nil {
			deps.ResolutionTracer.Trace(ResolutionTraceEvent{
				RequestID: request.RequestID, Stage: "evidence_census_commit",
				Subject: subject, Outcome: "refused", GraphExistenceOK: true,
			})
		}
		return "", false
	}
	// censusProvenanceMarker: a synthetic, non-caller-typed provenance
	// label -- allowExactMatch=false for the SAME reason
	// questionProvenanceMarker's own call site documents (must never win an
	// exact-match promotion just because some subject happens to share the
	// literal string). vectorArmSimilarity=nil: a census witness is not a
	// vector search and has nothing to contribute to the CHAOS-3829
	// carve-out's margin, the SAME exclusion the AliasLookup merge above
	// already documents. The accepted check above GUARANTEES NodeCandidate
	// (called again, internally, by mergeSearchResults) reaches the exact
	// same accept decision on the SAME node/principal/scope inputs, so this
	// call is never a second, independent authorization gate -- merely
	// where the actual merge/insert into candidatesBySubject happens.
	mergeSearchResults(ctx, principal, request, deps, censusProvenanceMarker, []CandidateNode{node}, candidatesBySubject, observationParentKey, observationBlocked, false, nil, identity, identityTerms)
	// codex xhigh review finding (HIGH, confirmed and fixed): a candidate
	// already sitting at exactly matchedTermsCap real terms overflows to
	// matchedTermsCap+1 once censusProvenanceMarker unions in above --
	// without this call, contractsv1.SubjectCandidate.Validate() rejected
	// the WHOLE investigation result at engine.go's result.Validate() call,
	// converting a valid census recovery into a hard validation failure.
	// See capMatchedTermsAfterMerge's own doc comment for the full account.
	capMatchedTermsAfterMerge(candidatesBySubject, censusProvenanceMarker)
	key := SubjectKey(subject)
	if deps.ResolutionTracer != nil {
		deps.ResolutionTracer.Trace(ResolutionTraceEvent{
			RequestID: request.RequestID, Stage: "evidence_census_commit",
			Subject: subject, Outcome: "merged", GraphExistenceOK: true,
		})
	}
	return key, true
}

// evidenceRoundDeadline is design brief v5 D4's own round budget ("...+ 3s
// wall"). runShadowEvidenceRoundForResolution derives its own
// context.WithTimeout from it (adversarial review finding: the round had
// no deadline of its own and could run un-bounded on the caller's
// context), so a slow/hanging CensusFunc can extend a real request's
// latency by at most this much, never indefinitely.
const evidenceRoundDeadline = 3 * time.Second

// runShadowEvidenceRoundForResolution assembles ShadowEvidenceRoundInput
// from values ResolveSubjects already computed (CHAOS-3899) and runs the
// shadow round. RunShadowEvidenceRound's own emit closure is still the ONLY
// place the round's content reaches anything OBSERVABLE
// (deps.ResolutionTracer), and a nil tracer makes even that a no-op --
// unchanged from Slice A. CHAOS-3896 Slice B changes what happens to the
// RETURN value: previously discarded ("shadow" -- traced, never consumed),
// now returned to the caller for SurvivorsFirstOrder's own presentation-only
// reorder. This function's own recover()'s fallback (a panic here yields the
// zero Attestation, empty Kinds) is deliberately still SAFE for that new
// consumer: SurvivorsFirstOrder treats a kind with no KindAttestation entry
// as verdictNeutral, so a recovered panic can only ever suppress reordering,
// never cause a wrong one.
//
// TWO hardening properties (adversarial review findings), both required
// for "zero production behavior change" to be STRUCTURAL rather than
// merely "true while deps.CensusFunc happens to be nil":
//  1. A bounded sub-context (evidenceRoundDeadline) so a slow/hanging
//     CensusFunc cannot extend the caller's own request past the brief's
//     own 3s round budget.
//  2. A deferred recover(): a panic inside a caller-supplied CensusFunc
//     (or anywhere in this purely-observational path) must never escape
//     into a real production request. Recovered silently into a
//     probe_error trace event when a tracer is wired, rather than
//     re-panicking or logging through a new dependency this ticket would
//     otherwise have to add.
//
// interpreted.TimeContext.Axis (not request.TimeContext.Axis -- adversarial
// review finding) is the axis the ENGINE treats as authoritative for what
// an answer speaks for (engine.go's own clamp runs against the
// interpretation, temporal.go's effectiveTimeContext reads
// interpretation.TimeContext) -- reading the raw, pre-clamp request value
// here would let a historical question submitted with the request's own
// (possibly current) context skip D7's historical-axis-skip refusal and
// run a current-state census against a historical question, silently on
// the wrong axis.
func runShadowEvidenceRoundForResolution(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, interpreted contextfabric.InterpretedQuestion, resolution contextfabric.SubjectResolution, aliasClaimantsByTerm map[string][]CandidateNode, aliasIdentityComplete bool, unscopedVisibility bool, deps ResolveDeps, confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection) (attestation Attestation) {
	defer func() {
		if r := recover(); r != nil {
			if deps.ResolutionTracer != nil {
				deps.ResolutionTracer.Trace(ResolutionTraceEvent{
					RequestID: request.RequestID, Stage: "evidence_round",
					ShadowOutcome: string(ShadowWouldClarify), ShadowReason: string(ReasonProbeError),
				})
			}
			// attestation stays its zero value -- see this function's own
			// doc comment on why that is a safe fallback for
			// SurvivorsFirstOrder specifically.
			attestation = Attestation{}
		}
	}()
	roundCtx, cancel := context.WithTimeout(ctx, evidenceRoundDeadline)
	defer cancel()
	pooledKinds := make([]CensusKind, 0, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		pooledKinds = append(pooledKinds, candidate.Subject.Kind)
	}
	// CHAOS-3972 P3 (design brief §2.0/§2.3, the P1.D hard precondition):
	// an EXPLICIT (non-receipt) request.ExpectedKinds narrows the census's
	// own pooled hypothesis set -- never candidatesBySubject/pooledKinds'
	// own SOURCE (that stays receipt-only, ConfirmedExpectedKind's own
	// tripwire). confirmedKind != nil means a kindr_ receipt ALREADY
	// narrowed candidatesBySubject upstream (filterCandidatesByConfirmedKind)
	// -- caller authority, no insensitivity proof needed, so explicit
	// narrowing is skipped entirely in that case (it would be redundant at
	// best; resolveExplicitStructure's own conflict check already proved
	// agreement when both are present).
	//
	// CHAOS-4079: the SAME classification additionally reports WHICH of
	// the no-narrowing cases a hint-carrying request landed in, so the
	// shadow kind-insensitivity probe can be OBSERVED (write-free) for a
	// hint that applied no narrowing -- previously indistinguishable from
	// "no hint at all", which made the probe unreachable by construction
	// for exactly the deliberately-wrong hint the trial's inferred-tier
	// arm injects. The narrowing behavior itself is UNCHANGED: only an
	// explicitKindNarrowingApplied mode alters censusKinds or populates
	// preNarrowingExplicitKinds, exactly as before.
	censusKinds := pooledKinds
	var preNarrowingExplicitKinds []CensusKind
	var narrowingMode explicitKindNarrowingMode
	if confirmedKind == nil {
		var narrowed []CensusKind
		narrowed, narrowingMode = classifyExplicitKindNarrowing(pooledKinds, request.ExpectedKinds)
		if narrowed != nil {
			preNarrowingExplicitKinds = pooledKinds
			censusKinds = narrowed
		}
	}
	// CHAOS-4042 (sol-max ruling): confirmedAnchor threads a redeemed ancr_
	// receipt's own resolved claimant into the round -- see
	// ShadowEvidenceRoundInput.ConfirmedAnchor's own doc comment for why
	// it takes priority over BindAnchor's own question-derived scan.
	var confirmedAnchorInput *AnchorBinding
	if confirmedAnchor != nil {
		confirmedAnchorInput = &AnchorBinding{Kind: confirmedAnchor.Kind, CanonicalID: confirmedAnchor.CanonicalID}
	}
	attestation = RunShadowEvidenceRound(roundCtx, ShadowEvidenceRoundInput{
		RequestID: request.RequestID, Question: request.Question, OrgID: principal.OrgID,
		PooledKinds: censusKinds, CurrentAxis: interpreted.TimeContext.Axis == contextfabric.TemporalCurrent,
		UnscopedVisibility: unscopedVisibility, AliasClaimants: claimantsFromCandidateNodes(aliasClaimantsByTerm),
		AliasLookupComplete: aliasIdentityComplete, CensusFunc: deps.CensusFunc,
		PreNarrowingExplicitKinds: preNarrowingExplicitKinds,
		ConfirmedAnchor:           confirmedAnchorInput,
		// Mutually exclusive with a non-empty PreNarrowingExplicitKinds:
		// classifyExplicitKindNarrowing returns a non-nil narrowed set
		// ONLY for explicitKindNarrowingApplied, and these two modes are
		// its other, nil-returning hint-present cases.
		ObservedExplicitKindHint:     narrowingMode == explicitKindNarrowingNoOverlap || narrowingMode == explicitKindNarrowingSubsumed,
		ObservedExplicitKindSubsumed: narrowingMode == explicitKindNarrowingSubsumed,
	}, deps.ResolutionTracer)
	return attestation
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
//
// Returns (traversalErrored, authzDropped): authzDropped (CHAOS-3888) is the
// count of results this call excluded specifically because
// AuthorizedAttributes denied them -- checked independently of
// NodeCandidate's own !ok result, which also fires for an invalid or
// internal-bookkeeping node, neither an authorization event. Folds into
// ResolveSubjects' own subjectCandidatesAuthzDropped aggregate exactly like
// traversalErrored folds into its traversalDegraded aggregate.
func mergeSearchResults(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, term string, results []CandidateNode, candidatesBySubject map[string]contextfabric.SubjectCandidate, observationParentKey map[string]string, observationBlocked map[string]bool, allowExactMatch bool, vectorArmSimilarity map[string]float64, identity identityClaimants, identityTerms identityMatchTerms) (int, int) {
	traversalErrored := 0
	authzDropped := 0
	for _, node := range results {
		if vectorArmSimilarity != nil && node.Mechanism == contextfabric.MatchVector && node.VectorSimilarity != nil {
			if subject, ok := NodeSubject(node); ok {
				key := SubjectKey(subject)
				if existing, exists := vectorArmSimilarity[key]; !exists || *node.VectorSimilarity > existing {
					vectorArmSimilarity[key] = *node.VectorSimilarity
				}
			}
		}
		nodeAuthorized := AuthorizedAttributes(principal, request.RequestedScope, node.Attributes)
		candidate, ok := NodeCandidate(principal, request.RequestedScope, term, node, deps.IsInternal, allowExactMatch, deps.ResolutionTracer, request.RequestID)
		if !ok {
			if !nodeAuthorized {
				authzDropped++
			}
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
	return traversalErrored, authzDropped
}
