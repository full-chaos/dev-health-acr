//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"strings"
	"testing"
)

// futureRepositories names every seeded repository whose created_at lies after
// the store clock. A repository node starts at created_at and the by-kind
// listing serves only nodes valid now, so such a repository is seeded and
// projected yet never listed until the clock catches up; every later use-case
// failure then reads as a serving defect. rows are (repo, created_at, now) as
// ClickHouse prints them.
func futureRepositories(rows [][]string) ([]string, error) {
	var future []string
	for _, row := range rows {
		if len(row) != 3 {
			return nil, fmt.Errorf("want (repo, created_at, now), got %v", row)
		}
		// ClickHouse prints DateTime as "YYYY-MM-DD hh:mm:ss", which orders as text.
		if row[1] > row[2] {
			future = append(future, fmt.Sprintf("%s created_at=%s now=%s", row[0], row[1], row[2]))
		}
	}
	return future, nil
}

func TestFutureRepositoriesNamesOnlyThoseSeededAfterTheClock(t *testing.T) {
	got, err := futureRepositories([][]string{
		{"acme/past", "2026-10-08 00:40:10", "2026-10-08 00:41:00"},
		{"acme/future", "2026-10-08 03:10:26", "2026-10-08 01:02:06"},
		{"acme/now", "2026-10-08 01:02:06", "2026-10-08 01:02:06"},
	})
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[0], "acme/future ") {
		t.Fatalf("futureRepositories = %v, %v, want only acme/future", got, err)
	}
	if _, err := futureRepositories([][]string{{"acme/short", "2026-10-08 00:40:10"}}); err == nil {
		t.Fatal("a row without the store clock was accepted")
	}
}

// Runs before the use-case tests (file order): a seed clock fault is named here,
// with the values, rather than surfacing as missing repositories elsewhere.
func TestSeededRepositoriesStartNoLaterThanNow(t *testing.T) {
	rows := ch(t, fmt.Sprintf("SELECT repo, toString(created_at), toString(now()) FROM repos FINAL WHERE org_id = %s", sqlStr(orgID(t))))
	if len(rows) == 0 {
		t.Fatal("the organization holds no repositories")
	}
	future, err := futureRepositories(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(future) > 0 {
		t.Fatalf("seeded repositories start after the store clock, so the by-kind listing cannot serve them yet: %s", strings.Join(future, "; "))
	}
}
