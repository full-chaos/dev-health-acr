package graphrank

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// ScopeMatch reports whether a node/edge's decoded authorization list (a
// plain []string of "owner/repo" slugs -- however the backend actually
// stores that list; zepgraph decodes its pipe-encoded attribute string
// before calling this, falkorgraph can pass a native list property
// directly) admits the caller-side scope value. value is one entry from a
// principal's storage.Principal.RepositoryScopes (or a requested-scope
// filter), which, per internal/auth.RepositoryAllowed, may be an exact
// "owner/repo" slug, the global wildcard "*", or an "owner/*" wildcard.
//
// This is the exact wildcard-matching core zepgraph's scopeContains used
// (Codex findings G3(a)/G3(b): a missing/empty list must deny, never
// authorize, regardless of how permissive the caller-side value is; an
// "owner/*" match must validate each decoded entry as a well-formed slug via
// internal/auth.NormalizeRepositorySlug before trusting its owner prefix, so
// a malformed entry like "acme/" or "acme/not/real" cannot satisfy the
// wildcard by prefix alone) -- extracted unchanged so it cannot drift
// between backends. Backend-specific "is this list absent/malformed at the
// wire level" handling (zepgraph's fail-closed sentinel for an unencodable
// write) stays in the backend package; entries reaching this function are
// assumed to already be the backend's best-effort decoded list, which may
// legitimately be empty (denies, per the caller loop below).
func ScopeMatch(entries []string, value string) bool {
	value = strings.TrimSpace(value)
	if value == "*" {
		return true
	}
	if owner, ok := strings.CutSuffix(value, "/*"); ok && owner != "" {
		owner = strings.ToLower(owner)
		for _, entry := range entries {
			normalized, err := auth.NormalizeRepositorySlug(entry)
			if err != nil {
				continue
			}
			if entryOwner, _, _ := strings.Cut(normalized, "/"); entryOwner == owner {
				return true
			}
		}
		return false
	}
	for _, entry := range entries {
		if entry == value {
			return true
		}
	}
	return false
}

// AnyScopeMatch reports whether ScopeMatch admits any of values.
func AnyScopeMatch(entries []string, values []string) bool {
	for _, value := range values {
		if ScopeMatch(entries, value) {
			return true
		}
	}
	return false
}
