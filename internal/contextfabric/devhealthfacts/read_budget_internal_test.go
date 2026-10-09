package devhealthfacts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

type stubQueryClient struct {
	queryErr error
	rows     contextpacket.ClickHouseRowScanner
	record   func(context.Context)
}

func (c stubQueryClient) Query(ctx context.Context, _ string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.record != nil {
		c.record(ctx)
	}
	return c.rows, c.queryErr
}

type stubRows struct{ err error }

func (r stubRows) Next() bool        { return false }
func (r stubRows) Scan(...any) error { return nil }
func (r stubRows) Err() error        { return r.err }
func (r stubRows) Close() error      { return nil }

func TestMeasuredClientTurnsABudgetExceptionIntoANamedErrorWithTheMeasuredFacts(t *testing.T) {
	t.Parallel()
	stats := &ReadStats{}
	stats.add(100, 4096)
	stats.add(23, 1000)
	ctx := contextWithReadStats(context.Background(), stats)
	exception := &clickhousedriver.Exception{Code: 307, Message: "Limit for (rows or bytes) to read exceeded"}

	for name, client := range map[string]contextpacket.ClickHouseQueryClient{
		"at query":     NewMeasuredQueryClient(stubQueryClient{queryErr: exception}, 1<<27),
		"at iteration": NewMeasuredQueryClient(stubQueryClient{rows: stubRows{err: exception}}, 1<<27),
	} {
		rows, err := client.Query(ctx, "SELECT 1", nil)
		if err == nil {
			err = rows.Err()
		}
		var budget *BudgetExceededError
		if !errors.As(err, &budget) {
			t.Fatalf("%s: error = %v, want a BudgetExceededError", name, err)
		}
		if budget.Code != 307 || budget.CapBytes != 1<<27 || budget.ReadRows != 123 || budget.ReadBytes != 5096 {
			t.Fatalf("%s: %+v, want code 307 cap %d rows 123 bytes 5096", name, budget, 1<<27)
		}
		if code, ok := runtimeclickhouse.QueryBudgetExceededCode(err); !ok || code != 307 {
			t.Fatalf("%s: the wrapped error is no longer classified as a budget exception (code %d ok %v)", name, code, ok)
		}
	}
}

func TestMeasuredClientPassesOtherErrorsThroughUnchanged(t *testing.T) {
	t.Parallel()
	plain := errors.New("connection reset")
	client := NewMeasuredQueryClient(stubQueryClient{queryErr: plain}, 1<<27)
	if _, err := client.Query(context.Background(), "SELECT 1", nil); err != plain {
		t.Fatalf("error = %v, want the original error untouched", err)
	}
	other := &clickhousedriver.Exception{Code: 60, Message: "unknown table"}
	client = NewMeasuredQueryClient(stubQueryClient{rows: stubRows{err: other}}, 1<<27)
	rows, err := client.Query(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var budget *BudgetExceededError
	if got := rows.Err(); errors.As(got, &budget) || got != other {
		t.Fatalf("a non-budget exception was wrapped: %v", got)
	}
}

func TestReadStatsInstrumentationLogsMeasuredReadsForEveryStatement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		err     error
		outcome string
	}{
		{"ok", nil, "outcome=ok"},
		{"budget", &clickhousedriver.Exception{Code: 307}, "outcome=budget_exceeded"},
		{"other", errors.New("connection reset"), "outcome=error"},
	} {
		var buffer bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
		ticks := []time.Time{time.Unix(100, 0), time.Unix(100, 0).Add(1500 * time.Millisecond)}
		instr := NewReadStatsInstrumentation(readers.NoopInstrumentation{}, logger, func() time.Time {
			now := ticks[0]
			ticks = ticks[1:]
			return now
		})
		ctx, finish := instr.StartQuery(context.Background(), "ReadRepositoryThemeMix", true)
		readStatsFromContext(ctx).add(288509, 67158099)
		finish(tc.err)
		line := buffer.String()
		for _, want := range []string{"level=INFO", "devhealthfacts.read_stats", "reader=ReadRepositoryThemeMix", "read_rows=288509", "read_bytes=67158099", "elapsed_ms=1500", tc.outcome} {
			if !strings.Contains(line, want) {
				t.Fatalf("%s: read stats line %q lacks %q", tc.name, line, want)
			}
		}
		if strings.Contains(line, "connection reset") {
			t.Fatalf("%s: the line carries the error text: %q", tc.name, line)
		}
	}
}

func TestRepoMixRowLimitHoldsEveryRepositoryWindowAndSpanRowPlusOneProbe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ repos, windows, want int }{{1, 1, 3}, {60, 2, 181}, {500, 2, 1501}, {500, 1, 1001}} {
		if got := repoMixRowLimit(tc.repos, tc.windows); got != tc.want {
			t.Fatalf("repoMixRowLimit(%d, %d) = %d, want %d", tc.repos, tc.windows, got, tc.want)
		}
	}
	statement := repoMixStatementScoped([]factTimeBound{{}}, membershipScope{mode: membershipScopeIDs, ids: []string{"a"}}, 1001)
	if !strings.HasSuffix(statement, "\nLIMIT 1001") {
		t.Fatalf("statement does not end with the sized LIMIT: %q", statement[len(statement)-40:])
	}
}
