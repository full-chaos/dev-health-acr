package devhealthfacts_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// The project-linked investment statement scans the organization's work units
// once, like the owned repository mix it sits beside, so its peak memory is
// held under the work-item reader's per-statement cap at two organization
// sizes and the served mix does not move with unrelated units.
func TestTeamLinkedInvestmentStatementStaysUnderTheMemoryCeilingOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClient(t, ctx)
	query := statementthreads.API(rawQuery)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-linked-investment-memory"
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	for _, label := range []string{"repo-r", "repo-filler"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", at)
	}
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-1", orgID, "linear", nil, "proj-1", uint8(1), "active", "", at)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-1", "team-1", at, orgID, "linear", uint8(1))
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-1", "proj-1", nil, "native", ts(2026, 1, 1, 0, 0, 0), nil, at)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'open', '', ?, '', 'linear', ?, NULL)`,
		"i-1", repoUUID("repo-filler"), orgID, "title", at, "proj-1")
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID("repo-r"), "i-1", uint32(2), float32(1), "native", "", at, orgID)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
		repoUUID("repo-r"), orgID, uint32(2), "pr", "open", at, at, "feature", "")
	exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		"wu-linked", at, at, 10.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr2"]}`, repoUUID("repo-r")), at, orgID)
	addFiller := func(prefix string, units int) {
		t.Helper()
		const instant = "toDateTime64('2026-09-18 00:00:00', 6, 'UTC')"
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)
SELECT concat('` + prefix + `', toString(number)), ` + instant + `, ` + instant + `, 1.0, map('risk', 1.0), map(), concat('{"issues":[],"prs":["` + repoUUID("repo-filler") + `#pr', toString(number), '"]}'), ` + instant + `, '` + orgID + `' FROM numbers(` + strconv.Itoa(units) + `)`)
	}

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	read := func() statementMemory {
		t.Helper()
		mark := serverNowMicros(t, ctx, direct)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment,
			Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
		}
		fact := factFor(t, result, teamSubject("team-1"))
		wantInt(t, fact.Fields, "project_reached_repository_count", 1)
		for _, row := range fact.Fields["theme_breakdown"].Table.Rows {
			if *row.Fields["theme"].String == "feature_delivery" && *row.Fields["weighted_effort"].Number != 1 {
				t.Fatalf("feature_delivery effort = %v, want the linked unit's weight of 1 whatever else the organization holds", *row.Fields["weighted_effort"].Number)
			}
			if *row.Fields["theme"].String == "risk" && *row.Fields["weighted_effort"].Number != 0 {
				t.Fatalf("risk effort = %v: an unrelated unit reached the team", *row.Fields["weighted_effort"].Number)
			}
		}
		return statementMemorySince(t, ctx, direct, statementFilter{Table: "work_unit_investments", QueryContains: "uniqExact(m.repo_uuid)"}, mark)
	}
	read()
	addFiller("a-", 2000)
	small := read()
	addFiller("b-", 20000)
	large := read()
	if len(small) == 0 || len(large) == 0 {
		t.Fatalf("measurement did not happen: %d and %d linked investment statements logged", len(small), len(large))
	}
	for _, peaks := range []statementMemory{small, large} {
		for hash, peak := range peaks {
			if peak > statementMemoryCeiling {
				t.Errorf("statement %d peaked at %d bytes, over the %d ceiling", hash, peak, statementMemoryCeiling)
			}
		}
	}
}
