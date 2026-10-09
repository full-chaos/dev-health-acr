package devhealthfacts

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

// instrumentedProvider decorates one contextfabric.FactProvider so every
// ReadFacts call carries instr on the context it hands to the wrapped
// provider -- specifically, so any
// github.com/full-chaos/dev-health-go/readers.QueryOrgScoped call the
// wrapped provider's readers.ReadXxx helpers make underneath (see e.g.
// metrics.go's readRepositoryMetrics) reports through instr instead of
// readers' default readers.NoopInstrumentation. QueryOrgScoped reads its
// Instrumentation off the SAME ctx its caller passed in (readers/query.go),
// so wiring it in here -- at the ReadFacts entry point, per call -- is the
// one seam that reaches every reader this package calls, without touching
// each domain file's ReadFacts body individually.
type instrumentedProvider struct {
	inner contextfabric.FactProvider
	instr readers.Instrumentation
}

func (p instrumentedProvider) Capability() contextfabric.FactCapability {
	return p.inner.Capability()
}

func (p instrumentedProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	ctx = context.WithValue(ctx, projectMixInstrumentationKey{}, p.instr)
	return p.inner.ReadFacts(readers.ContextWithInstrumentation(ctx, p.instr), principal, query)
}

// NewInstrumentedProviders wraps NewProviders' result so every fact read
// this package serves reports through instr's
// github.com/full-chaos/dev-health-go/readers.Instrumentation hook
// (CHAOS-4377). instr may be nil -- e.g. a caller with no logger available
// yet -- in which case this returns NewProviders' own providers unchanged;
// a ctx that never had an Instrumentation wired in behaves exactly like
// readers.NoopInstrumentation, so this is never a required call.
//
// WHY the production caller (internal/runtime/hosted/open.go) always wires
// this with readers.NewSlogInstrumentation, never
// readers.NewOTelInstrumentation: internal/contextfabric/modelprovider/
// provider.go's suppressGenkitTelemetryExport unconditionally overwrites the
// GLOBAL otel.SetTracerProvider/otel.SetMeterProvider with no-op/discard
// providers whenever the model provider initializes, purely to stop Genkit
// exporting its own telemetry. The process's real OTLP export
// (internal/otelexport) deliberately never touches those globals -- it hands
// its providers explicitly to its own handlers -- and it already exports these
// slog lines through its log bridge. Pointing readers.NewOTelInstrumentation
// at the suppressed global would silently discard every reader-telemetry
// event it produced --
// the exact failure mode a "wire it in and forget it" instrumentation hook
// must not have. acr's actual telemetry idiom (AGENTS.md: "Structured
// logging uses log/slog") is log/slog, which is why acr is dev-health-go's
// first readers.SlogInstrumentation consumer (see that repo's README
// "Boundary corrections" section for the matching reasoning on the library
// side).
func NewInstrumentedProviders(client contextpacket.ClickHouseQueryClient, instr readers.Instrumentation) []contextfabric.FactProvider {
	return NewInstrumentedProvidersWithOperations(client, instr, nil)
}

// NewInstrumentedProvidersWithOperations is NewInstrumentedProviders with the
// operation holder of NewProvidersWithOperations.
func NewInstrumentedProvidersWithOperations(client contextpacket.ClickHouseQueryClient, instr readers.Instrumentation, operations *OperationHolder) []contextfabric.FactProvider {
	providers := NewProvidersWithOperations(client, operations)
	if instr == nil {
		return providers
	}
	wrapped := make([]contextfabric.FactProvider, len(providers))
	for i, provider := range providers {
		wrapped[i] = instrumentedProvider{inner: provider, instr: instr}
	}
	return wrapped
}

// ReadBudgetExceededMessage is the log message of the Warn line
// NewBudgetWarningInstrumentation writes, and ReadBudgetExceededReason its
// closed reason value (CHAOS-7257).
const (
	ReadBudgetExceededMessage = "devhealthfacts.read_budget_exceeded"
	ReadBudgetExceededReason  = "read_budget_exceeded"
)

// budgetWarningInstrumentation decorates a readers.Instrumentation so a
// ClickHouse read-budget exception (Code 307 TOO_MANY_BYTES, 158
// TOO_MANY_ROWS) on any readers.QueryOrgScopedNamed statement is ALSO logged at
// Warn, with a closed reason and the statement id.
//
// Why: before this, the roll-up statement that exceeded max_bytes_to_read on
// prod (CHAOS-7257) surfaced only as readers.SlogInstrumentation's Info line
// with error_class=query_error -- the same class as a network blip -- and as
// "devhealthfacts: query project theme mix failed" to the caller. A statement
// that outgrew its read budget fails again on every identical retry, so it is
// a defect to page on, not a transient to retry; this line is what an alert
// keys on.
//
// Only closed values are logged: the reason, the reader name (the statement
// id, a constant chosen at each call site), and the numeric ClickHouse code.
// Never the exception text (it carries query fragments and byte counts), never
// a row, an org id or a subject id.
type budgetWarningInstrumentation struct {
	next   readers.Instrumentation
	logger *slog.Logger
}

// NewBudgetWarningInstrumentation wraps next (nil = no other instrumentation)
// so a read-budget exception is logged at Warn through logger (nil =
// slog.Default()) in addition to whatever next records.
func NewBudgetWarningInstrumentation(next readers.Instrumentation, logger *slog.Logger) readers.Instrumentation {
	if next == nil {
		next = readers.NoopInstrumentation{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return budgetWarningInstrumentation{next: next, logger: logger}
}

// StartQuery implements readers.Instrumentation.
func (b budgetWarningInstrumentation) StartQuery(ctx context.Context, reader string, orgScoped bool) (context.Context, func(error)) {
	ctx, finish := b.next.StartQuery(ctx, reader, orgScoped)
	return ctx, func(err error) {
		if code, exceeded := runtimeclickhouse.QueryBudgetExceededCode(err); exceeded {
			b.logger.LogAttrs(ctx, slog.LevelWarn, ReadBudgetExceededMessage,
				slog.String("reason", ReadBudgetExceededReason),
				slog.String("reader", contextfabric.SanitizeLogAttr(reader)),
				slog.Int("clickhouse_code", int(code)),
			)
		}
		finish(err)
	}
}

// ReadStatsMessage is the log message of the per-statement read line.
const ReadStatsMessage = "devhealthfacts.read_stats"

// readStatsInstrumentation decorates a readers.Instrumentation so every
// readers.QueryOrgScopedNamed statement is ALSO logged at Info with what the
// server reported it read (rows, bytes), how long it took and how it ended.
// The statistics are collected by the measured query client (read_budget.go)
// into a ReadStats it finds on the context this decorator wires in.
//
// Only closed values are logged: the reader name (a constant chosen at each
// call site), numbers and a closed outcome. Never the exception text, a row,
// an org id or a subject id.
type readStatsInstrumentation struct {
	next   readers.Instrumentation
	logger *slog.Logger
	now    func() time.Time
}

// NewReadStatsInstrumentation wraps next (nil = no other instrumentation) so
// every statement reports its measured reads through logger (nil =
// slog.Default()). now is the caller's clock (this package reads the wall
// clock nowhere itself); a nil now reports elapsed_ms as 0.
func NewReadStatsInstrumentation(next readers.Instrumentation, logger *slog.Logger, now func() time.Time) readers.Instrumentation {
	if next == nil {
		next = readers.NoopInstrumentation{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return readStatsInstrumentation{next: next, logger: logger, now: now}
}

// StartQuery implements readers.Instrumentation.
func (r readStatsInstrumentation) StartQuery(ctx context.Context, reader string, orgScoped bool) (context.Context, func(error)) {
	stats := &ReadStats{}
	var started time.Time
	if r.now != nil {
		started = r.now()
	}
	ctx, finish := r.next.StartQuery(contextWithReadStats(ctx, stats), reader, orgScoped)
	return ctx, func(err error) {
		rows, bytes := stats.Snapshot()
		var elapsed time.Duration
		if r.now != nil {
			elapsed = r.now().Sub(started)
		}
		outcome := "ok"
		if _, exceeded := runtimeclickhouse.QueryBudgetExceededCode(err); exceeded {
			outcome = "budget_exceeded"
		} else if err != nil {
			outcome = "error"
		}
		r.logger.LogAttrs(ctx, slog.LevelInfo, ReadStatsMessage,
			slog.String("reader", contextfabric.SanitizeLogAttr(reader)),
			slog.Uint64("read_rows", rows),
			slog.Uint64("read_bytes", bytes),
			slog.Int64("elapsed_ms", elapsed.Milliseconds()),
			slog.String("outcome", contextfabric.SanitizeLogAttr(outcome)),
		)
		finish(err)
	}
}
