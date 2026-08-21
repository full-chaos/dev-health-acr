package graphrank

import (
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// authorizedClaimantNodes filters a raw AliasLookup claimant map down to
// what principal may see under scope, per node -- the SAME
// AuthorizedAttributes check every ordinary NodeCandidate result already
// passes (candidate.go). CHAOS-4042 (sol-max ruling): aliasClaimantsByTerm
// itself is org-scoped but was never authorization-filtered before feeding
// anchor OFFER material (unlike candidatesBySubject, which always goes
// through AuthorizedAttributes via NodeCandidate/mergeSearchResults) --
// narrowly exposed while every anchor term was still required to be
// globally unique, broadly exposed the moment an ambiguous term can name
// multiple claimants in a disclosed offer.
//
// Callers MUST keep using the raw, unfiltered map as the source of TRUTH
// for BindAnchor and the shadow evidence round's own re-verification --
// authorization must never be allowed to manufacture uniqueness by hiding
// rivals from the completeness/uniqueness proof itself. This filtered view
// exists ONLY to build what a caller is shown.
func authorizedClaimantNodes(principal storage.Principal, scope contextfabric.RequestedScope, claimantsByTerm map[string][]CandidateNode) map[string][]CandidateNode {
	if claimantsByTerm == nil {
		return nil
	}
	out := make(map[string][]CandidateNode, len(claimantsByTerm))
	for term, nodes := range claimantsByTerm {
		visible := make([]CandidateNode, 0, len(nodes))
		for _, node := range nodes {
			if AuthorizedAttributes(principal, scope, node.Attributes) {
				visible = append(visible, node)
			}
		}
		out[term] = visible
	}
	return out
}

// claimantsFromCandidateNodes adapts deps.AliasLookup's own return shape
// (map[string][]CandidateNode) to BindAnchor's IdentityMatch shape, via
// NodeSubject -- the same kind/canonical-id extraction every other reader
// of a CandidateNode's identity already uses (subject.go). A node whose
// attributes do not resolve to a valid SubjectRef is dropped, mirroring
// NodeSubject's own "ok=false means skip" convention; this never happens
// for a well-formed AliasLookup implementation (every claimant it returns
// is a real graph node), so it is a defensive skip, not an expected path.
//
// Aliases/ProviderAliases (CHAOS-3918 widening measurement, codex xhigh
// review round 3, confirmed and fixed, 2026-08-19): this function used to
// build IdentityRow with ONLY Kind/CanonicalID/Label populated --
// BindAnchor never needed more (it only ever reads .Kind/.CanonicalID off
// a matched row), but chaos3899_source_native_grammar.go's own
// resolveSourceNative DOES need a row's real alias content to find a
// claimant, and every IdentityRow this function ever produced left
// .Aliases/.ProviderAliases at their nil zero value -- making
// resolveSourceNative's row-content scan structurally unable to match
// anything via those two fields (round-2's own map-key fix was locally
// correct but fed a row that could never actually satisfy it). The fix:
// AliasAttributes/ProviderAliasAttributes (subject.go) already read a
// CandidateNode's own "aliases"/"provider_aliases" graph properties --
// the SAME properties candidate.go's own aliasMatched/providerMatched
// check already reads for the (non-shadow, already-shipped) identity
// mechanism -- so this is REUSE of an existing, precedented read path,
// not a new one: node.Attributes is already in memory, no new I/O.
//
// STALENESS CAVEAT (inherited, not introduced, by this fix -- see
// candidate.go's own aliasMatched/providerMatched doc comment): a graph
// node's own "aliases"/"provider_aliases" properties are projection-time
// data and can lag the source-of-truth ClickHouse read by up to one
// projection cycle. Acceptable here for the exact reason it already is
// for candidate.go's non-shadow use: this ticket's own ENTIRE mechanism
// is shadow/measurement-only (chaos3899_source_native_grammar.go's own
// doc comment), so a stale alias list can only ever under- or over-count
// a MEASUREMENT, never a production decision.
//
// STUB-NODE CAVEAT (codex xhigh review round 4, non-blocking nit): a
// REFERENCED (not yet fully projected) graph node -- falkorgraph's own
// subjectMergeAttrs called with entityOwned=nil, projection.go -- omits
// the alias properties entirely, so a source claimant that exists ONLY as
// such a stub still resolves via BindAnchor's own Kind/CanonicalID read
// (unaffected by this fix) but resolveSourceNative's row-content scan
// finds nothing for it. A further, PURELY ADDITIVE undercount on an
// already best-effort shadow measurement -- never a production-decision
// concern, and not pursued here (this ticket's own scope is a cheap
// registry addition, not a stub-projection completeness fix).
func claimantsFromCandidateNodes(nodes map[string][]CandidateNode) map[string][]IdentityMatch {
	if nodes == nil {
		return nil
	}
	out := make(map[string][]IdentityMatch, len(nodes))
	for term, claimants := range nodes {
		matches := make([]IdentityMatch, 0, len(claimants))
		for _, node := range claimants {
			subject, ok := NodeSubject(node)
			if !ok {
				continue
			}
			matches = append(matches, IdentityMatch{
				Row: IdentityRow{
					Kind: subject.Kind, CanonicalID: subject.CanonicalID, Label: subject.Label,
					Aliases: AliasAttributes(node.Attributes), ProviderAliases: ProviderAliasAttributes(node.Attributes),
				},
				Mechanism: node.Mechanism,
			})
		}
		out[term] = matches
	}
	return out
}

// AnchorBinding is CHAOS-3896's ANCHOR discriminator class (design brief v5
// §1.2's "anchor" row): a term whose complete identity-universe claimant
// set has exactly one member. This is DELIBERATELY not new machinery --
// the brief calls it out explicitly ("the identity_fast_path uniqueness
// discipline applied to the anchor role"): it reuses the EXACT CHAOS-3884
// IdentityRow/MatchIdentityRows/AliasLookup completeness-and-uniqueness
// read that already backs the resolution's own identity fast path
// (resolution.go's identityIndex/aliasIdentityComplete), rather than a
// second, parallel census-side identity reader.
type AnchorBinding struct {
	Kind        CensusKind
	CanonicalID string
	// Term is the ORIGINAL (as-passed) subject term this anchor bound to --
	// in-process provenance only, exactly like BoundHandle.Value; never
	// traced (corpus-safety rule).
	Term string
}

// BindAnchor derives the unique-claimant anchor discriminator from an
// AliasLookup result (claimantsByTerm, the SAME map ResolveDeps.AliasLookup
// returns and resolve.go already merges) and its own completeness flag.
//
// R4, verbatim: "≥2 claimants or an incomplete read -> no anchor
// discriminator." complete=false refuses outright (the identity view
// itself is unproven, so no claim about uniqueness -- including a
// single-claimant one -- can be trusted). Otherwise, for EACH term with
// exactly one claimant, that (kind, canonical id) pair is an anchor
// candidate; a term with zero or with two-or-more claimants contributes no
// candidate for itself (its own ambiguity does not poison a DIFFERENT
// term's unrelated unique claimant) but never becomes an anchor either.
//
// Across every contributing term, BindAnchor requires the candidate set to
// name exactly ONE DISTINCT (kind, canonical id) pair -- two different
// terms are allowed to agree (the same repository named two ways) but must
// never be allowed to silently pick one of two GENUINELY different
// anchors; that case reports ok=false (ReasonAnchorNotUnique) exactly like
// a single term's own >=2-claimant case does, for the same "an ambiguous
// anchor must refuse, never guess" reason.
func BindAnchor(claimantsByTerm map[string][]IdentityMatch, complete bool) (AnchorBinding, bool) {
	seen := anchorTermCandidates(claimantsByTerm, complete)
	if len(seen) != 1 {
		return AnchorBinding{}, false
	}
	// Deterministic single-entry extraction (map has exactly one key here).
	keys := make([]anchorCandidateKey, 0, 1)
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].id < keys[j].id }) // no-op for len==1, kept for clarity/future-proofing
	k := keys[0]
	return AnchorBinding{Kind: k.kind, CanonicalID: k.id, Term: seen[k].term}, true
}

// anchorCandidateKey identifies one distinct (kind, canonical_id) pair a
// per-term identity-universe read named as its own SOLE claimant.
type anchorCandidateKey struct {
	kind CensusKind
	id   string
}

// anchorCandidateInfo carries what BindAnchor's own decisive path needs
// (term, for AnchorBinding.Term) plus what CHAOS-3900 P1.C's own
// anchorOfferMaterial (chaos3900_structure_offers.go) additionally needs
// (label, for a human-displayable AnchorOption.label) about ONE anchor
// candidate.
type anchorCandidateInfo struct {
	term  string
	label string
}

// anchorTermCandidates is BindAnchor's own per-term uniqueness scan (R4:
// "≥2 claimants or an incomplete read -> no anchor discriminator" for a
// SINGLE term's own claimant set), factored out so anchorOfferMaterial can
// reuse the IDENTICAL filtering logic BindAnchor's decisive path uses --
// "which terms have exactly one claimant" must never have two divergent
// implementations. complete=false returns nil, mirroring BindAnchor's own
// fail-closed shape (an incomplete read proves nothing, including a
// single-claimant one).
func anchorTermCandidates(claimantsByTerm map[string][]IdentityMatch, complete bool) map[anchorCandidateKey]anchorCandidateInfo {
	if !complete {
		return nil
	}
	// Codex xhigh review (chaos-pivot-p1, first round), finding 3: iterate
	// terms in a DETERMINISTIC (sorted) order rather than raw map-range
	// order. Two distinct unique-claimant terms can name the SAME (kind,
	// canonical_id) (e.g. "repo-a" and "full-chaos/repo-a" both uniquely
	// resolving to one repository) -- when that happens only one term's
	// info can occupy the key, and which one must not depend on Go's
	// randomized map iteration: that would make matched_term_hash (and
	// everything minted from it downstream: label, receipt_id, option_id)
	// nondeterministic across otherwise-identical retries. Sorted-term
	// iteration plus first-write-wins makes the lexicographically-smallest
	// term the stable, repeatable choice.
	terms := make([]string, 0, len(claimantsByTerm))
	for term := range claimantsByTerm {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	seen := map[anchorCandidateKey]anchorCandidateInfo{}
	for _, term := range terms {
		matches := claimantsByTerm[term]
		if len(matches) != 1 {
			continue
		}
		row := matches[0].Row
		key := anchorCandidateKey{kind: row.Kind, id: row.CanonicalID}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = anchorCandidateInfo{term: term, label: row.Label}
	}
	return seen
}
