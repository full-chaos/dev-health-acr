package falkorgraph

import (
	"context"
	"errors"
	"math"
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

// chaos4155WatermarkRow builds one _AcrWatermark row. CHAOS-4298: the
// snapshot read is keyed on (epoch, generation), not backend_watermark's
// raw value (see watermarkSnapshotsEqual's own doc comment for why) --
// these are what the fixtures below now script to drive Stable vs Drift.
// epoch defaults to a fixed "epoch-1" for every existing fixture that only
// needs to vary generation; the purge/epoch-specific tests build their own
// rows directly.
func chaos4155WatermarkRow(source string, generation int64) row {
	return row{"source": source, "generation": generation, "epoch": "epoch-1"}
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
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
		watermarkBefore: []row{chaos4155WatermarkRow("github", 1)},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", 1)},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
		watermarkBefore: []row{chaos4155WatermarkRow("github", 1)},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", 2)}, // moved mid-census
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
		watermarkBefore: []row{chaos4155WatermarkRow("github", 1)},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", 1), chaos4155WatermarkRow("jira", 1)},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
		watermarkBefore: []row{chaos4155WatermarkRow("github", 1)},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", 1)},
	}
	adapter := vectorAdapter(t, fake.toFakeConn(), &stubEmbedder{err: errors.New("embed provider unavailable")}, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 1000
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
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
	before := map[string]watermarkSnapshotEntry{"github": {Generation: 0, Epoch: "epoch-1"}}
	after := map[string]watermarkSnapshotEntry{"jira": {Generation: 0, Epoch: "epoch-1"}}
	if watermarkSnapshotsEqual(before, after) {
		t.Fatalf("watermarkSnapshotsEqual(%#v, %#v) = true, want false -- github disappearing while jira appears is drift, not equal-length coincidence", before, after)
	}
}

// TestWatermarkSnapshotsEqual_SameGenerationDifferentEpochIsDrift is
// CHAOS-4298's own follow-up unit-level pin (team-lead ruling,
// 2026-08-26): the purge-and-rebuild scenario this fix exists to close --
// a source at the SAME generation both reads (the plausible post-purge
// case: a freshly reprojected org lands back on generation 1) but a
// DIFFERENT epoch (the rebuilt node's own fresh ON CREATE nonce) must
// compare as drift. Generation alone could never distinguish this from a
// genuinely stable source; see TestLiveWatermarkEpochDetectsPurgeAndRebuildLandingOnSameGeneration
// for the real-FalkorDB end-to-end proof of the same property.
func TestWatermarkSnapshotsEqual_SameGenerationDifferentEpochIsDrift(t *testing.T) {
	t.Parallel()
	before := map[string]watermarkSnapshotEntry{"github": {Generation: 1, Epoch: "epoch-before-purge"}}
	after := map[string]watermarkSnapshotEntry{"github": {Generation: 1, Epoch: "epoch-after-purge"}}
	if watermarkSnapshotsEqual(before, after) {
		t.Fatalf("watermarkSnapshotsEqual(%#v, %#v) = true, want false -- same generation but a different epoch means the node was purged and recreated, not merely unwritten", before, after)
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
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
}

// TestConfirmedKindVectorCensus_MalformedWatermarkRowFailsClosed is codex
// R2's own Medium finding regression: a watermark row with an empty source
// used to be silently dropped from the snapshot map instead of aborting the
// census, which let two snapshots that both happen to drop the SAME
// malformed row compare as stable/equal -- hiding a data-integrity problem
// behind a false Complete reading. source is part of a watermark node's own
// MERGE identity key (projection.go), so an empty one can only mean a
// corrupted row, never a legitimate state.
func TestConfirmedKindVectorCensus_MalformedWatermarkRowFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{chaos4155WatermarkRow("", 1)}, // malformed: empty source
		watermarkAfter:  []row{chaos4155WatermarkRow("", 1)},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q -- a malformed (empty-source) watermark row must abort the census, never be silently dropped into a false-stable comparison", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
}

// TestConfirmedKindVectorCensus_FractionalGenerationFailsClosed is codex
// R1's own Medium finding regression (CHAOS-4298): a stray non-integral
// generation value must abort the census, never truncate silently -- two
// DIFFERENT generations (1.9 and 1.1) both truncate to the SAME int(1)
// under a naive numeric parse, which would report a stable comparison over
// values that were never actually equal. Every value this codebase's own
// writer (writeWatermark) ever produces is a non-negative whole number by
// construction; this row simulates a fractional value reaching the read
// path some other way.
func TestConfirmedKindVectorCensus_FractionalGenerationFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{{"source": "github", "generation": 1.9}},
		watermarkAfter:  []row{{"source": "github", "generation": 1.1}},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q -- a fractional generation must abort the census, never truncate to a coincidentally-equal int", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
}

// TestConfirmedKindVectorCensus_NegativeGenerationFailsClosed is the
// negative-value sibling of the fractional-generation regression above --
// writeWatermark's own coalesce(w.generation, 0) + 1 can never produce a
// negative value, so one reaching the read path is unconditionally
// malformed.
func TestConfirmedKindVectorCensus_NegativeGenerationFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{{"source": "github", "generation": -1}},
		watermarkAfter:  []row{{"source": "github", "generation": -1}},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q -- a negative generation must abort the census", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
}

// TestConfirmedKindVectorCensus_MalformedEmptyEpochFailsClosed is the
// epoch sibling of TestConfirmedKindVectorCensus_MalformedWatermarkRowFailsClosed
// (CHAOS-4298 follow-up): every node writeWatermark ever produces has a
// non-empty epoch (a fresh per-creation nonce, or the fixed
// chaos4298SentinelEpoch self-heal) -- a row with a real source and a
// valid generation but an empty epoch can only be malformed/corrupted,
// never a legitimate state, and must abort the census the same way an
// empty source already does.
func TestConfirmedKindVectorCensus_MalformedEmptyEpochFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{{"source": "github", "generation": int64(1), "epoch": ""}},
		watermarkAfter:  []row{{"source": "github", "generation": int64(1), "epoch": ""}},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q -- an empty epoch must abort the census, never be silently dropped into a false-stable comparison", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
}

// TestConfirmedKindVectorCensus_NonFiniteQueryVectorFailsClosed is codex
// R2's own Medium finding regression: a NaN or Inf component in the
// embedder's query vector used to fall through to trueCosineSimilarity's
// zero-norm/dimension guard, which silently returns similarity 0 rather
// than surfacing the broken vector -- NaN comparisons are always false in
// Go, so a poisoned query vector would score zero rivals against every
// corpus vector, indistinguishable telemetry-wise from "no rivals" and
// letting the term count toward completeness despite never being
// meaningfully scored.
func TestConfirmedKindVectorCensus_NonFiniteQueryVectorFailsClosed(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{chaos4155WatermarkRow("github", 1)},
		watermarkAfter:  []row{chaos4155WatermarkRow("github", 1)},
	}
	embedder := &stubEmbedder{vector: []float32{float32(math.NaN()), 0, 0, 0}}
	adapter := vectorAdapter(t, fake.toFakeConn(), embedder, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 1000
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeFailed {
		t.Fatalf("outcome = %+v, want State=%q -- a non-finite query vector must abort the census, never silently score as zero rivals", got, graphrank.ConfirmedKindVectorScopeFailed)
	}
	if got.QueriesScored != 0 {
		t.Fatalf("outcome.QueriesScored = %d, want 0 -- a poisoned vector must not count as a scored term", got.QueriesScored)
	}
}

// --- CHAOS-4311 Phase 3: Rivals extraction ---

// chaos4155VaryingEmbedder returns a DIFFERENT canned vector per call, in
// order -- needed to prove the dedup-keeps-highest-similarity contract
// below, which stubEmbedder's single fixed vector cannot exercise (every
// term would score identically against a given corpus row).
type chaos4155VaryingEmbedder struct {
	vectors [][]float32
	calls   int
}

func (e *chaos4155VaryingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = e.vectors[e.calls%len(e.vectors)]
		e.calls++
	}
	return out, nil
}

func (e *chaos4155VaryingEmbedder) Identity() contextfabric.EmbedderIdentity {
	return contextfabric.EmbedderIdentity{Provider: "stub", Model: "stub-embed", Dimension: len(e.vectors[0])}
}

// TestConfirmedKindVectorCensus_RivalsOnlyIncludeCandidatesAboveFloor pins
// the basic Rivals population contract alongside the pre-existing
// RivalCountAboveTau assertion: exactly the row that cleared the floor
// appears in Rivals, tagged MatchVector with its own raw similarity, and the
// at-floor row (excluded per aboveSimilarityFloor's own strict ">" contract)
// does not appear at all.
func TestConfirmedKindVectorCensus_RivalsOnlyIncludeCandidatesAboveFloor(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      2,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0}), chaos4155SubjectRow("wi_2", []float32{0, 1, 0, 0})},
		watermarkBefore: []row{},
		watermarkAfter:  []row{},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeComplete {
		t.Fatalf("outcome.State = %q, want %q", got.State, graphrank.ConfirmedKindVectorScopeComplete)
	}
	if len(got.Rivals) != 1 {
		t.Fatalf("len(got.Rivals) = %d, want 1 (only wi_1 clears the 0.5 floor; wi_2 sits exactly at it, excluded by the strict > contract)", len(got.Rivals))
	}
	// chaos4155SubjectRow sets propLabel to the SAME value as
	// propCanonicalID -- toCandidateNode carries that through as Name.
	if got.Rivals[0].Name != "wi_1" {
		t.Errorf("Rivals[0].Name = %q, want %q (wi_2 must not appear)", got.Rivals[0].Name, "wi_1")
	}
	if got.Rivals[0].Mechanism != contextfabric.MatchVector {
		t.Errorf("Rivals[0].Mechanism = %q, want %q", got.Rivals[0].Mechanism, contextfabric.MatchVector)
	}
	if got.Rivals[0].VectorSimilarity == nil || *got.Rivals[0].VectorSimilarity != 1.0 {
		t.Errorf("Rivals[0].VectorSimilarity = %v, want a pointer to 1.0 (stub query vector {1,0,0,0} against wi_1's identical vector)", got.Rivals[0].VectorSimilarity)
	}
	if got.Rivals[0].Relevance == nil {
		t.Fatal("Rivals[0].Relevance = nil, want a normalized relevance in the vector band")
	}
}

// TestConfirmedKindVectorCensus_RivalsDeduplicateAcrossTermsKeepingHighestSimilarity
// pins the CHAOS-4311 dedup contract: the SAME corpus row clearing the floor
// against TWO different query terms at two different similarities appears
// exactly ONCE in Rivals, carrying the HIGHER of the two similarities --
// while RivalCountAboveTau (the Phase 1/2 telemetry field, unchanged) still
// counts both occurrences, proving the two fields are deliberately
// different, not a refactor of one into the other.
func TestConfirmedKindVectorCensus_RivalsDeduplicateAcrossTermsKeepingHighestSimilarity(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal:      1,
		subjectRows:     []row{chaos4155SubjectRow("wi_1", []float32{1, 0, 0, 0})},
		watermarkBefore: []row{},
		watermarkAfter:  []row{},
	}
	adapter := vectorAdapter(t, fake.toFakeConn(), &chaos4155VaryingEmbedder{
		// term 1 -> similarity 1.0 against wi_1; term 2 -> similarity ~0.707
		// (still above the 0.5 floor). Both clear the floor; the max (1.0)
		// must be what Rivals keeps.
		vectors: [][]float32{{1, 0, 0, 0}, {1, 1, 0, 0}},
	}, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 1000
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"term-a", "term-b"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeComplete {
		t.Fatalf("outcome.State = %q, want %q", got.State, graphrank.ConfirmedKindVectorScopeComplete)
	}
	if got.RivalCountAboveTau != 2 {
		t.Fatalf("outcome.RivalCountAboveTau = %d, want 2 (unchanged Phase 1/2 semantics: one (term, candidate) pair per term)", got.RivalCountAboveTau)
	}
	if len(got.Rivals) != 1 {
		t.Fatalf("len(got.Rivals) = %d, want 1 -- the SAME candidate matched both terms and must be deduplicated", len(got.Rivals))
	}
	if got.Rivals[0].VectorSimilarity == nil || *got.Rivals[0].VectorSimilarity != 1.0 {
		t.Errorf("Rivals[0].VectorSimilarity = %v, want a pointer to 1.0 (the HIGHER of the two terms' similarities, not the last-seen 0.707)", got.Rivals[0].VectorSimilarity)
	}
}

// TestConfirmedKindVectorCensus_RivalsNilOnNonCompleteStates pins that
// Rivals is never populated on a discarded outcome -- OverBudget here, but
// the same return-path reasoning covers Malformed/Failed/Drift (none of
// them reach the Rivals-building code at the tail of the Complete path).
func TestConfirmedKindVectorCensus_RivalsNilOnNonCompleteStates(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, labelWatermark) {
			return nil, nil
		}
		if strings.Contains(cypher, "count(n)") {
			return []row{chaos4155CountRow(100)}, nil
		}
		return nil, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0, 0}}, 0.5)
	adapter.config.ConfirmedKindVectorCensusMaxComparisons = 50 // 100 * 1 > 50
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if got.State != graphrank.ConfirmedKindVectorScopeOverBudget {
		t.Fatalf("outcome.State = %q, want %q", got.State, graphrank.ConfirmedKindVectorScopeOverBudget)
	}
	if got.Rivals != nil {
		t.Errorf("outcome.Rivals = %+v, want nil on a refused (non-Complete) outcome", got.Rivals)
	}
}

// TestConfirmedKindVectorCensus_RivalsSortedByCanonicalID pins the
// deterministic-ordering contract: Rivals is sorted by CanonicalID
// regardless of the corpus's own fetch/iteration order, since this census
// builds the dedup set via a Go map internally (randomized iteration order).
func TestConfirmedKindVectorCensus_RivalsSortedByCanonicalID(t *testing.T) {
	t.Parallel()
	fake := &chaos4155FakeConn{
		countTotal: 3,
		subjectRows: []row{
			chaos4155SubjectRow("wi_c", []float32{1, 0, 0, 0}),
			chaos4155SubjectRow("wi_a", []float32{1, 0, 0, 0}),
			chaos4155SubjectRow("wi_b", []float32{1, 0, 0, 0}),
		},
		watermarkBefore: []row{},
		watermarkAfter:  []row{},
	}
	adapter := chaos4155Adapter(t, fake, 1000)
	got := adapter.confirmedKindVectorCensus(context.Background(), "k", "org-1", contextfabric.SubjectWorkItem, []string{"outage"}, temporalFilter{})
	if len(got.Rivals) != 3 {
		t.Fatalf("len(got.Rivals) = %d, want 3", len(got.Rivals))
	}
	want := []string{"wi_a", "wi_b", "wi_c"}
	for i, rival := range got.Rivals {
		// chaos4155SubjectRow sets propLabel to the SAME value as
		// propCanonicalID -- toCandidateNode carries that through as Name.
		if rival.Name != want[i] {
			t.Fatalf("Rivals[%d].Name = %q, want %q (input order was wi_c, wi_a, wi_b -- output must be sorted)", i, rival.Name, want[i])
		}
	}
}
