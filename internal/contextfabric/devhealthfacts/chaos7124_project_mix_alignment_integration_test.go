package devhealthfacts_test

// CHAOS-7124: the project theme mix (the owning-team roll-up and the
// project-native mix) reads the same latest work-unit set ops reads --
// superseded units excluded, only the current membership run, and (roll-up
// only, the native mix never reads repo_id) a NULL latest repo_id kept NULL.
// Each test asserts the SERVED project fact on a real ClickHouse; each clause
// has its own test so a regression of one clause fails exactly one test.

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// projectOwnedThroughRepo seeds project -> team -> repo ownership.
func (f *chaos7073Fixture) projectOwnedThroughRepo(projectID, teamID, repoLabel string) {
	f.t.Helper()
	f.exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		projectID, f.orgID, "linear", nil, "Project "+projectID, uint8(1), "active", "", f.at)
	f.exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		teamID, teamID, f.at, f.orgID, "linear", uint8(1))
	f.exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		f.orgID, "linear", teamID, projectID, nil, "native", f.at, nil, f.at)
	f.exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
		repoUUID(repoLabel), f.orgID, "acme/"+repoLabel, "github", f.at)
	f.exec("repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.orgID, "linear", teamID, repoUUID(repoLabel), "acme/"+repoLabel, "exact", "native", uint8(1), uint16(100), int32(0), f.at, nil, f.at)
}

// issueUnit seeds one work unit (no repo_id) whose issue evidence names
// itemID; the item is placed in projectID so the native mix reaches it.
func (f *chaos7073Fixture) issueUnit(id, projectID, itemID string, effort float64, theme string) {
	f.t.Helper()
	f.exec("work item "+itemID, `INSERT INTO work_items (repo_id, work_item_id, provider, title, type, status, project_key, project_id, native_team_key, project_name, created_at, updated_at, completed_at, parent_id, url, last_synced, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"00000000-0000-0000-0000-000000000000", itemID, "linear", "title", "issue", "open", "", projectID, "", "", f.at, f.at, nil, "", "", f.at, f.orgID)
	f.exec("issue unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
		id, f.at, f.at, effort, map[string]float64{theme: 1.0}, map[string]float64{}, fmt.Sprintf(`{"issues":[%q],"prs":[]}`, itemID), f.at, f.orgID)
}

// projectMix returns the served theme shares, work_unit_count and mix source
// of the project's single investment fact.
func (f *chaos7073Fixture) projectMix(projectID string) (shares map[string]float64, workUnits int64, source string) {
	f.t.Helper()
	result, err := f.provider.ReadFacts(f.ctx, storage.Principal{OrgID: f.orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", projectID)},
	})
	if err != nil {
		f.t.Fatalf("ReadFacts(project %s): %v", projectID, err)
	}
	if len(result.Facts) != 1 {
		f.t.Fatalf("project %s: facts = %#v, want exactly 1", projectID, result.Facts)
	}
	fields := result.Facts[0].Fields
	shares = map[string]float64{}
	for _, theme := range []string{"feature_delivery", "operational", "maintenance", "quality", "risk"} {
		value, ok := fields["theme_"+theme]
		if !ok || value.Number == nil {
			f.t.Fatalf("project %s: theme_%s = %#v, want a number (fields %v)", projectID, theme, value, fields)
		}
		shares[theme] = *value.Number
	}
	if value := fields["work_unit_count"]; value.Integer != nil {
		workUnits = *value.Integer
	}
	if value := fields["investment_mix_source"]; value.String != nil {
		source = *value.String
	}
	return shares, workUnits, source
}

func (f *chaos7073Fixture) assertProjectMix(projectID, wantSource string, want map[string]float64, wantUnits int64, why string) {
	f.t.Helper()
	got, units, source := f.projectMix(projectID)
	if source != wantSource {
		f.t.Fatalf("%s: investment_mix_source = %q, want %q (%s)", projectID, source, wantSource, why)
	}
	for theme, share := range got {
		if math.Abs(share-want[theme]) > 1e-9 {
			f.t.Fatalf("%s: theme_%s = %v, want %v (%s); shares %v", projectID, theme, share, want[theme], why, got)
		}
	}
	if units != wantUnits {
		f.t.Fatalf("%s: work_unit_count = %d, want %d (%s)", projectID, units, wantUnits, why)
	}
}

// --- owning-team roll-up (readProjectThemeMix) ---

func TestCHAOS7124RollupExcludesSupersededWorkUnit(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7124-rollup-sup")
	f.projectOwnedThroughRepo("proj-1", "team-1", "repo-a")
	f.unit("wu-keep", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-sup", "repo-a", 10, "risk", f.at)
	f.superseded("wu-sup")

	f.assertProjectMix("proj-1", "owning_team_rollup", map[string]float64{"feature_delivery": 1}, 1, "wu-sup is superseded")
}

func TestCHAOS7124RollupExcludesWorkUnitOutsideCurrentMembershipRun(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7124-rollup-run")
	f.projectOwnedThroughRepo("proj-1", "team-1", "repo-a")
	f.unit("wu-in", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-out", "repo-a", 10, "risk", f.at)
	f.run("run-1", f.at)
	f.member("run-1", "N-1", "wu-in", f.at)

	f.assertProjectMix("proj-1", "owning_team_rollup", map[string]float64{"feature_delivery": 1}, 1, "wu-out is outside run-1")
}

func TestCHAOS7124RollupNullLatestRepoIDDoesNotReviveOlderRepository(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7124-rollup-null")
	f.projectOwnedThroughRepo("proj-1", "team-1", "repo-a")
	f.unit("wu-anchor", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-moved", "repo-a", 7, "risk", f.at.Add(-time.Hour))
	f.unit("wu-moved", "", 7, "risk", f.at)

	f.assertProjectMix("proj-1", "owning_team_rollup", map[string]float64{"feature_delivery": 1}, 1, "wu-moved's latest row has no repository")
}

// --- project-native mix (readProjectNativeThemeMixRows) ---

func TestCHAOS7124NativeExcludesSupersededWorkUnit(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7124-native-sup")
	f.projectOwnedThroughRepo("proj-1", "team-1", "repo-a")
	f.issueUnit("wu-keep", "proj-1", "linear:N-1", 10, "operational")
	f.issueUnit("wu-sup", "proj-1", "linear:N-2", 10, "risk")
	f.superseded("wu-sup")

	f.assertProjectMix("proj-1", "project_native", map[string]float64{"operational": 1}, 1, "wu-sup is superseded")
}

func TestCHAOS7124NativeExcludesWorkUnitOutsideCurrentMembershipRun(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7124-native-run")
	f.projectOwnedThroughRepo("proj-1", "team-1", "repo-a")
	f.issueUnit("wu-in", "proj-1", "linear:N-1", 10, "operational")
	f.issueUnit("wu-out", "proj-1", "linear:N-2", 10, "risk")
	f.run("run-1", f.at)
	f.member("run-1", "N-1", "wu-in", f.at)

	f.assertProjectMix("proj-1", "project_native", map[string]float64{"operational": 1}, 1, "wu-out is outside run-1")
}
