package devhealthfacts

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTeamOwnedRepositoriesBoundIsInclusive(t *testing.T) {
	bound := factTimeBound{active: true, end: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	rowsOf := func(n int) [][]any {
		rows := make([][]any, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, []any{"team-a", fmt.Sprintf("repo-%05d", i), fmt.Sprintf("org/repo-%05d", i)})
		}
		return rows
	}
	for _, tc := range []struct {
		name    string
		rows    int
		wantErr bool
	}{
		{"one_below_bound", maxTeamOwnedRepositoryRows - 1, false},
		{"exactly_at_bound", maxTeamOwnedRepositoryRows, false},
		{"one_over_bound", maxTeamOwnedRepositoryRows + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &workItemMembershipFakeClient{scanErrAt: -1, rows: rowsOf(tc.rows)}
			owned, err := teamOwnedRepositories(context.Background(), client, "org-1", []string{"team-a"}, bound)
			if tc.wantErr {
				if err == nil || err.Error() != teamOwnedRepositoriesOverflowReason {
					t.Fatalf("want overflow error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(owned["team-a"]); got != tc.rows {
				t.Fatalf("owned = %d, want %d", got, tc.rows)
			}
		})
	}
}
