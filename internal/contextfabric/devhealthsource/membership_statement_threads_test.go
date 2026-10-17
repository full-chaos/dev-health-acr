package devhealthsource_test

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestMembershipStatementCapsThreadsOnEveryPrunedPage(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		since time.Time
		after string
		want  string
	}{
		{"first page", time.Time{}, "", " SETTINGS max_threads = 4"},
		{"keyset page after the first", time.Time{}, "cursor", " SETTINGS max_threads = 4"},
		{"incremental", since, "", " SETTINGS max_threads = 1"},
	}
	for _, ingest := range []bool{false, true} {
		for _, c := range cases {
			statement, _ := devhealthsource.MembershipStatementForTest("org", c.since, c.after, 151, ingest, true)
			if !strings.HasSuffix(statement, c.want) {
				t.Errorf("ingest=%v %s: statement does not end with %q", ingest, c.name, c.want)
			}
		}
	}
	unpruned, _ := devhealthsource.MembershipStatementForTest("org", time.Time{}, "", 151, true, false)
	if strings.Contains(unpruned, "SETTINGS") {
		t.Errorf("the unpruned reference statement carries settings")
	}
}
