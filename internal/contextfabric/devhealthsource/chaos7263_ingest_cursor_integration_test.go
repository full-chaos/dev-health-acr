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
)

func TestCHAOS7263IngestTimeCursor(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, st := range productionSchemaDDL() {
		if err := direct.Exec(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
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
		h := newHarness(t, "72630000-0000-4000-8000-000000000003", "72630000-0000-4000-8000-0000000000a3", 15*time.Minute, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
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
		const overlap = 15 * time.Minute
		h := newHarness(t, "72630000-0000-4000-8000-000000000005", "72630000-0000-4000-8000-0000000000a5", overlap, nil)
		h.workItem("WI-frontier", now.Add(-time.Hour), now.Add(-10*time.Minute))
		first := h.drain("")
		edge := now.Add(-2 * overlap)
		h.workItem("WI-at-edge", now.Add(-4*time.Hour), edge)
		h.workItem("WI-below-edge", now.Add(-4*time.Hour), edge.Add(-time.Millisecond))
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
		const overlap = 15 * time.Minute
		h := newHarness(t, "72630000-0000-4000-8000-00000000000a", "72630000-0000-4000-8000-0000000000aa", overlap, nil)
		frontier := now.Add(-3 * time.Hour)
		h.workItem("WI-old-frontier", now.Add(-4*time.Hour), frontier)
		first := h.drain("")
		// Stamped just behind the frontier but landing now, hours later: far
		// outside the bound, so a closed window does not re-read it.
		h.workItem("WI-hours-late", now.Add(-5*time.Hour), frontier.Add(-time.Minute))
		if second := h.drain(first.cursor); len(second.items) != 0 {
			t.Fatalf("a closed window re-read a row landing hours after its stamp: %v", second.items)
		}
	})

	// The memo is per process. A projector that restarts with a saved cursor
	// starts its first pass at (frontier - 2*overlap), so a row that landed
	// behind the frontier while no process was walking is still found.
	t.Run("a restarted projector finds a row that landed behind the saved cursor while it was down", func(t *testing.T) {
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

	// PeekProjectionBatch must stay side-effect free: a peek that walks the
	// window may not mark a late row as emitted, or the next real tick skips
	// a row no batch ever carried.
	t.Run("a peek does not consume a late row in the overlap window", func(t *testing.T) {
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
		for tick := 0; tick < 3; tick++ {
			if again := h.drain(first.cursor); len(again.batches) != 0 {
				t.Fatalf("idle tick %d emitted %d batches", tick, len(again.batches))
			}
		}
		if after := lastAsserted(); after != before {
			t.Fatalf("repository_team_edges_asserted grew from %s to %s over idle ticks: the overlap re-read is counted again every tick", before, after)
		}
	})

	// A window busier than one read. The client's max_result_rows is 1,000
	// and throws past it, so the window is walked page by page at the
	// ordinary page size, never in one oversized statement. A tick walks at
	// most a few pages; a deeper window is NOT cut off there: the walk stops
	// at the last fully read row and resumes from it on the next call, so a
	// late row at any depth is found (lossless). The planted window is 1,300
	// rows (7 pages) with late rows on pages 1, 3 and 7.
	t.Run("overlap over a window deeper than one tick's walk: no read error, every late row found at any depth", func(t *testing.T) {
		logs := &bytes.Buffer{}
		h := newHarness(t, "72630000-0000-4000-8000-000000000006", "72630000-0000-4000-8000-0000000000a6", 15*time.Minute, logs)
		burst := now.Add(-5 * time.Minute)
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('WI-burst-', leftPad(toString(number), 5, '0')), ?, ?, concat('issue WI-burst-', toString(number)), 'open', '', '', 'linear', '', ?, ? FROM numbers(1300)`,
			h.repo, h.orgID, now.Add(-time.Hour), burst)
		first := h.drain("")
		if got := len(first.items); got != 1300 {
			t.Fatalf("first drain projected %d work items, want 1300", got)
		}
		// Caught up over a 1,300-row window: no error, nothing re-emitted, and
		// the walk says it stopped short of the window's end.
		if again := h.drain(first.cursor); len(again.batches) != 0 {
			t.Fatalf("a caught-up tick over a seen window emitted %d batches", len(again.batches))
		}
		if !strings.Contains(logs.String(), "overlap window pass continues on the next tick") {
			t.Errorf("a walk stopped short of the window's end must be logged; logs:\n%s", logs.String())
		}
		// Late rows, all BEHIND the frontier (the last burst row): before the
		// burst (page 1), between burst rows 500 and 501 (page 3), and between
		// burst rows 1,250 and 1,251 (page 7, deeper than one tick walks).
		late := []string{"WI-late-shallow", "WI-burst-00500-late", "WI-burst-01250-late"}
		h.workItem(late[0], now.Add(-3*time.Hour), burst.Add(-time.Minute))
		h.workItem(late[1], now.Add(-3*time.Hour), burst)
		h.workItem(late[2], now.Add(-3*time.Hour), burst)
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
