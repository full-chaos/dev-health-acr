package devhealthsource_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// CHAOS-6182: ClickHouseOrgSource is the production organization-discovery
// adapter acr-projector runs at the start of every tick. Its job is one
// bounded, ordered, parameterized eligibility read, so these tests pin
// exactly that: the statement it issues, the bindings it supplies, which
// organizations it admits and excludes, and how it treats each failure
// phase.
//
// It gets its OWN recording client rather than reusing fakeClient: that
// fake asserts every statement carries an org_id binding (see
// requireOrgIDBinding), and this is the one query in the package that
// legitimately has none -- it asks WHICH organizations exist.

type recordingOrgQueryClient struct {
	statements []string
	bindings   [][]contextpacket.ClickHouseBinding
	rows       [][]any
	queryErr   error
	scanner    *stubOrgScanner
	// deadline captures whether the context the client received carried
	// one, which is how ListOrgs' own bound is observable at all.
	hadDeadline bool
	deadlineAt  time.Time
}

func (c *recordingOrgQueryClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	c.bindings = append(c.bindings, bindings)
	c.deadlineAt, c.hadDeadline = ctx.Deadline()
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	if c.scanner != nil {
		return c.scanner, nil
	}
	return &stubOrgScanner{rows: c.rows}, nil
}

type stubOrgScanner struct {
	rows    [][]any
	row     int
	scanErr error
	iterErr error
	closed  bool
}

func (s *stubOrgScanner) Next() bool { return s.row < len(s.rows) }

func (s *stubOrgScanner) Scan(dest ...any) error {
	if s.scanErr != nil {
		return s.scanErr
	}
	row := s.rows[s.row]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *uint64:
			*value = row[index].(uint64)
		case *time.Time:
			*value = row[index].(time.Time)
		default:
			return errors.New("devhealthsource_test: unsupported ListOrgs scan destination")
		}
	}
	s.row++
	return nil
}

func (s *stubOrgScanner) Err() error   { return s.iterErr }
func (s *stubOrgScanner) Close() error { s.closed = true; return nil }

// orgRow builds one eligibility row in the column order
// orgDiscoveryStatement selects: org_id, repo_rows, last_activity.
func orgRow(orgID string, repoRows uint64, lastActivity time.Time) []any {
	return []any{orgID, repoRows, lastActivity}
}

func skipReasons(skipped []contextfabric.SkippedOrg) map[string]contextfabric.OrgSkipReason {
	byOrg := map[string]contextfabric.OrgSkipReason{}
	for _, skip := range skipped {
		byOrg[skip.OrgID] = skip.Reason
	}
	return byOrg
}

func TestNewClickHouseOrgSourceRequiresAClient(t *testing.T) {
	t.Parallel()
	if _, err := devhealthsource.NewClickHouseOrgSource(nil); err == nil {
		t.Fatal("expected a refusal for a nil query client")
	}
	if _, err := devhealthsource.NewClickHouseOrgSourceWithWindow(nil, time.Hour); err == nil {
		t.Fatal("expected a refusal for a nil query client")
	}
}

// TestClickHouseOrgSourceIssuesOneBoundedEligibilityRead pins the statement
// and its bindings. The properties are load-bearing: ONE query covers every
// organization (a per-organization query at a 15s tick cadence is the cost
// this design exists to avoid), the read is bounded by a SERVER-side limit
// (a caller-side trim would still have scanned everything), blank ids are
// excluded in SQL, and results are ordered so the caller's union is
// deterministic.
func TestClickHouseOrgSourceIssuesOneBoundedEligibilityRead(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	client := &recordingOrgQueryClient{rows: [][]any{
		orgRow("org-a", 2, now), orgRow("org-b", 1, now),
	}}
	source, err := devhealthsource.NewClickHouseOrgSource(client)
	if err != nil {
		t.Fatalf("new org source: %v", err)
	}
	result, err := source.ListOrgs(context.Background())
	if err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if len(result.OrgIDs) != 2 || result.OrgIDs[0] != "org-a" || result.OrgIDs[1] != "org-b" {
		t.Fatalf("OrgIDs = %#v, want [org-a org-b]", result.OrgIDs)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("Skipped = %#v, want empty", result.Skipped)
	}
	if len(client.statements) != 1 {
		t.Fatalf("eligibility must be ONE grouped query, not one per organization; got %d", len(client.statements))
	}
	statement := client.statements[0]
	for _, fragment := range []string{
		"FROM repos", "FROM work_items", "FROM git_pull_requests",
		"UNION ALL", "org_id != ''", "GROUP BY org_id", "ORDER BY org_id ASC", "LIMIT {row_limit:UInt32}",
		// Explicit casts: work_items' timestamps carry no timezone while
		// the other two do, and count() is UInt64 while a bare 0 is UInt8.
		"toDateTime64(", "toUInt64(",
	} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("statement missing %q:\n%s", fragment, statement)
		}
	}
	if strings.Contains(statement, "FINAL") {
		t.Fatalf("discovery reads only grouping keys and aggregate maxima; FINAL cannot change either:\n%s", statement)
	}
	if len(client.bindings[0]) != 1 || client.bindings[0][0].Name != "row_limit" {
		t.Fatalf("bindings = %#v, want exactly one row_limit binding", client.bindings[0])
	}
	if got := client.bindings[0][0].Value; got != uint32(devhealthsource.OrgDiscoveryLimit) {
		t.Fatalf("row_limit = %#v, want uint32(%d)", got, devhealthsource.OrgDiscoveryLimit)
	}
}

// TestClickHouseOrgSourceAppliesTheEligibilityPredicate is the ruling this
// adapter exists to satisfy: a shared ClickHouse holds scores of throwaway
// tenants, and discovery must not turn each of them into a graph. Every
// cell of the predicate's own domain is here, with the SKIP REASON asserted
// -- a skipped organization reported without a reason is a tenant an
// operator cannot find out about.
func TestClickHouseOrgSourceAppliesTheEligibilityPredicate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	window := 720 * time.Hour
	client := &recordingOrgQueryClient{rows: [][]any{
		// Eligible: repositories, active within the window.
		orgRow("org-active", 3, now.Add(-24*time.Hour)),
		// Eligible at the boundary: exactly ON the window edge is inside
		// it (the comparison is strictly-before, so "exactly window old"
		// is retained -- a boundary that swung the other way would drop an
		// organization on the day it hit the limit).
		orgRow("org-boundary", 1, now.Add(-window)),
		// Skipped, inactive: boundary minus one second.
		orgRow("org-stale", 1, now.Add(-window-time.Second)),
		// Skipped, no repository: work items or pull requests only.
		orgRow("org-no-repo", 0, now),
		// Skipped for the FIRST failing condition, not the second: an
		// organization with no repository AND no recent activity reports
		// no_repo, so the reason names something an operator can act on.
		orgRow("org-neither", 0, now.Add(-10*window)),
	}}
	source, err := devhealthsource.NewClickHouseOrgSourceWithWindow(client, window)
	if err != nil {
		t.Fatalf("new org source: %v", err)
	}
	devhealthsource.SetOrgSourceClockForTest(source, func() time.Time { return now })

	result, err := source.ListOrgs(context.Background())
	if err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if len(result.OrgIDs) != 2 || result.OrgIDs[0] != "org-active" || result.OrgIDs[1] != "org-boundary" {
		t.Fatalf("OrgIDs = %#v, want [org-active org-boundary]", result.OrgIDs)
	}
	reasons := skipReasons(result.Skipped)
	for orgID, want := range map[string]contextfabric.OrgSkipReason{
		"org-stale":   contextfabric.OrgSkipReasonInactive,
		"org-no-repo": contextfabric.OrgSkipReasonNoRepo,
		"org-neither": contextfabric.OrgSkipReasonNoRepo,
	} {
		if got := reasons[orgID]; got != want {
			t.Fatalf("%s skip reason = %q, want %q", orgID, got, want)
		}
	}
	if len(result.Skipped) != 3 {
		t.Fatalf("Skipped = %#v, want exactly three entries", result.Skipped)
	}
}

// TestClickHouseOrgSourceZeroWindowDisablesTheActivityCondition: zero is a
// MEANINGFUL value, not "unset". An operator who wants every
// repository-owning organization projected says so with 0, and condition 1
// still applies -- which is why this asserts the no_repo row is still
// skipped rather than just counting admissions.
func TestClickHouseOrgSourceZeroWindowDisablesTheActivityCondition(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, window := range []time.Duration{0, -time.Hour} {
		t.Run(window.String(), func(t *testing.T) {
			client := &recordingOrgQueryClient{rows: [][]any{
				orgRow("org-ancient", 1, now.Add(-100*365*24*time.Hour)),
				orgRow("org-no-repo", 0, now),
			}}
			source, err := devhealthsource.NewClickHouseOrgSourceWithWindow(client, window)
			if err != nil {
				t.Fatalf("new org source: %v", err)
			}
			devhealthsource.SetOrgSourceClockForTest(source, func() time.Time { return now })
			result, err := source.ListOrgs(context.Background())
			if err != nil {
				t.Fatalf("list orgs: %v", err)
			}
			if len(result.OrgIDs) != 1 || result.OrgIDs[0] != "org-ancient" {
				t.Fatalf("OrgIDs = %#v, want [org-ancient] -- the activity condition is off, the repository one is not", result.OrgIDs)
			}
			if reasons := skipReasons(result.Skipped); reasons["org-no-repo"] != contextfabric.OrgSkipReasonNoRepo {
				t.Fatalf("the repository condition must still apply; skipped = %#v", result.Skipped)
			}
		})
	}
}

// TestClickHouseOrgSourceBoundsTheReadWithItsOwnTimeout: the read runs at
// the head of every tick, so it must fail fast on a wedged ClickHouse
// rather than holding the tick open for the caller's (much longer) budget.
// An UNBOUNDED caller context must still reach the client bounded.
func TestClickHouseOrgSourceBoundsTheReadWithItsOwnTimeout(t *testing.T) {
	t.Parallel()
	client := &recordingOrgQueryClient{}
	source, err := devhealthsource.NewClickHouseOrgSource(client)
	if err != nil {
		t.Fatalf("new org source: %v", err)
	}
	before := time.Now()
	if _, err := source.ListOrgs(context.Background()); err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if !client.hadDeadline {
		t.Fatal("ListOrgs must bound its own read even when the caller's context has no deadline")
	}
	if budget := client.deadlineAt.Sub(before); budget <= 0 || budget > devhealthsource.OrgDiscoveryTimeout+time.Second {
		t.Fatalf("deadline budget = %s, want (0, %s]", budget, devhealthsource.OrgDiscoveryTimeout)
	}
}

// TestClickHouseOrgSourceReturnsNoOrganizationsWithoutError: an empty
// catalog is an ANSWER, not a failure. The coordinator treats an error as
// "keep the last-known set" and an empty success as "this is the set", so
// conflating them would either wedge a genuinely-emptied deployment or
// silently drop every tenant on a blip.
func TestClickHouseOrgSourceReturnsNoOrganizationsWithoutError(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewClickHouseOrgSource(&recordingOrgQueryClient{})
	if err != nil {
		t.Fatalf("new org source: %v", err)
	}
	result, err := source.ListOrgs(context.Background())
	if err != nil {
		t.Fatalf("an empty catalog must not be an error, got %v", err)
	}
	if len(result.OrgIDs) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("result = %#v, want empty", result)
	}
}

// TestClickHouseOrgSourceSurfacesEveryFailurePhase covers the three phases
// a row-scanning read can fail in -- query, scan, iteration -- each of
// which must reach the caller as an error rather than a short, plausible
// organization list. A partial list returned as a success is the dangerous
// shape: the coordinator would accept it as the live set.
func TestClickHouseOrgSourceSurfacesEveryFailurePhase(t *testing.T) {
	t.Parallel()
	failure := errors.New("clickhouse is unavailable")
	now := time.Now().UTC()
	for _, testCase := range []struct {
		name   string
		client *recordingOrgQueryClient
	}{
		{name: "query", client: &recordingOrgQueryClient{queryErr: failure}},
		{name: "scan", client: &recordingOrgQueryClient{scanner: &stubOrgScanner{rows: [][]any{orgRow("org-a", 1, now)}, scanErr: failure}}},
		{name: "iteration", client: &recordingOrgQueryClient{scanner: &stubOrgScanner{rows: [][]any{orgRow("org-a", 1, now)}, iterErr: failure}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source, err := devhealthsource.NewClickHouseOrgSource(testCase.client)
			if err != nil {
				t.Fatalf("new org source: %v", err)
			}
			result, err := source.ListOrgs(context.Background())
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want it to wrap the underlying failure", err)
			}
			if result.OrgIDs != nil || result.Skipped != nil {
				t.Fatalf("a failed read must return nothing, got %#v", result)
			}
		})
	}
}

// TestClickHouseOrgSourceClosesItsRows: the read runs every 15 seconds
// forever, so a leaked scanner is a slow-burn connection leak rather than
// an immediate failure.
func TestClickHouseOrgSourceClosesItsRows(t *testing.T) {
	t.Parallel()
	scanner := &stubOrgScanner{rows: [][]any{orgRow("org-a", 1, time.Now().UTC())}}
	source, err := devhealthsource.NewClickHouseOrgSource(&recordingOrgQueryClient{scanner: scanner})
	if err != nil {
		t.Fatalf("new org source: %v", err)
	}
	if _, err := source.ListOrgs(context.Background()); err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if !scanner.closed {
		t.Fatal("ListOrgs must close its row scanner")
	}
}

// TestClickHouseOrgSourceAgainstRealClickHouse executes the statement
// against a real ClickHouse server carrying the production schema
// (devhealthschema's own declaration, so a column-type or engine change
// breaks this rather than a hand-written fixture that agrees with the
// reader's mistake). A fake cannot prove that the three-way UNION ALL
// parses, that the two timestamp spellings (`DateTime64(3)` for work_items,
// `DateTime64(3, 'UTC')` for the other two) reconcile, or that the driver
// really hands back a String org_id, a UInt64 count and a time.Time.
//
// The container is SHARED with this package's other org-isolation tests, so
// other tests' rows are visible to a query that is (by design) not
// org-scoped. The assertions are therefore containment and ordering -- the
// adapter's actual contract -- not an exact set.
func TestClickHouseOrgSourceAgainstRealClickHouse(t *testing.T) {
	query, direct := orgIsolationClickHouseFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	window := 720 * time.Hour
	orgPrefix := sharedTestOrgID(t)
	orgActive, orgStale, orgNoRepo := orgPrefix+"-active", orgPrefix+"-stale", orgPrefix+"-norepo"

	// org-active: two repositories, one synced long ago but a work item
	// updated today -- which is exactly why the activity signal spans three
	// tables. A repos-only signal would read this organization as stale.
	for _, row := range []struct {
		id, org string
		synced  time.Time
	}{
		{"6182aaaa-0000-0000-0000-000000000001", orgActive, now.Add(-10 * window)},
		{"6182aaaa-0000-0000-0000-000000000002", orgActive, now.Add(-10 * window)},
		{"6182bbbb-0000-0000-0000-000000000003", orgStale, now.Add(-10 * window)},
	} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			row.id, row.org, orgPrefix+"/"+row.id, "github", row.synced, row.synced); err != nil {
			t.Fatalf("seed repos row: %v", err)
		}
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-6182", "6182aaaa-0000-0000-0000-000000000001", orgActive, "recent", "open", "", now, ""); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
	// org-no-repo: a work item and nothing else.
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-6182-NR", "00000000-0000-0000-0000-000000000000", orgNoRepo, "orphan", "open", "", now, ""); err != nil {
		t.Fatalf("seed orphan work item: %v", err)
	}
	// A blank org_id row: real enough to exist, never a tenant.
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"6182cccc-0000-0000-0000-000000000004", "", orgPrefix+"/blank", "github", now, now); err != nil {
		t.Fatalf("seed blank-org repos row: %v", err)
	}

	source, err := devhealthsource.NewClickHouseOrgSourceWithWindow(query, window)
	if err != nil {
		t.Fatalf("new org source: %v", err)
	}
	result, err := source.ListOrgs(ctx)
	if err != nil {
		t.Fatalf("list orgs against real ClickHouse: %v", err)
	}

	admitted := map[string]int{}
	for _, orgID := range result.OrgIDs {
		admitted[orgID]++
	}
	if admitted[orgActive] != 1 {
		t.Fatalf("an organization whose work items moved today must be admitted exactly once; got %d in %#v", admitted[orgActive], result.OrgIDs)
	}
	if admitted[orgStale] != 0 {
		t.Fatalf("an organization whose newest row predates the window must not be admitted; got %#v", result.OrgIDs)
	}
	if admitted[orgNoRepo] != 0 {
		t.Fatalf("an organization with no repository row must not be admitted; got %#v", result.OrgIDs)
	}
	if admitted[""] != 0 {
		t.Fatal("a blank org_id must never be returned as an organization")
	}
	if !sort.StringsAreSorted(result.OrgIDs) {
		t.Fatalf("results must be ordered by org_id; got %#v", result.OrgIDs)
	}

	reasons := skipReasons(result.Skipped)
	if reasons[orgStale] != contextfabric.OrgSkipReasonInactive {
		t.Fatalf("%s skip reason = %q, want inactive", orgStale, reasons[orgStale])
	}
	if reasons[orgNoRepo] != contextfabric.OrgSkipReasonNoRepo {
		t.Fatalf("%s skip reason = %q, want no_repo", orgNoRepo, reasons[orgNoRepo])
	}
	for _, skip := range result.Skipped {
		if skip.OrgID == "" {
			t.Fatal("a blank org_id must not even appear as a skip -- it is excluded in SQL")
		}
	}
}
