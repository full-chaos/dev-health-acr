package devhealthsource

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func tableRow(table, key string, at time.Time) candidate {
	return candidate{table: table, sortKey: key, cursorAt: at}
}

func consumeTablePage(ledger *presenceTelemetryLedger, more bool, rows ...candidate) {
	byTable := map[string][]candidate{}
	for _, c := range rows {
		byTable[c.table] = append(byTable[c.table], c)
	}
	for _, name := range []string{"teams", "projects"} {
		ledger.recordTableStatement(name, byTable[name], more)
	}
	ledger.recordTableConsumed(rows)
}

// A row counts once however many pages carry it; the membership table has its
// own ledger and is not counted here.
func TestTableReadLedgerCountsEachRowOnce(t *testing.T) {
	at := time.Date(2026, 10, 1, 4, 37, 41, 0, time.UTC)
	team := func(key string) candidate { return tableRow("teams", key, at) }
	project := tableRow("projects", "p", at)
	member := candidate{table: membershipTable, arm: "transition", sortKey: "m", cursorAt: at}
	ledger := &presenceTelemetryLedger{}

	consumeTablePage(ledger, true, team("a"), team("b"), project, member)
	consumeTablePage(ledger, true, team("a"), team("b"), project, member)
	consumeTablePage(ledger, false, team("b"), team("c"))
	if got := ledger.tableConsumedTotal("teams"); got != 3 {
		t.Fatalf("teams total = %d, want 3", got)
	}
	if got := ledger.tableConsumedTotal("projects"); got != 1 {
		t.Fatalf("projects total = %d, want 1", got)
	}
	if got := ledger.tableConsumedTotal(membershipTable); got != 0 {
		t.Fatalf("membership counted by the table ledger: %d", got)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	logTableReadPages(context.Background(), logger, "org", ledger)
	out := logs.String()
	for _, want := range []string{
		"table=teams run=0 page_n=1 statement_rows=2 statement_more=true consumed_rows=2 consumed_new=2 consumed_total=2 replay=false",
		"table=teams run=0 page_n=2 statement_rows=2 statement_more=true consumed_rows=2 consumed_new=0 consumed_total=2 replay=true",
		"table=teams run=0 page_n=3 statement_rows=2 statement_more=false consumed_rows=2 consumed_new=1 consumed_total=3 replay=false",
		"table=projects run=0 page_n=2 statement_rows=1 statement_more=true consumed_rows=1 consumed_new=0 consumed_total=1 replay=true",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("page log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "table=projects run=0 page_n=3") {
		t.Fatalf("a page with no row for the table wrote a line:\n%s", out)
	}
	if strings.Contains(out, "table=membership") || strings.Contains(out, "read drained") {
		t.Fatalf("unexpected line:\n%s", out)
	}

	logs.Reset()
	ledger.recordTableStatement("teams", nil, false)
	ledger.recordTableStatement("projects", nil, false)
	logTableReadPages(context.Background(), logger, "org", ledger)
	out = logs.String()
	if !strings.Contains(out, "table=teams run=0 rows_total=3 pages=2 statements=4") || !strings.Contains(out, "table=projects run=0 rows_total=1 pages=1 statements=4") {
		t.Fatalf("drained summary wrong:\n%s", out)
	}
	if strings.Contains(out, "table read page") {
		t.Fatalf("an empty statement wrote a page line:\n%s", out)
	}
	logs.Reset()
	ledger.recordTableStatement("teams", nil, false)
	logTableReadPages(context.Background(), logger, "org", ledger)
	if logs.Len() != 0 {
		t.Fatalf("an idle statement logged again:\n%s", logs.String())
	}
}

// An empty read from an empty cursor says so once, per table.
func TestTableReadLedgerSummarisesAnEmptyFromScratchRead(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	source := &TeamsProjectsSource{}
	continued := source.presenceLedgerFor("org", false)
	continued.recordTableStatement("teams", nil, false)
	logTableReadPages(context.Background(), logger, "org", continued)
	if logs.Len() != 0 {
		t.Fatalf("a continued run logged an empty read:\n%s", logs.String())
	}
	fromScratch := source.presenceLedgerFor("org", true)
	fromScratch.recordTableStatement("teams", nil, false)
	logTableReadPages(context.Background(), logger, "org", fromScratch)
	if !strings.Contains(logs.String(), "table=teams run=2 rows_total=0 pages=0 statements=1") {
		t.Fatalf("no from-scratch summary with run 2:\n%s", logs.String())
	}
}

// Rows that are not new on a page that is not a replay are an error line.
func TestTableReadPageLogsAnErrorWhenAFirstReadConsumesRowsThatAreNotNew(t *testing.T) {
	at := time.Date(2026, 10, 1, 4, 37, 41, 0, time.UTC)
	team := func(key string) candidate { return tableRow("teams", key, at) }
	ledger := &presenceTelemetryLedger{}
	consumeTablePage(ledger, true, team("a"), team("b"))
	consumeTablePage(ledger, true, team("c"), team("d"), team("d"))
	consumeTablePage(ledger, false, team("d"), team("e"))
	var logs bytes.Buffer
	logTableReadPages(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), "org", ledger)
	out := logs.String()
	const msg = "table read page consumed rows that are not new and is not a replay"
	if strings.Count(out, msg) != 2 ||
		!strings.Contains(out, "page_n=2 consumed_rows=3 consumed_new=2 shared_position_rows=1") ||
		!strings.Contains(out, "page_n=3 consumed_rows=2 consumed_new=1 shared_position_rows=0") {
		t.Fatalf("error lines wrong:\n%s", out)
	}
}

func TestTableReadLedgerIsNilSafe(t *testing.T) {
	var ledger *presenceTelemetryLedger
	ledger.recordTableStatement("teams", nil, false)
	ledger.recordTableConsumed([]candidate{tableRow("teams", "a", time.Time{})})
	logTableReadPages(context.Background(), slog.Default(), "org", ledger)
	if ledger.tableConsumedTotal("teams") != 0 {
		t.Fatal("nil ledger counted a row")
	}
}

// The paged walk reports each table's statement and the page the cursor moves
// past through the hooks the source wires, and says when the read drains.
func TestPagedWalkReportsEachTablesStatementAndConsumption(t *testing.T) {
	rows := numbered(pageCutStamp, "a", 0, 3)
	ledger := &presenceTelemetryLedger{}
	var logs bytes.Buffer
	plan := sourcePlan{
		client: keysetRows{}, source: "page_cut_test", version: "v1",
		tables:      []entityTable{keysetTable("teams", rows), keysetTable("projects", numbered(pageCutStamp, "b", 0, 2))},
		logger:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		observeRead: ledger.recordTableStatement,
		observePage: ledger.recordTableConsumed,
	}
	if _, available, err := plan.pagedBatch(context.Background(), "org", "", cursorState{}, false); err != nil || available {
		t.Fatalf("pagedBatch available=%v err=%v", available, err)
	}
	logTableReadPages(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), "org", ledger)
	out := logs.String()
	for _, want := range []string{
		"table=teams run=0 page_n=1 statement_rows=3 statement_more=false consumed_rows=3 consumed_new=3 consumed_total=3 replay=false",
		"table=projects run=0 page_n=1 statement_rows=2 statement_more=false consumed_rows=2 consumed_new=2 consumed_total=2 replay=false",
		"table=teams run=0 rows_total=3 pages=1 statements=2",
		"table=projects run=0 rows_total=2 pages=1 statements=2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

// statement_rows and consumed_rows differ when the page cut leaves rows of a
// statement past the cursor, and a statement that says rows lie beyond its
// limit is not a drained read even when it returned none.
func TestTableReadLedgerKeepsStatementAndConsumedRowsApartAndNeedsNoMoreRowsToDrain(t *testing.T) {
	at := time.Date(2026, 10, 1, 4, 37, 41, 0, time.UTC)
	team := func(key string) candidate { return tableRow("teams", key, at) }
	ledger := &presenceTelemetryLedger{}
	ledger.recordTableStatement("teams", []candidate{team("a"), team("b"), team("c")}, true)
	ledger.recordTableConsumed([]candidate{team("a"), team("b")})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	logTableReadPages(context.Background(), logger, "org", ledger)
	if !strings.Contains(logs.String(), "table=teams run=0 page_n=1 statement_rows=3 statement_more=true consumed_rows=2 consumed_new=2 consumed_total=2") {
		t.Fatalf("statement and consumed rows are not kept apart:\n%s", logs.String())
	}
	logs.Reset()
	ledger.recordTableStatement("teams", nil, true)
	logTableReadPages(context.Background(), logger, "org", ledger)
	if strings.Contains(logs.String(), "read drained") {
		t.Fatalf("an empty statement that reports rows beyond its limit drained the read:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "table=teams run=0 page_n=2 statement_rows=0 statement_more=true consumed_rows=0 consumed_new=0 consumed_total=2") {
		t.Fatalf("an empty statement that reports rows beyond its limit wrote no page line:\n%s", logs.String())
	}
	ledger.recordTableStatement("teams", nil, false)
	logTableReadPages(context.Background(), logger, "org", ledger)
	if !strings.Contains(logs.String(), "table=teams run=0 rows_total=2 pages=1 statements=3") {
		t.Fatalf("the read did not drain once the statement ended:\n%s", logs.String())
	}
}
