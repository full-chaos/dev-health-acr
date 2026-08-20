package graphrank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

// anchorOfferMaterial builds the subject_anchor disclosure (CHAOS-3900
// P1.C', team-lead ruling) from the SAME per-term unique-claimant scan
// BindAnchor's own decisive path uses (anchorTermCandidates,
// chaos3899_anchor.go) -- never a second, divergent notion of "unique
// claimant."
//
// Three cases, all ruled explicitly (never inferred):
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
func anchorOfferMaterial(claimantsByTerm map[string][]IdentityMatch, complete bool) contextfabric.StructureOfferMaterial {
	candidates := anchorTermCandidates(claimantsByTerm, complete)
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
func handleOfferMaterial(question string) contextfabric.StructureOfferMaterial {
	bound := BindHandles(question)
	options := make([]contractsv1.ContextFabricHandleOption, 0, len(bound))
	// Codex xhigh review (chaos-pivot-p1, round 2, finding 1): BindHandles
	// reports every regex occurrence, so the SAME handle text repeated in
	// one question (or matched by more than one registry entry) would
	// otherwise mint two options with IDENTICAL content -- same
	// (kind, pattern_id, value, source_column, offer_source, prior fields)
	// -- and therefore the SAME receipt_id/option_id, which the wire
	// Validate() rejects as a duplicate. Dedup by that exact content tuple,
	// keeping BindHandles' own already-deterministic first-occurrence
	// order (no map iteration involved), before the offer even exists.
	seen := make(map[contractsv1.ContextFabricHandleOption]struct{}, len(bound))
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
	}
	return combined
}
