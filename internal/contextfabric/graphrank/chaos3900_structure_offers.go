package graphrank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-3900 P1.C (pivot-intent design brief §2.1). ResolveSubjects
// (resolve.go) calls kindOfferMaterial with the SAME candidate pool it
// already assembled, right before it returns -- this file owns turning
// that pool into the expected_kind disclosure StructureOfferMaterial
// carries back to the engine.
//
// SCOPE NOTE (P1.C increment 1, flagged rather than silently absent):
// AnchorOptions/HandleOptions are NOT built here yet. Their own candidate
// material (identity-universe unique-claimant candidates, handle-grammar
// bindings against question text) needs the SAME identity/alias data
// runShadowEvidenceRoundForResolution (resolve.go) already threads --
// which only exists on the gated shadow-evidence-round path
// (deps.CensusFunc != nil && len(resolution.Committed) == 0 &&
// searchTruncated), unlike expected_kind offers, which need only the
// unconditionally-available candidate pool. Building anchor/handle offers
// correctly needs that same gated data threaded out to this file too --
// a follow-up increment, not a design change.

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
// elicitation... a closed enum a human can pick in one tap"). Offered
// ONLY when the pool spans MORE THAN ONE distinct offerable kind -- a
// single-kind (or empty) pool has nothing to disambiguate on this axis,
// so offering it would disclose a choice the question does not actually
// present.
func kindOfferMaterial(candidates []contextfabric.SubjectCandidate) contextfabric.StructureOfferMaterial {
	seen := make(map[contractsv1.ContextFabricSubjectKind]bool, len(candidates))
	var distinct []contractsv1.ContextFabricSubjectKind
	for _, candidate := range candidates {
		kind := candidate.Subject.Kind
		if seen[kind] || !structureOfferKinds[kind] {
			continue
		}
		seen[kind] = true
		distinct = append(distinct, kind)
	}
	if len(distinct) < 2 {
		return contextfabric.StructureOfferMaterial{}
	}
	options := make([]contractsv1.ContextFabricKindOption, 0, len(distinct))
	for _, kind := range distinct {
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
// STANDALONE, PURE (besides the injected CensusFunc), and UNIT-TESTED --
// but DELIBERATELY UNWIRED into any decisive-path gate today (P1.D
// scoping, confirmed by repo-wide grep: no inferred-tier or
// explicit-unattributed kind-narrowing mechanism exists anywhere in this
// codebase yet, so there is no live branch for such a gate to guard).
// Wiring this into an actual decisive-path check is a HARD PRECONDITION
// of introducing any such kind source (tracked on CHAOS-3927 and the
// P3/P5 commissioning checklists) -- see ConfirmedExpectedKind's own doc
// comment (internal/contextfabric/ports.go) for the type-level half of
// this same guard.
func kindInsensitivityProof(ctx context.Context, orgID string, preNarrowingKinds []CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool, census CensusFunc) kindInsensitivityOutcome {
	censused, nonCensusedSurvivor := splitCensusKinds(preNarrowingKinds)
	if nonCensusedSurvivor || census == nil || len(censused) == 0 {
		return kindInsensitivitySensitive
	}
	total := 0
	for _, kind := range censused {
		outcome, err := census(ctx, orgID, kind, handleValue, handleBound, anchorKind, anchorCanonicalID, anchorBound)
		if err != nil {
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
)

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
