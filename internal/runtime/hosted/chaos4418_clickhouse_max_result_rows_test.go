package hosted

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestClickHouseClientOptionsSetMaxResultRowsAboveTheDocumentedWorstCase is
// CHAOS-4418's own pin for codex R3's confirmed BLOCKER. It reads
// MaxResultRows off the Options this binary actually ships
// (clickHouseClientOptions, the same value openClickHouse hands
// NewClickHouseQueryClientWithOptions) and compares it against the worst
// case recomputed HERE from the two upstream constants -- deliberately not
// against clickHouseMaxResultRowsWorstCase, which is that same expression
// and so could only ever agree with itself.
//
// Three distinct regressions fail this test, and all three are the ways
// this finding could silently reopen:
//   - the MaxResultRows field is deleted from the shipped Options, leaving
//     dev-health-go's own zero-value default of 1,000
//     (clickhouse/options.go's `defaultPositiveUint(options.MaxResultRows,
//     1_000)`) with ClickHouse's default result_overflow_mode of "throw":
//     ~12 repositories x 90 days ERRORS the whole ReadFacts call;
//   - the shipped value is lowered below the documented worst case;
//   - either upstream constant grows (ContextFabricMaxCohortMembersLimit
//     widens the validated cohort ceiling, or MetricsSeriesPerRepositoryRowCap
//     widens readRepositoryMetrics' own per-repository `LIMIT n BY repo_id`)
//     past a shipped value that was not revisited with it.
func TestClickHouseClientOptionsSetMaxResultRowsAboveTheDocumentedWorstCase(t *testing.T) {
	// dev-health-go v0.6.2 made MaxResultRows *uint (nil = unset, use the
	// package default; a pointer to 0 = explicitly unlimited). A nil
	// pointer here is the exact "field deleted from shipped Options"
	// regression this test's own doc comment names first.
	shipped := clickHouseClientOptions(config.Config{}, nil).MaxResultRows
	if shipped == nil {
		t.Fatal("shipped MaxResultRows is nil (field left unset) -- unset means dev-health-go's own 1,000-row default, which ClickHouse's default result_overflow_mode=throw turns into a FAILED query for as few as ~12 repositories x 90 days")
	}
	if *shipped == 0 {
		t.Fatal("shipped MaxResultRows = 0, which now means EXPLICITLY unlimited, not unset -- the same failure mode as leaving it nil is possible, but spelled as an accidental 'no ceiling' rather than a missing field")
	}
	worstCase := uint(contractsv1.ContextFabricMaxCohortMembersLimit * devhealthfacts.MetricsSeriesPerRepositoryRowCap)
	if *shipped < worstCase {
		t.Fatalf("shipped MaxResultRows = %d, want >= %d (ContextFabricMaxCohortMembersLimit=%d x MetricsSeriesPerRepositoryRowCap=%d) -- readRepositoryMetrics carries no query-wide LIMIT of its own, so a cohort-shaped request at both ceilings is the real row count this client must be able to return",
			*shipped, worstCase, contractsv1.ContextFabricMaxCohortMembersLimit, devhealthfacts.MetricsSeriesPerRepositoryRowCap)
	}
}
