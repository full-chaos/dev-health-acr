package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// This file pins CHAOS-4155 Phase 1's own falkorgraph-side regression bar:
// the exact, count-closed, kind-scoped vector census
// (chaos4155_confirmed_kind_vector_census.go) that implements
// graphrank.ResolveDeps.ConfirmedKindVectorCensus. See that file's own doc
// comment for the full design; these tests pin count-closure, fail-closed
// malformed handling, the before/after watermark drift check, and the
// budget refusal -- the four properties team-lead's Phase 1 ruling named
// explicitly.

// chaos4155CountRow builds the count(n) aggregate row
// countKindEmbedderFenceCorpus expects back.
func chaos4155CountRow(total int64) row {
	return row{"total": total}
}

// chaos4155SubjectRow builds one well-formed enumerated Subject row for
// fetchKindEmbedderFenceCorpus.
func chaos4155SubjectRow(canonicalID string, vector []float32) row {
	return row{"n": &node{Properties: map[string]interface{}{
		propCanonicalID: canonicalID, propLabel: canonicalID, propEmbedding: vector,
	}}}
}

// chaos4155WatermarkRow builds one _AcrWatermark row.
func chaos4155WatermarkRow(source, backendWatermark string) row {
	return row{"source": source, "backend_watermark": backendWatermark}
}

// chaos4155FakeConn dispatches on the CYPHER SHAPE (the three distinct
// query shapes confirmedKindVectorCensus issues: count(n), the paginated
// Subject fetch, and the _AcrWatermark read) -- exactly the same
// query-shape dispatch pattern chaos4038's own fake tests use, since
// fakeConn does not evaluate Cypher, only returns canned Go values keyed on
// what the caller asks for.
type chaos4155FakeConn struct {
	countTotal      int64
	countErr        error
	subjectRows     []row
	subjectErr      error
	watermarkCalls  int
	watermarkBefore []row
	watermarkAfter  []row
	watermarkErr    error
}

func (f *chaos4155FakeConn) toFakeConn() *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, labelWatermark):
			f.watermarkCalls++
			if f.watermarkErr != nil {
				return nil, f.watermarkErr
			}
			if f.watermarkCalls == 1 {
				return f.watermarkBefore, nil
			}
			return f.watermarkAfter, nil
		case strings.Contains(cypher, "count(n)"):
			if f.countErr != nil {
				return nil, f.countErr
			}
			return []row{chaos4155CountRow(f.countTotal)}, nil
		default:
			if f.subjectErr != nil {
				return nil, f.subjectErr
			}
			return f.subjectRows, nil
		}
	}}
}

func chaos4155Adapter(t *testing.T, fake *chaos4155FakeConn, maxComparisons int64) *Adapter {
	t.Helper()
	adapter := vectorAdapter(t, fake.toFakeConn(), &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = maxComparisons
	return adapter
}

// TestConfirmedKindVectorCensus_DisabledByDefaultNotAttempted proves the
// zero-value (no ACR_CONTEXT_FABRIC_CONFIRMED_KIND_VECTOR_CENSUS_MAX_COMPARISONS
// set) posture: zero cost, no backend calls at all.
func TestConfirmedKindVectorCensus_DisabledByDefaultNotAttempted(t *testing.T) {
	t.Parallel()
	called := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		called = true
		return nil, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.5)
	// ConfirmedKindVectorCensusMaxComparisons left at its zero value.
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeNotAttempted {
		t.Fatalf("State = %q, want %q", got.State, graphrank.ConfirmedKindVectorScopeNotAttempted)
	}
	if called {
		t.Fatal("queryFunc was called, want zero backend calls when the census is disabled by default")
	}
}

// TestConfirmedKindVectorCensus_NoEmbedderNotAttempted proves the census
// stays a no-op on a deployment with no live vector mechanism at all, even
// if the budget knob were somehow set.
func TestConfirmedKindVectorCensus_NoEmbedderNotAttempted(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter(t, &fakeConn{})
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 1000
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeNotAttempted {
		t.Fatalf("State = %q, want %q", got.State, graphrank.ConfirmedKindVectorScopeNotAttempted)
	}
}

// TestConfirmedKindVectorCensus_CompleteOnCleanCountClosedCorpusWithStableSnapshot
// is the census's own happy path: population count matches the enumerated,
// well-formed corpus exactly, the watermark snapshot is identical before
// and after, and the result is Complete with every count field populated.
func TestConfirmedKindVectorCensus_CompleteOnCleanCountClosedCorpusWithStableSnapshot(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      2,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0}), chaos4155SubjectRow("wi_2", []float32{0, 1, 0, 0})},
		watermarkBefore: []row{chaos4155WatermarkRow("github", "wm-1")},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", "wm-1")},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeComplete {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeComplete)
	}
	if got.PopulationCount != 2 || got.EnumeratedCount != 2 || got.MalformedCount != 0 {
		t.Fatalf("outcome = %+v, want PopulationCount=2 EnumeratedCount=2 MalformedCount=0", got)
	}
	if got.QueryCount != 1 || got.QueriesScored != 1 {
		t.Fatalf("outcome = %+v, want QueryCount=1 QueriesScored=1", got)
	}
	if !got.SnapshotStable {
		t.Fatalf("outcome = %+v, want SnapshotStable=true", got)
	}
	// stub embedder returns {1,0,0,0}: cosine 1.0 against wi_1's own
	// {1,0,0,0} (above the 0.5 floor) and 0.0 against wi_2's {0,1,0,0}
	// (at the floor, not above it) -- exactly 1 rival.
	if got.RivalCountAboveTau != 1 {
		t.Fatalf("outcome.RivalCountAboveTau = %d, want 1", got.RivalCountAboveTau)
	}
}

// TestConfirmedKindVectorCensus_OverBudgetNeverFetchesOrEmbeds proves the
// budget refusal is a REFUSAL, not a partial attempt: when
// population*queryCount exceeds the cap, the corpus fetch and embed calls
// never happen at all.
func TestConfirmedKindVectorCensus_OverBudgetNeverFetchesOrEmbeds(t *testing.T) {
	t.Parallel()
	embedder := &stubEmbedder{vector: []float32{1, 0, 0, 0}}
	fetchCalled := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, labelWatermark) {
			return nil, nil
		}
		if strings.Contains(cypher, "count(n)") {
			return []row{chaos4155CountRow(100)}, nil
		}
		fetchCalled = true
		return nil, nil
	}}
	adapter := vectorAdapter(t, fake, embedder, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 50 // 100 population * 1 term = 100 > 50
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeOverBudget {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeOverBudget)
	}
	if fetchCalled {
		t.Fatal("the paginated corpus fetch ran despite exceeding the comparison budget -- OverBudget must be a refusal, never a partial attempt")
	}
	if embedder.calls != 0 {
		t.Fatalf("embedder.calls = %d, want 0 -- no query embedding should happen when the census refuses on budget", embedder.calls)
	}
}

// TestConfirmedKindVectorCensus_MalformedRowFailsClosed proves a single
// undecodable row (no vector) fails the WHOLE census closed -- sol's
// consult note hardening this beyond oracle.go's own skip-and-reconcile
// measurement posture.
func TestConfirmedKindVectorCensus_MalformedRowFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal: 2,
		subjectRows: []row{
			chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0}),
			{"n": &node{Properties: map[string]interface{}{propCanonicalID: "wi_2"}}}, // no embedding property
		},
		watermarkBefore: []row{},
		watermarkAfter:  []row{},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeMalformed {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeMalformed)
	}
	if got.MalformedCount != 1 {
		t.Fatalf("outcome.MalformedCount = %d, want 1", got.MalformedCount)
	}
}

// TestConfirmedKindVectorCensus_CountMismatchFailsClosed proves the
// independent count(n) reconciliation: a fetch that returns FEWER
// well-formed rows than the count query reported (a concurrent-write race
// the pagination itself can silently produce, per oracle.go's own
// CHAOS-3849 note) is treated the same as a malformed row -- fail closed,
// never Complete.
func TestConfirmedKindVectorCensus_CountMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      3,                                                         // count(n) says 3
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})}, // fetch only returns 1
		watermarkBefore: []row{},
		watermarkAfter:  []row{},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeMalformed {
		t.Fatalf("outcome = %+v, want State=%q -- a count(n)/fetch disagreement must fail closed, never read as Complete", got, graphrank.ConfirmedKindVectorScopeMalformed)
	}
}

// TestConfirmedKindVectorCensus_SnapshotDriftFailsClosed proves the
// before/after _AcrWatermark comparison: a source's watermark value
// changing between the two reads means a projection write landed
// mid-census, and the outcome must be Drift, never Complete -- team-lead's
// lock-free stable-snapshot check ruling.
func TestConfirmedKindVectorCensus_SnapshotDriftFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{chaos4155WatermarkRow("github", "wm-1")},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", "wm-2")}, // moved mid-census
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeDrift {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeDrift)
	}
	if got.SnapshotStable {
		t.Fatalf("outcome.SnapshotStable = true, want false")
	}
}

// TestConfirmedKindVectorCensus_NewSourceAppearingIsDrift proves the drift
// check catches a source APPEARING between the two reads too, not only an
// existing source's value changing -- a projection run against a
// previously-silent source landing mid-census is exactly as much a
// snapshot violation as an existing watermark moving.
func TestConfirmedKindVectorCensus_NewSourceAppearingIsDrift(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{chaos4155WatermarkRow("github", "wm-1")},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", "wm-1"), chaos4155WatermarkRow("jira", "wm-1")},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeDrift {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeDrift)
	}
}

// TestConfirmedKindVectorCensus_TermEmbedFailureFailsClosedNeverComplete is
// codex R1's own High finding regression: a single term's embed failure
// used to be silently skipped, and the census still reported Complete
// whenever the watermark snapshot was stable -- understating what was
// actually censused (QueriesScored < QueryCount reading as State=complete).
// A genuine embed-provider failure must abort the whole census to Failed,
// never claim completeness over fewer terms than it was asked to cover.
func TestConfirmedKindVectorCensus_TermEmbedFailureFailsClosedNeverComplete(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{chaos4155WatermarkRow("github", "wm-1")},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", "wm-1")},
	}
	adapter := vectorAdapter(t, fake.toFakeConn(), &stubEmbedder{err: errors.New("embed provider unavailable")}, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 1000
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q -- a term embed failure must never read as Complete", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
	if got.QueriesScored != 0 {
		t.Fatalf("outcome.QueriesScored = %d, want 0", got.QueriesScored)
	}
}

// TestWatermarkSnapshotsEqual_EqualCardinalitySourceSwapIsNotEqual is codex
// R1's own Medium finding regression: an earlier version only checked
// map length plus a one-way after[source]!=watermark scan, which read a
// source being REPLACED by a different one of equal cardinality and equal
// zero-value watermark as stable. Presence must be checked explicitly.
func TestWatermarkSnapshotsEqual_EqualCardinalitySourceSwapIsNotEqual(t *testing.T) {
	t.Parallel()
	before := map[string]string{"github": ""}
	after := map[string]string{"jira": ""}
	if watermarkSnapshotsEqual(before, after) {
		t.Fatalf("watermarkSnapshotsEqual(%#v, %#v) = true, want false -- github disappearing while jira appears is drift, not equal-length coincidence", before, after)
	}
}

// TestConfirmedKindVectorCensus_DependencyErrorNeverPropagatesOnlyFailedState
// proves ResolveDeps.ConfirmedKindVectorCensus's own "no error return"
// contract holds at the implementation: a genuine backend failure (here,
// the count query) is captured as ConfirmedKindVectorScopeFailed, and the
// call still returns cleanly -- this shadow arm must never be able to fail
// the resolution it only observes.
func TestConfirmedKindVectorCensus_DependencyErrorNeverPropagatesOnlyFailedState(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{countErr: context.DeadlineExceeded}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
}
