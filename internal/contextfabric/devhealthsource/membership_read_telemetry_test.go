package devhealthsource

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func membershipRow(arm, key string, at time.Time) candidate {
	return candidate{table: membershipTable, arm: arm, sortKey: key, cursorAt: at}
}

func consumePage(ledger *presenceTelemetryLedger, more bool, rows ...candidate) {
	ledger.recordStatement(rows, more)
	ledger.recordConsumed(rows)
}

// A row counts once in the distinct total however many pages carry it: the
// same page built again, a retry with a changed interior row, and a retry that
// overlaps the page before it. Rows of other tables are not membership rows.
func TestMembershipReadLedgerCountsEachRowOnce(t *testing.T) {
	at := time.Date(2026, 10, 1, 4, 37, 41, 423_000_000, time.UTC)
	col := func(key string) candidate { return membershipRow("work_item_column", key, at) }
	other := candidate{table: "work_item_team_attributions", sortKey: "x", observedAt: at}
	ledger := &presenceTelemetryLedger{}

	consumePage(ledger, true, col("a"), col("b"), other, col("c"), membershipRow("transition", "t", at))
	consumePage(ledger, true, col("a"), col("b"), other, col("c"), membershipRow("transition", "t", at))
	if got := ledger.membershipConsumedTotal("work_item_column"); got != 3 {
		t.Fatalf("after a replayed page: work_item_column total = %d, want 3", got)
	}
	consumePage(ledger, true, col("a"), col("x"), col("c"))
	if got := ledger.membershipConsumedTotal("work_item_column"); got != 4 {
		t.Fatalf("after a retry with a changed interior row: work_item_column total = %d, want 4 (a, b, c, x)", got)
	}
	consumePage(ledger, false, col("b"), col("c"), col("d"))
	if got := ledger.membershipConsumedTotal("work_item_column"); got != 5 {
		t.Fatalf("after an overlapping retry: work_item_column total = %d, want 5 (a, b, c, x, d)", got)
	}
	if got := ledger.membershipConsumedTotal("transition"); got != 1 {
		t.Fatalf("transition total = %d, want 1", got)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	logMembershipPages(context.Background(), logger, "org", ledger)
	out := logs.String()
	for _, want := range []string{
		"arm=work_item_column run=0 page_n=1 statement_rows=3 statement_more=true consumed_rows=3 consumed_new=3 consumed_total=3 replay=false",
		"arm=work_item_column run=0 page_n=2 statement_rows=3 statement_more=true consumed_rows=3 consumed_new=0 consumed_total=3 replay=true",
		"arm=transition run=0 page_n=2 statement_rows=1 statement_more=true consumed_rows=1 consumed_new=0 consumed_total=1 replay=true",
		"arm=work_item_column run=0 page_n=3 statement_rows=3 statement_more=true consumed_rows=3 consumed_new=1 consumed_total=4 replay=false",
		// The arm that returned nothing is stated: no row inside the limit,
		// and the statement says if rows may lie beyond it.
		"arm=transition run=0 page_n=3 statement_rows=0 statement_more=true consumed_rows=0 consumed_new=0 consumed_total=1 replay=false",
		"arm=work_item_column run=0 page_n=4 statement_rows=3 statement_more=false consumed_rows=3 consumed_new=1 consumed_total=5 replay=false",
		"arm=transition run=0 page_n=4 statement_rows=0 statement_more=false",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("page log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "read drained") {
		t.Fatalf("a summary was logged while the last statement still returned rows:\n%s", out)
	}

	// The next statement returns nothing: the read is drained, one summary per arm, once.
	logs.Reset()
	ledger.recordStatement(nil, false)
	logMembershipPages(context.Background(), logger, "org", ledger)
	out = logs.String()
	if !strings.Contains(out, "arm=work_item_column run=0 rows_total=5 pages=3") || !strings.Contains(out, "arm=transition run=0 rows_total=1 pages=1") {
		t.Fatalf("drained summary lines are wrong:\n%s", out)
	}
	if strings.Contains(out, "membership page") {
		t.Fatalf("an empty statement wrote a page line:\n%s", out)
	}
	logs.Reset()
	ledger.recordStatement(nil, false)
	logMembershipPages(context.Background(), logger, "org", ledger)
	if logs.Len() != 0 {
		t.Fatalf("an idle statement logged again:\n%s", logs.String())
	}
}

// A run that starts from an empty cursor and reads no membership row still
// says so once; a ledger of a continued run stays silent on an empty read.
func TestMembershipReadLedgerSummarisesAnEmptyFromScratchRead(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	source := &TeamsProjectsSource{}
	continued := source.presenceLedgerFor("org", false)
	continued.recordStatement(nil, false)
	logMembershipPages(context.Background(), logger, "org", continued)
	if logs.Len() != 0 {
		t.Fatalf("a continued run logged an empty read:\n%s", logs.String())
	}
	fromScratch := source.presenceLedgerFor("org", true)
	fromScratch.recordStatement(nil, false)
	logMembershipPages(context.Background(), logger, "org", fromScratch)
	out := logs.String()
	if !strings.Contains(out, "arm=work_item_column run=2 rows_total=0 pages=0 statements=1") || !strings.Contains(out, "arm=transition run=2 rows_total=0") {
		t.Fatalf("an empty from-scratch read wrote no summary with run 2:\n%s", out)
	}
}
