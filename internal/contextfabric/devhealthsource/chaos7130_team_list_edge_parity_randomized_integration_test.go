package devhealthsource_test

// CHAOS-7130: deterministic randomized parity between the team authorization
// list, the repository->team open-edge set and a small Go reference oracle of
// "latest effective assertion per (provider, repo, team, source), listed when
// any stream's latest is open". Three review rounds each found one more
// disagreement between two hand-written derivations; this exercises the whole
// input space (future-dated, closed, reopened, multi-source, multi-provider,
// unresolved, orphan repo_id, missing team row) against BOTH sides.

import (
	"context"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

type parityRepo struct{ id, slug, provider string }

type parityRow struct {
	team, provider, name, source string
	repoID                       any // nil or a UUID string
	validFrom                    time.Time
	validTo                      any // nil or time.Time
}

func subCHAOS7130RandomizedListEdgeParity(t *testing.T, ctx context.Context, f *ownershipFixture) {
	const seed = 20260930
	rng := rand.New(rand.NewSource(seed))
	now := time.Now().UTC().Truncate(time.Second)
	repos := []parityRepo{
		{"71300000-0000-4000-8000-000000000001", "acme/r0", "github"},
		{"71300000-0000-4000-8000-000000000002", "acme/r1", "github"},
		{"71300000-0000-4000-8000-000000000003", "acme/r2", "github"},
		{"71300000-0000-4000-8000-000000000004", "acme/r0", "gitlab"},
		{"71300000-0000-4000-8000-000000000005", "acme/r3", "gitlab"},
	}
	for _, r := range repos {
		chaos7130Repo(t, ctx, f, r.id, r.slug, r.provider, now.Add(-24*time.Hour))
	}
	const orphanID = "71300000-0000-4000-8000-0000000000ff"
	teams := []string{"pt-a", "pt-b", "pt-c", "pt-d"}
	for _, team := range teams {
		chaos7119Team(t, ctx, f, team, now.Add(-24*time.Hour))
	}
	const missingTeam = "pt-missing" // ownership rows with NO teams row
	allTeams := append(append([]string(nil), teams...), missingTeam)
	names := []string{"acme/r0", "ACME/R1", "acme/r2", "Acme/R3", "acme/ghost", "acme/*"}
	providers := []string{"github", "gitlab"}
	sources := []string{"native", "manual"}
	minute := 0
	var rows []parityRow
	for i := 0; i < 90; i++ {
		row := parityRow{
			team: allTeams[rng.Intn(len(allTeams))], provider: providers[rng.Intn(2)],
			name: names[rng.Intn(len(names))], source: sources[rng.Intn(2)],
		}
		switch rng.Intn(4) {
		case 0:
			row.repoID = repos[rng.Intn(len(repos))].id
		case 1:
			if rng.Intn(3) == 0 {
				row.repoID = orphanID
			}
		}
		// A unique valid_from per row: no latest-assertion ties.
		minute++
		offset := time.Duration(minute) * time.Minute
		switch rng.Intn(3) {
		case 0: // future-dated
			row.validFrom = now.Add(time.Hour + offset)
		default: // in the past
			row.validFrom = now.Add(-200*time.Hour + offset)
		}
		switch rng.Intn(3) {
		case 0:
			row.validTo = nil
		case 1:
			row.validTo = row.validFrom.Add(30 * time.Minute) // closed (may lie in the future when validFrom is)
		default:
			row.validTo = nil
		}
		rows = append(rows, row)
	}
	// The ReplacingMergeTree key includes valid_from, so identical keys are
	// impossible here (unique minutes). Insert.
	for _, r := range rows {
		mustExec(t, ctx, f.direct,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.orgID, r.provider, r.team, r.repoID, r.name, "exact", r.source, uint8(1), uint16(100), int32(0), r.validFrom, r.validTo, now.Add(-time.Hour))
	}

	// Reference oracle.
	byNameProvider := map[string]parityRepo{}
	byID := map[string]parityRepo{}
	for _, r := range repos {
		byNameProvider[r.provider+"|"+strings.ToLower(r.slug)] = r
		byID[r.id] = r
	}
	type streamKey struct{ provider, repoID, team, source string }
	type latest struct {
		from time.Time
		open bool
		slug string
	}
	streams := map[streamKey]latest{}
	for _, r := range rows {
		if r.validFrom.After(now) {
			continue // not currently owned: ignored by BOTH sides
		}
		var repo parityRepo
		var ok bool
		if id, isID := r.repoID.(string); isID {
			repo, ok = byID[id]
		} else {
			repo, ok = byNameProvider[r.provider+"|"+strings.ToLower(r.name)]
		}
		if !ok {
			continue // ghost, glob or orphan id: unresolved
		}
		key := streamKey{r.provider, repo.id, r.team, r.source}
		if cur, seen := streams[key]; !seen || r.validFrom.After(cur.from) {
			streams[key] = latest{from: r.validFrom, open: r.validTo == nil, slug: repo.slug}
		}
	}
	want := map[string][]string{}
	for key, l := range streams {
		if l.open {
			want[key.team] = append(want[key.team], l.slug)
		}
	}
	for team := range want {
		sort.Strings(want[team])
		dedup := want[team][:0]
		for i, s := range want[team] {
			if i == 0 || s != want[team][i-1] {
				dedup = append(dedup, s)
			}
		}
		want[team] = dedup
	}

	nonEmpty := 0
	for _, team := range teams {
		if len(want[team]) > 0 {
			nonEmpty++
		}
	}
	t.Logf("seed %d: %d rows, %d streams, oracle lists per team %v", seed, len(rows), len(streams), want)
	if nonEmpty < 2 {
		t.Fatalf("degenerate fixture: only %d teams own anything -- the parity comparison would be vacuous", nonEmpty)
	}
	res := chaos7139Run(t, ctx, f)
	sentinelOnly := func(got []string) []string {
		if len(got) == 1 && got[0] == devhealthsource.NoTeamOwnershipSentinelForTest() {
			return nil
		}
		return got
	}
	for _, team := range teams {
		listGot := sentinelOnly(chaos7130List(res, team))
		edgeGot := append([]string(nil), res.openEdgeSlugs[team]...)
		sort.Strings(edgeGot)
		dedup := edgeGot[:0]
		for i, s := range edgeGot {
			if i == 0 || s != edgeGot[i-1] {
				dedup = append(dedup, s)
			}
		}
		expected := want[team]
		if len(expected) == 0 {
			expected = nil
		}
		if len(listGot) == 0 {
			listGot = nil
		}
		if len(dedup) == 0 {
			dedup = nil
		}
		if !reflect.DeepEqual(listGot, expected) {
			t.Errorf("seed %d %s: team list %v != oracle %v", seed, team, listGot, expected)
		}
		if !reflect.DeepEqual(dedup, expected) {
			t.Errorf("seed %d %s: open-edge set %v != oracle %v", seed, team, dedup, expected)
		}
	}
	if _, ok := res.entities[missingTeam]; ok {
		t.Errorf("a team with no teams row must not be projected")
	}
	if len(res.openEdgeSlugs[missingTeam]) != 0 {
		t.Errorf("edges emitted for a team with no teams row: %v", res.openEdgeSlugs[missingTeam])
	}
}
