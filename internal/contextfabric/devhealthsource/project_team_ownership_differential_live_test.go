package devhealthsource_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func projectTeamsComparable(ingest bool, statement string) string {
	columns := "project_id, team_id, source_name, provider, toString(first_valid_from), latest_is_open, toString(latest_valid_to), toString(observed_at), edge_suppressed, arraySort(conflict_identities), conflicting_identity_present"
	if ingest {
		columns += ", toString(ingest_at)"
	}
	return "SELECT toString(tuple(" + columns + ")) AS r FROM (" + statement + ") AS g ORDER BY r"
}

func readProjectTeamsRows(t *testing.T, f *ingestColumnsFixture, query contextpacket.ClickHouseQueryClient, ingest bool, statement string) []string {
	t.Helper()
	rows, err := query.Query(f.ctx, projectTeamsComparable(ingest, statement), []contextpacket.ClickHouseBinding{{Name: "org_id", Value: f.h.orgID}})
	if err != nil {
		t.Fatalf("read project-team rows: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedOwnershipEdgeCases seeds ownership history of every kind the statement
// resolves: versions that close and reopen, a re-asserted version, a key-only
// row, a row with no key, a conflicting identity (ref and key resolve to two
// projects) and, when ambiguous is set, two projects sharing one key with
// ownership rows that name it (the retraction case).
func seedOwnershipEdgeCases(t *testing.T, f *ingestColumnsFixture, ambiguous bool) {
	t.Helper()
	synced := f.old
	for _, id := range []string{"T1", "T2"} {
		f.team(id, f.old, synced)
	}
	secondKey := "U3"
	if ambiguous {
		secondKey = "DUP"
	}
	for _, p := range [][2]any{{"P1", "K1"}, {"P2", "DUP"}, {"P3", secondKey}, {"P4", "K4"}} {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', ?, ?, 1, 'started', '', ?, ?)`,
			p[0], f.h.orgID, p[1], "project "+p[0].(string), f.old, synced)
	}
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES ('P5', ?, 'linear', NULL, 'project P5', 1, 'started', '', ?, ?)`, f.h.orgID, f.old, synced)
	day := func(n int) time.Time { return f.old.Add(time.Duration(n) * 24 * time.Hour) }
	type version struct {
		ref, key, team, source string
		from, to               int
		toOpen                 bool
		updated                int
	}
	versions := []version{
		{"P1", "K1", "T1", "native", 0, 1, false, 1},
		{"P1", "K1", "T1", "native", 1, 2, false, 2},
		{"P1", "K1", "T1", "native", 2, 0, true, 3},
		{"P1", "K1", "T1", "native", 2, 0, true, 5},
		{"", "K1", "T2", "native", 0, 4, false, 1},
		{"", "K1", "T2", "native", 4, 0, true, 2},
		{"P4", "", "T1", "manual", 0, 0, true, 1},
		{"P2", "DUP", "T1", "native", 0, 0, true, 1},
		{"", "DUP", "T2", "native", 0, 0, true, 1},
		{"P4", "K1", "T2", "native", 0, 0, true, 1},
		{"P5", "", "T2", "native", 1, 3, false, 1},
		{"P5", "", "T2", "native", 3, 0, true, 2},
		{"P3", "", "T1", "native", 0, 0, true, 1},
	}
	for _, v := range versions {
		var key any = v.key
		if v.key == "" {
			key = nil
		}
		var to any = day(v.to)
		if v.toOpen {
			to = nil
		}
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced) VALUES (?, 'linear', ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.h.orgID, v.team, v.ref, key, v.source, day(v.from), to, day(v.updated), day(v.updated))
	}
}

func requireSameProjectTeamsRows(t *testing.T, f *ingestColumnsFixture, query contextpacket.ClickHouseQueryClient, label string, wantSuppressed bool) {
	t.Helper()
	for _, ingest := range []bool{false, true} {
		old := readProjectTeamsRows(t, f, query, ingest, devhealthsource.LegacyProjectTeamsGroupSQLForTest(ingest))
		now := readProjectTeamsRows(t, f, query, ingest, devhealthsource.ProjectTeamsGroupSQLForTest(ingest))
		if len(old) == 0 {
			t.Fatalf("%s ingest=%v: the pre-collapse statement returned no rows, so this comparison proves nothing", label, ingest)
		}
		if strings.Join(old, "\n") != strings.Join(now, "\n") {
			t.Errorf("%s ingest=%v: the statement changed what it returns\nbefore:\n%s\nafter:\n%s", label, ingest, strings.Join(old, "\n"), strings.Join(now, "\n"))
		}
		suppressed := 0
		for _, r := range old {
			if strings.Contains(r, ", 1, [") || strings.Contains(r, ",1,[") {
				suppressed++
			}
		}
		if wantSuppressed && suppressed == 0 {
			t.Errorf("%s ingest=%v: no suppressed or conflicting group among the rows, so the retraction and conflict cases are not exercised:\n%s", label, ingest, strings.Join(old, "\n"))
		}
		t.Logf("%s ingest=%v: %d rows compared", label, ingest, len(old))
	}
}

// Old and new statements return the same rows, column for column.
func TestProjectTeamOwnershipStatementReturnsTheRowsItReturnedBeforeTheCollapse(t *testing.T) {
	query, _ := orgIsolationClickHouseFixture(t)
	for i, ambiguous := range []bool{true, false} {
		f := newIngestColumnsFixture(t, fmt.Sprintf("95460000-0000-4000-8000-00000000020%d", i), nil, nil)
		seedOwnershipEdgeCases(t, f, ambiguous)
		requireSameProjectTeamsRows(t, f, query, fmt.Sprintf("edge cases ambiguous=%v", ambiguous), ambiguous)
	}
	f := newIngestColumnsFixture(t, "95460000-0000-4000-8000-000000000210", nil, nil)
	seedProjectTeamOwnershipShape(t, f, memoryShapeRows, memoryShapeUnmerged)
	requireSameProjectTeamsRows(t, f, query, "prod shape", false)
}
