package devhealthfacts_test

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Two sizes of one work unit that share a computed_at millisecond: the one
// inserted later is the unit's size (the row a merge keeps), whichever size
// it is, before and after a merge. A later computed_at wins over a later
// insert. The read is the served ISSUE_SIZE mix, so the pick is the one
// readers.IssueSizedSource puts in every sized statement.
func TestIssueSizeSameMillisecondVersionsPickTheLaterInsertAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newDedicatedClickHouseIntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-issue-size-tie"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-a"), orgID, "acme/repo-a", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	units := []struct {
		id, theme string
	}{{"u-up", "feature_delivery"}, {"u-down", "risk"}, {"u-later-ms", "maintenance"}, {"s-up", "quality"}, {"s-down", "operational"}}
	for i, u := range units {
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			u.id, at, at, 1.0, map[string]float64{u.theme: 1}, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID("repo-a"), i+1), at, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", u.id, err)
		}
	}
	if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_unit_issue_size`); err != nil {
		t.Fatalf("stop merges: %v", err)
	}
	size := func(id string, value float64, computedAt int) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_unit_issue_size (org_id, work_unit_id, issue_size, computed_at) VALUES (?,?,?,?)`,
			orgID, id, value, ts(2026, 9, computedAt, 0, 0, 0)); err != nil {
			t.Fatalf("seed size %s: %v", id, err)
		}
	}
	// One insert per row, so each version is its own part. u-up: the later
	// insert holds the larger size; u-down: the later insert holds the smaller
	// one, so a pick by size or by first row fails one of the two. u-later-ms:
	// the later insert has the EARLIER computed_at, which loses.
	size("u-up", 5, 17)
	size("u-up", 9, 17)
	size("u-down", 9, 17)
	size("u-down", 5, 17)
	size("u-later-ms", 3, 17)
	size("u-later-ms", 8, 10)
	// Both versions of a unit in ONE part, the earlier row first: only
	// _part_offset tells them apart, and a pick that keeps the first row of a
	// tie reads the earlier one.
	for id, pair := range map[string][2]float64{"s-up": {5, 9}, "s-down": {9, 5}} {
		if err := direct.Exec(ctx, `INSERT INTO work_unit_issue_size (org_id, work_unit_id, issue_size, computed_at)
SETTINGS optimize_on_insert = 0
SELECT ?, ?, issue_size, toDateTime64('2026-09-17 00:00:00.000', 3, 'UTC')
FROM (SELECT arrayJoin([?, ?]) AS issue_size)`, orgID, id, pair[0], pair[1]); err != nil {
			t.Fatalf("insert the same-part pair of %s: %v", id, err)
		}
		var parts, rows uint64
		var first, second float64
		if err := direct.QueryRow(ctx, `SELECT uniqExact(_part), count(), argMin(issue_size, _part_offset), argMax(issue_size, _part_offset)
FROM work_unit_issue_size WHERE org_id = ? AND work_unit_id = ?`, orgID, id).Scan(&parts, &rows, &first, &second); err != nil ||
			parts != 1 || rows != 2 || first != pair[0] || second != pair[1] {
			t.Fatalf("the same-part pair of %s: %d parts, %d rows, offsets %v then %v (%v); want 1 part, 2 rows, %v then %v", id, parts, rows, first, second, err, pair[0], pair[1])
		}
	}

	read := func(phase string) {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: investmentAllHistory(),
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{repositorySubject("repo-a")},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("%s: err=%v facts=%d reason=%q", phase, err, len(result.Facts), result.Reason)
		}
		fact := result.Facts[0]
		got := map[string]float64{}
		for _, row := range fact.Fields["theme_breakdown"].Table.Rows {
			got[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
		}
		for theme, want := range map[string]float64{"feature_delivery": 9, "risk": 5, "maintenance": 3, "quality": 9, "operational": 5} {
			if math.Abs(got[theme]-want) > 1e-9 {
				t.Errorf("%s: %s weighted = %v, want %v", phase, theme, got[theme], want)
			}
		}
		if d := *fact.Fields["denominator"].Number; math.Abs(d-31) > 1e-9 {
			t.Errorf("%s: denominator = %v, want 31", phase, d)
		}
	}
	read("before merge")
	for _, statement := range []string{`SYSTEM START MERGES work_unit_issue_size`, `OPTIMIZE TABLE work_unit_issue_size FINAL`} {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	read("after merge")
}
