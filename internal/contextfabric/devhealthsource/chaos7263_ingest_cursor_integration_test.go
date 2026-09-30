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

	// The window is [frontier - overlap, frontier]: the lower edge itself is
	// inside, one millisecond (the column's precision) below it is not.
	t.Run("overlap boundary: the window's lower edge is inclusive, one tick below it is not", func(t *testing.T) {
		const overlap = 15 * time.Minute
		h := newHarness(t, "72630000-0000-4000-8000-000000000005", "72630000-0000-4000-8000-0000000000a5", overlap, nil)
		frontier := now.Add(-10 * time.Minute)
		h.workItem("WI-frontier", now.Add(-time.Hour), frontier)
		first := h.drain("")
		h.workItem("WI-at-edge", now.Add(-4*time.Hour), frontier.Add(-overlap))
		h.workItem("WI-below-edge", now.Add(-4*time.Hour), frontier.Add(-overlap-time.Millisecond))
		second := h.drain(first.cursor)
		if _, ok := second.items[title("WI-at-edge")]; !ok {
			t.Fatalf("a row stamped exactly at frontier-overlap was not re-read: %v", second.items)
		}
		if _, ok := second.items[title("WI-below-edge")]; ok {
			t.Fatalf("a row stamped 1ms below the window was re-read: the window is wider than documented")
		}
	})

	// A window busier than one read: the client's max_result_rows is 1,000
	// and throws past it, so the window must be walked page by page at the
	// ordinary page size, never read in one oversized statement. A late row
	// near the window's start is found; one deeper than the walk bound is the
	// documented limit and is logged.
	t.Run("overlap over a window holding more than 1,000 rows: no read error, late row found, depth bound logged", func(t *testing.T) {
		logs := &bytes.Buffer{}
		h := newHarness(t, "72630000-0000-4000-8000-000000000006", "72630000-0000-4000-8000-0000000000a6", 15*time.Minute, logs)
		burst := now.Add(-5 * time.Minute)
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('WI-burst-', leftPad(toString(number), 5, '0')), ?, ?, concat('issue WI-burst-', toString(number)), 'open', '', '', 'linear', '', ?, ? FROM numbers(1100)`,
			h.repo, h.orgID, now.Add(-time.Hour), burst)
		first := h.drain("")
		if got := len(first.items); got != 1100 {
			t.Fatalf("first drain projected %d work items, want 1100", got)
		}
		// Caught up over a 1,100-row window: must not fail, must emit nothing.
		if again := h.drain(first.cursor); len(again.batches) != 0 {
			t.Fatalf("a caught-up tick over a seen window emitted %d batches", len(again.batches))
		}
		// Late, inside the window, BEFORE the burst: on the first page walked.
		h.workItem("WI-late-shallow", now.Add(-3*time.Hour), burst.Add(-time.Minute))
		// Late, at the burst's own instant, sorting between burst rows 500 and
		// 501: BEHIND the frontier, on the window's third page -- found only if
		// the walk steps past pages whose rows were all seen.
		h.workItem("WI-burst-00500-late", now.Add(-3*time.Hour), burst)
		// Late, at the burst's own instant, sorting between burst rows 1,050
		// and 1,051 -- BEHIND the frontier (the last burst row) and deeper
		// into the window than the walk bound reaches.
		h.workItem("WI-burst-01050-late", now.Add(-3*time.Hour), burst)
		second := h.drain(first.cursor)
		if _, ok := second.items[title("WI-late-shallow")]; !ok {
			t.Fatalf("a late row on the window's first page was not projected: %v", len(second.items))
		}
		if _, ok := second.items[title("WI-burst-00500-late")]; !ok {
			t.Fatalf("a late row on the window's third page was not projected: the walk did not step past seen pages")
		}
		if _, ok := second.items[title("WI-burst-01050-late")]; ok {
			t.Fatalf("a late row deeper than the walk bound was projected: the bound this test documents is gone -- update the bound's docs")
		}
		if !strings.Contains(logs.String(), "overlap window walk stopped at its depth bound") {
			t.Fatalf("hitting the window's depth bound must be logged; logs:\n%s", logs.String())
		}
	})
}
