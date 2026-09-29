package devhealthfacts_test

// CHAOS-7073 (ruling K11): a team_repo_ownership row with no repo_id is
// resolved by (provider, lower-cased repository name) against repos, as ops'
// teamscope.RepoCondition does, at every fact read that asks which
// repositories a team owns: the team investment mix, the project theme mix
// and the project health roll-up (risk_breakdown, severity, daily series).
// Each case asserts the SERVED fact value on a real ClickHouse.

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// zeroRepoUUID is the value ClickHouse fills into an unmatched LEFT JOIN's
// UUID column. A repository carrying it is seeded so that a resolution which
// stops testing the join's match sentinel is caught: without the sentinel an
// unmatched name would resolve to this repository.
const zeroRepoUUID = "00000000-0000-0000-0000-000000000000"

// seedCHAOS7073Ownership seeds the K11 org:
//
//	repos: acme/repo-k, acme/repo-b (github) and acme/zero (github, zero UUID).
//	work: repo-k 10 feature, repo-b 4 maintenance, zero 50 operational,
//	      an orphan id (in no repos row) 20 risk.
//	ownership:
//	  team-name      github "ACME/Repo-K", repo_id NULL      -> repo-k (case-insensitive name)
//	  team-ghost     github "acme/ghost",  repo_id NULL      -> nothing (no such repository)
//	  team-provider  gitlab "acme/repo-k", repo_id NULL      -> nothing (provider differs)
//	  team-orphan    github "acme/orphan", repo_id = orphan  -> nothing (id not in repos)
//	  team-id-wins   github "acme/repo-k", repo_id = repo-b  -> repo-b only (own id wins)
func seedCHAOS7073Ownership(f *chaos7073Fixture) {
	f.t.Helper()
	for _, r := range []struct{ id, name string }{
		{repoUUID("repo-k"), "acme/repo-k"}, {repoUUID("repo-b"), "acme/repo-b"}, {zeroRepoUUID, "acme/zero"},
	} {
		f.exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, r.id, f.orgID, r.name, "github", f.at)
	}
	f.unit("wu-k", "repo-k", 10, "feature_delivery", f.at)
	f.unit("wu-b", "repo-b", 4, "maintenance", f.at)
	f.unit("wu-orphan", "orphan", 20, "risk", f.at)
	f.exec("work unit wu-zero", `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"wu-zero", f.at, f.at, zeroRepoUUID, 50.0, map[string]float64{"operational": 1.0}, map[string]float64{}, `{"issues":[],"prs":[]}`, f.at, f.orgID)
	own := func(teamID, provider, name string, repoID any) {
		f.t.Helper()
		f.exec("ownership "+teamID, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.orgID, provider, teamID, repoID, name, "exact", "native", uint8(1), uint16(100), int32(0), f.at, nil, f.at)
	}
	own("team-name", "github", "ACME/Repo-K", nil)
	own("team-ghost", "github", "acme/ghost", nil)
	own("team-provider", "gitlab", "acme/repo-k", nil)
	own("team-orphan", "github", "acme/orphan", repoUUID("orphan"))
	own("team-id-wins", "github", "acme/repo-k", repoUUID("repo-b"))
}

func TestCHAOS7073TeamMixResolvesOwnershipWithoutRepoIDByName(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-owned-by-name")
	seedCHAOS7073Ownership(f)

	f.assertMix(teamSubject("team-name"), map[string]float64{"feature_delivery": 10},
		`ownership "ACME/Repo-K" with no repo_id must resolve to acme/repo-k`)
	f.assertMix(teamSubject("team-id-wins"), map[string]float64{"maintenance": 4},
		"a row's own repo_id wins over its name")
	for _, team := range []string{"team-ghost", "team-provider", "team-orphan"} {
		if got := f.mix(teamSubject(team)); got != nil {
			t.Fatalf("%s: served mix %v, want none (its ownership row resolves to no repository)", team, got)
		}
	}
}

func TestCHAOS7073ProjectRollupsResolveOwnershipWithoutRepoIDByName(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-project-by-name")
	for _, statement := range devhealthschema.DDL("compounding_risk_daily") {
		f.exec("compounding_risk_daily table", statement)
	}
	seedCHAOS7073Ownership(f)
	for _, p := range []struct{ id, key, team string }{
		{"proj-name", "PN", "team-name"}, {"proj-ghost", "PG", "team-ghost"},
	} {
		f.exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			p.id, f.orgID, "linear", p.key, "Project "+p.id, uint8(1), "active", "", f.at)
		f.exec("project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			f.orgID, "linear", p.team, p.id, p.key, "native", f.at, nil, f.at)
	}
	// Only a REPO-scope risk row exists, for repo-k (and one for the zero
	// repository): the project reaches it only through team-name's
	// name-resolved ownership.
	for _, r := range []struct {
		id       string
		risk     float64
		severity string
	}{{repoUUID("repo-k"), 0.81, "high"}, {zeroRepoUUID, 0.3, "low"}} {
		f.exec("compounding_risk_daily", `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
			f.orgID, recentHealthDay(1), "repo", r.id, r.risk, r.severity, f.at)
	}

	// Project theme mix (investment.go readProjectThemeMix).
	investment := f.readProjectFact(contextfabric.FactInvestment, "proj-name")
	if investment == nil {
		t.Fatal("proj-name: no investment fact, want the theme mix of repo-k reached through team-name")
	}
	if got := investment.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)].Number; got == nil || *got != 1 {
		t.Fatalf("proj-name theme_feature_delivery = %s, want 1 (repo-k's wu-k only)", chaos7073Number(got))
	}
	if got := investment.Fields["work_unit_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("proj-name work_unit_count = %s, want 1", chaos7073Integer(got))
	}
	if ghost := f.readProjectFact(contextfabric.FactInvestment, "proj-ghost"); ghost != nil {
		if v, ok := ghost.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeOperational)]; ok {
			t.Fatalf("proj-ghost served theme_operational %s, want no theme mix (team-ghost owns nothing)", chaos7073Number(v.Number))
		}
	}

	// Project health (health.go readProjectHealth, the severity aggregate and
	// the daily series).
	health := f.readProjectFact(contextfabric.FactHealth, "proj-name")
	if health == nil {
		t.Fatal("proj-name: no health fact, want repo-k's risk reached through team-name (severity aggregate)")
	}
	if got := health.Fields["severity"].String; got == nil || *got != "high" {
		t.Fatalf("proj-name severity = %s, want high (repo-k's row, severity aggregate)", chaos7073String(got))
	}
	sawRepoK := false
	for _, row := range health.Fields["risk_breakdown"].Rows {
		if s := row.Fields["scope_id"].String; s != nil && *s == repoUUID("repo-k") {
			sawRepoK = true
		}
	}
	if !sawRepoK {
		t.Fatalf("proj-name risk_breakdown = %#v, want a repo row for repo-k", health.Fields["risk_breakdown"].Rows)
	}
	if got := health.Fields["compounding_risk"].Number; got == nil || *got != 0.81 {
		t.Fatalf("proj-name compounding_risk = %s, want 0.81 (repo-k's freshest daily row, daily series)", chaos7073Number(got))
	}
	if ghost := f.readProjectFact(contextfabric.FactHealth, "proj-ghost"); ghost != nil {
		t.Fatalf("proj-ghost health fact %v, want none (team-ghost owns nothing)", ghost.Fields)
	}
}

// readProjectFact reads kind for one linear project (nil when none served).
func (f *chaos7073Fixture) readProjectFact(kind contextfabric.FactKind, projectID string) *contextfabric.CanonicalFact {
	f.t.Helper()
	provider := f.provider
	if kind != contextfabric.FactInvestment {
		provider = findProvider(f.t, f.providers, kind)
	}
	result, err := provider.ReadFacts(f.ctx, storage.Principal{OrgID: f.orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: kind, Subjects: []contextfabric.SubjectRef{projectSubject("linear", projectID)},
	})
	if err != nil {
		f.t.Fatalf("ReadFacts(%s, %s): %v", kind, projectID, err)
	}
	if len(result.Facts) == 0 {
		return nil
	}
	if len(result.Facts) != 1 {
		f.t.Fatalf("%s %s: %d facts, want at most 1", kind, projectID, len(result.Facts))
	}
	return &result.Facts[0]
}

func chaos7073Number(v *float64) string {
	if v == nil {
		return "<absent>"
	}
	return fmt.Sprint(*v)
}

func chaos7073Integer(v *int64) string {
	if v == nil {
		return "<absent>"
	}
	return fmt.Sprint(*v)
}

func chaos7073String(v *string) string {
	if v == nil {
		return "<absent>"
	}
	return *v
}
