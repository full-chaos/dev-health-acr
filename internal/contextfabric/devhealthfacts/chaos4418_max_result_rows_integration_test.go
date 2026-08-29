package devhealthfacts_test

import (
	"context"
	"fmt"
	"net"
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
			// Logged, not asserted on: whether ClickHouse's own
			// exception code survives into the ReadFacts error is a
			// question this test ANSWERS rather than presumes, and the
			// answer belongs in the PR body from an observed CI run --
			// asserting a guessed error string would be a claim without
			// evidence, which is the failure mode this whole finding's
			// red/green pair exists to avoid. The load-bearing claim
			// here is that the read FAILS at the driver default and
			// SUCCEEDS at the configured cap; that pair is asserted.
			t.Logf("CHAOS-4418 R3 red half, verbatim ReadFacts error at MaxResultRows=1000: %v", err)
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
