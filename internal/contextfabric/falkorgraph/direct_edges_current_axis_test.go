package falkorgraph

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The current-axis rule filters no node by its end and keeps an edge that
// lasted until the earlier of its end nodes ended; the strict rule (as_of,
// owned_by) keeps the window on the edge and on both nodes. Both are pinned as
// text: the live tests show what each returns.
func TestDirectEdgePageCurrentAxisPredicate(t *testing.T) {
	base := directread.EdgePageQuery{
		Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:r:1"}},
		Limit:   5, ValidAt: time.Unix(100, 0),
	}
	nodeEnd := func(alias string) string {
		return alias + ".valid_to_ns IS NULL OR " + alias + ".valid_to_ns > $tStart"
	}
	edgeRule := "(r.valid_to_ns IS NULL OR r.valid_to_ns > $tStart OR (a.valid_to_ns IS NOT NULL AND r.valid_to_ns >= a.valid_to_ns) OR (b.valid_to_ns IS NOT NULL AND r.valid_to_ns >= b.valid_to_ns))"
	started := func(alias string) string {
		return "(" + alias + ".valid_from_ns IS NULL OR " + alias + ".valid_from_ns <= $tEnd)"
	}

	current := base
	current.Current = true
	cypher, params := directEdgePageCypher("org", current)
	if !strings.Contains(cypher, edgeRule) {
		t.Fatalf("current axis: edge rule missing: %s", cypher)
	}
	for _, alias := range []string{"r", "a", "b"} {
		if !strings.Contains(cypher, started(alias)) {
			t.Fatalf("current axis: start bound of %s missing: %s", alias, cypher)
		}
	}
	for _, alias := range []string{"a", "b"} {
		if strings.Contains(cypher, nodeEnd(alias)) {
			t.Fatalf("current axis: node %s is filtered by its end: %s", alias, cypher)
		}
	}
	if params["tStart"] != int64(100_000_000_000) || params["tEnd"] != int64(100_000_000_000) {
		t.Fatalf("current axis params: %v", params)
	}

	strict, _ := directEdgePageCypher("org", base)
	for _, alias := range []string{"r", "a", "b"} {
		if !strings.Contains(strict, nodeEnd(alias)) || !strings.Contains(strict, started(alias)) {
			t.Fatalf("strict window on %s missing: %s", alias, strict)
		}
	}
	if strings.Contains(strict, edgeRule) {
		t.Fatalf("strict read carries the current-axis rule: %s", strict)
	}
}
