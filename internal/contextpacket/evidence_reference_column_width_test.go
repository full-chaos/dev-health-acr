package contextpacket_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// columnWidthRows enforces that scanEvidenceRow's Scan call requests exactly
// as many destinations as the statement actually projects. Unlike the
// hand-rolled fixture rows used elsewhere in this package (which return a
// fixed-width row regardless of what the statement text asks for), this
// derives the expected width from the real statement passed to the driver
// -- the same way a real ClickHouse driver would return exactly the columns
// named in the query, no more, no less. That is precisely the class of bug
// this regression test exists to catch: ResolveEvidenceReference's
// empty-locator branch builds its own SELECT column list independently of
// standardColumns/scanEvidenceRow (clickhouse.go), so it silently drifted
// out of sync -- projecting 10 columns while scanEvidenceRow scanned 11 --
// when event_at was added, and every other fixture in this package stayed
// green because none of them check width against the statement text.
type columnWidthRows struct {
	width int
	done  bool
}

func (r *columnWidthRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}

func (r *columnWidthRows) Scan(dest ...any) error {
	if len(dest) != r.width {
		return fmt.Errorf("clickhouse: column count mismatch: statement projects %d columns, scan requested %d", r.width, len(dest))
	}
	return nil
}

func (r *columnWidthRows) Err() error   { return nil }
func (r *columnWidthRows) Close() error { return nil }

type columnWidthClient struct{}

func (columnWidthClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	const prefix, suffix = "SELECT ", " FROM ("
	header := statement[len(prefix):]
	header = header[:strings.Index(header, suffix)]
	return &columnWidthRows{width: strings.Count(header, ",") + 1}, nil
}

// TestResolveEvidenceReference_emptyLocatorScanWidthMatchesItsOwnProjection
// covers only the empty-locatorHash branch: the non-empty/exact-hash branch
// uses "SELECT * FROM (...)", which always passes through whatever the
// inner catalog query (standardColumns) actually projects and so cannot
// drift the same way.
func TestResolveEvidenceReference_emptyLocatorScanWidthMatchesItsOwnProjection(t *testing.T) {
	rows := contextpacket.NewCatalogClickHouseRows(columnWidthClient{})
	if _, err := rows.ResolveEvidenceReference(context.Background(), "org-fixture", contractsv1.ResolvedScope{RepoID: "repo-1", RepoSlug: "owner/repo"}, "ci_pipeline_runs.v1", ""); err != nil {
		t.Fatalf("empty-locator resolve: %v (the empty-locator SELECT column list has drifted from scanEvidenceRow's destination count)", err)
	}
}
