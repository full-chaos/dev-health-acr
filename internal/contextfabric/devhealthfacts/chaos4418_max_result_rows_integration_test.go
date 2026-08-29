package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// clickHouseTooManyRowsOrBytes is ClickHouse's own TOO_MANY_ROWS_OR_BYTES
// error code, thrown when a result exceeds max_result_rows under the
// default result_overflow_mode of "throw". Used here only as a string the
// sanitized ReadFacts error must NOT contain -- see the R3 red half.
const clickHouseTooManyRowsOrBytes int32 = 396

// MetricsSeriesPerRepositoryRowCapForTest names the production cap in a
// const-expression context, so the R4 day-count scenario below derives its
// seed width from the same constant the query uses rather than a literal
// that could drift away from it.
const MetricsSeriesPerRepositoryRowCapForTest = devhealthfacts.MetricsSeriesPerRepositoryRowCap

// seedRepositoryMetricsDays inserts dayCount consecutive days for one
// repository in ONE batch. Deliberately not a loop of single-row Exec
// calls: the scenarios below seed up to 1,080 rows, and a round trip per
// row is minutes of CI wall clock spent proving nothing about the claim.
func seedRepositoryMetricsDays(t *testing.T, ctx context.Context, connection clickhousedriver.Conn, orgID, repoID string, start time.Time, dayCount int) {
	t.Helper()
	batch, err := connection.PrepareBatch(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at)`)
	if err != nil {
		t.Fatalf("prepare seed batch: %v", err)
	}
	for d := 0; d < dayCount; d++ {
		day := start.AddDate(0, 0, d)
		if err := batch.Append(repoID, orgID, day, uint32(1), uint32(1), 1.0, 0.0, nil, uint32(1), 0.1, day.Add(10*time.Hour)); err != nil {
			t.Fatalf("append seed row (repo %s day %d): %v", repoID, d, err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send seed batch: %v", err)
	}
}

// TestCHAOS4418RepositoryMetricsAgainstRealClickHouse is CHAOS-4418's own
// EXECUTED evidence for codex R3's confirmed BLOCKER and for the 64-row
// boundary the PR body claims -- neither of which this package's fakeClient
// can prove, because it returns whatever rows a test hands it verbatim and
// never executes SQL, never enforces a server-side row cap, and never
// applies `ORDER BY ... day DESC`.
func TestCHAOS4418RepositoryMetricsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "clickhouse/clickhouse-server:24.8", ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort(host, port.Port())

	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("clickhouse not ready for connections: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, statement := range devhealthschema.DDL("repo_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	dsn := "clickhouse://acr:acr@" + addr + "/default"
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// R3_the_driver_default_max_result_rows_errors_a_multi_repository_read
	// is the red/green pair codex R3's finding demands as EXECUTED
	// evidence (team-lead ruling: "a real-ClickHouse test that leaves the
	// client at the default and shows ReadFacts ERRORS ... then passes
	// with the new setting"). Red half: readRepositoryMetrics deliberately
	// carries no query-wide SQL LIMIT of its own (the R2 fix -- a
	// query-wide cap and a per-group cap do not compose into "each group
	// gets its fair share"), so with the production client left at
	// dev-health-go's own default MaxResultRows of 1,000 and ClickHouse's
	// default result_overflow_mode of "throw", a modest multi-repository
	// read FAILS THE WHOLE QUERY -- an error, not a truncated series, and
	// strictly worse than the starvation bug this ticket set out to fix.
	//
	// 12 repositories x 90 days (metricsSeriesDefaultWindow's own default
	// width) = 1,080 raw rows: over the driver's 1,000-row default, far
	// under this PR's configured cap.
	t.Run("R3_the_driver_default_max_result_rows_errors_a_multi_repository_read", func(t *testing.T) {
		const orgID = "org-4418-max-result-rows"
		const repoCount = 12
		const daysPerRepo = 90
		subjects := make([]contextfabric.SubjectRef, 0, repoCount)
		for r := 0; r < repoCount; r++ {
			repoID := fmt.Sprintf("77777777-7777-7777-7777-%012d", r)
			seedRepositoryMetricsDays(t, ctx, direct, orgID, repoID, start, daysPerRepo)
			subjects = append(subjects, repoSubject(repoID))
		}
		query := contextfabric.FactQuery{
			Time: repositoryMetricsTimeContext(start, start.AddDate(0, 0, daysPerRepo)),
			Kind: contextfabric.FactMetrics, Subjects: subjects,
		}

		t.Run("red_at_the_driver_default_of_1000", func(t *testing.T) {
			defaultClient, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
				DSN: dsn, DialTimeout: 10 * time.Second,
			})
			if err != nil {
				t.Fatalf("open default-options query client: %v", err)
			}
			t.Cleanup(func() { _ = defaultClient.Close() })
			provider := findProvider(t, devhealthfacts.NewProviders(defaultClient), contextfabric.FactMetrics)
			_, err = provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, query)
			if err == nil {
				t.Fatalf("ReadFacts() error = nil, want an error: %d repositories x %d days = %d raw rows exceeds the driver's own default MaxResultRows=1000", repoCount, daysPerRepo, repoCount*daysPerRepo)
			}
			// What the overflow error actually looks like, established by
			// running this (CHAOS-4418, team-lead condition 3). The
			// question asked was whether ClickHouse's own exception code
			// survives into the ReadFacts error. It does NOT, and that is
			// DELIBERATE, not a defect: readFailure (shared.go) accepts
			// the cause and never lets it reach the returned error,
			// because a raw SDK error can carry connection details,
			// internal hostnames and query fragments -- the same posture
			// falkorgraph's safeDependencyError takes, and what acr's own
			// "never log DSNs, raw payloads or request bodies" rule
			// requires. FactReadFailure has no cause field and no Unwrap.
			//
			// So the invariant worth pinning here is the sanitization
			// itself, which IS falsifiable: the day someone wires the raw
			// error through for diagnosability, these assertions go red
			// and force the corpus-safety question to be answered first.
			// Operator diagnosability ("the row cap fired" vs "the server
			// is down") needs a server-side telemetry seam, not a wider
			// error surface; this package has none today.
			var failure *contextfabric.FactReadFailure
			if !errors.As(err, &failure) {
				t.Fatalf("ReadFacts() error = %v (%T), want a *contextfabric.FactReadFailure", err, err)
			}
			if failure.State != contextfabric.SourceUnavailable {
				t.Fatalf("FactReadFailure.State = %v, want SourceUnavailable -- a failed read must degrade the source, never report it as available", failure.State)
			}
			for _, leak := range []string{addr, "acr:acr", "clickhouse://", "repo_metrics_daily", "SELECT", "max_result_rows", strconv.Itoa(int(clickHouseTooManyRowsOrBytes))} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("ReadFacts() error = %q leaks %q -- the raw driver error must never reach the returned error (readFailure's own doc comment; corpus safety)", err.Error(), leak)
				}
			}
		})

		t.Run("green_at_the_configured_cap", func(t *testing.T) {
			// Mirrors internal/runtime/hosted/clickhouse.go's own shipped
			// value; that package's own
			// TestClickHouseClientOptionsSetMaxResultRowsAboveTheDocumentedWorstCase
			// pins the derivation, this proves the EFFECT of configuring
			// it against a real server.
			const configuredMaxResultRows = 2 * contractsv1.ContextFabricMaxCohortMembersLimit * devhealthfacts.MetricsSeriesPerRepositoryRowCap
			fixedClient, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
				DSN: dsn, DialTimeout: 10 * time.Second, MaxResultRows: configuredMaxResultRows,
			})
			if err != nil {
				t.Fatalf("open configured-options query client: %v", err)
			}
			t.Cleanup(func() { _ = fixedClient.Close() })
			provider := findProvider(t, devhealthfacts.NewProviders(fixedClient), contextfabric.FactMetrics)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, query)
			if err != nil {
				t.Fatalf("ReadFacts() error = %v, want success with MaxResultRows=%d", err, configuredMaxResultRows)
			}
			if len(result.Facts) != repoCount {
				t.Fatalf("len(result.Facts) = %d, want exactly %d -- every requested repository must get its own fact", len(result.Facts), repoCount)
			}
		})
	})

	// R4_the_day_count_is_not_capped_by_the_per_repository_row_limit is
	// codex R4 finding 3's EXECUTED proof, and the only place it can be
	// executed: the defect lives in ClickHouse's own evaluation order --
	// `LIMIT ... BY repo_id` runs server-side, before Go sees a single
	// row -- so the fake client, which replays whatever rows a test hands
	// it, cannot produce the saturation at all. A window wider than
	// MetricsSeriesPerRepositoryRowCap is what makes len(rows) and the
	// true distinct-day count diverge.
	//
	// 250 days: comfortably past the 200-row per-repository cap, so a
	// day_count taken from the returned slice reports exactly 200 -- an
	// EXACT count a model may ground a claim in, and wrong by 50 days.
	t.Run("R4_the_day_count_is_not_capped_by_the_per_repository_row_limit", func(t *testing.T) {
		const orgID = "org-4418-day-count"
		const repoID = "55555555-5555-5555-5555-000000000001"
		const dayCount = MetricsSeriesPerRepositoryRowCapForTest + 50
		client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
			DSN: dsn, DialTimeout: 10 * time.Second, MaxResultRows: 2 * contractsv1.ContextFabricMaxCohortMembersLimit * devhealthfacts.MetricsSeriesPerRepositoryRowCap,
		})
		if err != nil {
			t.Fatalf("open query client: %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })
		seedRepositoryMetricsDays(t, ctx, direct, orgID, repoID, start, dayCount)
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: repositoryMetricsTimeContext(start, start.AddDate(0, 0, dayCount)),
			Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
		if len(result.Facts) != 1 {
			t.Fatalf("len(result.Facts) = %d, want exactly 1", len(result.Facts))
		}
		if got := result.Facts[0].Fields["day_count"].Integer; got == nil || *got != int64(dayCount) {
			t.Fatalf("day_count = %v, want the true %d distinct days -- the server-side per-repository cap returns only %d rows, so a count taken from them saturates and grounds a false exact count", got, dayCount, devhealthfacts.MetricsSeriesPerRepositoryRowCap)
		}
		// Truncation semantics unchanged by the day-count fix: the
		// 64-row per-fact cap is still what Truncated reports on.
		if !result.Truncated {
			t.Fatalf("result.Truncated = false, want true -- %d days still exceeds the %d-row per-fact cap", dayCount, contextfabric.MaxFactValueRows)
		}
		if got := len(result.Facts[0].Fields["daily_metrics"].Rows); got != contextfabric.MaxFactValueRows {
			t.Fatalf("daily_metrics rows = %d, want the unchanged %d-row per-fact cap", got, contextfabric.MaxFactValueRows)
		}
	})

	// B_the_64_row_per_fact_boundary_keeps_the_newest_days is the day-count
	// boundary check the PR body owed (team-lead condition 4). The fake
	// client cannot prove either half: `contextfabric.MaxFactValueRows` is
	// applied by capFactValueRows to whatever slice the SQL produced, and
	// it is the statement's own `ORDER BY repo_id, day DESC` -- executed
	// only by a real server -- that makes capFactValueRows' "keep the
	// first N" mean "keep the NEWEST N".
	t.Run("B_the_64_row_per_fact_boundary_keeps_the_newest_days", func(t *testing.T) {
		client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
			DSN: dsn, DialTimeout: 10 * time.Second, MaxResultRows: 2 * contractsv1.ContextFabricMaxCohortMembersLimit * devhealthfacts.MetricsSeriesPerRepositoryRowCap,
		})
		if err != nil {
			t.Fatalf("open query client: %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)

		read := func(t *testing.T, orgID, repoID string, dayCount int) contextfabric.FactProviderResult {
			t.Helper()
			seedRepositoryMetricsDays(t, ctx, direct, orgID, repoID, start, dayCount)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: repositoryMetricsTimeContext(start, start.AddDate(0, 0, dayCount)),
				Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if len(result.Facts) != 1 {
				t.Fatalf("len(result.Facts) = %d, want exactly 1", len(result.Facts))
			}
			return result
		}

		t.Run("exactly_at_the_cap_is_not_truncated", func(t *testing.T) {
			result := read(t, "org-4418-boundary-at", "66666666-6666-6666-6666-000000000001", contextfabric.MaxFactValueRows)
			if result.Truncated {
				t.Fatalf("result.Truncated = true, want false: exactly %d days is AT the per-fact cap, not past it", contextfabric.MaxFactValueRows)
			}
			if got := len(result.Facts[0].Fields["daily_metrics"].Rows); got != contextfabric.MaxFactValueRows {
				t.Fatalf("daily_metrics rows = %d, want %d", got, contextfabric.MaxFactValueRows)
			}
			if got := result.Facts[0].Fields["day_count"].Integer; got == nil || *got != int64(contextfabric.MaxFactValueRows) {
				t.Fatalf("day_count = %v, want %d", got, contextfabric.MaxFactValueRows)
			}
		})

		t.Run("one_past_the_cap_truncates_and_drops_the_OLDEST_day", func(t *testing.T) {
			const dayCount = contextfabric.MaxFactValueRows + 1
			result := read(t, "org-4418-boundary-past", "66666666-6666-6666-6666-000000000002", dayCount)
			if !result.Truncated {
				t.Fatalf("result.Truncated = false, want true: %d days is one past the per-fact cap", dayCount)
			}
			rows := result.Facts[0].Fields["daily_metrics"].Rows
			if len(rows) != contextfabric.MaxFactValueRows {
				t.Fatalf("daily_metrics rows = %d, want %d", len(rows), contextfabric.MaxFactValueRows)
			}
			// day_count reports the pre-cap series width, so a truncated
			// series stays diagnosable rather than looking like a series
			// that genuinely had only 64 days.
			if got := result.Facts[0].Fields["day_count"].Integer; got == nil || *got != int64(dayCount) {
				t.Fatalf("day_count = %v, want %d (the series width BEFORE the cap)", got, dayCount)
			}
			newest := start.AddDate(0, 0, dayCount-1).Format("2006-01-02")
			oldest := start.Format("2006-01-02")
			seen := map[string]bool{}
			for _, row := range rows {
				if day := row.Fields["day"].String; day != nil {
					seen[*day] = true
				}
			}
			if !seen[newest] {
				t.Fatalf("newest day %s absent from the capped series -- `ORDER BY repo_id, day DESC` plus capFactValueRows' keep-the-first-N must drop the OLDEST days, never the freshest", newest)
			}
			if seen[oldest] {
				t.Fatalf("oldest day %s survived the cap while %d days were seeded -- truncation is keeping the wrong end of the series", oldest, dayCount)
			}
		})
	})
}
