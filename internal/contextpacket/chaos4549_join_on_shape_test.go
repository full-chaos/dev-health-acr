package contextpacket_test

// CHAOS-4549 (chris, RULED 2026-08-29 "26"): the standing rule this ticket
// re-established -- every JOIN ON clause is a plain column-equality
// conjunction, checked statically instead of by an old-engine fixture --
// applies to every SQL producer in the estate, not just
// internal/contextfabric/devhealthfacts and devhealthsource. Codex R3
// review finding on this ticket's own PR: SourceQueryCatalogV1
// (source_queries.go) carries production JOINs of its own (e.g.
// pull_request_reviews.v1's INNER JOIN git_pull_requests AS p FINAL ON
// r.repo_id = p.repo_id AND r.number = p.number) and is executed against
// the pinned fixture (catalog_query_coverage_integration_test.go, one of
// this package's own tests), but this catalog was never swept by any
// CHAOS-4549 guard.
//
// MECHANISM: unlike devhealthfacts/devhealthsource, SourceQueryCatalogV1's
// entries are a plain []SourceQuery of already-fully-rendered SQL text
// (const string concatenation, no runtime query-building function to
// call and no client to fake) -- so this sweep is simpler: read
// SourceQuery.Statement directly off every catalog entry and check it,
// no execution needed at all.
import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func TestChaos4549SourceQueryCatalogJoinOnClausesArePortable(t *testing.T) {
	t.Parallel()
	if len(contextpacket.SourceQueryCatalogV1) == 0 {
		t.Fatal("SourceQueryCatalogV1 is empty -- nothing to sweep")
	}
	totalClauses, totalConjuncts := 0, 0
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		violations, clauses, conjuncts := chfixture.JoinONViolations(query.Statement)
		totalClauses += clauses
		totalConjuncts += conjuncts
		for _, violation := range violations {
			t.Errorf("%s: a JOIN ON conjunct is not exactly <operand> = <operand> (%q); the pre-26 ClickHouse analyzer rejects anything else in an ON clause (CHAOS-4549)\n%s", query.ID, violation, query.Statement)
		}
	}
	// Mirrors the devhealthfacts/devhealthsource sweeps' own coverage
	// guard (codex review finding there): a sweep that only iterates the
	// catalog without asserting it actually found any JOIN structure
	// would silently pass if the parser ever stopped matching ON at all.
	if totalClauses == 0 {
		t.Fatal("0 JOIN ON clauses found across SourceQueryCatalogV1 -- the ON/AND parser is not matching anything, not proof of portability")
	}
	t.Logf("CHAOS-4549: checked %d JOIN ON clauses (%d conjuncts) across %d catalog statements (contextpacket.SourceQueryCatalogV1)", totalClauses, totalConjuncts, len(contextpacket.SourceQueryCatalogV1))
}
