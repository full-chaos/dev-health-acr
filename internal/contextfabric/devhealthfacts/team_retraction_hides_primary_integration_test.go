package devhealthfacts_test

// A team-id carry writes a RETRACTION row over the retired bare team id: same
// key (org_id, repo_id, work_item_id, team_id=<bare id>, source), is_primary=0,
// a newer computed_at. The project theme-mix wita CTE picks, per work item,
// the rows at max(computed_at); without an active-team scope the newer
// retraction row hides the live primary row of the active keyed team, and the
// evidence-attributed repo_id-null work unit silently drops out of
// work_units_without_repo_link. This pins that the retired (is_active = 0) bare
// team id never wins the max(computed_at) pick.

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

func TestProjectMixRetractionOverRetiredBareTeamIDDoesNotHideActiveKeyedPrimaryAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	t1 := ts(2026, 9, 18, 0, 0, 0)
	t2 := ts(2026, 9, 18, 6, 0, 0) // strictly newer than t1

	const (
		bareID  = "platform"
		keyedID = "team:linear:platform"
	)

	seedTeamRow := func(orgID, id string, active uint8) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			id, "Platform", t1, orgID, "linear", active); err != nil {
			t.Fatalf("seed team %s: %v", id, err)
		}
	}
	seedScenario := func(orgID, projectID, repoLabel string, withRetraction bool) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			projectID, orgID, "linear", nil, "Project "+projectID, uint8(1), "active", "", t1); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		seedTeamRow(orgID, bareID, 0)
		seedTeamRow(orgID, keyedID, 1)
		if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", keyedID, projectID, nil, "native", t1, nil, t1); err != nil {
			t.Fatalf("seed team_project_ownership: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(repoLabel), orgID, "acme/"+repoLabel, "github", t1); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", keyedID, repoUUID(repoLabel), "acme/"+repoLabel, "exact", "native", uint8(1), uint16(100), int32(0), t1, nil, t1); err != nil {
			t.Fatalf("seed team_repo_ownership: %v", err)
		}
		// Counted: repo-linked work unit, so the project serves a roll-up.
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			"wu-counted", t1, t1, repoUUID(repoLabel), 10.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, "{}", t1, orgID); err != nil {
			t.Fatalf("seed counted unit: %v", err)
		}
		// Excluded-by-repo but evidence-attributed: reachable only through
		// the wita vote for the keyed team.
		evidence := fmt.Sprintf(`{"issues":[],"prs":["%s#pr7"]}`, repoUUID(repoLabel))
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			"wu-evidence", t1, t1, 20.0, map[string]float64{"operational": 1.0}, map[string]float64{}, evidence, t1, orgID); err != nil {
			t.Fatalf("seed evidence unit: %v", err)
		}
		workItemID := fmt.Sprintf("ghpr:acme/%s#7", repoLabel)
		insertAttr := func(teamID string, primary uint8, name string, computedAt interface{}) {
			t.Helper()
			if err := direct.Exec(ctx,
				`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				orgID, repoUUID(repoLabel), workItemID, teamID, name, "linked_issue", primary, "high", computedAt); err != nil {
				t.Fatalf("seed attribution team=%s: %v", teamID, err)
			}
		}
		insertAttr(keyedID, 1, "Platform", t1)
		if withRetraction {
			insertAttr(bareID, 0, "Platform", t2)
		}
	}

	seedScenario("org-retraction-control", "proj-ctl", "repo-ctl", false)
	seedScenario("org-retraction", "proj-ret", "repo-ret", true)

	read := func(orgID, projectID string) (excluded, counted int64, featureShare float64) {
		t.Helper()
		fact := readInvestmentFact(t, providers, orgID, projectSubject("linear", projectID))
		if fact == nil {
			t.Fatalf("%s: facts = none, want a served roll-up", projectID)
		}
		return factInt(t, *fact, "work_units_without_repo_link"), factInt(t, *fact, "work_unit_count"), factNumber(t, *fact, "theme_feature_delivery")
	}

	ctlExcluded, ctlCounted, ctlShare := read("org-retraction-control", "proj-ctl")
	// Expected from the seeded rows: one repo-linked unit (counted), one
	// repo_id-null unit whose PR evidence votes for the keyed team that owns
	// the project (excluded), only the counted unit's feature_delivery effort
	// backs the share.
	if ctlExcluded != 1 || ctlCounted != 1 || ctlShare != 1.0 {
		t.Fatalf("control (no retraction row) excluded=%d counted=%d share=%v, want 1/1/1.0", ctlExcluded, ctlCounted, ctlShare)
	}

	excluded, counted, share := read("org-retraction", "proj-ret")
	if excluded != ctlExcluded {
		t.Errorf("work_units_without_repo_link = %d, want %d: the newer is_primary=0 retraction row over the retired bare team id %q must not hide the active keyed team's live primary row", excluded, ctlExcluded, bareID)
	}
	if counted != ctlCounted || share != ctlShare {
		t.Errorf("counted=%d share=%v, want %d / %v (identical to the no-retraction control)", counted, share, ctlCounted, ctlShare)
	}
}
