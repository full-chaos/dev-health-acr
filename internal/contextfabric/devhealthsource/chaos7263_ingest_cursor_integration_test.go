package devhealthsource_test

// CHAOS-7263 cursor mechanics on a real ClickHouse: a cursor saved before the
// ingest-time position space decodes as a reset; the trailing overlap re-read
// projects a row that lands just behind the frontier without moving the
// cursor, is idempotent, and is bounded -- at the window's edge, and in depth
// when the window holds more rows than one tick walks.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCHAOS7263IngestTimeCursor(t *testing.T) {
	ctx := context.Background()
	// The package's shared org-scoped container (orgIsolationClickHouseFixture,
	// productionSchemaDDL's tables): no container start of its own. Every
	// case below uses its own organization.
	query, direct := orgIsolationClickHouseFixture(t)
	createProjectMembershipPresenceView(t, ctx, direct)
	now := time.Now().UTC().Truncate(time.Second)
	newHarness := func(t *testing.T, orgID, repoID string, overlap time.Duration, logs *bytes.Buffer) *ingestHarness {
		src, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		if overlap > 0 {
			if src, err = src.WithOverlap(overlap); err != nil {
				t.Fatal(err)
			}
		}
		if logs != nil {
			src.WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
		}
		// The clock dates overlap passes; pinning it makes every window edge
		// below exact.
		src.SetClockForTest(func() time.Time { return now })
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repoID, "acme/probe-"+orgID[len(orgID)-2:], nil, now, nil, now.Add(-3*time.Hour), orgID, "linear")
		return &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.SourceName, orgID: orgID, repo: repoID}
	}
	title := func(id string) string { return "issue " + id }
	cursorWith := func(fields map[string]any) string {
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	canonicalIDs := func(d drained) []string {
		var ids []string
		for id := range d.all {
			ids = append(ids, id)
		}
		for _, rels := range d.relationships {
			for _, r := range rels {
				ids = append(ids, r.RelationshipID)
			}
		}
		sort.Strings(ids)
		return ids
	}

	// Every shape the saved cursor's `space` field can take. Only the
	// canonical value resumes; everything else is a position in some other
	// clock and must restart from zero (idempotent full re-read) under the
	// ORIGINAL cursor string, which the worker requires as batch.Cursor.
	t.Run("the cursor space field: only the canonical value resumes, every other shape resets", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "72630000-0000-4000-8000-000000000002", "72630000-0000-4000-8000-0000000000a2", 0, nil)
		h.workItem("WI-a", now.Add(-48*time.Hour), now.Add(-2*time.Hour))
		h.workItem("WI-b", now.Add(-24*time.Hour), now.Add(-time.Hour))
		fresh := h.drain("")
		since := now.Add(-90 * time.Minute) // between WI-a's and WI-b's ingest stamps
		cells := []struct {
			name      string
			cursor    string
			wantReset bool
			wantErr   bool
		}{
			{"absent (saved before the change)", cursorWith(map[string]any{"since": since, "after": "zzz"}), true, false},
			{"null", cursorWith(map[string]any{"since": since, "after": "zzz", "space": nil}), true, false},
			{"empty string", cursorWith(map[string]any{"since": since, "after": "zzz", "space": ""}), true, false},
			{"out of vocabulary", cursorWith(map[string]any{"since": since, "after": "zzz", "space": "ingest.v0"}), true, false},
			{"case variant", cursorWith(map[string]any{"since": since, "after": "zzz", "space": "INGEST.V1"}), true, false},
			{"wrong scalar type", cursorWith(map[string]any{"since": since, "after": "zzz", "space": 7}), false, true},
			{"canonical", cursorWith(map[string]any{"since": since, "after": "", "space": "ingest.v1"}), false, false},
		}
		for _, cell := range cells {
			t.Run(cell.name, func(t *testing.T) {
				b, ok, err := h.src.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: h.orgID, Source: devhealthsource.SourceName, Cursor: cell.cursor})
				if cell.wantErr {
					if err == nil {
						t.Fatalf("a cursor whose space is not a string must fail loudly, got ok=%v", ok)
					}
					return
				}
				if err != nil || !ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				if b.Cursor != cell.cursor {
					t.Fatalf("batch.Cursor = %q, want the ORIGINAL cursor (the worker requires it)", b.Cursor)
				}
				names := map[string]bool{}
				for _, e := range b.Entities {
					names[e.Subject.Label] = true
				}
				if cell.wantReset != names[title("WI-a")] {
					t.Fatalf("reset=%v but WI-a (ingested before the saved position) re-read=%v; batch=%v", cell.wantReset, names[title("WI-a")], names)
				}
				if !names[title("WI-b")] {
					t.Fatalf("WI-b (ingested after the saved position) missing: %v", names)
				}
				decoded, err := base64.RawURLEncoding.DecodeString(b.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				var next map[string]any
				_ = json.Unmarshal(decoded, &next)
				if next["space"] != "ingest.v1" {
					t.Fatalf("NextCursor %s is not in the ingest space", decoded)
				}
			})
		}
		// A reset drain projects exactly the identities a from-scratch drain
		// does: the cursor change moves no canonical id or relationship id, so
		// the one re-read MERGEs onto what is already there (no rebuild). The
		// one difference is the synthesized organization node, which only a
		// from-scratch run seeds and which a reset leaves where it is.
		reset := h.drain(cursorWith(map[string]any{"since": since, "after": "zzz"}))
		freshIDs, resetIDs := canonicalIDs(fresh), canonicalIDs(reset)
		var freshWithoutSeed []string
		for _, id := range freshIDs {
			if id != "organization:"+h.orgID {
				freshWithoutSeed = append(freshWithoutSeed, id)
			}
		}
		if len(freshWithoutSeed) != len(freshIDs)-1 || len(resetIDs) == 0 || strings.Join(freshWithoutSeed, ",") != strings.Join(resetIDs, ",") {
			t.Fatalf("a reset drain and a fresh drain project different identities:\nfresh %v\nreset %v", freshIDs, resetIDs)
		}
	})

	t.Run("overlap: a row stamped just BEHIND the frontier that lands after the cursor passed is projected once, cursor unchanged; idempotent", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "72630000-0000-4000-8000-000000000003", "72630000-0000-4000-8000-0000000000a3", 15*time.Minute, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		// The caught-up tick at the end of the first drain walks a window
		// whose rows the paged read just emitted: it must emit nothing again.
		for i, b := range first.batches {
			if b.NextCursor == b.Cursor {
				t.Fatalf("first drain batch %d is an overlap re-emission of rows the paged read already emitted", i)
			}
		}
		// A slower writer stamped this row inside the 15m overlap, but its
		// insert only lands now, after the cursor passed.
		h.workItem("WI-late", now.Add(-3*time.Hour), now.Add(-25*time.Minute+10*time.Second))
		second := h.drain(first.cursor)
		if _, ok := second.items[title("WI-late")]; !ok {
			t.Fatalf("a row stamped inside the overlap that landed after the cursor passed was NOT projected: %v", second.items)
		}
		if second.cursor != first.cursor {
			t.Fatalf("the overlap batch moved the cursor (%q -> %q); it must not", first.cursor, second.cursor)
		}
		// Idempotent: the same window is not re-emitted while nothing new lands.
		third := h.drain(second.cursor)
		if len(third.batches) != 0 {
			t.Fatalf("a caught-up tick re-emitted the overlap window (%d batches)", len(third.batches))
		}
		// A row stamped OUTSIDE the overlap that lands late is the documented bound.
		h.workItem("WI-too-late", now.Add(-5*time.Hour), now.Add(-2*time.Hour))
		if fourth := h.drain(third.cursor); len(fourth.items) != 0 {
			t.Fatalf("a row older than the overlap must not be re-read (bounded window), got %v", fourth.items)
		}
	})

	// The window's lower edge is (start of the last completed pass - overlap -
	// clock slack), slack = overlap: with the clock pinned at now, 2*overlap
	// behind now once the first caught-up tick has completed a pass. The edge
	// itself is inside, one millisecond (the column's precision) below it is
	// not.
	t.Run("overlap boundary: the window's lower edge is inclusive, one tick below it is not", func(t *testing.T) {
		t.Parallel()
		const overlap = 15 * time.Minute
		h := newHarness(t, "72630000-0000-4000-8000-000000000005", "72630000-0000-4000-8000-0000000000a5", overlap, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		edge := now.Add(-2 * overlap)
		// Millisecond-exact ingest stamps: a positional time.Time binding is
		// sent at whole-second precision, which would put "1 ms below" a full
		// second below and make this case pass for the wrong reason.
		atMilli := func(id string, at time.Time) {
			mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, ?, ?, ?, 'open', '', '', 'linear', '', ?, fromUnixTimestamp64Milli(?, 'UTC'))`,
				id, h.repo, h.orgID, title(id), now.Add(-4*time.Hour), at.UnixMilli())
		}
		atMilli("WI-at-edge", edge)
		atMilli("WI-below-edge", edge.Add(-time.Millisecond))
		var belowStored string
		if err := direct.QueryRow(ctx, `SELECT toString(last_synced) FROM work_items FINAL WHERE org_id = ? AND work_item_id = 'WI-below-edge'`, h.orgID).Scan(&belowStored); err != nil {
			t.Fatal(err)
		}
		if want := edge.Add(-time.Millisecond).UTC().Format("2006-01-02 15:04:05.000"); belowStored != want {
			t.Fatalf("precondition: WI-below-edge stored at %s, want %s", belowStored, want)
		}
		second := h.drain(first.cursor)
		if _, ok := second.items[title("WI-at-edge")]; !ok {
			t.Fatalf("a row stamped exactly at the window's lower edge was not re-read: %v", second.items)
		}
		if _, ok := second.items[title("WI-below-edge")]; ok {
			t.Fatalf("a row stamped 1ms below the window was re-read: the window is wider than documented")
		}
	})

	// A quiet organization's window closes once a pass starts more than
	// 2*overlap after the frontier: nothing behind the frontier can still land
	// within the bound, and the walk stops costing reads.
	t.Run("overlap: a quiet organization's window closes after the bound", func(t *testing.T) {
		t.Parallel()
		const overlap = 15 * time.Minute
		h := newHarness(t, "72630000-0000-4000-8000-00000000000a", "72630000-0000-4000-8000-0000000000aa", overlap, nil)
		counting := &countingQueryClient{inner: query}
		counted, err := devhealthsource.NewClickHouseProjectionSource(counting)
		if err != nil {
			t.Fatal(err)
		}
		if counted, err = counted.WithOverlap(overlap); err != nil {
			t.Fatal(err)
		}
		counted.SetClockForTest(func() time.Time { return now })
		h.src = counted
		frontier := now.Add(-3 * time.Hour)
		h.workItem("WI-old-frontier", now.Add(-4*time.Hour), frontier)
		first := h.drain("")
		// Stamped just behind the frontier but landing now, hours later: far
		// outside the bound, so a closed window does not re-read it.
		h.workItem("WI-hours-late", now.Add(-5*time.Hour), frontier.Add(-time.Minute))
		counting.statements = 0
		if second := h.drain(first.cursor); len(second.items) != 0 {
			t.Fatalf("a closed window re-read a row landing hours after its stamp: %v", second.items)
		}
		// A closed window costs nothing: the idle tick runs the paged read
		// (one statement per table) and no window statement at all.
		if tables := len(devhealthsource.EntityTableNamesForTest()); counting.statements != tables {
			t.Fatalf("an idle tick over a closed window ran %d statements, want %d (the paged read only)", counting.statements, tables)
		}
	})

	// An idle tick over an OPEN window costs the paged read and one walk
	// page: a window smaller than one page ends on that page (every table
	// reports its end, so no empty read is needed to learn it), and a pass
	// this tick started is not walked a second time in the same tick.
	t.Run("an idle tick over an open window reads it once", func(t *testing.T) {
		t.Parallel()
		const overlap = 15 * time.Minute
		h := newHarness(t, "72630000-0000-4000-8000-000000000010", "72630000-0000-4000-8000-0000000000ae", overlap, nil)
		counting := &countingQueryClient{inner: query}
		counted, err := devhealthsource.NewClickHouseProjectionSource(counting)
		if err != nil {
			t.Fatal(err)
		}
		if counted, err = counted.WithOverlap(overlap); err != nil {
			t.Fatal(err)
		}
		counted.SetClockForTest(func() time.Time { return now })
		h.src = counted
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		counting.statements = 0
		if again := h.drain(first.cursor); len(again.batches) != 0 {
			t.Fatalf("an idle tick emitted %d batches", len(again.batches))
		}
		if tables := len(devhealthsource.EntityTableNamesForTest()); counting.statements != 2*tables {
			t.Fatalf("an idle tick over an open one-row window ran %d statements, want %d (the paged read and one walk page)", counting.statements, 2*tables)
		}
	})

	// A page ends the window only when no table had rows past its LIMIT AND
	// the merged page was not cut back: two tables of 150 window rows each
	// fit their own LIMIT, but their 300 merged rows do not fit one page.
	// The rows past the cut must still be walked.
	t.Run("a window page cut back across tables does not end the pass", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "72630000-0000-4000-8000-000000000020", "72630000-0000-4000-8000-0000000000af", 15*time.Minute, nil)
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('WI-early-', leftPad(toString(number), 3, '0')), ?, ?, concat('issue WI-early-', toString(number)), 'open', '', '', 'linear', '', ?, ? FROM numbers(150)`,
			h.repo, h.orgID, now.Add(-time.Hour), now.Add(-8*time.Minute))
		mustExec(t, ctx, direct, `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced)
SELECT ?, ?, concat('dep-', leftPad(toString(number), 3, '0')), 'success', 'prod', ?, ?, ? FROM numbers(150)`,
			h.repo, h.orgID, now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-7*time.Minute))
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-5*time.Minute))
		first := h.drain("")
		// Behind the frontier and behind all 300 burst rows: the merged
		// window order puts it at position 301, on the second page.
		h.workItem("WI-past-the-cut-late", now.Add(-3*time.Hour), now.Add(-6*time.Minute))
		second := h.drain(first.cursor)
		if second.items[title("WI-past-the-cut-late")].Subject.Label == "" {
			t.Fatalf("a late row past a page cut back across tables was not projected (%d items): the pass ended at the cut", len(second.items))
		}
		if second.cursor != first.cursor {
			t.Fatalf("the window walk moved the cursor (%q -> %q)", first.cursor, second.cursor)
		}
	})

	// The memo is per process. A projector that restarts with a saved cursor
	// starts its first pass at (frontier - 2*overlap), so a row that landed
	// behind the frontier while no process was walking is still found.
	t.Run("a restarted projector finds a row that landed behind the saved cursor while it was down", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "72630000-0000-4000-8000-00000000000b", "72630000-0000-4000-8000-0000000000ab", 15*time.Minute, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		h.workItem("WI-landed-while-down", now.Add(-3*time.Hour), now.Add(-15*time.Minute))
		restarted, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		restarted.SetClockForTest(func() time.Time { return now })
		h.src = restarted
		if second := h.drain(first.cursor); second.items[title("WI-landed-while-down")].Subject.Label == "" {
			t.Fatalf("the restarted source did not re-read the window behind the saved cursor (%d items)", len(second.items))
		}
	})

	// A pass that spans several calls needs a window deeper than one call's
	// walk. Production walks overlapWindowPagesPerCall (5) pages of 200 rows
	// per call; the cases below lower that to windowPagesPerCall so a 500-row
	// burst (3 pages) already spans calls -- the same walk at a fraction of
	// the rows. (A window over 1,000 rows against the client's real
	// max_result_rows is exercised by the ownership suite's 5,000-repository
	// team cases.)
	const windowPagesPerCall, burstRows = 2, 500
	lowerWalk := func(h *ingestHarness) {
		h.src.(*devhealthsource.ClickHouseProjectionSource).SetWindowPagesPerCallForTest(windowPagesPerCall)
	}

	// seedBurstMidPass lands n work items at one ingest instant and drains
	// them, then runs one caught-up tick so a pass over the burst stops
	// mid-window.
	seedBurstMidPass := func(t *testing.T, h *ingestHarness, logs *bytes.Buffer, n int, at time.Time) drained {
		t.Helper()
		lowerWalk(h)
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('WI-burst-', leftPad(toString(number), 5, '0')), ?, ?, concat('issue WI-burst-', toString(number)), 'open', '', '', 'linear', '', ?, ? FROM numbers(?)`,
			h.repo, h.orgID, now.Add(-time.Hour), at, n)
		first := h.drain("")
		if got := len(first.items); got != n {
			t.Fatalf("first drain projected %d work items, want %d", got, n)
		}
		if again := h.drain(first.cursor); len(again.batches) != 0 {
			t.Fatalf("a caught-up tick over a seen window emitted %d batches", len(again.batches))
		}
		if !strings.Contains(logs.String(), "overlap window pass continues on the next tick") {
			t.Fatalf("precondition: the pass did not stop mid-window; logs:\n%s", logs.String())
		}
		return first
	}

	// A pass that spans many ticks must not hold the frontier: a normal new
	// row lands beyond it and is projected on the next tick, the cursor moves,
	// and the pass still finishes the window afterwards.
	t.Run("the frontier keeps advancing while an overlap pass is mid-window", func(t *testing.T) {
		t.Parallel()
		logs := &bytes.Buffer{}
		h := newHarness(t, "72630000-0000-4000-8000-00000000000c", "72630000-0000-4000-8000-0000000000ac", 15*time.Minute, logs)
		burst := now.Add(-5 * time.Minute)
		first := seedBurstMidPass(t, h, logs, burstRows, burst)
		h.workItem("WI-burst-00450-late", now.Add(-3*time.Hour), burst)    // behind the frontier, page 3
		h.workItem("WI-new", now.Add(-time.Minute), now.Add(-time.Minute)) // beyond the frontier
		b, ok, err := h.src.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: h.orgID, Source: devhealthsource.SourceName, Cursor: first.cursor})
		if err != nil || !ok {
			t.Fatalf("next tick: ok=%v err=%v", ok, err)
		}
		if b.NextCursor == first.cursor {
			t.Fatal("the first batch after a mid-window pass did not advance the cursor: the pass is holding the frontier")
		}
		var sawNew bool
		for _, e := range b.Entities {
			sawNew = sawNew || e.Subject.Label == title("WI-new")
		}
		if !sawNew {
			t.Fatal("the new row beyond the frontier was not in the first batch of the next tick")
		}
		if rest := h.drain(b.NextCursor); rest.items[title("WI-burst-00450-late")].Subject.Label == "" {
			t.Fatalf("after the frontier moved, the pass did not go on to find the late row on page 3 (%d items)", len(rest.items))
		}
	})

	// The pass position lives in memory only. A projector that restarts
	// mid-pass must restart the pass from the window's start (re-read), never
	// resume past rows it cannot prove it read.
	t.Run("a restart mid-pass re-reads the window from its start", func(t *testing.T) {
		t.Parallel()
		logs := &bytes.Buffer{}
		h := newHarness(t, "72630000-0000-4000-8000-00000000000d", "72630000-0000-4000-8000-0000000000ad", 15*time.Minute, logs)
		burst := now.Add(-5 * time.Minute)
		first := seedBurstMidPass(t, h, logs, burstRows, burst)
		// Behind the stopped pass's position (page 1 of a pass that already
		// read pages 1-2) and behind the frontier's own stamp, landing after
		// the pass went past it.
		h.workItem("WI-burst-00300-late", now.Add(-3*time.Hour), burst.Add(-time.Second))
		restarted, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		restarted.SetClockForTest(func() time.Time { return now })
		h.src = restarted
		second := h.drain(first.cursor)
		if second.items[title("WI-burst-00300-late")].Subject.Label == "" {
			t.Fatalf("the restarted source did not re-read the part of the window the old pass had passed (%d items)", len(second.items))
		}
		if second.cursor != first.cursor {
			t.Fatalf("the re-read moved the cursor (%q -> %q)", first.cursor, second.cursor)
		}
	})

	// An ingest-time cursor makes equal timestamps the normal case: one sync
	// batch stamps thousands of rows with one last_synced. The keyset is
	// (ingest stamp, row key) with a strict key tie-breaker, and the bound is
	// sent at the column's full (microsecond) precision -- a bound truncated
	// to milliseconds re-reads every row of that millisecond on every page,
	// so a page of 201 rows sharing one millisecond repeats forever.
	// teams.last_synced is DateTime64(6). 250 rows is the smallest shape that
	// needs a second page (200 rows per page) inside one millisecond.
	for _, tc := range []struct {
		name  string
		orgID string
		stamp string // microseconds since epoch, per row (number = 0..249)
	}{
		{"250 rows sharing one microsecond-exact ingest stamp", "72630000-0000-4000-8000-00000000000e", "?"},
		{"250 rows whose ingest stamps differ only in microseconds", "72630000-0000-4000-8000-00000000000f", "? + number"},
	} {
		t.Run("equal ingest stamps page to the end: "+tc.name, func(t *testing.T) {
			t.Parallel()
			src, err := devhealthsource.NewTeamsProjectsSource(query, true)
			if err != nil {
				t.Fatal(err)
			}
			base := now.Add(-5 * time.Minute).Truncate(time.Millisecond).Add(123 * time.Microsecond)
			mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active)
SELECT concat('T-', leftPad(toString(number), 4, '0')), concat('team ', toString(number)), '', ?, fromUnixTimestamp64Micro(`+tc.stamp+`, 'UTC'), ?, 'linear', concat('T-', toString(number)), [], 1 FROM numbers(250)`,
				now.Add(-24*time.Hour), base.UnixMicro(), tc.orgID)
			var distinct, total uint64
			if err := direct.QueryRow(ctx, `SELECT uniqExact(last_synced), count() FROM teams FINAL WHERE org_id = ?`, tc.orgID).Scan(&distinct, &total); err != nil {
				t.Fatal(err)
			}
			if total != 250 || (tc.stamp == "?" && distinct != 1) || (tc.stamp != "?" && distinct != 250) {
				t.Fatalf("precondition: %d rows, %d distinct stamps", total, distinct)
			}
			h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.TeamsProjectsSourceName, orgID: tc.orgID}
			got := h.drain("")
			teams := 0
			for _, e := range got.all {
				if e.Subject.Kind == contractsv1.ContextFabricSubjectTeam {
					teams++
				}
			}
			if teams != 250 {
				t.Fatalf("%d of 250 teams projected", teams)
			}
			for i, b := range got.batches {
				if b.NextCursor == b.Cursor {
					t.Fatalf("batch %d did not advance the cursor", i)
				}
			}
		})
	}

	// PeekProjectionBatch must stay side-effect free: a peek that walks the
	// window may not mark a late row as emitted, or the next real tick skips
	// a row no batch ever carried.
	t.Run("a peek does not consume a late row in the overlap window", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "72630000-0000-4000-8000-000000000007", "72630000-0000-4000-8000-0000000000a7", 15*time.Minute, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		h.workItem("WI-late", now.Add(-3*time.Hour), now.Add(-20*time.Minute))
		peeker, ok := h.src.(contextfabric.ProjectionPeeker)
		if !ok {
			t.Fatal("the source no longer implements ProjectionPeeker; this case needs a new home")
		}
		available, err := peeker.PeekProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: h.orgID, Source: devhealthsource.SourceName, Cursor: first.cursor})
		if err != nil || !available {
			t.Fatalf("peek over a window holding a late row: available=%v err=%v, want true", available, err)
		}
		if second := h.drain(first.cursor); second.items[title("WI-late")].Subject.Label == "" {
			t.Fatalf("the late row was not projected after a peek: the peek consumed it (%d items)", len(second.items))
		}
	})

	// A build-aside rebuild drains the same organization into a second graph
	// epoch through the same source: a late row emitted into one epoch's graph
	// must still reach the other's.
	t.Run("each graph epoch receives a late row on its own", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, "72630000-0000-4000-8000-000000000008", "72630000-0000-4000-8000-0000000000a8", 15*time.Minute, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		serving, building := h.drainEpoch("", 1), h.drainEpoch("", 2)
		h.workItem("WI-late", now.Add(-3*time.Hour), now.Add(-20*time.Minute))
		if got := h.drainEpoch(serving.cursor, 1); got.items[title("WI-late")].Subject.Label == "" {
			t.Fatalf("epoch 1 did not receive the late row")
		}
		if got := h.drainEpoch(building.cursor, 2); got.items[title("WI-late")].Subject.Label == "" {
			t.Fatalf("epoch 2 did not receive the late row: the emitted-row memo is shared across epochs")
		}
	})

	// The teams/projects source keeps cumulative per-organization telemetry
	// (rows read, edges asserted). The overlap walk re-reads rows that were
	// already read and counted; counting them again on every caught-up tick
	// would make the counters grow while nothing happens.
	t.Run("caught-up ticks do not inflate the teams/projects run telemetry", func(t *testing.T) {
		t.Parallel()
		const orgID, repoID = "72630000-0000-4000-8000-000000000009", "72630000-0000-4000-8000-0000000000a9"
		logs := &bytes.Buffer{}
		src, err := devhealthsource.NewTeamsProjectsSource(query, true)
		if err != nil {
			t.Fatal(err)
		}
		src.WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
		recent := now.Add(-5 * time.Minute)
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`, repoID, "acme/tele", nil, recent, nil, recent, orgID, "github")
		mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"T-tele", "tele team", "", recent, recent, orgID, "github", "T-tele", []string{}, uint8(1))
		mustExec(t, ctx, direct, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "T-tele", repoID, "acme/tele", "exact", "native", uint8(1), uint16(100), int32(0), recent.Add(-time.Hour), nil, recent)
		h := &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.TeamsProjectsSourceName, orgID: orgID}
		lastAsserted := func() string {
			const key = "repository_team_edges_asserted="
			text := logs.String()
			i := strings.LastIndex(text, key)
			if i < 0 {
				t.Fatalf("no ownership telemetry line logged:\n%s", text)
			}
			rest := text[i+len(key):]
			return rest[:strings.IndexAny(rest, " \n")]
		}
		first := h.drain("")
		if len(first.batches) == 0 {
			t.Fatal("nothing projected")
		}
		before := lastAsserted()
		if before != "1" {
			t.Fatalf("asserted edges after the first drain = %s, want 1", before)
		}
		if again := h.drain(first.cursor); len(again.batches) != 0 {
			t.Fatalf("an idle tick emitted %d batches", len(again.batches))
		}
		if after := lastAsserted(); after != before {
			t.Fatalf("repository_team_edges_asserted grew from %s to %s over an idle tick: the overlap re-read is counted again every tick", before, after)
		}
	})

	// A window busier than one call's walk. The window is walked page by page
	// at the ordinary page size (the client's max_result_rows is 1,000 and
	// throws past it), and a call walks at most a few pages; a deeper window
	// is NOT cut off there: the walk stops at the last fully read row and
	// resumes from it on the next call, so a late row at any depth is found
	// (lossless). With the walk lowered to 2 pages per call, the planted
	// window is 500 rows (3 pages) with late rows on pages 1, 2 and 3.
	t.Run("overlap over a window deeper than one tick's walk: no read error, every late row found at any depth", func(t *testing.T) {
		t.Parallel()
		logs := &bytes.Buffer{}
		h := newHarness(t, "72630000-0000-4000-8000-000000000006", "72630000-0000-4000-8000-0000000000a6", 15*time.Minute, logs)
		lowerWalk(h)
		burst := now.Add(-5 * time.Minute)
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('WI-burst-', leftPad(toString(number), 5, '0')), ?, ?, concat('issue WI-burst-', toString(number)), 'open', '', '', 'linear', '', ?, ? FROM numbers(?)`,
			h.repo, h.orgID, now.Add(-time.Hour), burst, burstRows)
		first := h.drain("")
		if got := len(first.items); got != burstRows {
			t.Fatalf("first drain projected %d work items, want %d", got, burstRows)
		}
		// Caught up over a 3-page window: no error, nothing re-emitted, and
		// the walk says it stopped short of the window's end.
		if again := h.drain(first.cursor); len(again.batches) != 0 {
			t.Fatalf("a caught-up tick over a seen window emitted %d batches", len(again.batches))
		}
		if !strings.Contains(logs.String(), "overlap window pass continues on the next tick") {
			t.Errorf("a walk stopped short of the window's end must be logged; logs:\n%s", logs.String())
		}
		// First ONLY a deep late row, between burst rows 450 and 451 (page 3):
		// nothing unseen sits on pages 1-2, so it is reached only if the pass
		// RESUMES where the previous tick stopped rather than restarting from
		// the window's start.
		h.workItem("WI-burst-00450-late", now.Add(-3*time.Hour), burst)
		if deep := h.drain(first.cursor); deep.items[title("WI-burst-00450-late")].Subject.Label == "" {
			t.Fatalf("a late row on page 3 behind two seen pages was not projected: the pass restarted instead of resuming (%d items)", len(deep.items))
		}
		// Then late rows before the burst (page 1) and between burst rows 250
		// and 251 (page 2), all BEHIND the frontier (the last burst row).
		late := []string{"WI-late-shallow", "WI-burst-00250-late"}
		h.workItem(late[0], now.Add(-3*time.Hour), burst.Add(-time.Minute))
		h.workItem(late[1], now.Add(-3*time.Hour), burst)
		second := h.drain(first.cursor)
		for _, id := range late {
			if _, ok := second.items[title(id)]; !ok {
				t.Errorf("late row %s was not projected (%d items in the drain): the window walk skipped rows", id, len(second.items))
			}
		}
		if second.cursor != first.cursor {
			t.Fatalf("the window walk moved the cursor (%q -> %q)", first.cursor, second.cursor)
		}
	})
}

// countingQueryClient counts the statements a source sends through it.
type countingQueryClient struct {
	inner      contextpacket.ClickHouseQueryClient
	statements int
}

func (c *countingQueryClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements++
	return c.inner.Query(ctx, statement, bindings)
}
