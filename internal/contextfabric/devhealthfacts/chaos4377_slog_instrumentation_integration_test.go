package devhealthfacts_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// recordingSlogHandler is a minimal slog.Handler that captures every record
// passed to it, mirroring dev-health-go's own
// readers/slog_instrumentation_test.go recordingHandler exactly -- so this
// test asserts an actually-emitted slog.Record's typed attributes, never a
// string match against formatted log output.
type recordingSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *recordingSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingSlogHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingSlogHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

func recordAttrs(r slog.Record) map[string]slog.Value {
	out := make(map[string]slog.Value, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value
		return true
	})
	return out
}

// TestCHAOS4377_InstrumentedProvidersEmitSlogRecordOnRealClickHouseRead
// exercises acr's REAL construction seam -- devhealthfacts.NewInstrumentedProviders,
// the exact call internal/runtime/hosted/open.go makes for the hosted
// runtime's fact registry -- against a real ClickHouse container (reusing
// CHAOS-3780's newCHAOS3780IntegrationClient/createCHAOS3780Tables helpers
// from chaos3780_findings_integration_test.go). This package's fakeClient
// never executes SQL, so it cannot prove the wiring actually reaches
// github.com/full-chaos/dev-health-go/readers.QueryOrgScoped -- only a real
// server evaluating a real query, through the real provider construction
// path, can.
//
// The assertion is on a decoded slog.Record's typed attributes (a
// recordingSlogHandler, the same pattern dev-health-go's own
// slog_instrumentation_test.go uses), never a substring match on formatted
// log text -- so a future change to the log message text or attribute
// ordering cannot make this test pass without the record actually existing.
func TestCHAOS4377_InstrumentedProvidersEmitSlogRecordOnRealClickHouseRead(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS3780Tables(t, ctx, direct)

	handler := &recordingSlogHandler{}
	instr := readers.NewSlogInstrumentation(slog.New(handler), slog.LevelInfo)
	providers := devhealthfacts.NewInstrumentedProviders(query, instr)

	const orgID = "org-chaos4377-instrumentation"
	repoID := "66666666-6666-6666-6666-666666666666"
	if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		repoID, orgID, date(2026, 8, 20), uint32(5), uint32(2), 3.5, 0.1, 4.0, uint32(2), 0.3, ts(2026, 8, 20, 2, 0, 0)); err != nil {
		t.Fatalf("seed repo_metrics_daily: %v", err)
	}

	provider := findProvider(t, providers, contextfabric.FactMetrics)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactMetrics,
		Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1 repository metrics fact", result.Facts)
	}

	// CHAOS-4418: readRepositoryMetrics no longer calls
	// readers.ReadRepositoryMetrics (that shared reader collapses to one
	// row per repository, which cannot carry a per-day series) -- it
	// builds its own raw SQL through readers.QueryOrgScopedNamed instead,
	// attributed as "ReadRepositoryMetricsSeries" (metrics.go's own doc
	// comment). Same instrumentation coverage, new reader name.
	records := handler.snapshot()
	var sawReadRepositoryMetricsSeries bool
	for _, record := range records {
		attrs := recordAttrs(record)
		readerAttr, ok := attrs["reader"]
		if !ok || readerAttr.String() != "ReadRepositoryMetricsSeries" {
			continue
		}
		sawReadRepositoryMetricsSeries = true
		orgScopedAttr, ok := attrs["org_scoped"]
		if !ok || !orgScopedAttr.Bool() {
			t.Fatalf("readers.query_org_scoped record for reader=ReadRepositoryMetricsSeries has org_scoped = %v (present=%v), want true", orgScopedAttr, ok)
		}
		if _, ok := attrs["error"]; ok {
			t.Fatalf("readers.query_org_scoped record for reader=ReadRepositoryMetricsSeries carries an error attr on a successful read: %#v", attrs["error"])
		}
	}
	if !sawReadRepositoryMetricsSeries {
		t.Fatalf("no slog record emitted for reader=ReadRepositoryMetricsSeries -- devhealthfacts.NewInstrumentedProviders wiring did not reach readers.QueryOrgScopedNamed; records captured = %#v", records)
	}
}

// TestCHAOS4377_NewInstrumentedProvidersNilInstrumentationMatchesNewProviders
// pins NewInstrumentedProviders' documented nil-instrumentation behavior:
// with instr == nil it must return providers that behave exactly like
// NewProviders' own (readers.QueryOrgScoped falls back to its own
// no-op instrumentation), never a provider that panics or drops facts.
func TestCHAOS4377_NewInstrumentedProvidersNilInstrumentationMatchesNewProviders(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS3780Tables(t, ctx, direct)

	providers := devhealthfacts.NewInstrumentedProviders(query, nil)

	const orgID = "org-chaos4377-nil-instrumentation"
	repoID := "88888888-8888-8888-8888-888888888888"
	if err := direct.Exec(ctx, `INSERT INTO repo_metrics_daily (repo_id, org_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		repoID, orgID, date(2026, 8, 20), uint32(7), uint32(3), 2.5, 0.2, 5.0, uint32(3), 0.4, ts(2026, 8, 20, 2, 0, 0)); err != nil {
		t.Fatalf("seed repo_metrics_daily: %v", err)
	}

	provider := findProvider(t, providers, contextfabric.FactMetrics)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactMetrics,
		Subjects: []contextfabric.SubjectRef{repoSubject(repoID)},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	dailyMetrics := result.Facts[0].Fields["daily_metrics"].Rows
	if len(result.Facts) != 1 || len(dailyMetrics) != 1 || dailyMetrics[0].Fields["commits_count"].Integer == nil || *dailyMetrics[0].Fields["commits_count"].Integer != 7 {
		t.Fatalf("facts = %#v, want exactly 1 fact with a 1-row daily_metrics series (commits_count=7)", result.Facts)
	}
}
