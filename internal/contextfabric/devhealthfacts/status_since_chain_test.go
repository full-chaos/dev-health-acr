package devhealthfacts_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

func pairPermutations(pairs [][2]string) [][][2]string {
	if len(pairs) <= 1 {
		return [][][2]string{append([][2]string(nil), pairs...)}
	}
	var out [][][2]string
	for i := range pairs {
		rest := append(append([][2]string(nil), pairs[:i]...), pairs[i+1:]...)
		for _, tail := range pairPermutations(rest) {
			out = append(out, append([][2]string{pairs[i]}, tail...))
		}
	}
	return out
}

// The pick over the last instant follows the ops order cases (dev-health-ops
// workitemmetrics/order_test.go TestOrderTransitionsIsRowOrderFree, synthetic
// statuses): each case is the LAST instant of that table, want is the to_status
// the ops order ends on. The one case where the bounded read (no prior status)
// differs from ops is named in its own cell below.
func TestLastToStatusOfInstantFollowsTheOpsOrderCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		pairs [][2]string
		want  string
	}{
		{"one transition", [][2]string{{"todo", "in_progress"}}, "in_progress"},
		{"chain at one instant", [][2]string{{"todo", "in_progress"}, {"in_progress", "done"}}, "done"},
		{"three step chain at one instant", [][2]string{{"todo", "in_progress"}, {"in_progress", "in_review"}, {"in_review", "done"}}, "done"},
		{"cycle at the first instant sorts by string", [][2]string{{"in_progress", "done"}, {"done", "in_progress"}}, "done"},
		{"cycle not leaving the prior status sorts by string", [][2]string{{"in_progress", "done"}, {"done", "in_progress"}}, "done"},
		{"no chain: string order of from then to", [][2]string{{"alpha", "yankee"}, {"beta", "zeta"}}, "zeta"},
		{"chain head first, then its successor, then string order", [][2]string{{"b", "c"}, {"a", "b"}, {"x", "y"}}, "y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for i, order := range pairPermutations(tc.pairs) {
				if got := devhealthfacts.LastToStatusOfInstantForTest(order); got != tc.want {
					t.Errorf("row order %d: last to_status = %q, want %q", i, got, tc.want)
				}
			}
		})
	}
}

// The divergence from ops, pinned: the last instant is a cycle, and ops starts
// it at the status the previous instant ended on (in_progress, so ops walks
// in_progress to done first and ends on in_progress). The bounded read has no
// previous status and takes string order (done to in_progress first), so it
// ends on done. entered_at is the same for both, so the answer can only turn
// transition_observed into transition_inconsistent or back.
func TestLastToStatusOfInstantDiffersFromOpsOnlyWhenThePriorStatusDecidesACycle(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{{"in_progress", "done"}, {"done", "in_progress"}}
	const opsWithPrior, bounded = "in_progress", "done"
	if got := devhealthfacts.LastToStatusOfInstantForTest(pairs); got != bounded || got == opsWithPrior {
		t.Fatalf("last to_status = %q, want the bounded pick %q (ops with the prior status: %q)", got, bounded, opsWithPrior)
	}
}

// A last instant with more transitions than the cap is not read: the item
// answers transition_inconsistent. At the cap it is read.
func TestStatusSinceInstantOverTheCapIsInconsistent(t *testing.T) {
	entered := statusSinceNow.Add(-time.Hour)
	instant := func(rows int) [][]any {
		froms, tos := make([]string, 0, rows), make([]string, 0, rows)
		for i := 0; i < rows; i++ {
			froms = append(froms, fmt.Sprintf("s%02d", i))
			tos = append(tos, fmt.Sprintf("s%02d", i+1))
		}
		return [][]any{{"repo-1", "W-1", uint64(rows), froms, tos, entered}}
	}
	cap := devhealthfacts.StatusSinceInstantCapForTest
	for name, tc := range map[string]struct {
		rows, current int
		want          string
	}{
		"at the cap the chain is walked":     {rows: cap, current: cap, want: "transition_observed"},
		"over the cap nothing is decided":    {rows: cap + 1, current: cap + 1, want: "transition_inconsistent"},
		"over the cap with any current":      {rows: cap + 1, current: 0, want: "transition_inconsistent"},
		"one transition is the plain answer": {rows: 1, current: 1, want: "transition_observed"},
	} {
		t.Run(name, func(t *testing.T) {
			result, _, err := readStatusSince(t,
				[][]any{{"W-1", fmt.Sprintf("s%02d", tc.current), "repo-1", "jira"}},
				instant(tc.rows), workItemSubject("repo-1", "W-1"))
			if err != nil {
				t.Fatal(err)
			}
			if _, secs, basis, _, _ := statusSinceFields(t, result, 0); basis != tc.want || (tc.want == "transition_inconsistent" && secs != nil) {
				t.Fatalf("basis = %q secs = %v, want %q", basis, secs, tc.want)
			}
		})
	}
}

// Control: one transition per instant reads exactly as before the chain read.
func TestStatusSinceOneTransitionPerInstantIsUnchanged(t *testing.T) {
	entered := statusSinceNow.Add(-90 * time.Minute)
	result, client, err := readStatusSince(t,
		[][]any{{"W-1", "in_progress", "repo-1", "jira"}},
		[][]any{transitionRow("repo-1", "W-1", "todo", "in_progress", entered)},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	since, secs, basis, _, _ := statusSinceFields(t, result, 0)
	if basis != "transition_observed" || since == nil || *since != "2026-10-11T10:30:00Z" || secs == nil || *secs != 5400 {
		t.Fatalf("since=%v secs=%v basis=%q", since, secs, basis)
	}
	// The work of the read: the status read and one transitions statement.
	if len(client.queries) != 2 {
		t.Fatalf("statements = %d, want 2 (status read, one transitions read)", len(client.queries))
	}
}

// The warn line says which items were capped: an item whose last instant holds
// more transitions than the cap and an item whose status simply differs both
// answer transition_inconsistent, and the counts must tell them apart.
func TestStatusSinceCappedInstantsAreCountedSeparatelyInTheWarn(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	rows := devhealthfacts.StatusSinceInstantCapForTest + 1
	froms, tos := make([]string, 0, rows), make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		froms = append(froms, fmt.Sprintf("s%02d", i))
		tos = append(tos, fmt.Sprintf("s%02d", i+1))
	}
	_, _, err := readStatusSince(t,
		[][]any{{"W-1", "s17", "repo-1", "jira"}},
		[][]any{{"repo-1", "W-1", uint64(rows), froms, tos, statusSinceNow.Add(-time.Hour)}},
		workItemSubject("repo-1", "W-1"))
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "inconsistent_items=1") || !strings.Contains(out, "capped_items=1") {
		t.Fatalf("the warn line does not count the capped item apart: %q", out)
	}
}

// The served answer of a same-instant chain changed (the chain is walked, not
// last_synced). An answer saved under the version before it holds the old
// null/null and must not be reused: v292 is the version before this change on
// its own, v293 the one before it when the failed-read wording change merged
// first.
func TestQueryVersionMovedPastTheStatusSinceChainPick(t *testing.T) {
	t.Parallel()
	for _, before := range []string{"devhealthfacts.clickhouse.v292", "devhealthfacts.clickhouse.v293"} {
		if devhealthfacts.QueryVersion == before {
			t.Fatalf("QueryVersion = %q, want it moved past %q -- an answer saved under it may hold a status_since the chain pick now corrects and must not be reused", devhealthfacts.QueryVersion, before)
		}
	}
}
