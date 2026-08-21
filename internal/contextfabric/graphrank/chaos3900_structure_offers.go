package graphrank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3900 P1.C (pivot-intent design brief §2.1). ResolveSubjects
// (resolve.go) calls kindOfferMaterial/anchorOfferMaterial/handleOfferMaterial
// with the SAME pool/identity/question-text data it already assembled,
// right before it returns -- this file owns turning that data into the
// StructureOfferMaterial the engine carries back.
//
// P1.C' (team-lead ruling): anchorOfferMaterial/handleOfferMaterial are
// built from data computed UNCONDITIONALLY, before the gated
// shadow-evidence-round check (deps.CensusFunc != nil &&
// len(resolution.Committed) == 0 && searchTruncated) -- aliasClaimantsByTerm/
// aliasIdentityComplete (AliasLookup's own read) and request.Question are
// both already in scope at ResolveSubjects' own return point regardless of
// whether the shadow round fires. This is what satisfies the
// disclosure-not-gated-on-shadow-round requirement: a stalled resolution
// that never triggers the census still gets a real (possibly empty)
// StructureNeeds block, never an absent one.

// structureOfferMaxOptions bounds AnchorOptions/HandleOptions before they
// ever reach composeStructureNeeds -- mirrors
// internal/contracts/v1's own unexported contextFabricStructureNeedsMaxOptions
// (the wire Validate() bound every offer list is checked against). Codex
// xhigh review (chaos-pivot-p1, round 2, finding 1): neither builder
// capped its own output before this fix -- an ambiguous term matching many
// distinct identity-universe entities, or a question mentioning more than
// this many handle-shaped tokens, could mint an offer list the wire
// contract itself would then reject, turning what should be a graceful
// clarification disclosure into an internal validation error. Truncating
// to the FIRST entries in each builder's own already-deterministic order
// is a conservative, always-safe choice: fewer offered options, never a
// validation failure.
const structureOfferMaxOptions = 20

// structureOfferKinds is the closed set of subject kinds an expected_kind
// offer may name (design brief §1.1's expected_kind row: "the census-kind
// registry... + the identity-scoped kinds"). A superset of
// censusKindRegistry -- repository/project/team are identity-scoped, not
// census kinds (they never run a census; IsCensusKindRegistered would
// wrongly exclude them), but the SAME expected_kind member still covers
// disambiguating "is this about a repository or a project" the way it
// covers "is this a PR or a work item".
var structureOfferKinds = map[contractsv1.ContextFabricSubjectKind]bool{
	contractsv1.ContextFabricSubjectPullRequest:       true,
	contractsv1.ContextFabricSubjectWorkItem:          true,
	contractsv1.ContextFabricSubjectCIRun:             true,
	contractsv1.ContextFabricSubjectPullRequestReview: true,
	contractsv1.ContextFabricSubjectRepository:        true,
	contractsv1.ContextFabricSubjectProject:           true,
	contractsv1.ContextFabricSubjectTeam:              true,
}

// kindOfferMaterial builds the expected_kind disclosure from the pool of
// SubjectCandidate a resolution already assembled (design brief §1.2
// reading 1: "kind disambiguation is the cheapest, highest-leverage
// elicitation... a closed enum a human can pick in one tap"), PLUS
// (CHAOS-3972 P3) any caller-supplied explicitKinds
// (contextfabric.InvestigationRequest.ExpectedKinds) -- ranked FIRST
// (design brief §2.3: "an explicit value the engine can verify becomes a
// top-ranked, receipt-bound offer"), pool-derived kinds after, deduped.
// Offered when EITHER the pool spans more than one distinct offerable
// kind OR the caller named at least one explicit kind -- a single-kind
// (or empty) pool with NO explicit hint has nothing to disambiguate on
// this axis, so offering it would disclose a choice the question does not
// actually present; a caller-named kind is always worth offering back
// (it may be wrong, and the receipt-bound offer is exactly how a caller
// finds out).
func kindOfferMaterial(candidates []contextfabric.SubjectCandidate, explicitKinds []contractsv1.ContextFabricSubjectKind) contextfabric.StructureOfferMaterial {
	seen := make(map[contractsv1.ContextFabricSubjectKind]bool, len(candidates)+len(explicitKinds))
	var ranked []contractsv1.ContextFabricSubjectKind
	for _, kind := range explicitKinds {
		if seen[kind] || !structureOfferKinds[kind] {
			continue
		}
		seen[kind] = true
		ranked = append(ranked, kind)
	}
	explicitCount := len(ranked)
	var poolDistinct []contractsv1.ContextFabricSubjectKind
	for _, candidate := range candidates {
		kind := candidate.Subject.Kind
		if seen[kind] || !structureOfferKinds[kind] {
			continue
		}
		seen[kind] = true
		poolDistinct = append(poolDistinct, kind)
	}
	ranked = append(ranked, poolDistinct...)
	// The pool alone still needs >=2 DISTINCT kinds (its own kinds plus
	// whatever explicit kinds already claimed) to be worth disambiguating
	// on its own; an explicit kind is ALWAYS worth offering regardless of
	// pool cardinality.
	if explicitCount == 0 && len(ranked) < 2 {
		return contextfabric.StructureOfferMaterial{}
	}
	options := make([]contractsv1.ContextFabricKindOption, 0, len(ranked))
	for _, kind := range ranked {
		options = append(options, contractsv1.ContextFabricKindOption{
			Kind:        kind,
			Label:       kindOfferLabel(kind),
			OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		})
	}
	return contextfabric.StructureOfferMaterial{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: options,
	}
}

// kindOfferLabel renders a server-owned, closed-vocabulary label for one
// kind offer -- mirrors WindowOption's own server-rendered label
// discipline (CHAOS-3900 W1): never model- or source-derived prose, a
// fixed sentence per closed-enum member.
func kindOfferLabel(kind contractsv1.ContextFabricSubjectKind) string {
	switch kind {
	case contractsv1.ContextFabricSubjectPullRequest:
		return "a pull request"
	case contractsv1.ContextFabricSubjectPullRequestReview:
		return "a pull request review"
	case contractsv1.ContextFabricSubjectCIRun:
		return "a CI pipeline run"
	case contractsv1.ContextFabricSubjectWorkItem:
		return "a work item"
	case contractsv1.ContextFabricSubjectRepository:
		return "a repository"
	case contractsv1.ContextFabricSubjectProject:
		return "a project"
	case contractsv1.ContextFabricSubjectTeam:
		return "a team"
	default:
		// Unreachable given structureOfferKinds' own closed membership --
		// kindOfferMaterial never calls this with a kind outside that map.
		return string(kind)
	}
}

// filterCandidatesByConfirmedKind (CHAOS-3900 P1.D) narrows
// candidatesBySubject to ONLY the confirmed kind, when one is present --
// design brief §2.1: "the confirmed kind becomes the census scope (drops
// non-confirmed kinds from the hypothesis set)."
//
// A nil confirmed returns candidatesBySubject UNCHANGED -- the overwhelming
// common case (no kindr_ receipt confirmed), and what keeps an ordinary
// request's pool composition provably byte-identical to the pre-P1.D code
// path (TestFilterCandidatesByConfirmedKind_NilIsNoOp pins this).
//
// Deliberately typed on *contextfabric.ConfirmedExpectedKind, not a bare
// contextfabric.SubjectKind -- see that type's own doc comment
// (internal/contextfabric/ports.go) for why this is the §2.0
// kind-insensitivity rule's own enforcement mechanism, not merely a
// convenience wrapper: only canonicalizeStructure's receipt-confirmation
// path can construct one.
func filterCandidatesByConfirmedKind(candidatesBySubject map[string]contextfabric.SubjectCandidate, confirmed *contextfabric.ConfirmedExpectedKind) map[string]contextfabric.SubjectCandidate {
	if confirmed == nil {
		return candidatesBySubject
	}
	filtered := make(map[string]contextfabric.SubjectCandidate, len(candidatesBySubject))
	for key, candidate := range candidatesBySubject {
		if candidate.Subject.Kind == confirmed.Kind {
			filtered[key] = candidate
		}
	}
	return filtered
}

// narrowPooledKindsByExplicitKinds (CHAOS-3972 P3, design brief §2.0/§2.3)
// intersects pooled with explicitKinds, order-preserving over pooled.
// Returns nil (meaning "no narrowing applied") when explicitKinds is
// empty, when NONE of pooled's kinds survive the intersection (an
// explicit hint that disagrees with the whole pool is not treated as
// authoritative enough to force a no-hypothesis round -- the ordinary
// pooled set stays in force, and the mismatch is simply not a narrowing
// event), or when EVERY pooled kind survives (intersecting changed
// nothing, so there is nothing to prove insensitive against). Callers
// pass a non-nil result as ShadowEvidenceRoundInput.PooledKinds AND the
// UNnarrowed pooled slice as PreNarrowingExplicitKinds together -- see
// runShadowEvidenceRoundForResolution (resolve.go).
func narrowPooledKindsByExplicitKinds(pooled []CensusKind, explicitKinds []contractsv1.ContextFabricSubjectKind) []CensusKind {
	if len(explicitKinds) == 0 {
		return nil
	}
	allow := make(map[CensusKind]bool, len(explicitKinds))
	for _, kind := range explicitKinds {
		allow[CensusKind(kind)] = true
	}
	var narrowed []CensusKind
	for _, kind := range pooled {
		if allow[kind] {
			narrowed = append(narrowed, kind)
		}
	}
	if len(narrowed) == 0 || len(narrowed) == len(pooled) {
		return nil
	}
	return narrowed
}

// kindInsensitivityOutcome is the closed vocabulary CHAOS-3900 P1.D's
// insensitivity proof reports (design brief §2.0/§4's kind_sensitive_outcome
// degradation reason, split into its three concrete verdicts here).
type kindInsensitivityOutcome string

const (
	// kindInsensitivityCommitSound: the all-kinds census found EXACTLY
	// one satisfier across every pre-narrowing hypothesis kind -- a
	// decisive commit is sound regardless of which kind an inferred
	// narrowing picked.
	kindInsensitivityCommitSound kindInsensitivityOutcome = "commit_sound"
	// kindInsensitivityNoMatchSound: the all-kinds census found ZERO
	// satisfiers -- a literal no_match is sound regardless of narrowing.
	kindInsensitivityNoMatchSound kindInsensitivityOutcome = "no_match_sound"
	// kindInsensitivitySensitive: any other combination (>1 satisfier,
	// a census error, or a pre-narrowing kind outside the closed
	// registry -- the registry-miss poison rule) -- an inferred
	// narrowing's decisive outcome is NOT provably sound; the design
	// brief's own rule demotes this to clarify.
	kindInsensitivitySensitive kindInsensitivityOutcome = "kind_sensitive_outcome"
)

// kindInsensitivityProof is design brief §2.0's own all-kinds census
// proof, implementing both its stated implementation pins: (a) runs over
// preNarrowingKinds -- the PRE-narrowing hypothesis kind-set, which the
// caller must capture BEFORE any narrowing was applied, never the
// already-narrowed set; (b) a pre-narrowing kind outside the closed
// census registry poisons the round (reuses splitCensusKinds' own
// registry-miss split -- the identical primitive
// chaos3899_evidence_round.go already established for this exact shape).
//
// WIRED (CHAOS-3972 P3): consulted from RunShadowEvidenceRound's own
// decisive switch whenever a round's PooledKinds was narrowed at the
// explicit (non-receipt) tier -- see that function's own call site. P1.D's
// original scoping named wiring this into a live decisive-path gate a
// HARD PRECONDITION of introducing any inferred-tier kind source (tracked
// on CHAOS-3927 and the P3/P5 commissioning checklists) -- see
// ConfirmedExpectedKind's own doc comment (internal/contextfabric/ports.go)
// for the type-level half of this same guard.
//
// handleKind/handleValue/handleBound and anchorKind/anchorCanonicalID/
// anchorBound describe ONE round-wide discriminator D, exactly like
// RunShadowEvidenceRound's own main census loop -- and, exactly like that
// loop, a keyed predicate applies to a given censused kind ONLY when it is
// actually valid for that kind (codex xhigh review, CHAOS-3972 round 1,
// finding 1: applying handleBound/anchorBound GLOBALLY to every kind --
// the original, unreviewed version of this function -- let a handle bound
// to one kind masquerade as a valid predicate for an unrelated kind, e.g.
// querying pull_request with a ci_pipeline_run handle's own numeric value
// as if it were a PR number). handleApplies is gated by kind==handleKind;
// anchorApplies is gated by kindHasAnchorFK, the SAME per-kind anchor-FK
// registry check the main loop uses. A censused kind neither predicate
// reaches at all cannot be proven anything about -- exactly the
// NonCensusedSurvivor gap the main loop's own §3(2) rule names -- so it
// poisons the whole proof (kindInsensitivitySensitive), never silently
// skipped.
func kindInsensitivityProof(ctx context.Context, orgID string, preNarrowingKinds []CensusKind, handleKind CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool, census CensusFunc) kindInsensitivityOutcome {
	censused, nonCensusedSurvivor := splitCensusKinds(preNarrowingKinds)
	if nonCensusedSurvivor || census == nil || len(censused) == 0 {
		return kindInsensitivitySensitive
	}
	total := 0
	for _, kind := range censused {
		handleApplies := handleBound && kind == handleKind
		anchorApplies := anchorBound && kindHasAnchorFK(kind, anchorKind)
		if !handleApplies && !anchorApplies {
			// No keyed predicate reaches this pre-narrowing kind at all --
			// the proof cannot speak for a kind it cannot query, so it
			// cannot certify insensitivity across the whole set.
			return kindInsensitivitySensitive
		}
		value := ""
		if handleApplies {
			value = handleValue
		}
		outcome, err := census(ctx, orgID, kind, value, handleApplies, anchorKind, anchorCanonicalID, anchorApplies)
		if err != nil {
			return kindInsensitivitySensitive
		}
		// codex xhigh review, CHAOS-3972 round 1, finding 2: a census
		// outcome that could not PROVE closure (ClosureMismatch /
		// SatisfierSetClosureMismatch) is exactly as untrustworthy here as
		// it is everywhere else this package reads a CensusOutcome (see
		// VerifyHandle's own identical check, this file, and
		// RunShadowEvidenceRound's own `mismatch` handling) -- a bare
		// Count from an outcome that could not prove closure must never
		// feed this proof's total, in either direction (a false
		// commit_sound OR a false no_match_sound).
		if outcome.ClosureMismatch || outcome.SatisfierSetClosureMismatch {
			return kindInsensitivitySensitive
		}
		total += outcome.Count
	}
	switch total {
	case 0:
		return kindInsensitivityNoMatchSound
	case 1:
		return kindInsensitivityCommitSound
	default:
		return kindInsensitivitySensitive
	}
}

// HandleVerificationReason is the closed vocabulary VerifyHandle reports
// (CHAOS-3900 P1.E).
type HandleVerificationReason string

const (
	HandleVerificationValid             HandleVerificationReason = "valid"
	HandleVerificationGrammarMismatch   HandleVerificationReason = "grammar_mismatch"
	HandleVerificationNotFound          HandleVerificationReason = "not_found"
	HandleVerificationCensusUnavailable HandleVerificationReason = "census_unavailable"
)

// VerifyHandle is design brief §2.1's own handr_ redemption-time
// re-verification: "redemption re-validates the value against the
// registry grammar AND re-runs the keyed source-row existence check."
// Beside BindHandles/CensusFunc, its own derive-side siblings -- never
// widening either signature (team-lead ruling).
//
// Grammar first (cheap, no I/O): a stored value that no longer matches its
// own claimed pattern_id is rejected without ever reaching the census.
//
// Existence second, over the SAME CensusFunc the shadow evidence round
// already calls, with handleBound=true and anchorBound=false (existence
// of the handle value alone, no anchor constraint). DELIBERATELY carries
// NO epoch/binding parameter (team-lead ruling): base tables are the
// EPOCH-INDEPENDENT source of record -- CHAOS-3896's own ratified design
// moved decisive proofs off the (epoch-versioned) graph and onto them
// specifically so a census read never needs a pinned graph key. Do not
// add one; a future maintainer "fixing" this by threading a
// ResolvedGraphBinding in would be re-coupling this read to the wrong
// epoch model.
//
// (storedValue, org) in -> valid/invalid + reason out, question-free and
// independently unit-testable, matching the same contract shape
// kindInsensitivityProof above already establishes for D.
func VerifyHandle(ctx context.Context, orgID string, kind CensusKind, patternID, value string, census CensusFunc) (bool, HandleVerificationReason) {
	if !ValidateHandleGrammar(kind, patternID, value) {
		return false, HandleVerificationGrammarMismatch
	}
	if census == nil {
		return false, HandleVerificationCensusUnavailable
	}
	outcome, err := census(ctx, orgID, kind, value, true, "", "", false)
	if err != nil {
		return false, HandleVerificationCensusUnavailable
	}
	// Codex xhigh review (chaos-pivot-p1, first round), finding 1: a
	// ClosureMismatch/SatisfierSetClosureMismatch outcome is the SAME "race
	// can only demote, never mint" signal chaos3899_census.go's own
	// producer and this package's other CensusOutcome consumers
	// (chaos3896_slice_b_presentation.go, chaos3896_slice_c_evidence_census.go)
	// already treat as untrustworthy -- Count alone does not prove the
	// fetched set/witness actually closed. An outcome that could not prove
	// closure must fail the SAME way an unreachable census does
	// (HandleVerificationCensusUnavailable), never silently validate on a
	// bare Count>0.
	if outcome.ClosureMismatch || outcome.SatisfierSetClosureMismatch {
		return false, HandleVerificationCensusUnavailable
	}
	if outcome.Count == 0 {
		return false, HandleVerificationNotFound
	}
	return true, HandleVerificationValid
}

// HashAliasTerm is the ONE place a matched term is turned into
// ContextFabricAnchorOption.MatchedTermHash's own wire value (CHAOS-3900
// P1.E, team-lead ruling): SHA-256 of the term normalized through
// NormalizeAliasTerm -- the SAME function MatchIdentityRows already
// applies to both sides of every identity match, so a hash computed here
// is provably comparable to a hash computed at derive time, not a
// lookalike normalization that could silently diverge. hex-truncated to
// 24 characters, the same digest length mintStructureReceiptID/
// mintStructureOptionID already use (internal/contextfabric/structure.go)
// -- this repo's own digest idiom. Deliberately one-way: the raw term is
// question-derived text and must never persist on a durable, server-minted
// offer (standing rule: term identity via hash, never the term itself).
func HashAliasTerm(term string) string {
	sum := sha256.Sum256([]byte(NormalizeAliasTerm(term)))
	return hex.EncodeToString(sum[:])[:24]
}

// AnchorVerificationReason is the closed vocabulary VerifyAnchorClaimantUnique
// reports (CHAOS-3900 P1.E).
type AnchorVerificationReason string

const (
	AnchorVerificationValid AnchorVerificationReason = "valid"
	// AnchorVerificationClaimContested: MORE THAN ONE identity-universe row
	// now carries an alias hashing to the stored matched_term_hash -- a
	// rival claimant gained the SAME term after this anchor was offered
	// (the live CHAOS-3917 class this field exists to catch).
	AnchorVerificationClaimContested AnchorVerificationReason = "anchor_claim_contested"
	// AnchorVerificationClaimLost: either NO row carries the hash any
	// longer (the claim's own source row/alias was removed or renamed), or
	// exactly one row does but its (Kind, CanonicalID) no longer matches
	// what was stored -- the claim moved out from under the offer.
	AnchorVerificationClaimLost AnchorVerificationReason = "anchor_claim_lost"
	// AnchorVerificationIncompleteEnumeration: the identity-universe read
	// itself was truncated or errored -- design brief 3917's own rule:
	// NO uniqueness proof on an incomplete enumeration. Fail closed, never
	// assume the unseen rows would not have contested the claim.
	AnchorVerificationIncompleteEnumeration AnchorVerificationReason = "incomplete_enumeration"
	// AnchorVerificationUnauthorized (CHAOS-4042 PR3, sol-max ruling) is
	// VerifyAnchorClaimantMembership's own reason for "the selected
	// claimant's graph node exists under the pinned binding, but the
	// caller's own principal/scope is no longer authorized to see it" --
	// INTERNAL/telemetry only. The ruling: "generic unresolved veto; do not
	// expose whether it still exists" -- the caller-visible effect is
	// IDENTICAL to AnchorVerificationClaimLost (structure_confirmation_
	// unresolved), so this value must never itself be surfaced on the wire;
	// it exists only so an operator reading telemetry can distinguish "the
	// claim vanished" from "the principal lost visibility" without that
	// distinction ever reaching a caller. Mirrored in
	// contextfabric.AnchorVerificationUnauthorized (structure.go).
	AnchorVerificationUnauthorized AnchorVerificationReason = "anchor_claim_unauthorized"
	// AnchorVerificationGraphUnverifiable (CHAOS-4042 PR3, sol-max ruling,
	// team-lead's own cf_binding_epoch_delta mapping correction) is the
	// CANNOT-VERIFY reason: the pinned ResolvedGraphBinding's own graph key
	// does not exist at all (its epoch was already retired -- a race with
	// the retire executor, DeleteEpochGraph never touches the ACTIVE
	// epoch, so this can only happen to a binding that has gone stale), or
	// the graph-side read itself errored. Deliberately distinct from
	// AnchorVerificationClaimLost: a retired epoch or a read error proves
	// NOTHING about whether the claimant still exists in a LIVE epoch --
	// asserting "lost" here would be a false claim the ruling forbids
	// (a wrong-epoch read must never manufacture a claim-vanished verdict).
	// Maps to the SAME generic vetoed_unresolved structure disposition as
	// AnchorVerificationIncompleteEnumeration -- the wire stays generic per
	// the existing veto-collapse design; only internal telemetry
	// distinguishes a graph-side cannot-verify from a ClickHouse-side one.
	AnchorVerificationGraphUnverifiable AnchorVerificationReason = "graph_binding_unverifiable"
)

// GraphAnchorMemberResult is the pinned-epoch graph-side half of CHAOS-4042
// PR3's own redemption-time re-verification (the sol-max ruling's
// authorized_B(...) term) -- what GraphAnchorMemberFunc reports about ONE
// (kind, canonicalID) node read under a specific ResolvedGraphBinding.
// Unverifiable and Exists/Authorized are mutually exclusive in meaning:
// Unverifiable==true means the binding's own graph key could not be read
// at all (a retired epoch), so Exists/Authorized carry no information and
// must be ignored -- never treated as "false".
type GraphAnchorMemberResult struct {
	// Exists reports whether the node was found in the graph identified by
	// binding.GraphKey. Meaningless when Unverifiable is true.
	Exists bool
	// Authorized reports the SAME AuthorizedAttributes check every ordinary
	// NodeCandidate result already passes, applied to the found node's own
	// attributes. Meaningless when Exists is false or Unverifiable is true.
	Authorized bool
	// Unverifiable reports that binding's own graph key does not exist at
	// all (ErrNotFound on the WHOLE graph, not merely the one node) -- see
	// AnchorVerificationGraphUnverifiable's own doc comment for why this
	// must never be folded into "claim lost".
	Unverifiable bool
}

// GraphAnchorMemberFunc is CHAOS-4042 PR3's own pinned-epoch, single-node
// graph read + re-authorization dependency -- mirrors IdentityUniverseFunc/
// CensusFunc's existing narrow-dependency-function convention (never a
// GraphReader interface addition: a new interface method would ripple
// through every fake GraphReader implementation PR3a already had to touch,
// for a capability only VerifyAnchorClaimantMembership needs). The
// production implementation (internal/contextfabric/falkorgraph) composes
// the adapter's own existing effectiveKey (binding.GraphKey is already the
// pinned epoch's own distinct graph namespace -- CHAOS-3898's own
// graphKeyForEpoch build-aside-and-swap design -- so a read via
// binding.GraphKey is epoch-addressed BY CONSTRUCTION; no separate
// epoch-comparison mechanism is needed) with the SAME node-by-kind-id read
// and AuthorizedAttributes check every ordinary candidate/edge resolution
// already uses.
type GraphAnchorMemberFunc func(ctx context.Context, principal storage.Principal, scope contextfabric.RequestedScope, binding contextfabric.ResolvedGraphBinding, kind contextfabric.SubjectKind, canonicalID string) (GraphAnchorMemberResult, error)

// IdentityUniverseFunc mirrors falkorgraph.Config.IdentityUniverse's own
// shape exactly (CHAOS-3884 Option C) -- the SAME complete, per-organization
// identity-universe read VerifyAnchorClaimantUnique re-runs at redemption
// time, never a second, potentially-divergent read path. complete==false
// (a truncated backend read) is handled identically to err!=nil: both fail
// AnchorVerificationIncompleteEnumeration, per that reason's own doc
// comment.
type IdentityUniverseFunc func(ctx context.Context, orgID string) (rows []IdentityRow, observedAt time.Time, complete bool, err error)

// VerifyAnchorClaimantUnique is design brief §2.1's own ancr_ redemption-time
// re-verification (CHAOS-3900 P1.E, team-lead ruling): re-reads the
// identity universe, hashes each row's Label/Aliases/ProviderAliases
// through the SAME HashAliasTerm this offer's own matched_term_hash was
// minted with, and counts rows carrying ANY alias whose hash equals it.
// Valid iff EXACTLY ONE row matches AND its (Kind, CanonicalID) equals the
// stored pair -- re-proving the per-TERM claimant association
// AliasLookup's own uniqueness is scoped to (MatchIdentityRows), not a
// canonical-id-only check (P1.E's own earlier finding: a canonical-id-only
// re-check cannot detect a rival gaining the SAME term, and would have
// been a false sense of safety).
//
// Deliberately works HASH-side only: the raw term never needs to exist
// here, matching the field it re-verifies.
func VerifyAnchorClaimantUnique(ctx context.Context, orgID string, kind contextfabric.SubjectKind, canonicalID, matchedTermHash string, identityUniverse IdentityUniverseFunc) (bool, AnchorVerificationReason) {
	if identityUniverse == nil {
		return false, AnchorVerificationIncompleteEnumeration
	}
	rows, _, complete, err := identityUniverse(ctx, orgID)
	if err != nil || !complete {
		return false, AnchorVerificationIncompleteEnumeration
	}
	var matches []IdentityRow
	for _, row := range rows {
		if identityRowCarriesTermHash(row, matchedTermHash) {
			matches = append(matches, row)
		}
	}
	switch len(matches) {
	case 0:
		return false, AnchorVerificationClaimLost
	case 1:
		if matches[0].Kind == kind && matches[0].CanonicalID == canonicalID {
			return true, AnchorVerificationValid
		}
		return false, AnchorVerificationClaimLost
	default:
		return false, AnchorVerificationClaimContested
	}
}

// identityRowCarriesTermHash reports whether ANY of row's Label/Aliases/
// ProviderAliases normalizes and hashes to termHash -- mirrors
// matchedTermsForRow's own three-class scan (chaos3884_identity_universe.go),
// including its empty-normalized-term guard: an empty Label/alias never
// participates in matching there, and must not participate in hashing
// here either, or an attacker-uncontrollable empty term could collide with
// a row that simply has no label set.
func identityRowCarriesTermHash(row IdentityRow, termHash string) bool {
	if label := NormalizeAliasTerm(row.Label); label != "" && HashAliasTerm(label) == termHash {
		return true
	}
	for _, alias := range row.Aliases {
		if a := NormalizeAliasTerm(alias); a != "" && HashAliasTerm(a) == termHash {
			return true
		}
	}
	for _, alias := range row.ProviderAliases {
		if a := NormalizeAliasTerm(alias); a != "" && HashAliasTerm(a) == termHash {
			return true
		}
	}
	return false
}

// VerifyAnchorClaimantMembership is CHAOS-4042's (sol-max ruling) own
// redemption-time re-verification for a v2 (membership-verify) anchor
// confirmation: re-reads the SAME identity-universe hash-side read
// VerifyAnchorClaimantUnique uses, and proves the selected (kind,
// canonicalID) remains ANY member of the term's complete claimant set --
// never that it is the term's SOLE member. Mere multiplicity is not an
// error under membership semantics (this function never returns
// AnchorVerificationClaimContested); a rival gaining or losing the term
// does not, by itself, invalidate the selected claimant's own standing.
//
// Deliberately works HASH-side only, same reasoning as
// VerifyAnchorClaimantUnique: the raw term never needs to exist here.
//
// PR3 of CHAOS-4042's three-PR slice adds the graph-side half: after the
// ClickHouse-backed membership check above passes, the selected claimant's
// graph node must ALSO be found and re-authorized under the PINNED
// binding B -- the ruling's own membership rule, `valid = complete(C_B(h))
// && e in C_B(h) && authorized_B(...)`. ClickHouse alone is checked FIRST
// and short-circuits on its own negative (a lost/contested claim never
// needs a graph read to already be invalid -- also proves the "ClickHouse
// says lost" direction of the ruling's fail-closed-both-directions
// requirement without the graph dependency even needing to agree). A
// nil graphAnchorMember is NOT "trust ClickHouse alone" -- same
// fail-CLOSED default as every other reverify dependency in this package.
func VerifyAnchorClaimantMembership(ctx context.Context, principal storage.Principal, scope contextfabric.RequestedScope, binding contextfabric.ResolvedGraphBinding, kind contextfabric.SubjectKind, canonicalID, matchedTermHash string, identityUniverse IdentityUniverseFunc, graphAnchorMember GraphAnchorMemberFunc) (bool, AnchorVerificationReason) {
	if identityUniverse == nil {
		return false, AnchorVerificationIncompleteEnumeration
	}
	rows, _, complete, err := identityUniverse(ctx, principal.OrgID)
	if err != nil || !complete {
		return false, AnchorVerificationIncompleteEnumeration
	}
	clickhouseValid := false
	for _, row := range rows {
		if row.Kind == kind && row.CanonicalID == canonicalID && identityRowCarriesTermHash(row, matchedTermHash) {
			clickhouseValid = true
			break
		}
	}
	if !clickhouseValid {
		return false, AnchorVerificationClaimLost
	}
	if graphAnchorMember == nil {
		return false, AnchorVerificationGraphUnverifiable
	}
	result, err := graphAnchorMember(ctx, principal, scope, binding, kind, canonicalID)
	if err != nil {
		return false, AnchorVerificationGraphUnverifiable
	}
	if result.Unverifiable {
		return false, AnchorVerificationGraphUnverifiable
	}
	if !result.Exists {
		return false, AnchorVerificationClaimLost
	}
	if !result.Authorized {
		return false, AnchorVerificationUnauthorized
	}
	return true, AnchorVerificationValid
}

// anchorOfferMaterial builds the subject_anchor disclosure (CHAOS-3900
// P1.C', team-lead ruling) from the SAME per-term unique-claimant scan
// BindAnchor's own decisive path uses (anchorTermCandidates,
// chaos3899_anchor.go) -- never a second, divergent notion of "unique
// claimant." rawClaimantsByTerm is the COMPLETE, unauthorized-filtered
// per-term read; authorizedClaimantsByTerm is the SAME map with the
// caller's own AuthorizedAttributes filter already applied (resolve.go).
// rawClaimantsByTerm is used ONLY to detect mixed claimant visibility
// (CHAOS-4042 below) -- every candidate/option computation reads
// authorizedClaimantsByTerm exclusively, so an unauthorized claimant's
// content never reaches an offer.
//
// v1 cases (unchanged, byte-identical to pre-CHAOS-4042 behavior whenever
// no term has 2+ AUTHORIZED claimants -- all ruled explicitly, never
// inferred):
//  1. EXACTLY ONE distinct (kind, canonical_id) candidate: this is already
//     decisive by design brief §1.1/R4 -- BindAnchor itself would succeed
//     on this same data, so there is nothing to elicit. Offering it anyway
//     would be a clarification stop with zero information gain (the exact
//     category error the §1.3 never-elicit rule targets), and would feed
//     the Bridge a low-information single-option "confirmation" -- a noise
//     label, not a real one. Returns an EMPTY StructureOfferMaterial (no
//     Missing entry at all).
//  2. TWO OR MORE distinct candidates (BindAnchor itself would refuse,
//     ReasonAnchorNotUnique): genuine ambiguity across terms -- offer ONE
//     AnchorOption per distinct candidate, subject_anchor becomes Missing.
//  3. ZERO candidates (no unique-claimant material at all, or an
//     incomplete identity-universe read): subject_anchor is STILL
//     disclosed as Missing, with an EMPTY AnchorOptions list -- "disclosed
//     as missing-and-helpful, nothing offerable" (team-lead ruling); an
//     absent block or a silently dropped Missing row is forbidden either
//     way.
//
// CHAOS-4042 (sol-max ruling) v2 case: if ambiguousAnchorTermClaimants
// finds any term with 2+ AUTHORIZED claimants, EVERY claimant of every
// such term (that is not itself mixed-visibility, see below) is offered
// as a v2 AnchorOptionV2 -- one option per (term, claimant) pair, all
// sharing that term's own matched_term_hash. This REPLACES the v1 cases
// above for this call (a result mixes v1 and v2 anchor semantics), because
// a decisive v1 candidate elsewhere needs no disambiguation of its own,
// exactly the case-1 rationale already establishes. Mixed-visibility rule:
// a term whose authorized claimant count is LESS than its raw count had at
// least one hidden claimant -- disclosing partial claimant content (or
// even a claimant COUNT) for that term would leak the existence of an
// entity the principal cannot otherwise read, so that term's ENTIRE
// candidate group is suppressed, not truncated.
func anchorOfferMaterial(rawClaimantsByTerm, authorizedClaimantsByTerm map[string][]IdentityMatch, complete bool, membershipOffersEnabled bool) contextfabric.StructureOfferMaterial {
	// CHAOS-4042 auth-gap closure (codex xhigh review finding, round 2):
	// a term where authorization hid ANY claimant must never contribute to
	// candidacy at all -- not just the v2 ambiguous scan, but v1's own
	// unique-claimant scan too. A term with one visible claimant and one
	// HIDDEN rival has raw count 2 but authorized count 1; feeding the
	// authorized view alone into anchorTermCandidates would let it look
	// like a genuine unique claimant, which is exactly "filter the proof
	// universe by authorization then claim uniqueness" -- forbidden by the
	// ruling's do-not list, and it applies to the v1 case-2 offer path
	// (cross-term disagreement) exactly as much as the v2 path. Build the
	// fully-visible view ONCE, upfront, unconditionally (not gated by
	// membershipOffersEnabled): every term where raw and authorized
	// claimant counts agree, nothing more. Both the v1 candidate scan and
	// the v2 ambiguous scan read ONLY this view from here on -- neither
	// ever sees a term authorization could have distorted.
	fullyVisible := map[string][]IdentityMatch{}
	for term, claimants := range authorizedClaimantsByTerm {
		if len(rawClaimantsByTerm[term]) != len(claimants) {
			continue // some claimant was hidden for this term: exclude entirely
		}
		fullyVisible[term] = claimants
	}

	// CHAOS-4042 (team-lead ruling): the entire v2 ambiguous-claimant path
	// is gated DARK by default -- membershipOffersEnabled false (every
	// production deployment until PR3 lands pinned-epoch reconciliation
	// and redemption-time re-authorization) skips straight to the v1 path
	// below, byte-identical to before this ticket (modulo the mixed-
	// visibility fix above, which is a strict narrowing: it can only
	// EXCLUDE a candidate v1 previously wrongly offered, never add one).
	// See ResolveDeps.AnchorMembershipOffersEnabled's own doc comment.
	if membershipOffersEnabled {
		ambiguous := ambiguousAnchorTermClaimants(fullyVisible, complete)
		if len(ambiguous) > 0 {
			return anchorOfferMaterialV2(ambiguous)
		}
	}

	candidates := anchorTermCandidates(fullyVisible, complete)
	if len(candidates) == 1 {
		return contextfabric.StructureOfferMaterial{}
	}
	keys := make([]anchorCandidateKey, 0, len(candidates))
	for k := range candidates {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})
	// Codex xhigh review (chaos-pivot-p1, round 2, finding 1): cap AFTER
	// sorting so truncation is deterministic (always the same
	// lexicographically-first candidates), never a function of map order.
	if len(keys) > structureOfferMaxOptions {
		keys = keys[:structureOfferMaxOptions]
	}
	options := make([]contractsv1.ContextFabricAnchorOption, 0, len(keys))
	for _, k := range keys {
		info := candidates[k]
		options = append(options, contractsv1.ContextFabricAnchorOption{
			Kind: k.kind, CanonicalID: k.id, Label: anchorOfferLabel(info.label),
			MatchedTermHash: HashAliasTerm(info.term),
			OfferSource:     contractsv1.ContextFabricStructureOfferEngine,
		})
	}
	return contextfabric.StructureOfferMaterial{
		Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: options,
	}
}

// anchorOfferMaterialV2 mints one ContextFabricAnchorOptionV2 per (term,
// claimant) pair in visible, converted to the wire ContextFabricAnchorOption
// shape via ToV1Wire() -- see that method's own doc comment for why the
// wire slice never forks even though redemption meaning does. Deterministic
// ordering: terms sorted, then claimants within a term sorted by (kind,
// canonical_id), matching anchorTermCandidates' own iteration discipline so
// output never depends on map order. Capped at structureOfferMaxOptions
// AFTER sorting, same reasoning as the v1 cap above.
func anchorOfferMaterialV2(visible map[string][]IdentityMatch) contextfabric.StructureOfferMaterial {
	terms := make([]string, 0, len(visible))
	for term := range visible {
		terms = append(terms, term)
	}
	sort.Strings(terms)

	var options []contractsv1.ContextFabricAnchorOption
	for _, term := range terms {
		claimants := append([]IdentityMatch(nil), visible[term]...)
		sort.Slice(claimants, func(i, j int) bool {
			if claimants[i].Row.Kind != claimants[j].Row.Kind {
				return claimants[i].Row.Kind < claimants[j].Row.Kind
			}
			return claimants[i].Row.CanonicalID < claimants[j].Row.CanonicalID
		})
		termHash := HashAliasTerm(term)
		for _, claimant := range claimants {
			options = append(options, contractsv1.ContextFabricAnchorOptionV2{
				Kind: claimant.Row.Kind, CanonicalID: claimant.Row.CanonicalID,
				Label: anchorOfferLabel(claimant.Row.Label), MatchedTermHash: termHash,
				OfferSource: contractsv1.ContextFabricStructureOfferEngine,
			}.ToV1Wire())
			if len(options) == structureOfferMaxOptions {
				return contextfabric.StructureOfferMaterial{
					Missing:                []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
					AnchorOptions:          options,
					AnchorOptionsRequireV2: true,
				}
			}
		}
	}
	return contextfabric.StructureOfferMaterial{
		Missing:                []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions:          options,
		AnchorOptionsRequireV2: true,
	}
}

// anchorOfferLabel renders an AnchorOption's display label from the
// candidate's own identity-universe Label -- the SAME display text a
// SubjectCandidate would already show for this row (not new disclosure;
// unlike kindOfferLabel's fixed-sentence discipline, an anchor's whole
// purpose is disambiguating BETWEEN named entities, so the offer must show
// the entity's own name, not a generic sentence).
func anchorOfferLabel(label string) string {
	return label
}

// handleOfferMaterial builds the subject_handle disclosure (design brief
// §2.1: "offered when a grammar-valid value arrived explicit_unattributed")
// from BindHandles' own pure question-text scan. Every value BindHandles
// finds here is, by construction, unattributed: composeStructureNeeds
// (structure.go) is only ever composed on the subjectless terminal path,
// so nothing has committed for a found handle to be attributed to.
//
// Mirrors anchorOfferMaterial's own three-case discipline for the
// zero-candidates case: subject_handle is Missing whenever this function
// runs (P1.C' does not yet implement §1.3's class-conditional gate, the
// same known, accepted scope gap kindOfferMaterial already carries), with
// HandleOptions holding however many grammar-valid values were found --
// possibly zero, never an absent block.
//
// handleOfferMaterial additionally takes explicitHandles
// (contextfabric.InvestigationRequest.SubjectHandles, CHAOS-3972 P3) and
// checker (the engine's own offer-time HandleGrammarChecker dependency):
// each explicit entry that checker validates against the closed registry
// becomes a top-ranked HandleOption, ahead of anything BindHandles finds
// in the question text (design brief §2.3's same top-ranked upgrade-turn
// rule kindOfferMaterial's own doc comment describes). checker == nil
// (the dependency was never wired) means NO explicit handle can ever
// become an offer -- HandleGrammarChecker's own doc comment names this
// the safe degradation, never a veto.
func handleOfferMaterial(question string, explicitHandles []contractsv1.ContextFabricRequestedHandle, checker contextfabric.HandleGrammarChecker) contextfabric.StructureOfferMaterial {
	var explicitOptions []contractsv1.ContextFabricHandleOption
	if checker != nil {
		explicitSeen := make(map[contractsv1.ContextFabricHandleOption]struct{}, len(explicitHandles))
		for _, h := range explicitHandles {
			sourceColumn, ok := checker(h.Kind, h.PatternID, h.Value)
			if !ok {
				continue
			}
			opt := contractsv1.ContextFabricHandleOption{
				Kind: h.Kind, PatternID: h.PatternID, Value: h.Value, SourceColumn: sourceColumn,
				Label:       handleOfferLabel(h.Kind, h.Value),
				OfferSource: contractsv1.ContextFabricStructureOfferEngine,
			}
			if _, exists := explicitSeen[opt]; exists {
				continue
			}
			explicitSeen[opt] = struct{}{}
			explicitOptions = append(explicitOptions, opt)
		}
	}
	bound := BindHandles(question)
	options := make([]contractsv1.ContextFabricHandleOption, 0, len(explicitOptions)+len(bound))
	options = append(options, explicitOptions...)
	// Codex xhigh review (chaos-pivot-p1, round 2, finding 1): BindHandles
	// reports every regex occurrence, so the SAME handle text repeated in
	// one question (or matched by more than one registry entry) would
	// otherwise mint two options with IDENTICAL content -- same
	// (kind, pattern_id, value, source_column, offer_source, prior fields)
	// -- and therefore the SAME receipt_id/option_id, which the wire
	// Validate() rejects as a duplicate. Dedup by that exact content tuple,
	// keeping BindHandles' own already-deterministic first-occurrence
	// order (no map iteration involved), before the offer even exists.
	// Seeded from explicitOptions' own content (CHAOS-3972 P3): an explicit
	// handle already offered above must not ALSO appear via question-text
	// scanning below -- same identical-content dedup rule, extended to
	// cover both sources.
	seen := make(map[contractsv1.ContextFabricHandleOption]struct{}, len(bound)+len(explicitOptions))
	for _, opt := range explicitOptions {
		seen[opt] = struct{}{}
	}
	for _, b := range bound {
		sourceColumn, ok := HandleSourceColumn(b.Kind, b.Grammar)
		if !ok {
			// Registry-miss: BindHandles only ever returns a (kind, grammar)
			// pair from handleGrammarRegistry, and HandleSourceColumn reads
			// the SAME registry -- unreachable by construction today. Skipped
			// defensively rather than offered without a redemption target,
			// mirroring structureReceiptPrefixForMember's own "never panic on
			// a closed-registry mismatch" discipline.
			continue
		}
		opt := contractsv1.ContextFabricHandleOption{
			Kind: b.Kind, PatternID: b.Grammar, Value: b.Value, SourceColumn: sourceColumn,
			Label:       handleOfferLabel(b.Kind, b.Value),
			OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		}
		// opt's ReceiptID/OptionID are still unset (minted later, in
		// composeStructureNeeds), so opt itself is already exactly the
		// content-only dedup key -- no need to build a separate one.
		if _, exists := seen[opt]; exists {
			continue
		}
		seen[opt] = struct{}{}
		options = append(options, opt)
	}
	// Codex xhigh review (chaos-pivot-p1, round 2, finding 1): cap AFTER
	// dedup, in BindHandles' own deterministic order, for the same
	// always-safe-truncation reasoning as anchorOfferMaterial above.
	if len(options) > structureOfferMaxOptions {
		options = options[:structureOfferMaxOptions]
	}
	return contextfabric.StructureOfferMaterial{
		Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle},
		HandleOptions: options,
	}
}

// handleOfferLabel renders a server-owned label for one handle offer --
// mirrors kindOfferLabel's fixed-sentence-per-kind discipline, with the
// grammar-extracted VALUE interpolated (a verbatim span already surfaced
// in the offer's own value field, not new disclosure).
func handleOfferLabel(kind contractsv1.ContextFabricSubjectKind, value string) string {
	switch kind {
	case contractsv1.ContextFabricSubjectPullRequest:
		return "pull request #" + value
	case contractsv1.ContextFabricSubjectWorkItem:
		return "work item " + value
	case contractsv1.ContextFabricSubjectCIRun:
		return "CI run #" + value
	default:
		// Unreachable given handleGrammarRegistry's own closed kind set.
		return value
	}
}

// combineStructureOfferMaterial merges the per-member StructureOfferMaterial
// values ResolveSubjects builds (kind, anchor, handle -- window is a later
// slice) into the single value it returns. Missing entries concatenate in
// CALL order, which callers must pass in §1.2 reading 1's own elicitation
// priority (kind before anchor before window) -- each per-member builder
// contributes at most one Missing entry for its own member, so a plain
// concatenation can never duplicate or reorder across members.
func combineStructureOfferMaterial(materials ...contextfabric.StructureOfferMaterial) contextfabric.StructureOfferMaterial {
	var combined contextfabric.StructureOfferMaterial
	for _, m := range materials {
		combined.Missing = append(combined.Missing, m.Missing...)
		combined.KindOptions = append(combined.KindOptions, m.KindOptions...)
		combined.AnchorOptions = append(combined.AnchorOptions, m.AnchorOptions...)
		combined.HandleOptions = append(combined.HandleOptions, m.HandleOptions...)
		combined.AnchorOptionsRequireV2 = combined.AnchorOptionsRequireV2 || m.AnchorOptionsRequireV2
	}
	return combined
}
