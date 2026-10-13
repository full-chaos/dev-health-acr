package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
)

const splitDiffOrg = "org-split-differential"

type splitDiffSeed struct {
	org    string
	t      *testing.T
	ctx    context.Context
	direct clickhousedriver.Conn
	at     time.Time
	units  []string
	rows   [][]any
}

func (s *splitDiffSeed) exec(statement string, args ...any) {
	s.t.Helper()
	if err := s.direct.Exec(s.ctx, statement, args...); err != nil {
		s.t.Fatalf("seed: %v\n%s", err, statement)
	}
}

func (s *splitDiffSeed) repo(label, name, provider string) {
	s.t.Helper()
	s.exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), s.org, name, provider, s.at)
}

func splitDiffEvidence(prs, issues []string) string {
	if prs == nil {
		prs = []string{}
	}
	if issues == nil {
		issues = []string{}
	}
	encoded, _ := json.Marshal(map[string][]string{"prs": prs, "issues": issues, "commits": {"c0ffee"}, "edges": {"e1"}})
	return string(encoded)
}

// unit writes one version of a work unit; repoLabel "" is a NULL repo_id.
func (s *splitDiffSeed) unit(id string, computedAt, from, to time.Time, repoLabel string, effort float64, themes map[string]float64, prs, issues []string) {
	s.t.Helper()
	var repo any
	if repoLabel != "" {
		repo = repoUUID(repoLabel)
	}
	s.rows = append(s.rows, []any{id, from, to, repo, effort, themes, map[string]float64{readers.BugfixSubcategoryKey: 0.25}, splitDiffEvidence(prs, issues), computedAt, s.org})
	if !slices.Contains(s.units, id) {
		s.units = append(s.units, id)
	}
}

func (s *splitDiffSeed) batch(statement string, rows [][]any) {
	s.t.Helper()
	b, err := s.direct.PrepareBatch(s.ctx, statement)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, row := range rows {
		if err := b.Append(row...); err != nil {
			s.t.Fatal(err)
		}
	}
	if err := b.Send(); err != nil {
		s.t.Fatal(err)
	}
}

func pr(label string, number int) string { return fmt.Sprintf("%s#pr%d", repoUUID(label), number) }

// The repository mix and the repository unit listing (now on the requested
// split) return the same rows as their old text on one organization that
// holds every reach rule: direct, issue-side github and gitlab, the repository fallback,
// an older version that reached a requested repository, a superseded unit, a
// unit outside the membership run, ambiguous and multi-provider names,
// unresolved and duplicate references, and high fan-out units (16 references
// over 16 repositories each). Every value is dyadic, so equal rows
// are equal bytes whatever the summation order. The organization statements
// keep their text (TestWholeOrganizationSplitTextIsUnchanged).
func TestInvestmentSplitStatementsMatchTheirOldTextOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	s := &splitDiffSeed{org: splitDiffOrg, t: t, ctx: ctx, direct: direct, at: ts(2026, 9, 2, 0, 0, 0)}
	for _, r := range [][3]string{
		{"sd-r1", "acme/r1", "github"}, {"sd-r2", "grp/r2", "gitlab"}, {"sd-r3", "acme/r3", "github"}, {"sd-r5", "acme/r5", "github"},
		{"sd-dup1", "acme/dup", "github"}, {"sd-dup2", "acme/dup", "github"},
		{"sd-mp", "acme/mp", "github"}, {"sd-mp", "acme/mp", "gitlab"},
	} {
		s.repo(r[0], r[1], r[2])
	}
	for i := 0; i < 16; i++ {
		s.repo(fmt.Sprintf("sd-b%d", i), fmt.Sprintf("acme/b%d", i), "github")
	}
	requested := []string{repoUUID("sd-r1"), repoUUID("sd-r2"), repoUUID("sd-r5"), repoUUID("sd-dup1"), repoUUID("sd-mp")}
	for i := 0; i < 4; i++ {
		requested = append(requested, repoUUID(fmt.Sprintf("sd-b%d", i)))
	}

	v1, v2 := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 1, 6, 0, 0)
	in, inTo := ts(2026, 8, 10, 0, 0, 0), ts(2026, 8, 12, 0, 0, 0)
	half := map[string]float64{"feature_delivery": 0.5, "maintenance": 0.5}
	risk := map[string]float64{"risk": 1}
	s.unit("e01-direct", v2, in, inTo, "", 2, half, []string{pr("sd-r1", 1)}, nil)
	s.unit("e02-direct-and-other", v2, in, inTo, "", 4, half, []string{pr("sd-r1", 2), pr("sd-r3", 7)}, nil)
	s.unit("e03-github-lookup", v2, in, inTo, "", 3, risk, nil, []string{"ghpr:acme/r1#9"})
	s.unit("e04-gitlab-lookup", v2, in, inTo, "", 6, half, nil, []string{"gitlab:grp/r2!4", "ghpr:acme/r3#5"})
	s.unit("e05-fallback", v2, in, inTo, "sd-r5", 5, risk, nil, nil)
	s.unit("e06-fallback-other", v2, in, inTo, "sd-r3", 7, risk, nil, nil)
	s.unit("e07-older-reached", v1, in, inTo, "", 1000, risk, []string{pr("sd-r1", 30)}, nil)
	s.unit("e07-older-reached", v2, in, inTo, "", 8, half, []string{pr("sd-r3", 31)}, nil)
	s.unit("e08-latest-reached", v1, in, inTo, "", 1000, risk, []string{pr("sd-r3", 32)}, nil)
	s.unit("e08-latest-reached", v2, in, inTo, "", 9, half, []string{pr("sd-r1", 32)}, nil)
	s.unit("e09-superseded", v2, in, inTo, "", 1000, risk, []string{pr("sd-r1", 40)}, nil)
	s.unit("e10-superseded-older-reached", v1, in, inTo, "", 1000, risk, []string{pr("sd-r1", 41)}, nil)
	s.unit("e10-superseded-older-reached", v2, in, inTo, "", 1000, risk, []string{pr("sd-r3", 41)}, nil)
	s.unit("e11-unresolved", v2, in, inTo, "", 10, half, []string{pr("sd-r1", 3)}, []string{"ghpr:acme/nowhere#1"})
	s.unit("e12-ambiguous-name", v2, in, inTo, "", 12, half, nil, []string{"ghpr:acme/dup#1"})
	s.unit("e13-multi-provider", v2, in, inTo, "", 13, risk, nil, []string{"ghpr:acme/mp#1"})
	s.unit("e14-other-only", v2, in, inTo, "", 14, risk, []string{pr("sd-r3", 8)}, nil)
	s.unit("e15-both-windows", v2, ts(2026, 7, 10, 0, 0, 0), ts(2026, 8, 5, 0, 0, 0), "", 15, half, []string{pr("sd-r1", 50)}, nil)
	s.unit("e17-older-fallback", v1, in, inTo, "sd-r5", 1000, risk, nil, nil)
	s.unit("e17-older-fallback", v2, in, inTo, "", 17, risk, nil, nil)
	s.unit("e18-uppercase-direct", v2, in, inTo, "", 18, risk, []string{strings.ToUpper(repoUUID("sd-r1")) + "#pr70"}, nil)
	s.unit("e19-prior-only", v2, ts(2026, 7, 15, 0, 0, 0), ts(2026, 7, 16, 0, 0, 0), "", 19, half, []string{pr("sd-r2", 80), pr("sd-r1", 81)}, nil)
	s.unit("e20-duplicate-key", v2, in, inTo, "", 20, risk, []string{pr("sd-r1", 90), pr("sd-r1", 90)}, []string{"ghpr:acme/r1#90"})
	s.unit("e21-linked-only", v2, in, inTo, "", 21, half, []string{pr("sd-r3", 7)}, nil)
	for i := 0; i < 1500; i++ {
		b := func(k int) string { return fmt.Sprintf("sd-b%d", (i+k)%16) }
		id := fmt.Sprintf("bulk-%04d", i)
		from := ts(2026, 8, 31, 0, 0, 0).Add(-time.Duration(i%60) * 24 * time.Hour)
		prs := []string{pr(b(0), 1000+i), pr(b(5), 1000+i)}
		issues := []string{fmt.Sprintf("ghpr:acme/b%d#%d", (i+9)%16, 5000+i), fmt.Sprintf("ghpr:acme/b%d#%d", (i+13)%16, 5000+i)}
		if i%3 == 0 {
			s.unit(id, v1, from, from, "", 1000, risk, []string{pr(b(1), 1000+i)}, nil)
		}
		s.unit(id, v2, from, from, "", float64(1+i%9), half, prs, issues)
	}
	for i := 0; i < 300; i++ {
		var prs, issues []string
		for k := 0; k < 8; k++ {
			prs = append(prs, pr(fmt.Sprintf("sd-b%d", (i+2*k)%16), 20000+i*16+k))
			issues = append(issues, fmt.Sprintf("ghpr:acme/b%d#%d", (i+2*k+1)%16, 40000+i*16+k))
		}
		from := ts(2026, 8, 31, 0, 0, 0).Add(-time.Duration(i%45) * 24 * time.Hour)
		s.unit(fmt.Sprintf("fan-%03d", i), v2, from, from, "", float64(1+i%7), half, prs, issues)
	}
	for _, id := range []string{"e09-superseded", "e10-superseded-older-reached"} {
		s.exec(`INSERT INTO work_unit_supersessions (org_id, superseded_work_unit_id, superseded_at) VALUES (?,?,?)`, splitDiffOrg, id, s.at)
	}
	s.unit("e16-outside-run", v2, in, inTo, "", 1000, risk, []string{pr("sd-r1", 60)}, nil)
	s.batch(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`, s.rows)
	s.exec(`INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, splitDiffOrg, "run-1", s.at)
	var members [][]any
	for i, id := range s.units {
		if id == "e16-outside-run" {
			continue
		}
		members = append(members, []any{splitDiffOrg, "issue", fmt.Sprintf("n-%d", i), id, "theme", "feature_delivery", s.at, "run-1"})
	}
	s.batch(`INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)`, members)
	s.exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		splitDiffOrg, "github", "team-1", repoUUID("sd-r1"), "acme/r1", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, s.at)
	s.exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-1", splitDiffOrg, "linear", nil, "proj-1", uint8(1), "active", "", s.at)
	s.exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-1", "team-1", s.at, splitDiffOrg, "linear", uint8(1))
	s.exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		splitDiffOrg, "linear", "team-1", "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, s.at)
	s.exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-2", splitDiffOrg, "linear", nil, "proj-2", uint8(1), "active", "", s.at)
	s.exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-2", "team-2", s.at, splitDiffOrg, "linear", uint8(1))
	s.exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		splitDiffOrg, "linear", "team-2", "proj-2", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, s.at)
	// Linked pull requests: direct and issue-side references, a pull request
	// in a repository the team owns (counted by the owned arm, not here), one
	// linked to both teams, and high fan-out units.
	for i, link := range []struct {
		project, repo string
		number        int
	}{
		{"proj-1", "sd-r3", 7}, {"proj-1", "sd-r1", 1}, {"proj-2", "sd-r3", 7}, {"proj-2", "sd-r1", 9},
		{"proj-2", "sd-b5", 1005}, {"proj-2", "sd-b3", 5010}, {"proj-2", "sd-b0", 20000}, {"proj-2", "sd-b1", 40000},
		{"proj-1", "sd-b3", 20017}, {"proj-1", "sd-b6", 1006}, {"proj-1", "sd-dup1", 1}, {"proj-1", "sd-r2", 4},
	} {
		item := fmt.Sprintf("i-%d", i)
		s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
			item, repoUUID(link.repo), splitDiffOrg, "title", s.at, link.project)
		s.exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoUUID(link.repo), item, uint32(link.number), float32(1), "native", "", s.at, splitDiffOrg)
		s.exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			repoUUID(link.repo), splitDiffOrg, uint32(link.number), "pr", "open", s.at, s.at, "feature", "")
	}

	rows := func(name, statement string, ids []string, bindings []readers.Binding) []string {
		t.Helper()
		return splitDiffRows(t, ctx, query, splitDiffOrg, name, statement, ids, bindings)
	}
	columnTypes := func(name, statement string, ids []string, bindings []readers.Binding) string {
		t.Helper()
		var out string
		err := readers.QueryOrgScopedNamed(ctx, query, "SplitDifferential", "SELECT toTypeName(tuple(*)) FROM (\n"+statement+"\n) LIMIT 1", splitDiffOrg, ids,
			func(row readers.RowScanner) error { return row.Scan(&out) }, bindings...)
		if err != nil {
			t.Fatalf("%s: column types: %v: %s", name, err, readFailureCause(err))
		}
		if out == "" {
			t.Fatalf("%s: column types: no row", name)
		}
		return out
	}
	idsFor := map[string][]string{
		"repository mix":               requested,
		"repository unit listing":      requested,
		"organization unit listing":    {},
		"organization mix":             {},
		"team linked pull request mix": {"team-1", "team-2"},
	}
	pairs := devhealthfacts.InvestmentSplitStatementPairs(ts(2026, 8, 1, 0, 0, 0), ts(2026, 9, 1, 0, 0, 0), 900)
	if len(pairs) != len(idsFor) {
		t.Fatalf("%d split statements, want the %d this test seeds for", len(pairs), len(idsFor))
	}
	for _, p := range pairs {
		ids, ok := idsFor[p.Name]
		if !ok {
			t.Fatalf("no ids for split statement %q", p.Name)
		}
		if p.Old == p.New {
			if p.Name != "organization mix" && p.Name != "organization unit listing" {
				t.Fatalf("%s: the old and new statements are the same text", p.Name)
			}
			continue
		}
		if oldTypes, newTypes := columnTypes(p.Name+" (old)", p.Old, ids, p.Bindings), columnTypes(p.Name+" (new)", p.New, ids, p.Bindings); withoutNullable(oldTypes) != newTypes {
			t.Errorf("%s: column types differ from the old statement: old %v, new %v", p.Name, oldTypes, newTypes)
		}
		old := rows(p.Name+" (old)", p.Old, ids, p.Bindings)
		got := rows(p.Name+" (new)", p.New, ids, p.Bindings)
		if len(old) < 2 {
			t.Errorf("%s: the old statement returned %d rows; the fixture proves nothing", p.Name, len(old))
		}
		if !slices.Equal(old, got) {
			t.Errorf("%s: rows differ from the old statement\nold (%d):\n%s\nnew (%d):\n%s", p.Name, len(old), strings.Join(old, ""), len(got), strings.Join(got, ""))
		}
		if slices.Equal(old, got) {
			t.Logf("%s: %d equal rows", p.Name, len(got))
		}
		for defect, swap := range devhealthfacts.ReachDefects(p.Name) {
			if !strings.Contains(p.New, swap[0]) {
				t.Fatalf("%s: defect %q does not apply to the statement", p.Name, defect)
			}
			broken := rows(p.Name+" ("+defect+")", strings.Replace(p.New, swap[0], swap[1], 1), ids, p.Bindings)
			if slices.Equal(old, broken) {
				t.Errorf("%s: the differential does not see the defect %q", p.Name, defect)
			}
		}
	}
}

// splitDiffRows runs statement for org and returns its rows as sorted TSV
// lines; the unit listing's arrays are sorted (the fact sorts both lists).
func splitDiffRows(t *testing.T, ctx context.Context, query readers.QueryClient, org, name, statement string, ids []string, bindings []readers.Binding) []string {
	t.Helper()
	var out []string
	columns := "*"
	if strings.Contains(statement, "scope_unattributed_rows") {
		columns = "work_unit_id, repo_uuid, share, effort_value, theme_distribution_json, from_ts, to_ts, arraySort(prs), unresolved_n, arraySort(unresolved_refs), scope_total, scope_units, scope_unattributed_total, scope_unattributed_rows"
	}
	err := readers.QueryOrgScopedNamed(ctx, query, "SplitDifferential", "SELECT formatRow('TSV', "+columns+") FROM (\n"+statement+"\n)", org, ids,
		func(row readers.RowScanner) error {
			var line string
			if err := row.Scan(&line); err != nil {
				return err
			}
			out = append(out, line)
			return nil
		}, bindings...)
	if err != nil {
		t.Fatalf("%s: %v: %s", name, err, readFailureCause(err))
	}
	slices.Sort(out)
	return out
}

// Two versions of one unit with the same computed_at, one reaching a
// requested repository (or linked pull request) and one not: on one thread,
// over two parts written in a known order with merges stopped, the old and the
// new statements pick the same version (each keeps the first version it reads
// at the top computed_at). Across threads the pick depends on the order the
// thread states merge, in each query; old and new are then NOT guaranteed to
// agree on such a tie. The tie only exists between unmerged parts: a merge
// keeps the last inserted version.
func TestInvestmentSplitStatementsPickTheSameVersionOnAComputedAtTieOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_unit_investments`); err != nil {
		t.Fatalf("stop merges: %v", err)
	}
	const org = "org-split-tie"
	s := &splitDiffSeed{org: org, t: t, ctx: ctx, direct: direct, at: ts(2026, 9, 2, 0, 0, 0)}
	s.repo("sd-r1", "acme/r1", "github")
	s.repo("sd-r3", "acme/r3", "github")
	tie, in := ts(2026, 9, 1, 0, 0, 0), ts(2026, 8, 10, 0, 0, 0)
	half := map[string]float64{"feature_delivery": 0.5, "maintenance": 0.5}
	reaching := []string{pr("sd-r1", 100), pr("sd-r3", 200)}
	other := []string{pr("sd-r3", 201)}
	// Each version is its own part, written in this order.
	for _, v := range []struct {
		id  string
		prs []string
	}{
		{"tie-reaching-first", reaching}, {"tie-reaching-first", other},
		{"tie-other-first", other}, {"tie-other-first", reaching},
	} {
		s.rows = nil
		s.unit(v.id, tie, in, in, "", 4, half, v.prs, nil)
		s.batch(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`, s.rows)
	}
	var parts uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = 'work_unit_investments' AND active`).Scan(&parts); err != nil || parts != 4 {
		t.Fatalf("want the 4 versions in 4 unmerged parts, got %d (%v)", parts, err)
	}
	s.exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		org, "github", "team-1", repoUUID("sd-r3"), "acme/r3", "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, s.at)
	s.exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-1", org, "linear", nil, "proj-1", uint8(1), "active", "", s.at)
	s.exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-1", "team-1", s.at, org, "linear", uint8(1))
	s.exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		org, "linear", "team-1", "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, s.at)
	s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		"i-1", repoUUID("sd-r1"), org, "title", s.at, "proj-1")
	s.exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID("sd-r1"), "i-1", uint32(100), float32(1), "native", "", s.at, org)
	s.exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("sd-r1"), org, uint32(100), "pr", "open", s.at, s.at, "feature", "")

	idsFor := map[string][]string{
		"repository mix": {repoUUID("sd-r1")}, "repository unit listing": {repoUUID("sd-r1")}, "team linked pull request mix": {"team-1"},
	}
	const oneThread = "\nSETTINGS max_threads = 1"
	for _, p := range devhealthfacts.InvestmentSplitStatementPairs(ts(2026, 8, 1, 0, 0, 0), ts(2026, 9, 1, 0, 0, 0), 900) {
		ids, ok := idsFor[p.Name]
		if !ok {
			continue
		}
		old := splitDiffRows(t, ctx, query, org, p.Name+" (old)", p.Old+oneThread, ids, p.Bindings)
		got := splitDiffRows(t, ctx, query, org, p.Name+" (new)", p.New+oneThread, ids, p.Bindings)
		if len(old) == 0 {
			t.Errorf("%s: the old statement returned no rows; the tie was not read", p.Name)
		}
		if !slices.Equal(old, got) {
			t.Errorf("%s: on a computed_at tie the new statement picks another version\nold:\n%s\nnew:\n%s", p.Name, strings.Join(old, ""), strings.Join(got, ""))
		}
		if slices.Equal(old, got) {
			t.Logf("%s: %d equal rows on the tie", p.Name, len(got))
		}
	}
}

// withoutNullable reads a top-level Nullable(String) column of a row type as
// String: the old statements typed repository ids Nullable(String) though
// they are never NULL, and both scan into a Go string. A Nullable nested in
// an array is kept: it does not scan into a Go []string.
func withoutNullable(rowType string) string {
	rowType = strings.ReplaceAll(rowType, ", Nullable(String)", ", String")
	return strings.Replace(rowType, "Tuple(Nullable(String)", "Tuple(String", 1)
}
