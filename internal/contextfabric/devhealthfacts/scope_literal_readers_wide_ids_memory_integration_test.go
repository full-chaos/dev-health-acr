package devhealthfacts_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// scopeLiteralBound is the frozen bound of one scope-literal reader at
// prod-width unit ids: the highest warm peak may not pass memoryMiB, and the
// rows read (stable for a fixed seed) may not pass readRows. A bound moves
// only by a new measurement over at least three hosted CI runs, never to fit
// a failing run.
type scopeLiteralBound struct {
	memoryMiB float64
	readRows  uint64
}

var scopeLiteralBounds = map[string]scopeLiteralBound{
	"repo_mix":                     {memoryMiB: 240, readRows: 1 << 40},
	"repo_units":                   {memoryMiB: 240, readRows: 1 << 40},
	"org_units":                    {memoryMiB: 240, readRows: 1 << 40},
	"owned_unit_count":             {memoryMiB: 240, readRows: 1 << 40},
	"project_rollup_repo_themes":   {memoryMiB: 240, readRows: 1 << 40},
	"project_rollup_repo_bugfix":   {memoryMiB: 240, readRows: 1 << 40},
	"project_rollup_evidence_arm":  {memoryMiB: 240, readRows: 1 << 40},
	"project_native_placement":     {memoryMiB: 240, readRows: 1 << 40},
	"project_native_theme_values":  {memoryMiB: 240, readRows: 1 << 40},
	"project_native_bugfix_values": {memoryMiB: 240, readRows: 1 << 40},
}

const scopeLiteralRepoUnits = 500

// seedRepositoryUnits adds wide-id units that carry an owned repository id and
// sit in the membership run: the shape's own units carry none, so the owned
// unit count and the project roll-up's repository arm would otherwise return
// no row. It returns the new scope ids.
func seedRepositoryUnits(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, s linkedProdShape) []string {
	t.Helper()
	at := "toDateTime64('2026-09-18 00:00:00', 3, 'UTC')"
	owned := make([]string, 0, s.ownedRepos)
	for i := 0; i < s.ownedRepos; i++ {
		owned = append(owned, "'"+repoUUID(fmt.Sprintf("shape-r-%d", i))+"'")
	}
	unit := "concat('wu-repo-', toString(number), '-', lower(hex(SHA256(concat('repo', toString(number))))))"
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
SELECT %s, %s, %s, toUUID([%s][1 + number %% %d]), 1.0, map('feature_delivery', 0.6, 'operational', 0.4), map(), '{}', %s, '%s' FROM numbers(%d)`,
		unit, at, at, strings.Join(owned, ","), s.ownedRepos, at, orgID, scopeLiteralRepoUnits)); err != nil {
		t.Fatalf("seed repository units: %v", err)
	}
	if err := direct.Exec(ctx, fmt.Sprintf(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)
SELECT '%s', 'issue', concat('n-repo-', toString(number)), %s, 'theme', 'feature_delivery', toDateTime64('2026-09-19 00:00:00', 3, 'UTC'), 'run-1' FROM numbers(%d)`,
		orgID, unit, scopeLiteralRepoUnits)); err != nil {
		t.Fatalf("seed repository unit membership: %v", err)
	}
	ids := make([]string, 0, scopeLiteralRepoUnits)
	for i := 0; i < scopeLiteralRepoUnits; i++ {
		ids = append(ids, fmt.Sprintf("wu-repo-%d-%x", i, sha256.Sum256([]byte(fmt.Sprintf("repo%d", i)))))
	}
	return ids
}

type scopeLiteralRun struct {
	mib      float64
	readRows uint64
	rows     int
	err      error
}

// runScopeLiteralCase runs one statement through the capped client and reads
// its peak and rows read from query_log, finished or failed.
func runScopeLiteralCase(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID string, c devhealthfacts.ScopeLiteralCase) scopeLiteralRun {
	t.Helper()
	mark := serverNowMicros(t, ctx, direct)
	rows := 0
	err := readers.QueryOrgScopedNamed(ctx, client, "ScopeLiteralWideIDs", c.SQL, orgID, c.IDs,
		func(readers.RowScanner) error { rows++; return nil }, c.Bindings...)
	if flushErr := direct.Exec(ctx, "SYSTEM FLUSH LOGS"); flushErr != nil {
		t.Fatalf("flush logs (measurement did not happen): %v", flushErr)
	}
	var logged, mem, readRows uint64
	if qErr := direct.QueryRow(ctx, `SELECT count(), max(memory_usage), max(read_rows) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_unit_investments'))`, mark).Scan(&logged, &mem, &readRows); qErr != nil {
		t.Fatalf("%s: read query_log (measurement did not happen): %v (run error %v)", c.Name, qErr, err)
	}
	if logged != 1 {
		t.Fatalf("%s: %d statements on work_unit_investments logged for one run, want 1 (measurement did not happen; run error %v)", c.Name, logged, err)
	}
	return scopeLiteralRun{mib: float64(mem) / (1 << 20), readRows: readRows, rows: rows, err: err}
}

// The scope-literal readers at the largest prod organization's shape with
// unit ids as wide as prod's: the scope literal is then about a megabyte, the
// dimension CHAOS-9664 showed sets the peak. Each reader runs once cold and
// linkedShapeRuns times warm under the API's cap and readonly=2; the highest
// warm peak must stay under the reader's frozen bound, and the rows read
// under theirs. Every reader must return rows, or the row measured nothing.
func TestScopeLiteralReadersMemoryWithProdWidthUnitIDs(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-scope-literal-wide-ids"
	query, direct, _, scopeIDs := newLinkedProdShapeStore(t, ctx, orgID, linkedWideIDProdShape)
	scopeIDs = append(scopeIDs, seedRepositoryUnits(t, ctx, direct, orgID, linkedWideIDProdShape)...)
	capped := contextpacket.NewMemoryCappedQueryClient(readonlyQueryClient{inner: query}, contextpacket.APIStatementMaxMemoryUsage)
	provider := findProvider(t, devhealthfacts.NewProviders(capped), contextfabric.FactInvestment)
	assertReadonlyInForce(t, ctx, capped, orgID)

	width := 0
	for _, id := range scopeIDs {
		width += len(id)
	}
	t.Logf("seeded: %d scope ids, average width %.1f characters", len(scopeIDs), float64(width)/float64(len(scopeIDs)))

	var repoIDs, projectIDs []string
	for i := 0; i < linkedWideIDProdShape.ownedRepos; i++ {
		repoIDs = append(repoIDs, repoUUID(fmt.Sprintf("shape-r-%d", i)))
	}
	for i := 0; i < linkedWideIDProdShape.teamProjects; i++ {
		projectIDs = append(projectIDs, fmt.Sprintf("linear:P-%d", i))
	}
	cases, err := devhealthfacts.ScopeLiteralCasesForTest(ctx, provider, orgID, scopeIDs, repoIDs, projectIDs)
	if err != nil {
		t.Fatalf("render the scope-literal readers: %v", err)
	}
	if len(cases) != len(scopeLiteralBounds) {
		t.Fatalf("%d readers rendered, %d bounds frozen", len(cases), len(scopeLiteralBounds))
	}

	var report strings.Builder
	fmt.Fprintf(&report, "prod-width unit ids, API cap %d MiB, %d warm runs after one cold run:\n", contextpacket.APIStatementMaxMemoryUsage>>20, linkedShapeRuns)
	for _, c := range cases {
		bound, ok := scopeLiteralBounds[c.Name]
		if !ok {
			t.Errorf("%s: no frozen bound", c.Name)
			continue
		}
		runScopeLiteralCase(t, ctx, capped, direct, orgID, c)
		var warm []scopeLiteralRun
		for i := 0; i < linkedShapeRuns; i++ {
			warm = append(warm, runScopeLiteralCase(t, ctx, capped, direct, orgID, c))
		}
		lowest, highest := -1.0, 0.0
		var highestRows uint64
		for i, r := range warm {
			var exception *clickhousedriver.Exception
			if r.err != nil {
				if errors.As(r.err, &exception) && exception.Code == 241 {
					t.Errorf("%s run %d: refused with code 241 at the %d MiB API cap (peak %.2f MiB): a finding, not a bound to raise", c.Name, i, contextpacket.APIStatementMaxMemoryUsage>>20, r.mib)
				} else {
					t.Errorf("%s run %d: %v", c.Name, i, r.err)
				}
			}
			if r.rows == 0 {
				t.Errorf("%s run %d: returned no rows, the row measured nothing", c.Name, i)
			}
			if lowest < 0 || r.mib < lowest {
				lowest = r.mib
			}
			if r.mib > highest {
				highest = r.mib
			}
			if r.readRows > highestRows {
				highestRows = r.readRows
			}
		}
		fmt.Fprintf(&report, "  %-30s peak lowest %7.2f MiB, highest %7.2f MiB, bound %6.1f MiB; rows read highest %d, bound %d\n",
			c.Name, lowest, highest, bound.memoryMiB, highestRows, bound.readRows)
		if highest > bound.memoryMiB {
			t.Errorf("%s: highest warm peak %.2f MiB is over the frozen bound %.1f MiB", c.Name, highest, bound.memoryMiB)
		}
		if highestRows > bound.readRows {
			t.Errorf("%s: rows read %d is over the frozen bound %d", c.Name, highestRows, bound.readRows)
		}
	}
	t.Log(report.String())
}
