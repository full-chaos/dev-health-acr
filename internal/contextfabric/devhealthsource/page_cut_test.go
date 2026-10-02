package devhealthsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type keysetRow struct {
	at  time.Time
	key string
}

// keysetRows answers a statement the way the server does: the rows strictly
// after the bound (since, after), in (position, row key) order, cut at the
// statement's row limit. Its order is written out here, apart from the code
// under test.
type keysetRows []keysetRow

func (rows keysetRows) Query(_ context.Context, _ string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	var since time.Time
	var after string
	limit := len(rows)
	for _, binding := range bindings {
		switch binding.Name {
		case "since_us":
			since = time.UnixMicro(binding.Value.(int64)).UTC()
		case "after":
			after = binding.Value.(string)
		case "row_limit":
			limit = int(binding.Value.(uint32))
		}
	}
	var out []keysetRow
	for _, row := range rows {
		if row.at.After(since) || (row.at.Equal(since) && row.key > after) {
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].at.Equal(out[j].at) {
			return out[i].key < out[j].key
		}
		return out[i].at.Before(out[j].at)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return &keysetScanner{rows: out}, nil
}

type keysetScanner struct {
	rows []keysetRow
	next int
}

func (s *keysetScanner) Next() bool { return s.next < len(s.rows) }

func (s *keysetScanner) Scan(dest ...any) error {
	row := s.rows[s.next]
	s.next++
	*dest[0].(*time.Time), *dest[1].(*string) = row.at, row.key
	return nil
}

func (s *keysetScanner) Err() error   { return nil }
func (s *keysetScanner) Close() error { return nil }

// voidKey marks a row whose scan yields no candidate.
const voidKey = "void"

func scanKeysetRow(r contextpacket.ClickHouseRowScanner) ([]candidate, error) {
	var at time.Time
	var key string
	if err := r.Scan(&at, &key); err != nil {
		return nil, err
	}
	if strings.HasPrefix(key, voidKey) {
		return nil, nil
	}
	return []candidate{progressCandidate(at, key)}, nil
}

// keysetTable is a producer over rows that reads through the real fetch.
func keysetTable(name string, rows keysetRows) entityTable {
	return entityTable{name: name, query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, rows, "", rowLimitBindings(orgID, cursor, limit), limit, scanKeysetRow)
	}}
}

func numbered(at time.Time, prefix string, from, to int) keysetRows {
	rows := make(keysetRows, 0, to-from)
	for n := from; n < to; n++ {
		rows = append(rows, keysetRow{at: at, key: fmt.Sprintf("%s%03d", prefix, n)})
	}
	return rows
}

func keysOf(rows []candidate) []string {
	keys := make([]string, len(rows))
	for i, c := range rows {
		keys[i] = c.table + "/" + c.sortKey
	}
	return keys
}

func keysetKeys(table string, rows keysetRows) []string {
	keys := make([]string, len(rows))
	for i, row := range rows {
		keys[i] = table + "/" + row.key
	}
	return keys
}

// drainPages runs the paged read from an empty cursor until it is caught up
// and returns every page the cursor moved past, and the bounded-page lines.
func drainPages(t *testing.T, tables ...entityTable) (pages [][]string, bounded []map[string]any, err error) {
	t.Helper()
	var logs bytes.Buffer
	plan := sourcePlan{
		client: keysetRows{}, source: "page_cut_test", version: "v1", tables: tables,
		logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
		observePage: func(all []candidate) { pages = append(pages, keysOf(all)) },
	}
	_, available, err := plan.pagedBatch(context.Background(), "org", "", cursorState{}, false)
	if available {
		t.Fatal("rows that carry no payload built a batch")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if jsonErr := json.Unmarshal(line, &record); jsonErr != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if msg, _ := record["msg"].(string); strings.HasPrefix(msg, "devhealthsource page ended at the last row a truncated table returned") {
			bounded = append(bounded, record)
		}
	}
	return pages, bounded, err
}

func requirePages(t *testing.T, got [][]string, want ...[]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the read took %d pages, want %d; page sizes %v", len(got), len(want), pageSizes(got))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("page %d holds %d rows %v ... %v, want %d rows %v ... %v", i+1, len(got[i]), head(got[i]), tail(got[i]), len(want[i]), head(want[i]), tail(want[i]))
		}
	}
}

func pageSizes(pages [][]string) []int {
	sizes := make([]int, len(pages))
	for i, page := range pages {
		sizes[i] = len(page)
	}
	return sizes
}

func head(keys []string) []string {
	if len(keys) > 3 {
		return keys[:3]
	}
	return keys
}

func tail(keys []string) []string {
	if len(keys) > 3 {
		return keys[len(keys)-3:]
	}
	return keys
}

func join(parts ...[]string) []string {
	var out []string
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

var pageCutStamp = time.Date(2026, 10, 1, 4, 37, 41, 397_000_000, time.UTC)

// One table fills its statement limit with rows on one stamp, two of them on
// one row key. The page then holds 199 distinct positions, and rows of other
// tables that sort later would complete it: a row on a later stamp with a
// smaller key, and a row on the same stamp with a larger key. The page ends at
// the last row the truncated table returned, and the next pages read the rest.
func TestPageNeverEndsPastTheLastRowATruncatedTableReturned(t *testing.T) {
	const limit = incrementalBatchCap
	first := numbered(pageCutStamp, "a", 0, 101)
	pair := keysetRows{{at: pageCutStamp, key: "a100"}}
	rest := numbered(pageCutStamp, "a", 101, 450)
	full := keysetTable("full", join3(first, pair, rest))
	earlier := keysetTable("earlier", keysetRows{{at: pageCutStamp.Add(-time.Hour), key: "zz"}})
	sameStamp := keysetTable("same_stamp", keysetRows{{at: pageCutStamp, key: "zzz"}})
	later := keysetTable("later", keysetRows{{at: pageCutStamp.Add(time.Hour), key: "0"}})

	pages, bounded, err := drainPages(t, earlier, full, sameStamp, later)
	if err != nil {
		t.Fatal(err)
	}
	// Page 1: the earlier row and the 200 statement rows a000..a198 (a100
	// twice), 200 distinct positions: the cap ends it. Page 2: a199..a398,
	// again the cap. Page 3: the last 51 rows, then the rows of the other
	// tables that sort after them.
	requirePages(t, pages,
		join([]string{"earlier/zz"}, keysetKeys("full", first), keysetKeys("full", pair), keysetKeys("full", rest[:limit-102])),
		keysetKeys("full", rest[limit-102:2*limit-102]),
		join(keysetKeys("full", rest[2*limit-102:]), []string{"same_stamp/zzz", "later/0"}),
	)
	if len(bounded) != 0 {
		t.Fatalf("pages that the cap ended wrote %d bounded-page lines: %v", len(bounded), bounded)
	}

	// Without the earlier row the page of the pair holds 199 positions and is
	// not full: the bound ends it, and says so.
	pages, bounded, err = drainPages(t, full, sameStamp, later)
	if err != nil {
		t.Fatal(err)
	}
	requirePages(t, pages,
		join(keysetKeys("full", first), keysetKeys("full", pair), keysetKeys("full", rest[:limit-102])),
		keysetKeys("full", rest[limit-102:2*limit-102]),
		join(keysetKeys("full", rest[2*limit-102:]), []string{"same_stamp/zzz", "later/0"}),
	)
	if len(bounded) != 1 || bounded[0]["table"] != "full" || bounded[0]["level"] != "WARN" ||
		bounded[0]["last_stamp"] != pageCutStamp.Format(time.RFC3339Nano) || bounded[0]["last_key_digest"] != keyDigest("a198") {
		t.Fatalf("bounded-page lines = %v, want one for table full at the last row of page 1", bounded)
	}
}

func join3(a, b, c keysetRows) keysetRows {
	out := append(keysetRows{}, a...)
	out = append(out, b...)
	return append(out, c...)
}

// Two tables are truncated on one page. The page ends at the earlier of their
// last rows, whichever table is read first.
func TestPageBoundIsTheEarliestLastRowOfTheTruncatedTables(t *testing.T) {
	const limit = incrementalBatchCap
	first := numbered(pageCutStamp, "a", 0, 101)
	pair := keysetRows{{at: pageCutStamp, key: "a100"}}
	rest := numbered(pageCutStamp, "a", 101, 300)
	early := keysetTable("early", join3(first, pair, rest))
	lateRows := numbered(pageCutStamp.Add(time.Hour), "c", 0, 299)
	late := keysetTable("late", lateRows)
	want := [][]string{
		join(keysetKeys("early", first), keysetKeys("early", pair), keysetKeys("early", rest[:limit-102])),
		join(keysetKeys("early", rest[limit-102:]), keysetKeys("late", lateRows[:limit-(len(rest)-(limit-102))])),
		keysetKeys("late", lateRows[limit-(len(rest)-(limit-102)):]),
	}
	for name, tables := range map[string][]entityTable{"early table first": {early, late}, "late table first": {late, early}} {
		pages, bounded, err := drainPages(t, tables...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		requirePages(t, pages, want...)
		if len(bounded) != 1 || bounded[0]["table"] != "early" {
			t.Fatalf("%s: bounded-page lines = %v, want one for table early", name, bounded)
		}
	}
}

// The rows on one position lie across the statement limit: the last rows the
// statement kept and the first row past the limit. A page that ended on that
// position would make the next keyset predicate pass the row past the limit,
// so the statement leaves every row of the position for the next page.
func TestRowsOnOnePositionAcrossTheStatementLimitStayTogether(t *testing.T) {
	const limit = incrementalBatchCap
	for _, group := range []int{2, 3} {
		t.Run(fmt.Sprintf("%d rows", group), func(t *testing.T) {
			// The group ends on the first row past the limit (index limit).
			before := numbered(pageCutStamp, "a", 0, limit+1-group)
			var shared keysetRows
			for n := 0; n < group; n++ {
				shared = append(shared, keysetRow{at: pageCutStamp, key: fmt.Sprintf("a%03d", limit+1-group)})
			}
			after := numbered(pageCutStamp, "a", limit+2-group, 450)
			full := keysetTable("full", join3(before, shared, after))
			later := keysetTable("later", keysetRows{{at: pageCutStamp.Add(time.Hour), key: "0"}})

			pages, bounded, err := drainPages(t, full, later)
			if err != nil {
				t.Fatal(err)
			}
			second := limit - group
			requirePages(t, pages,
				keysetKeys("full", before),
				join(keysetKeys("full", shared), keysetKeys("full", after[:second])),
				join(keysetKeys("full", after[second:]), []string{"later/0"}),
			)
			if len(bounded) != 2 {
				t.Fatalf("%d bounded-page lines, want 2 (pages 1 and 2): %v", len(bounded), bounded)
			}
		})
	}
}

// More rows share one position than one page holds. No keyset step can pass
// through them without a loss, so the read fails and the cursor stays.
func TestMoreRowsOnOnePositionThanAPageHoldsFailTheRead(t *testing.T) {
	var rows keysetRows
	for n := 0; n <= incrementalBatchCap; n++ {
		rows = append(rows, keysetRow{at: pageCutStamp, key: "same"})
	}
	pages, _, err := drainPages(t, keysetTable("stuck", rows), keysetTable("later", keysetRows{{at: pageCutStamp.Add(time.Hour), key: "0"}}))
	var rejection *ProducerRejection
	if !errors.As(err, &rejection) || !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("err = %v, want a producer rejection classified as unavailable", err)
	}
	if want := "read stuck: more rows share one cursor position than one page holds"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want it to name the table and the cause: %q", err, want)
	}
	if len(pages) != 0 {
		t.Fatalf("the failed read moved the cursor past %d pages", len(pages))
	}
}

func TestFetchKeepsWholePositionsInsideTheLimit(t *testing.T) {
	at := pageCutStamp
	row := func(key string) keysetRow { return keysetRow{at: at, key: key} }
	for _, tc := range []struct {
		name      string
		rows      keysetRows
		limit     int
		want      []string
		truncated bool
	}{
		{name: "fewer rows than the limit", rows: keysetRows{row("a"), row("b")}, limit: 3, want: []string{"a", "b"}},
		{name: "as many rows as the limit", rows: keysetRows{row("a"), row("b"), row("c")}, limit: 3, want: []string{"a", "b", "c"}},
		{name: "a distinct row past the limit", rows: keysetRows{row("a"), row("b"), row("c"), row("d")}, limit: 3, want: []string{"a", "b", "c"}, truncated: true},
		{name: "a pair inside the limit", rows: keysetRows{row("a"), row("b"), row("b"), row("c")}, limit: 3, want: []string{"a", "b", "b"}, truncated: true},
		{name: "a pair across the limit", rows: keysetRows{row("a"), row("b"), row("c"), row("c")}, limit: 3, want: []string{"a", "b"}, truncated: true},
		{name: "three rows across the limit", rows: keysetRows{row("a"), row("c"), row("c"), row("c")}, limit: 3, want: []string{"a"}, truncated: true},
		{name: "every row on one position", rows: keysetRows{row("c"), row("c"), row("c"), row("c")}, limit: 3, want: []string{}, truncated: true},
		{name: "one row key at two stamps across the limit", rows: keysetRows{row("a"), row("b"), row("c"), {at: at.Add(time.Millisecond), key: "c"}}, limit: 3, want: []string{"a", "b", "c"}, truncated: true},
		{name: "the row past the limit yields no candidate", rows: keysetRows{row("a"), row("b"), row("c"), row(voidKey)}, limit: 3, want: []string{"a", "b", "c"}, truncated: true},
		{name: "the last row inside the limit yields no candidate", rows: keysetRows{row("a"), row("b"), row(voidKey), row(voidKey + "2")}, limit: 3, want: []string{"a", "b"}, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated, err := fetch(context.Background(), tc.rows, "", rowLimitBindings("org", cursorState{}, tc.limit), tc.limit, scanKeysetRow)
			if err != nil {
				t.Fatal(err)
			}
			keys := make([]string, len(got))
			for i, c := range got {
				keys[i] = c.sortKey
			}
			if !reflect.DeepEqual(keys, tc.want) || truncated != tc.truncated {
				t.Fatalf("fetch kept %v truncated=%v, want %v truncated=%v", keys, truncated, tc.want, tc.truncated)
			}
		})
	}
}

// The caught-up overlap walk reads the trailing window with the same page cut.
// A walk position that moves past the last row a truncated table returned
// leaves that table's later rows unread in this pass and in every pass after.
func TestOverlapWalkNeverStepsPastTheLastRowATruncatedTableReturned(t *testing.T) {
	first := numbered(pageCutStamp, "a", 0, 101)
	pair := keysetRows{{at: pageCutStamp, key: "a100"}}
	rest := numbered(pageCutStamp, "a", 101, 450)
	var logs bytes.Buffer
	now := pageCutStamp.Add(10 * time.Minute)
	plan := sourcePlan{
		client: keysetRows{}, source: "page_cut_test", version: "v1",
		tables: []entityTable{
			keysetTable("full", join3(first, pair, rest)),
			keysetTable("later", keysetRows{{at: pageCutStamp.Add(30 * time.Second), key: "0"}}),
		},
		now: func() time.Time { return now }, overlap: 15 * time.Minute, window: newWindowMemo(), windowScope: windowScopeFor("org", 0),
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	// Every row lies behind the frontier, inside the window.
	frontier := cursorState{Since: pageCutStamp.Add(time.Minute), After: "zz"}
	for call := 0; call < 8; call++ {
		if _, available, err := plan.overlapBatch(context.Background(), "org", "cursor", frontier); err != nil || available {
			t.Fatalf("call %d: available=%v err=%v", call, available, err)
		}
	}
	walked := map[string]bool{}
	for key := range plan.window.scopes[plan.windowScope].seen {
		walked[key] = true
	}
	want := join(keysetKeys("full", first), keysetKeys("full", rest), []string{"later/0"})
	missing := 0
	for _, c := range []struct {
		table string
		rows  keysetRows
	}{{"full", first}, {"full", rest}, {"later", keysetRows{{at: pageCutStamp.Add(30 * time.Second), key: "0"}}}} {
		for _, row := range c.rows {
			if !walked[rowMemoKey(candidate{table: c.table, observedAt: row.at, sortKey: row.key})] {
				missing++
			}
		}
	}
	if missing != 0 || len(walked) != len(want) {
		t.Fatalf("the walk reached %d of %d rows; %d rows were never read", len(walked), len(want), missing)
	}
	if !strings.Contains(logs.String(), `"msg":"devhealthsource page ended at the last row a truncated table returned`) || !strings.Contains(logs.String(), `"table":"full"`) {
		t.Fatalf("the walk page that the bound ended wrote no bounded-page line:\n%s", logs.String())
	}
}
