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
	teams := make([]string, 12)
	for i := range teams {
		teams[i] = fmt.Sprintf("T-%d", i+1)
	}
	for _, id := range teams {
		f.team(id, stamp(), stamp())
	}
	projects := make([]string, 12)
	for i := range projects {
		projects[i] = fmt.Sprintf("10715000-0000-4000-8000-0000000001%02d", i)
		f.project(projects[i], fmt.Sprintf("K%d", i), stamp(), stamp())
	}
	for i := 0; i < 50; i++ {
		f.ownership(teams[rng.Intn(len(teams))], projects[rng.Intn(len(projects))], fmt.Sprintf("K%d", rng.Intn(len(projects))), stamp(), stamp())
	}
	for i := 0; i < 60; i++ {
		item := fmt.Sprintf("linear:CARRY-%03d", i)
		project := ""
		if rng.Intn(3) != 0 {
			project = projects[rng.Intn(len(projects))]
		}
		f.workItem(item, project, stamp(), stamp())
		f.attribution(item, teams[rng.Intn(len(teams))], []string{"native_team", "manual_fallback"}[rng.Intn(2)], uint8(rng.Intn(2)), stamp())
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

	// A deterministic tie block: several rows per producer at ONE identical
	// stamp with distinct keys, so the (position, key) tie rule is crossed on
	// purpose and not by the luck of 250 ms random stamps.
	tie := base.Add(20 * time.Minute)
	for i := 0; i < 4; i++ {
		f.team(fmt.Sprintf("T-tie-%d", i), tie, tie)
		f.project(fmt.Sprintf("10715000-0000-4000-8000-0000000002%02d", i), fmt.Sprintf("TIE%d", i), tie, tie)
		f.ownership(teams[i], projects[i], fmt.Sprintf("K%d", i), tie, tie)
		item := fmt.Sprintf("linear:CARRY-TIE-%d", i)
		f.workItem(item, projects[i], tie, tie)
		f.attribution(item, teams[i], "native_team", 0, tie)
		f.transition(fmt.Sprintf("linear:CARRY-TIE-T-%d", i), projects[(i+1)%len(projects)], "K1", fmt.Sprintf("linear:ev-tie-%d", i), tie, tie)
		mustExec(t, ctx, f.h.direct,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.h.orgID, "github", teams[i], repos[i%len(repos)], "acme/carry-0", "exact", "native", uint8(0), uint16(100), int32(0), tie, nil, tie)
	}
	// A work item that keeps a project in its column AND has transition
	// history, at the tie stamp: the column_superseded arm.
	f.workItem("linear:CARRY-TIE-SUP", projects[0], tie, tie)
	f.transition("linear:CARRY-TIE-SUP", projects[1], "K1", "linear:ev-tie-sup", tie, tie)

	query, _ := orgIsolationClickHouseFixture(t)
	limits := []int{7, 50, 200, 200}
	for _, ingest := range []bool{true, false} {
		mode := map[bool]string{true: "ingest_columns", false: "legacy"}[ingest]
		for _, producer := range devhealthsource.CarryProducersForTest(ingest) {
			positions, err := devhealthsource.CarryPositionsForTest(ctx, query, f.h.orgID, producer.Name, ingest, 200)
			if err != nil {
				t.Fatalf("%s/%s: %v", mode, producer.Name, err)
			}
			if len(positions) < 8 {
				t.Fatalf("%s/%s: the seed produced %d positions, want at least 8", mode, producer.Name, len(positions))
			}
			var cov carryCoverage
			trial := func(label string, c0, c1 devhealthsource.CarryCursorForTest, limit int) {
				result, err := devhealthsource.CarryTrialRunForTest(ctx, query, f.h.orgID, producer.Name, ingest, c0, c1, limit)
				if err != nil {
					t.Fatalf("%s/%s %s: %v", mode, producer.Name, label, err)
				}
				cov.add(result)
				if !result.Equal && !result.Skipped && producer.Carry {
					t.Errorf("%s/%s %s: from %v to %v the carry holds %d rows, the statement returns %d:\n  carried %v\n  fetched %v", mode, producer.Name, label, c0, c1, len(result.Carried), len(result.Fetched), headKeys(result.Carried), headKeys(result.Fetched))
				}
			}
			for n := 0; n < 40; n++ {
				i, j := rng.Intn(len(positions)+1)-1, rng.Intn(len(positions))
				if i > j {
					i, j = j, i
				}
				c0 := devhealthsource.CarryCursorForTest{}
				if i >= 0 {
					c0 = positions[i]
				}
				trial(fmt.Sprintf("random %d", n), c0, positions[j], limits[rng.Intn(len(limits))])
			}
			// Everything past the first position, from the very start: every
			// membership arm the seed holds is read and compared.
			trial("whole table", devhealthsource.CarryCursorForTest{}, positions[0], 200)
			// Each tie group: c1 is the first position of a run of positions
			// sharing one timestamp, c0 the position just before the run, so
			// the read at c0 holds rows at c1's stamp on both sides of c1's key.
			for start := 0; start < len(positions); {
				end := start + 1
				for end < len(positions) && positions[end].Since.Equal(positions[start].Since) {
					end++
				}
				if end-start >= 2 {
					c0 := devhealthsource.CarryCursorForTest{}
					if start > 0 {
						c0 = positions[start-1]
					}
					trial(fmt.Sprintf("tie group at %s", positions[start].Since.UTC().Format(time.RFC3339Nano)), c0, positions[start], 200)
				}
				start = end
			}
			var arms []string
			if producer.Name == "project_membership_presence" {
				arms = devhealthsource.MembershipArmsForTest()
			}
			t.Logf("carry-parity mode=%s table=%s carry=%v %s", mode, producer.Name, producer.Carry, cov.String())
			for _, gap := range cov.gaps(arms) {
				t.Errorf("%s/%s: %s", mode, producer.Name, gap)
			}
		}
	}
}

func headKeys(keys []string) []string {
	if len(keys) > 3 {
		return keys[:3]
	}
	return keys
}
