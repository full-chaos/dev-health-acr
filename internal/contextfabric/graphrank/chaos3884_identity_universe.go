package graphrank

import (
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// IdentityRow (CHAOS-3884, Option C) is one row of a backend's COMPLETE,
// keyed identity-universe read -- every repository/project/team/work_item
// enumerated in full for an organization, never ranked or truncated the
// way an ordinary Search() result is. This is the source data an
// AliasLookup implementation matches against a resolution's own subject
// terms, entirely in Go (CRITICAL-2's "one normal form, one
// implementation" -- see NormalizeAliasTerm).
type IdentityRow struct {
	Kind            contextfabric.SubjectKind
	CanonicalID     string
	Label           string
	Aliases         []string
	ProviderAliases []string
	// ObservedAt is this row's own last_synced/updated_at -- the per-row
	// input to the IdentityObservationTime receipt field, aggregated
	// (MAX) by the caller across every row read.
	ObservedAt time.Time
}

// IdentityMatch pairs an IdentityRow with the key class that matched
// (surfaced as a contextfabric.MatchMechanism, the same public vocabulary
// NodeCandidate tags with, since a keyed-lookup-sourced CandidateNode's
// Mechanism is trusted AS DECLARED -- see CandidateNode.FromKeyedIdentityLookup's
// own doc comment).
type IdentityMatch struct {
	Row       IdentityRow
	Mechanism contextfabric.MatchMechanism
}

// MatchIdentityRows performs the Go-side complete-enumeration match Option
// C requires: for every (row, term) pair, in KEY-CLASS PRIORITY order
// (label, then alias, then provider-alias -- a row that matches multiple
// classes for the SAME term is reported once, under its strongest class,
// mirroring NodeCandidate's own mutual-exclusivity discipline for a single
// candidate/term pair), record a match keyed by the ORIGINAL (as-passed,
// un-normalized) term -- matching AliasLookup's own documented key
// convention, so a caller can correlate results back to the exact term
// string it searched with.
//
// Normalization (NormalizeAliasTerm) is applied to BOTH sides of every
// comparison -- this is the ONE place Option C performs identity matching,
// so there is nowhere else a second, potentially-divergent normalization
// could creep in.
func MatchIdentityRows(rows []IdentityRow, terms []string) map[string][]IdentityMatch {
	if len(rows) == 0 || len(terms) == 0 {
		return nil
	}
	normalizedTerms := make(map[string]string, len(terms)) // normalized -> original
	for _, term := range terms {
		norm := NormalizeAliasTerm(term)
		if norm == "" {
			continue
		}
		// First original term wins a normalization collision (e.g. two
		// terms that only differ in case) -- deterministic, and matches
		// SubjectTerms' own "first occurrence" dedup convention
		// (candidate.go's SubjectTerms).
		if _, exists := normalizedTerms[norm]; !exists {
			normalizedTerms[norm] = term
		}
	}
	if len(normalizedTerms) == 0 {
		return nil
	}

	results := map[string][]IdentityMatch{}
	for _, row := range rows {
		for norm, mechanism := range matchedTermsForRow(row, normalizedTerms) {
			original := normalizedTerms[norm]
			results[original] = append(results[original], IdentityMatch{Row: row, Mechanism: mechanism})
		}
	}
	return results
}

// matchedTermsForRow reports EVERY normalized term row matches, each
// tagged with its own mechanism -- a row genuinely claiming two DIFFERENT
// query terms (via any combination of label/alias/provider-alias) is
// reported for BOTH, never silently collapsed to one (that would be a
// recall loss this function has no need to accept). Within one term,
// label/alias/provider-alias are still mutually exclusive in KEY-CLASS
// PRIORITY order (label wins, then alias, then provider-alias), mirroring
// NodeCandidate's own single-candidate mutual-exclusivity discipline --
// scanned in that fixed order so the first class found for a given term
// is the one kept.
func matchedTermsForRow(row IdentityRow, normalizedTerms map[string]string) map[string]contextfabric.MatchMechanism {
	matched := map[string]contextfabric.MatchMechanism{}
	if label := NormalizeAliasTerm(row.Label); label != "" {
		if _, ok := normalizedTerms[label]; ok {
			matched[label] = contextfabric.MatchExact
		}
	}
	for _, alias := range row.Aliases {
		a := NormalizeAliasTerm(alias)
		if a == "" {
			continue
		}
		if _, ok := normalizedTerms[a]; !ok {
			continue
		}
		if _, already := matched[a]; !already {
			matched[a] = contextfabric.MatchAlias
		}
	}
	for _, alias := range row.ProviderAliases {
		a := NormalizeAliasTerm(alias)
		if a == "" {
			continue
		}
		if _, ok := normalizedTerms[a]; !ok {
			continue
		}
		if _, already := matched[a]; !already {
			matched[a] = contextfabric.MatchProviderKey
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return matched
}
