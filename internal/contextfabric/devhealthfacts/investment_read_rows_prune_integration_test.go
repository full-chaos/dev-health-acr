package devhealthfacts_test

// Leg (never merge). EXECUTED against a real ClickHouse: does a narrower request
// make a statement read fewer rows? The budget sentence tells the caller to
// "narrow the window or the subject"; that is true for a statement only if
// narrowing lowers the work the server does. For each pair (narrow, wide) of
// real provider reads, every statement the server logged is compared by its
// normalized query hash on read_rows (work, not wall time). A statement whose
// narrow read does not read fewer rows than its wide read is a statement the
// advice is false for, on that axis.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// legSpreadDays is the number of distinct end stamps of the seeded work units:
// a 7 day window selects about 2 percent of them, and the stamps do not follow
// the work_unit_id order, so the sort key cannot prune them.
const legSpreadDays = 400

// seedLeg14668Shape is seedCHAOS7257ProdShape with the work units spread over
// legSpreadDays end stamps, so a window selects a fraction of them. Synthetic.
func seedLeg14668Shape(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, units int) chaos7257Shape {
	t.Helper()
	shape := chaos7257Shape{orgID: orgID, at: ts(2026, 9, 20, 0, 0, 0)}
	at := shape.at
	exec := func(what, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	for i := 0; i < chaos7257Projects; i++ {
		project := repoUUID(fmt.Sprintf("project-%d", i))
		shape.projects = append(shape.projects, project)
		exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, orgID, "linear", nil, "Project "+project, uint8(1), "active", "", at)
		exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			shape.team(i), shape.team(i), at, orgID, "linear", uint8(1))
		exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", shape.team(i), project, nil, "native", at, nil, at)
	}
	// The last project is ALSO owned by team-0: a team owning more than one
	// project and a project owned by more than one team.
	exec("second project owner", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", shape.team(0), shape.projects[chaos7257Projects-1], nil, "native", at, nil, at)
	for j := 0; j < chaos7257Repos; j++ {
		label := fmt.Sprintf("mix-%d", j)
		shape.repos = append(shape.repos, label)
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(label), orgID, "acme/"+label, "github", at)
		exec("repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", shape.team(j%chaos7257Projects), repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
	}

	// work_unit_investments: prod row widths (structural ~670 B, subcategory
	// ~390 B, theme ~100 B), 1 in 25 units recomputed (two versions).
	wui, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
	if err != nil {
		t.Fatalf("prepare work_unit_investments: %v", err)
	}
	wita, err := direct.PrepareBatch(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at)`)
	if err != nil {
		t.Fatalf("prepare work_item_team_attributions: %v", err)
	}
	items, err := direct.PrepareBatch(ctx, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id)`)
	if err != nil {
		t.Fatalf("prepare work_items: %v", err)
	}
	members, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)`)
	if err != nil {
		t.Fatalf("prepare work_unit_membership: %v", err)
	}
	themeNames := []string{"feature_delivery", "operational", "maintenance", "quality", "risk"}
	for i := 0; i < units; i++ {
		unit := repoUUID(fmt.Sprintf("wu-%d", i))
		var repoID any
		nullRepo := i%chaos7257NullRepoMod == 3
		if !nullRepo {
			repoID = repoUUID(shape.repos[i%chaos7257Repos])
		}
		prs := make([]string, 0, 12)
		for k := 0; k < 12; k++ {
			prs = append(prs, fmt.Sprintf("%q", fmt.Sprintf("%s#pr%d", repoUUID(shape.repos[(i+k)%chaos7257Repos]), 1000+i*16+k)))
		}
		issue := fmt.Sprintf("linear:CH-%d", i)
		evidence := fmt.Sprintf(`{"issues":["ghpr:acme/%s#%d",%q,"jira:OPS-%d"],"prs":[%s]}`,
			shape.repos[i%chaos7257Repos], 5000+i, issue, i, strings.Join(prs, ","))
		to := at.Add(-time.Duration(i%legSpreadDays) * 24 * time.Hour)
		from := to.Add(-7 * 24 * time.Hour)
		versions := 1
		if i%25 == 0 {
			versions = 2
		}
		for v := 0; v < versions; v++ {
			effort := float64(1 + i%9)
			if v == 0 && versions == 2 {
				effort = 1000 // stale version: must never be counted
			}
			if err := wui.Append(unit, from, to, repoID, effort, chaos7257Themes(i), chaos7257Subcategories(i), evidence, at.Add(time.Duration(v)*time.Hour), orgID); err != nil {
				t.Fatalf("append work unit: %v", err)
			}
		}
		// Project-native path: the unit's linear item sits in one project.
		if err := items.Append("00000000-0000-0000-0000-000000000000", issue, "linear", "title", "issue", "open", "", shape.projects[i%chaos7257Projects], "", "", at, at, nil, "", "", at, orgID); err != nil {
			t.Fatalf("append work item: %v", err)
		}
		// Evidence-vote path: a unit without repo_id reaches a team through the
		// attribution of its PR-side work items (the first three refs).
		if nullRepo {
			for k := 0; k < 3; k++ {
				label := shape.repos[(i+k)%chaos7257Repos]
				if err := wita.Append(orgID, repoUUID(label), fmt.Sprintf("ghpr:acme/%s#%d", label, 1000+i*16+k),
					shape.team((i+k)%chaos7257Repos%chaos7257Projects), "team", "linked_issue", uint8(1), "high", at); err != nil {
					t.Fatalf("append attribution: %v", err)
				}
			}
		}
		// Membership: an old run listing every unit under four categories, and
		// the current run listing 9 in 10 under three (prod: 136,531 rows, 2 runs).
		for c := 0; c < 4; c++ {
			if err := members.Append(orgID, "issue", issue, unit, "theme", themeNames[c], at.Add(-48*time.Hour), "run-1"); err != nil {
				t.Fatalf("append membership: %v", err)
			}
		}
		if i%10 != 0 {
			for c := 0; c < 3; c++ {
				if err := members.Append(orgID, "issue", issue, unit, "theme", themeNames[c], at.Add(-24*time.Hour), "run-2"); err != nil {
					t.Fatalf("append membership: %v", err)
				}
			}
		}
	}
	for i, batch := range []clickhousedriver.Batch{wui, wita, items, members} {
		if err := batch.Send(); err != nil {
			t.Fatalf("send batch %d of 4 (units, attributions, work items, membership): %v", i, err)
		}
	}
	exec("run-1", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-1", at.Add(-48*time.Hour))
	exec("run-2", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-2", at.Add(-24*time.Hour))
	exec("supersession", `INSERT INTO work_unit_supersessions (org_id, superseded_work_unit_id, superseded_at) VALUES (?,?,?)`, orgID, repoUUID("wu-1"), at)
	return shape
}

// seedLeg14668Links gives every seeded issue one native link row to a pull
// request of one of the seeded repositories, so the team link candidate
// statements (team_link_candidates.go) have link rows to read.
func seedLeg14668Links(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, shape chaos7257Shape, units int) {
	t.Helper()
	batch, err := direct.PrepareBatch(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)`)
	if err != nil {
		t.Fatalf("prepare work_graph_issue_pr: %v", err)
	}
	for i := 0; i < units; i++ {
		if err := batch.Append(repoUUID(shape.repos[i%chaos7257Repos]), fmt.Sprintf("linear:CH-%d", i), uint32(1000+i), float32(1), "native", "", shape.at, orgID); err != nil {
			t.Fatalf("append work_graph_issue_pr: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send work_graph_issue_pr: %v", err)
	}
}

// legKnownStatements names the statements by a text marker, so the FAIL lines
// cite the reader. A statement not listed is reported by its first 100 chars.
var legKnownStatements = []struct{ marker, reader string }{
	{"AS chunk_bounds", "investment_project_mix_phased.go ReadProjectMixChunkBounds"},
	{"groupArrayIf(work_unit_id, in_window)", "investment_project_mix_phased.go ReadProjectMixScope"},
	{"groupArray(project_provider)", "investment_project_mix_phased.go ReadProjectMixLinks"},
	{"sum(cityHash64(", "investment_project_mix_phased.go ReadProjectMixInputs"},
	{"AS pinned_present", "investment_project_mix_phased.go ReadProjectMixPinnedVersions"},
	{"groupArray(multi_placed)", "investment_project_mix_phased.go ReadProjectNativeMix placement"},
	{"groupArray(theme_feature_delivery)", "investment_project_mix_phased.go ReadProjectThemeMixThemeValues"},
	{"count() FROM work_unit_investments WHERE org_id", "investment.go ReadInvestmentWatermark"},
	{"AS sized_unit_hashes", "investment_repo_mix.go ReadRepositoryThemeMix"},
	{"cityHash64(cp.work_item_id) IN", "team_link_candidates.go ReadTeamLinkCandidates"},
	{"SELECT groupArray(toString(h))", "team_link_candidates.go ReadTeamAmbiguousLinkHashes"},
	{"FROM work_graph_issue_pr", "link rows over work_graph_issue_pr (team_rollup.go / issue_pull_request_link_rows.go)"},
	{"FROM work_item_dependencies", "work_item_dependencies statement"},
	{"FROM git_pull_requests", "git_pull_requests statement"},
}

func legReaderOf(query string) string {
	for _, known := range legKnownStatements {
		if strings.Contains(query, known.marker) {
			return known.reader
		}
	}
	text := strings.Join(strings.Fields(query), " ")
	if len(text) > 100 {
		text = text[:100]
	}
	return "unknown: " + text
}

type legStatement struct {
	rows, bytes uint64
	runs        int
	failed      bool
	query       string
}

// legStatementsSince returns every SELECT the server logged after the mark for
// this database, summed per normalized query hash (a statement that runs once
// per chunk adds its chunks up: the work of the read).
func legStatementsSince(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, mark int64) map[string]*legStatement {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, the measurement did not happen): %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT toString(normalized_query_hash), type, read_rows, read_bytes, substring(query, 1, 3000) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND current_database = currentDatabase() AND position(query, 'system.') = 0
ORDER BY event_time_microseconds`, mark)
	if err != nil {
		t.Fatalf("read query_log (the measurement did not happen): %v", err)
	}
	defer rows.Close()
	out := map[string]*legStatement{}
	for rows.Next() {
		var hash, kind, query string
		var readRows, readBytes uint64
		if err := rows.Scan(&hash, &kind, &readRows, &readBytes, &query); err != nil {
			t.Fatalf("scan query_log: %v", err)
		}
		s := out[hash]
		if s == nil {
			s = &legStatement{query: query}
			out[hash] = s
		}
		s.rows += readRows
		s.bytes += readBytes
		s.runs++
		s.failed = s.failed || kind != "QueryFinish"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query_log rows (the measurement did not happen): %v", err)
	}
	return out
}

type legPair struct {
	axis, name   string
	kind         contextfabric.FactKind
	narrow, wide contextfabric.FactQuery
}

func legRead(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, provider contextfabric.FactProvider, orgID string, query contextfabric.FactQuery) map[string]*legStatement {
	t.Helper()
	mark := serverNowMicros(t, ctx, direct)
	if _, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, query); err != nil {
		t.Logf("the read of %s ended with an error (its statements up to the error are still measured): %v", query.Kind, err)
	}
	return legStatementsSince(t, ctx, direct, mark)
}

// controlRows runs one statement on the native connection and returns the
// read_rows the server logged for it (found by a unique tag in the text).
func controlRows(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, tag, statement string, args ...any) uint64 {
	t.Helper()
	mark := serverNowMicros(t, ctx, direct)
	rows, err := direct.Query(ctx, "/* "+tag+" */ "+statement, args...)
	if err != nil {
		t.Fatalf("control %s: %v", tag, err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("control %s rows: %v", tag, err)
	}
	_ = rows.Close()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	var readRows uint64
	if err := direct.QueryRow(ctx, `SELECT read_rows FROM system.query_log WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND position(query, ?) > 0 AND position(query, 'system.query_log') = 0 ORDER BY event_time_microseconds DESC LIMIT 1`, mark, tag).Scan(&readRows); err != nil {
		t.Fatalf("control %s: no logged statement (the measurement did not happen): %v", tag, err)
	}
	return readRows
}

func TestNarrowerRequestsReadFewerRowsPerStatementAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	// Every other declared table too, so the statements of the pull request and
	// blocker reads run (empty tables read 0 rows and are reported NOT MEASURED).
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Logf("create table skipped: %v", err)
		}
	}
	const orgID = "org-leg-1"
	shape := seedLeg14668Shape(t, ctx, direct, orgID, 2*chaos7257Units)
	seedLeg14668Links(t, ctx, direct, orgID, shape, 2*chaos7257Units)
	var tableRows uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM work_unit_investments WHERE org_id = ?`, orgID).Scan(&tableRows); err != nil {
		t.Fatalf("count work_unit_investments: %v", err)
	}
	t.Logf("work_unit_investments rows: %d", tableRows)

	// Controls: the method must show both outcomes. A sort key prefix prunes
	// granules; a predicate on a non-key column does not.
	var boundary string
	if err := direct.QueryRow(ctx, `SELECT work_unit_id FROM work_unit_investments WHERE org_id = ? ORDER BY work_unit_id LIMIT 1 OFFSET 2000`, orgID).Scan(&boundary); err != nil {
		t.Fatalf("read the key boundary: %v", err)
	}
	keyWide := controlRows(t, ctx, direct, "leg_ctl_key_wide", `SELECT count() FROM work_unit_investments WHERE org_id = ?`, orgID)
	keyNarrow := controlRows(t, ctx, direct, "leg_ctl_key_narrow", `SELECT count() FROM work_unit_investments WHERE org_id = ? AND work_unit_id < ?`, orgID, boundary)
	t.Logf("control key prefix: wide read_rows=%d narrow read_rows=%d", keyWide, keyNarrow)
	if keyNarrow*2 >= keyWide {
		t.Errorf("CONTROL FAILED: a sort key prefix predicate read %d rows against %d: the method cannot show pruning at this table size", keyNarrow, keyWide)
	}
	nonKeyWide := controlRows(t, ctx, direct, "leg_ctl_nonkey_wide", `SELECT count() FROM work_unit_investments WHERE org_id = ? AND to_ts >= ?`, orgID, shape.at.Add(-2*legSpreadDays*24*time.Hour))
	nonKeyNarrow := controlRows(t, ctx, direct, "leg_ctl_nonkey_narrow", `SELECT count() FROM work_unit_investments WHERE org_id = ? AND to_ts >= ?`, orgID, shape.at.Add(-7*24*time.Hour))
	t.Logf("control non-key column: wide read_rows=%d narrow read_rows=%d", nonKeyWide, nonKeyNarrow)
	if nonKeyNarrow < nonKeyWide {
		t.Logf("NOTE: a predicate on the non-key column to_ts read fewer rows (%d < %d): something prunes it", nonKeyNarrow, nonKeyWide)
	}

	providers := devhealthfacts.NewProviders(query)
	investment := findProvider(t, providers, contextfabric.FactInvestment)
	pullRequests := findProvider(t, providers, contextfabric.FactPullRequests)
	blockers := findProvider(t, providers, contextfabric.FactBlockers)
	provider := map[contextfabric.FactKind]contextfabric.FactProvider{
		contextfabric.FactInvestment: investment, contextfabric.FactPullRequests: pullRequests, contextfabric.FactBlockers: blockers,
	}

	teams := func(n int) []contextfabric.SubjectRef {
		var out []contextfabric.SubjectRef
		for i := 0; i < n; i++ {
			out = append(out, teamSubject(shape.team(i)))
		}
		return out
	}
	projects := func(n int) []contextfabric.SubjectRef {
		var out []contextfabric.SubjectRef
		for i := 0; i < n; i++ {
			out = append(out, projectSubject("linear", shape.projects[i]))
		}
		return out
	}
	repositories := func(n int) []contextfabric.SubjectRef {
		var out []contextfabric.SubjectRef
		for j := 0; j < n; j++ {
			out = append(out, contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(shape.repos[j]), Label: "acme/" + shape.repos[j]})
		}
		return out
	}
	organization := []contextfabric.SubjectRef{{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:" + orgID, Label: orgID}}

	allHistory := investmentAllHistory()
	rangeOf := func(days int) contextfabric.TimeContext {
		end := shape.at.Add(24 * time.Hour)
		start := end.Add(-time.Duration(days) * 24 * time.Hour)
		return contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
	}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	q := func(kind contextfabric.FactKind, window contextfabric.TimeContext, subjects []contextfabric.SubjectRef, exclusive bool) contextfabric.FactQuery {
		return contextfabric.FactQuery{Time: window, Kind: kind, Subjects: subjects, TimeEndExclusive: exclusive}
	}
	var pairs []legPair
	for _, set := range []struct {
		name         string
		narrow, wide []contextfabric.SubjectRef
	}{
		{"team", teams(1), teams(chaos7257Projects)},
		{"project", projects(1), projects(chaos7257Projects)},
		{"repository", repositories(1), repositories(chaos7257Repos)},
	} {
		pairs = append(pairs, legPair{"subject", "investment/" + set.name, contextfabric.FactInvestment,
			q(contextfabric.FactInvestment, allHistory, set.narrow, false), q(contextfabric.FactInvestment, allHistory, set.wide, false)})
	}
	pairs = append(pairs,
		legPair{"subject", "pull_requests/team", contextfabric.FactPullRequests, q(contextfabric.FactPullRequests, current, teams(1), false), q(contextfabric.FactPullRequests, current, teams(chaos7257Projects), false)},
		legPair{"subject", "blockers/team", contextfabric.FactBlockers, q(contextfabric.FactBlockers, current, teams(1), false), q(contextfabric.FactBlockers, current, teams(chaos7257Projects), false)},
	)
	for _, set := range []struct {
		name     string
		subjects []contextfabric.SubjectRef
	}{
		{"team", teams(1)}, {"project", projects(1)}, {"repository", repositories(1)}, {"organization", organization},
	} {
		pairs = append(pairs, legPair{"window", "investment/" + set.name, contextfabric.FactInvestment,
			q(contextfabric.FactInvestment, rangeOf(7), set.subjects, true), q(contextfabric.FactInvestment, rangeOf(legSpreadDays+1), set.subjects, true)})
	}

	for _, pair := range pairs {
		wide := legRead(t, ctx, direct, provider[pair.kind], orgID, pair.wide)
		narrow := legRead(t, ctx, direct, provider[pair.kind], orgID, pair.narrow)
		hashes := make([]string, 0, len(wide)+len(narrow))
		seen := map[string]bool{}
		for hash := range wide {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
		for hash := range narrow {
			if !seen[hash] {
				hashes = append(hashes, hash)
			}
		}
		sort.Strings(hashes)
		t.Logf("== %s axis, %s: %d statements in the wide read, %d in the narrow read", pair.axis, pair.name, len(wide), len(narrow))
		for _, hash := range hashes {
			w, n := wide[hash], narrow[hash]
			switch {
			case w == nil || n == nil:
				only := w
				side := "wide only"
				if w == nil {
					only, side = n, "narrow only"
				}
				t.Logf("UNMATCHED (%s) %s | %s | rows=%d", side, pair.axis+"/"+pair.name, legReaderOf(only.query), only.rows)
			case w.rows == 0:
				t.Logf("NOT MEASURED (the wide read read 0 rows: empty table) %s | %s", pair.axis+"/"+pair.name, legReaderOf(w.query))
			case n.rows >= w.rows:
				t.Errorf("NO PRUNE %s axis %s: narrow read_rows=%d >= wide read_rows=%d (bytes %d vs %d, runs %d vs %d) | %s", pair.axis, pair.name, n.rows, w.rows, n.bytes, w.bytes, n.runs, w.runs, legReaderOf(w.query))
			default:
				t.Logf("prunes %s axis %s: narrow read_rows=%d < wide read_rows=%d | %s", pair.axis, pair.name, n.rows, w.rows, legReaderOf(w.query))
			}
		}
	}
}
