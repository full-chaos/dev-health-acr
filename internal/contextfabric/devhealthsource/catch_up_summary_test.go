package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// catchUpBackend applies every batch and remembers which repositories it was
// given, and on which tick.
type catchUpBackend struct {
	mu         sync.Mutex
	tick       int
	batches    int
	repository map[string]int
	watermarks map[string]contextfabric.ProjectionWatermark
}

func (b *catchUpBackend) ApplyProjectionBatch(_ context.Context, batch contextfabric.ProjectionBatch) (contextfabric.ProjectionReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches++
	for _, e := range batch.Entities {
		if id, ok := strings.CutPrefix(e.Subject.CanonicalID, "repository:"); ok {
			if _, seen := b.repository[id]; !seen {
				b.repository[id] = b.tick
			}
		}
	}
	b.watermarks[batch.Source] = contextfabric.ProjectionWatermark{OrgID: batch.OrgID, Source: batch.Source, SourceVersion: batch.SourceVersion, ProjectedAt: time.Now().UTC(), BackendWatermark: batch.NextCursor}
	return contextfabric.ProjectionReceipt{BatchID: batch.BatchID, AppliedAt: time.Now().UTC(), BackendWatermark: batch.NextCursor, EntitiesApplied: len(batch.Entities)}, nil
}

func (b *catchUpBackend) ProjectionWatermark(_ context.Context, _, source string) (contextfabric.ProjectionWatermark, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watermarks[source], nil
}

func (b *catchUpBackend) PurgeOrganization(context.Context, string) error { return nil }

// catchUpTick is what one tick of a from-zero catch-up left behind.
type catchUpTick struct {
	reason       string
	summary      map[string]any
	lagSeconds   float64 // the test's own reading: clock - the checkpoint cursor's stamp
	repositories int
	targetThere  bool
}

// runFromZeroCatchUp drives the production source under the production
// coordinator from an empty checkpoint: 90 days of work items (ten pages), a
// drain budget of two batches per tick, so the catch-up spans several ticks.
// Eleven repositories: two old, nine re-stamped by the sync before every tick
// (created_at = last_synced = that tick's hour), as the hourly sync does. The
// target is one of the nine; no other row names it. It returns one entry per
// tick, until the first tick that did not end on the drain budget. The walk
// runs once for the package; its two rows read the same ticks.
func runFromZeroCatchUp(t *testing.T) []catchUpTick {
	t.Helper()
	fromZeroCatchUp.once.Do(func() { fromZeroCatchUp.ticks, fromZeroCatchUp.failure = walkFromZeroCatchUp(t.Logf) })
	if fromZeroCatchUp.failure != "" {
		t.Fatal(fromZeroCatchUp.failure)
	}
	return fromZeroCatchUp.ticks
}

var fromZeroCatchUp struct {
	once    sync.Once
	ticks   []catchUpTick
	failure string
}

func walkFromZeroCatchUp(logf func(string, ...any)) ([]catchUpTick, string) {
	const orgID, target = "org-1", 7
	start := time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC)
	now := start
	repoID := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	var repos [][]any
	for n := 1; n <= 11; n++ {
		at := start.Add(-90 * 24 * time.Hour).Add(time.Duration(n) * time.Second)
		id := repoID(n)
		repos = append(repos, []any{id, "acme/repo-" + id[30:], "github", at, at, ""})
	}
	restamp := func() {
		for n := 3; n <= 11; n++ {
			at := now.Add(-5 * time.Minute).Add(time.Duration(n) * 400 * time.Millisecond)
			repos[n-1][3], repos[n-1][4] = at, at
		}
	}
	var items [][]any
	for n := 0; n < 2000; n++ {
		at := start.Add(-90 * 24 * time.Hour).Add(time.Duration(n) * ((90*24*time.Hour - 2*time.Hour) / 2000))
		items = append(items, []any{fmt.Sprintf("WI-%04d", n), repoID(1), "acme/repo-" + repoID(1)[30:], fmt.Sprintf("item %d", n), "open", "", at, at, uint8(0), zeroTime, "bug", "ACME", "Acme", []string{}})
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repos", rows: repos, cursorOf: repoCursorOf},
		{match: "FROM work_items AS w", rows: items, cursorOf: workItemCursorOf},
	}}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		return nil, err.Error()
	}
	source.SetClockForTest(func() time.Time { return now })
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	source.WithLogger(logger)
	backend := &catchUpBackend{repository: map[string]int{}, watermarks: map[string]contextfabric.ProjectionWatermark{}}
	checkpoints := &summaryCheckpoints{data: map[string]contextfabric.ProjectionCheckpoint{}}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  []string{orgID},
		Sources: []projectionrun.SourcePair{{Name: devhealthsource.SourceName, Source: source}},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: summaryRebuildMarker{},
		DrainBatchBudget: 1, Logger: logger, Observer: projectionrun.SlogObserver{Logger: logger},
		Now: func() time.Time { return now },
	})
	if err != nil {
		return nil, err.Error()
	}
	cursorStamp := func() (time.Time, error) {
		raw, err := base64.RawURLEncoding.DecodeString(checkpoints.data[devhealthsource.SourceName].Cursor)
		if err != nil {
			return time.Time{}, err
		}
		var decoded struct {
			Since time.Time `json:"since"`
		}
		err = json.Unmarshal(raw, &decoded)
		return decoded.Since, err
	}
	var ticks []catchUpTick
	for tick := 1; tick <= 30; tick++ {
		if tick > 1 {
			now = now.Add(time.Hour)
		}
		restamp()
		backend.tick = tick
		logs.Reset()
		coordinator.Tick(context.Background())
		entry := catchUpTick{}
		for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var line map[string]any
			if json.Unmarshal([]byte(raw), &line) != nil {
				continue
			}
			switch {
			case line["msg"] == "context_fabric: projection tick freshness summary":
				entry.summary = line
			case line["drain_yield_reason"] != nil:
				entry.reason, _ = line["drain_yield_reason"].(string)
			}
		}
		if entry.summary == nil {
			return nil, fmt.Sprintf("tick %d: no summary line", tick)
		}
		stamp, err := cursorStamp()
		if err != nil {
			return nil, fmt.Sprintf("tick %d: the checkpoint cursor does not decode: %v", tick, err)
		}
		entry.lagSeconds = now.Sub(stamp).Seconds()
		entry.repositories = len(backend.repository)
		_, entry.targetThere = backend.repository[repoID(target)]
		logf("tick %2d: drain_yield_reason=%-15q cursor_lag_days=%5.1f orgs_ok=%v orgs_catching_up=%v catch_up_lag_seconds_max=%v tick_complete=%v repositories=%d/11 target=%v",
			tick, entry.reason, entry.lagSeconds/86400, entry.summary["orgs_ok"], entry.summary["orgs_catching_up"], entry.summary["catch_up_lag_seconds_max"], entry.summary["tick_complete"], entry.repositories, entry.targetThere)
		ticks = append(ticks, entry)
		if entry.reason != "budget_exceeded" {
			break
		}
	}
	if len(ticks) < 4 || ticks[len(ticks)-1].reason == "budget_exceeded" {
		return nil, fmt.Sprintf("precondition: the catch-up took %d ticks (last reason %q); it must span several ticks and end", len(ticks), ticks[len(ticks)-1].reason)
	}
	return ticks, ""
}

// While the catch-up has work left the summary must not count the
// organization ok or call the tick complete, and it must give the lag of the
// cursor. After the catch-up it says ok and complete again.
func TestTickSummaryIsNotOKWhileAFromZeroCatchUpHasWorkLeft(t *testing.T) {
	ticks := runFromZeroCatchUp(t)
	number := func(tick int, key string) float64 {
		t.Helper()
		value, ok := ticks[tick].summary[key].(float64)
		if !ok {
			t.Fatalf("tick %d: the summary has no numeric %q: %v", tick+1, key, ticks[tick].summary)
		}
		return value
	}
	last := len(ticks) - 1
	for i := 0; i < last; i++ {
		if ok := number(i, "orgs_ok"); ok != 0 {
			t.Errorf("tick %d ended with work left and the cursor %.1f days behind, and the summary says orgs_ok=%v", i+1, ticks[i].lagSeconds/86400, ok)
			continue
		}
		if complete, _ := ticks[i].summary["tick_complete"].(bool); complete {
			t.Errorf("tick %d ended with work left and the summary says tick_complete=true", i+1)
		}
		if catching, pairs := number(i, "orgs_catching_up"), number(i, "sources_catching_up"); catching != 1 || pairs != 1 {
			t.Errorf("tick %d: orgs_catching_up=%v sources_catching_up=%v, want 1 and 1", i+1, catching, pairs)
		}
		if lag := number(i, "catch_up_lag_seconds_max"); lag < ticks[i].lagSeconds-1 || lag > ticks[i].lagSeconds+1 {
			t.Errorf("tick %d: catch_up_lag_seconds_max=%v, want the cursor's lag %.0f", i+1, lag, ticks[i].lagSeconds)
		}
	}
	if ok, complete := number(last, "orgs_ok"), ticks[last].summary["tick_complete"]; ok != 1 || complete != true {
		t.Errorf("the tick after the catch-up: orgs_ok=%v tick_complete=%v, want 1 and true", ok, complete)
	}
}

// A repository row is rewritten by every sync, so its stamp is always the
// newest: the walk reaches it last. Its node must not wait for the whole
// history. Every repository has a node after the first tick of the build.
func TestEveryRepositoryHasANodeOnTheFirstTickOfAFromZeroBuild(t *testing.T) {
	ticks := runFromZeroCatchUp(t)
	if !ticks[0].targetThere || ticks[0].repositories != 11 {
		firstWithTarget := 0
		for i, tick := range ticks {
			if tick.targetThere {
				firstWithTarget = i + 1
				break
			}
		}
		t.Fatalf("after tick 1 of %d: %d of 11 repositories were emitted, the re-stamped one among them: %v (it was first emitted on tick %d)", len(ticks), ticks[0].repositories, ticks[0].targetThere, firstWithTarget)
	}
}
