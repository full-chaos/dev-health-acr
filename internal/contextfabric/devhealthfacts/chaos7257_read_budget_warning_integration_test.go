package devhealthfacts_test

// CHAOS-7257 telemetry: a ClickHouse read-budget exception on a fact statement
// is logged at Warn with the closed reason read_budget_exceeded and the
// statement id -- never the exception text, never a row.
//
// Before this the statement that exceeded max_bytes_to_read on prod surfaced
// only as readers.SlogInstrumentation's Info line with error_class=query_error,
// the same class as a network blip.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

// finishRecorder is a readers.Instrumentation that records what it was told.
type finishRecorder struct {
	readers []string
	errs    []error
}

func (f *finishRecorder) StartQuery(ctx context.Context, reader string, _ bool) (context.Context, func(error)) {
	f.readers = append(f.readers, reader)
	return ctx, func(err error) { f.errs = append(f.errs, err) }
}

func TestBudgetWarningInstrumentationLogsClosedReasonAndStatementIDOnly(t *testing.T) {
	const secret = "SELECT secret_column FROM t WHERE org_id = 'org-secret-123' (row 42)"
	for _, tc := range []struct {
		name     string
		err      error
		wantWarn bool
		wantCode int64
	}{
		{"too many bytes", fmt.Errorf("query: %w", &clickhousedriver.Exception{Code: 307, Name: "DB::Exception", Message: secret}), true, 307},
		{"too many rows", &clickhousedriver.Exception{Code: 158, Name: "DB::Exception", Message: secret}, true, 158},
		{"another server exception", &clickhousedriver.Exception{Code: 60, Name: "DB::Exception", Message: secret}, false, 0},
		{"a plain error", errors.New(secret), false, 0},
		{"no error", nil, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &recordingSlogHandler{}
			next := &finishRecorder{}
			instr := devhealthfacts.NewBudgetWarningInstrumentation(next, slog.New(handler))
			ctx, finish := instr.StartQuery(context.Background(), "ReadProjectThemeMix", true)
			if ctx == nil {
				t.Fatal("StartQuery returned a nil context")
			}
			finish(tc.err)

			// The wrapped instrumentation always sees the query start and its
			// exact error: the decorator adds a line, it never swallows one.
			if len(next.readers) != 1 || next.readers[0] != "ReadProjectThemeMix" || len(next.errs) != 1 || next.errs[0] != tc.err {
				t.Fatalf("wrapped instrumentation saw readers %v errs %v, want [ReadProjectThemeMix] and the same error", next.readers, next.errs)
			}
			records := handler.snapshot()
			if !tc.wantWarn {
				if len(records) != 0 {
					t.Fatalf("logged %d record(s) for a non-budget outcome, want none", len(records))
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("logged %d record(s), want exactly 1", len(records))
			}
			record := records[0]
			if record.Level != slog.LevelWarn || record.Message != devhealthfacts.ReadBudgetExceededMessage {
				t.Fatalf("record = %v %q, want Warn %q", record.Level, record.Message, devhealthfacts.ReadBudgetExceededMessage)
			}
			attrs := recordAttrs(record)
			if len(attrs) != 3 {
				t.Fatalf("attrs = %v, want exactly reason, reader, clickhouse_code", attrs)
			}
			if got := attrs["reason"].String(); got != "read_budget_exceeded" {
				t.Errorf("reason = %q, want read_budget_exceeded", got)
			}
			if got := attrs["reader"].String(); got != "ReadProjectThemeMix" {
				t.Errorf("reader = %q, want the statement id ReadProjectThemeMix", got)
			}
			if got := attrs["clickhouse_code"].Int64(); got != tc.wantCode {
				t.Errorf("clickhouse_code = %d, want %d", got, tc.wantCode)
			}
			for key, value := range attrs {
				if strings.Contains(value.String(), "secret") || strings.Contains(value.String(), "org-") {
					t.Errorf("attr %s = %q carries exception text or an org id", key, value.String())
				}
			}
		})
	}
}

// A real ClickHouse under a read budget too small for the statement: the fact
// provider fails the read (as prod did), and the wired instrumentation says
// why -- once, at Warn, with the statement id.
func TestReadBudgetExceededOnRealClickHouseIsLoggedAtWarn(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(o *runtimeclickhouse.Options) {
		tiny := uint64(1024) // far below one granule of work_unit_investments
		o.MaxBytesToRead = &tiny
	})
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-7257-warn"
	seedCHAOS7257Parity(t, ctx, direct, orgID)

	handler := &recordingSlogHandler{}
	logger := slog.New(handler)
	providers := devhealthfacts.NewInstrumentedProviders(query, devhealthfacts.NewBudgetWarningInstrumentation(readers.NewSlogInstrumentation(logger, slog.LevelInfo), logger))
	provider := findProvider(t, providers, contextfabric.FactInvestment)

	_, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err == nil {
		t.Fatal("ReadFacts under a 1 KiB read budget succeeded; the budget did not bind, the measurement did not happen")
	}
	var warns []map[string]slog.Value
	var infoWithBudgetClass int
	for _, record := range handler.snapshot() {
		attrs := recordAttrs(record)
		switch {
		case record.Level == slog.LevelWarn && record.Message == devhealthfacts.ReadBudgetExceededMessage:
			warns = append(warns, attrs)
		case record.Message == "readers.query_org_scoped" && attrs["error_class"].String() == "query_error":
			infoWithBudgetClass++
		}
	}
	if len(warns) != 1 {
		t.Fatalf("Warn %q lines = %d, want exactly 1 (the failing statement); all records: %v", devhealthfacts.ReadBudgetExceededMessage, len(warns), handler.snapshot())
	}
	if warns[0]["reason"].String() != "read_budget_exceeded" || warns[0]["clickhouse_code"].Int64() != 307 || warns[0]["reader"].String() == "" {
		t.Fatalf("Warn attrs = %v, want reason read_budget_exceeded, clickhouse_code 307 and a reader", warns[0])
	}
	if infoWithBudgetClass != 1 {
		t.Errorf("the pre-existing Info line for the failed statement appeared %d times, want 1 (the Warn is an addition, not a replacement)", infoWithBudgetClass)
	}
	t.Logf("Warn: %v", warns[0])
}
