package contextfabric

import "strings"

// TeamCanonicalIDPrefix is THE prefix a team subject's CanonicalID carries,
// for every producer, owner and reader in this repository.
//
// It exists as one exported constant because it previously existed as two
// unexported ones that merely happened to agree textually: devhealthsource's
// team producer minted "team:" + id in teamCanonicalID, and devhealthfacts
// declared its own teamPrefix = "team:" to strip back off before querying
// capacity_forecasts, investment_metrics_daily, estimate_coverage_metrics_daily
// and the team health tables. Two constants that must agree, with nothing
// making them agree, is a coincidence rather than a contract -- and the
// grouped-cohort path, which knew about neither, published raw source-row keys
// as team identities for exactly that reason. Everything that mints or
// recovers a team identity now reads this one symbol.
const TeamCanonicalIDPrefix = "team:"

// TeamCanonicalID mints the canonical identity of a team from the raw key its
// producing source row carries (teams.id, and the team_id column of the fact
// tables keyed on it).
//
// It is a fixed-prefix concatenation, which makes it deterministic and
// injective: TeamCanonicalID(a) == TeamCanonicalID(b) exactly when a == b, so
// canonicalising a set of distinct raw keys can never collapse two teams into
// one identity. It is NOT org-namespaced, because every store and every query
// this identity reaches is already org-scoped; an identity minted here is only
// ever compared with another identity from the same organization.
//
// An empty raw key has no identity and is returned unchanged rather than as a
// bare prefix: "team:" is not the name of a team, and minting it would create
// a well-formed identity naming nothing, which is the failure mode this whole
// helper exists to remove rather than relocate.
func TeamCanonicalID(rawKey string) string {
	if rawKey == "" {
		return ""
	}
	if _, already := TeamRawKey(rawKey); already {
		// Already canonical. Minting again would produce "team:team:x",
		// an identity no reader can resolve -- and a caller that
		// canonicalises defensively at two layers is a normal thing to
		// happen, so this is idempotent by construction rather than by
		// every caller remembering.
		return rawKey
	}
	return TeamCanonicalIDPrefix + rawKey
}

// TeamRawKey recovers the source-row key from a canonical team identity, and
// reports whether the identity was canonical at all.
//
// The second return is the whole point: an identity that does not carry the
// prefix is NOT a team identity whose key happens to be itself -- it is a
// value no team reader can use, and the caller must be able to tell the
// difference rather than silently querying a raw string. This mirrors the
// rejection devhealthfacts.subjectIndex already performs, so that the reject
// rule lives in one place next to the mint rule it is the inverse of.
func TeamRawKey(canonicalID string) (string, bool) {
	raw := strings.TrimPrefix(canonicalID, TeamCanonicalIDPrefix)
	if raw == "" || raw == canonicalID {
		return "", false
	}
	return raw, true
}
