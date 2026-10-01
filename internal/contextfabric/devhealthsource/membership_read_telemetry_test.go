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

// A page built again from the same cursor is a replay: it is logged, and the
// distinct totals do not count its rows twice. Rows of other tables in the
// page are not membership rows.
func TestMembershipReadLedgerCountsAReplayedPageOnce(t *testing.T) {
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	page := []candidate{
		membershipRow("work_item_column", "a", at), membershipRow("work_item_column", "b", at),
		{table: "work_item_team_attributions", sortKey: "x", observedAt: at},
		membershipRow("transition", "c", at),
	}
	ledger := &presenceTelemetryLedger{}
	for i := 0; i < 2; i++ {
		ledger.recordStatement(page, true)
		ledger.recordConsumed(page)
	}
	next := []candidate{membershipRow("work_item_column", "d", at.Add(time.Second))}
	ledger.recordStatement(next, false)
	ledger.recordConsumed(next)
	if got := ledger.membershipConsumedTotal("work_item_column"); got != 3 {
		t.Fatalf("work_item_column consumed total = %d, want 3 (a, b, d; the replay of a, b counts once)", got)
	}
	if got := ledger.membershipConsumedTotal("transition"); got != 1 {
		t.Fatalf("transition consumed total = %d, want 1", got)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	logMembershipPages(context.Background(), logger, "org", ledger)
	out := logs.String()
	if n := strings.Count(out, "replay=true"); n != 2 {
		t.Fatalf("%d replay page lines, want 2 (one per arm of the replayed page):\n%s", n, out)
	}
	if strings.Contains(out, "read drained") {
		t.Fatalf("a summary was logged while the last statement still returned rows:\n%s", out)
	}

	// The next statement returns nothing: the read is drained, one summary per arm.
	logs.Reset()
	ledger.recordStatement(nil, false)
	logMembershipPages(context.Background(), logger, "org", ledger)
	out = logs.String()
	if !strings.Contains(out, "arm=work_item_column rows_total=3 pages=2") || !strings.Contains(out, "arm=transition rows_total=1 pages=1") {
		t.Fatalf("drained summary lines are wrong:\n%s", out)
	}
	logs.Reset()
	ledger.recordStatement(nil, false)
	logMembershipPages(context.Background(), logger, "org", ledger)
	if logs.Len() != 0 {
		t.Fatalf("an idle statement logged again:\n%s", logs.String())
	}
}
