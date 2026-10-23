package devhealthsource_test

// The window walk serves a complete table's next page from the rows of an
// earlier read. This holds only where, for two cursors c0 before c1 and a read
// at c0 that was complete, the rows past c1 of that read are, value for value,
// what a statement at c1 returns. The teams source's producers read joined,
// aggregated and cursor-pruned statements, so the property is proven here on
// the real SQL for every producer the registry marks carry.

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestWindowCarryMatchesTheStatementOnTheRealSQL(t *testing.T) {
	const seed = 20261023
	rng := rand.New(rand.NewSource(seed))
	f := newIngestColumnsFixture(t, "10715000-0000-4000-8000-000000000001", nil, nil)
	ctx := f.ctx
	base := f.old
	stamp := func() time.Time {
		return base.Add(time.Duration(rng.Intn(14))*time.Minute + time.Duration(rng.Intn(4))*250*time.Millisecond)
	}
	repos := []string{"10715000-0000-4000-8000-0000000000a1", "10715000-0000-4000-8000-0000000000a2", "10715000-0000-4000-8000-0000000000a3"}
	for i, id := range repos {
		mustExec(t, ctx, f.h.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
			id, fmt.Sprintf("acme/carry-%d", i), nil, base, nil, base, f.h.orgID, "github")
	}
	teams := []string{"T-1", "T-2", "T-3", "T-4", "T-5", "T-6"}
	for _, id := range teams {
		f.team(id, stamp(), stamp())
	}
	projects := make([]string, 8)
	for i := range projects {
		projects[i] = fmt.Sprintf("10715000-0000-4000-8000-0000000001%02d", i)
		f.project(projects[i], fmt.Sprintf("K%d", i), stamp(), stamp())
	}
	for i := 0; i < 40; i++ {
		f.ownership(teams[rng.Intn(len(teams))], projects[rng.Intn(len(projects))], fmt.Sprintf("K%d", rng.Intn(len(projects))), stamp(), stamp())
	}
	for i := 0; i < 60; i++ {
		item := fmt.Sprintf("linear:CARRY-%03d", i)
		project := ""
		if rng.Intn(3) != 0 {
			project = projects[rng.Intn(len(projects))]
		}
		f.workItem(item, project, stamp(), stamp())
		f.attribution(item, teams[rng.Intn(len(teams))], []string{"native_team", "manual"}[rng.Intn(2)], uint8(rng.Intn(2)), stamp())
		if rng.Intn(2) == 0 {
			f.transition(item, projects[rng.Intn(len(projects))], "K1", fmt.Sprintf("linear:ev-%d-a", i), stamp(), stamp())
		}
		if rng.Intn(5) == 0 {
			f.removal(item, projects[rng.Intn(len(projects))], "K1", fmt.Sprintf("linear:ev-%d-r", i), stamp(), stamp())
		}
	}
	for i := 0; i < 24; i++ {
		at := stamp()
		mustExec(t, ctx, f.h.direct,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.h.orgID, "github", teams[rng.Intn(len(teams))], repos[rng.Intn(len(repos))], "acme/carry-0", "exact", []string{"native", "manual"}[rng.Intn(2)], uint8(rng.Intn(2)), uint16(100), int32(0), at, nil, at)
	}

	query, _ := orgIsolationClickHouseFixture(t)
	limits := []int{3, 7, 50, 200}
	for _, producer := range devhealthsource.CarryProducersForTest() {
		positions, err := devhealthsource.CarryPositionsForTest(ctx, query, f.h.orgID, producer.Name, 200)
		if err != nil {
			t.Fatalf("%s: %v", producer.Name, err)
		}
		if len(positions) < 8 {
			t.Fatalf("%s: the seed produced %d positions, want at least 8", producer.Name, len(positions))
		}
		trials, skipped, mismatches := 0, 0, 0
		for trial := 0; trial < 60; trial++ {
			i, j := rng.Intn(len(positions)+1)-1, rng.Intn(len(positions))
			if i > j {
				i, j = j, i
			}
			c0 := devhealthsource.CarryCursorForTest{}
			if i >= 0 {
				c0 = positions[i]
			}
			c1 := positions[j]
			result, err := devhealthsource.CarryTrialRunForTest(ctx, query, f.h.orgID, producer.Name, c0, c1, limits[rng.Intn(len(limits))])
			if err != nil {
				t.Fatalf("%s trial %d: %v", producer.Name, trial, err)
			}
			if result.Skipped {
				skipped++
				continue
			}
			trials++
			if !result.Equal {
				mismatches++
				if producer.Carry {
					t.Errorf("%s: from %v to %v the carry holds %d rows, the statement returns %d:\n  carried %v\n  fetched %v", producer.Name, c0, c1, len(result.Carried), len(result.Fetched), headKeys(result.Carried), headKeys(result.Fetched))
				}
			}
		}
		t.Logf("carry-parity table=%s carry=%v trials=%d skipped=%d mismatches=%d", producer.Name, producer.Carry, trials, skipped, mismatches)
		if trials < 15 {
			t.Errorf("%s: only %d of 60 trials read a complete table; the seed or the limits are too small to decide", producer.Name, trials)
		}
		if !producer.Carry && mismatches == 0 {
			t.Logf("table %s is excluded from the carry and passed every trial", producer.Name)
		}
	}
}

func headKeys(keys []string) []string {
	if len(keys) > 3 {
		return keys[:3]
	}
	return keys
}
