package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// reuseTimeAxisKeyMaxLength is the stored column's bound
// (ck_acr_cf_investigation_results_time_axis_key, migration 0013).
const reuseTimeAxisKeyMaxLength = 128

// GrantScopedTimeAxisKey widens the reuse key's time-axis dimension with a
// digest of a repository-restricted caller's grant (CHAOS-7127).
//
// The engine's embedded-subject gate (engine_fact_gate.go) removes, from a
// restricted caller's facts, the rows naming subjects outside its grant, so
// a restricted caller's stored answer is built from a FILTERED fact set. The
// reuse key has no other grant component: without this, that answer could be
// served to a caller with a different grant (reduced) and, worse, an
// unrestricted caller's unfiltered answer could be served to a restricted
// one. Both sides of the key -- the lookup (tryReuseWithReading) and the
// engine's only Save (saveResult) -- apply it, so they cannot disagree.
//
// Unrestricted and universal callers keep the key unchanged, byte for byte:
// their rows and hits are exactly what they were.
//
// A key that would exceed the stored column's bound returns "", which both
// sides already treat as never reusable (lookup misses; Save stores it as
// "unkeyed"): fail closed, never a failed Save.
func GrantScopedTimeAxisKey(principal storage.Principal, axisKey string) string {
	if axisKey == "" || classifyStoredResultPrincipalScope(principal) != StoredResultScopeRestricted {
		return axisKey
	}
	scoped := axisKey + "+g:" + reuseGrantDigest(principal.RepositoryScopes)
	if len(scoped) > reuseTimeAxisKeyMaxLength {
		return ""
	}
	return scoped
}

// RequestScopeTimeAxisKey widens the reuse key's time-axis dimension with a
// digest of the repository slugs the caller named in the request. The census
// that identifies a ticket key or a pull request number runs inside that
// scope, so an answer built under one scope must not be served to a request
// under another, or to one with none: the unscoped census would see every
// holder of the key and ask which one. An unscoped request keeps its key
// byte for byte. A key that would exceed the stored column's bound returns "",
// which the lookup misses and Save stores as never reusable.
func RequestScopeTimeAxisKey(request InvestigationRequest, axisKey string) string {
	if axisKey == "" || len(request.RequestedScope.RepositorySlugs) == 0 {
		return axisKey
	}
	scoped := axisKey + "+s:" + reuseGrantDigest(request.RequestedScope.RepositorySlugs)
	if len(scoped) > reuseTimeAxisKeyMaxLength {
		return ""
	}
	return scoped
}

// reuseGrantDigest is a stable digest of a grant: trimmed, de-duplicated and
// sorted slugs, so the same grant in any order keys the same. 128 bits.
func reuseGrantDigest(scopes []string) string {
	seen := map[string]struct{}{}
	slugs := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		slug := strings.TrimSpace(scope)
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	sum := sha256.Sum256([]byte(strings.Join(slugs, "\x00")))
	return hex.EncodeToString(sum[:16])
}
