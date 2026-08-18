package graphrank

import "sort"

// claimantsFromCandidateNodes adapts deps.AliasLookup's own return shape
// (map[string][]CandidateNode) to BindAnchor's IdentityMatch shape, via
// NodeSubject -- the same kind/canonical-id extraction every other reader
// of a CandidateNode's identity already uses (subject.go). A node whose
// attributes do not resolve to a valid SubjectRef is dropped, mirroring
// NodeSubject's own "ok=false means skip" convention; this never happens
// for a well-formed AliasLookup implementation (every claimant it returns
// is a real graph node), so it is a defensive skip, not an expected path.
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
				Row:       IdentityRow{Kind: subject.Kind, CanonicalID: subject.CanonicalID, Label: subject.Label},
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
	if !complete {
		return AnchorBinding{}, false
	}
	type candidateKey struct {
		kind CensusKind
		id   string
	}
	seen := map[candidateKey]string{} // -> one contributing term, for determinism/testability only
	for term, matches := range claimantsByTerm {
		if len(matches) != 1 {
			continue
		}
		row := matches[0].Row
		seen[candidateKey{kind: row.Kind, id: row.CanonicalID}] = term
	}
	if len(seen) != 1 {
		return AnchorBinding{}, false
	}
	// Deterministic single-entry extraction (map has exactly one key here).
	keys := make([]candidateKey, 0, 1)
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].id < keys[j].id }) // no-op for len==1, kept for clarity/future-proofing
	k := keys[0]
	return AnchorBinding{Kind: k.kind, CanonicalID: k.id, Term: seen[k]}, true
}
