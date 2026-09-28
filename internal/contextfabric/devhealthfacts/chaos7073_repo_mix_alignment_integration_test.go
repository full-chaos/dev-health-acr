package devhealthfacts_test

// CHAOS-7073 (ruling K16): the repository/team theme mix reads the latest
// work-unit set ops' LatestWorkUnitInvestmentsSource reads -- superseded work
// units excluded, only the current membership run (none recorded => no
// filter; the legacy marker => each node's latest pre-run row), and a NULL
// latest repo_id kept NULL. Each test asserts the SERVED fact value on a real
// ClickHouse, for the repository subject and for the team that owns it.

import (
	"context"
	"math"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos7073Fixture seeds one org and reads its repository/team mixes.
type chaos7073Fixture struct {
	t         *testing.T
	ctx       context.Context
	direct    clickhousedriver.Conn
	providers []contextfabric.FactProvider
	provider  contextfabric.FactProvider
	orgID     string
	at        time.Time
}

func newCHAOS7073Fixture(t *testing.T, orgID string) *chaos7073Fixture {
	t.Helper()
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	return &chaos7073Fixture{
		t: t, ctx: ctx, direct: direct, orgID: orgID, at: ts(2026, 9, 18, 0, 0, 0),
		providers: providers, provider: findProvider(t, providers, contextfabric.FactInvestment),
	}
}

func (f *chaos7073Fixture) exec(what, statement string, args ...any) {
	f.t.Helper()
	if err := f.direct.Exec(f.ctx, statement, args...); err != nil {
		f.t.Fatalf("seed %s: %v", what, err)
	}
}

// repoOwnedBy seeds a repository and a current ownership row (repo_id set)
// naming it for teamID.
func (f *chaos7073Fixture) repoOwnedBy(label, teamID string) {
	f.t.Helper()
	f.exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
		repoUUID(label), f.orgID, "acme/"+label, "github", f.at)
	f.exec("ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.orgID, "github", teamID, repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), f.at, nil, f.at)
}

// unit seeds one work_unit_investments version with no PR ref, so the unit
// reaches the repository only through its own repo_id ("" = NULL).
func (f *chaos7073Fixture) unit(id, repoLabel string, effort float64, theme string, computedAt time.Time) {
	f.t.Helper()
	var repo any
	if repoLabel != "" {
		repo = repoUUID(repoLabel)
	}
	f.exec("work unit "+id, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, f.at, f.at, repo, effort, map[string]float64{theme: 1.0}, map[string]float64{}, `{"issues":[],"prs":[]}`, computedAt, f.orgID)
}

func (f *chaos7073Fixture) superseded(workUnitID string) {
	f.t.Helper()
	f.exec("supersession", `INSERT INTO work_unit_supersessions (org_id, superseded_work_unit_id, superseded_at) VALUES (?,?,?)`,
		f.orgID, workUnitID, f.at)
}

func (f *chaos7073Fixture) run(runID string, completedAt time.Time) {
	f.t.Helper()
	f.exec("membership run", `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`,
		f.orgID, runID, completedAt)
}

func (f *chaos7073Fixture) member(runID, nodeID, workUnitID string, computedAt time.Time) {
	f.t.Helper()
	f.exec("membership", `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id) VALUES (?,?,?,?,?,?,?,?)`,
		f.orgID, "issue", nodeID, workUnitID, "theme", "feature_delivery", computedAt, runID)
}

// mix returns the served weighted effort per theme of subject's investment
// fact (nil when no fact carries a theme_breakdown).
func (f *chaos7073Fixture) mix(subject contextfabric.SubjectRef) map[string]float64 {
	f.t.Helper()
	result, err := f.provider.ReadFacts(f.ctx, storage.Principal{OrgID: f.orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
	})
	if err != nil {
		f.t.Fatalf("ReadFacts(%s): %v", subject.CanonicalID, err)
	}
	for _, fact := range result.Facts {
		table := fact.Fields["theme_breakdown"].Table
		if table == nil {
			continue
		}
		out := map[string]float64{}
		for _, row := range table.Rows {
			out[*row.Fields["theme"].String] = *row.Fields["weighted_effort"].Number
		}
		return out
	}
	return nil
}

func repositorySubject(label string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID(label), Label: label}
}

// assertMix fails unless subject's served mix is exactly want (themes absent
// from want must be 0).
func (f *chaos7073Fixture) assertMix(subject contextfabric.SubjectRef, want map[string]float64, why string) {
	f.t.Helper()
	got := f.mix(subject)
	if got == nil {
		f.t.Fatalf("%s: no mix served, want %v (%s)", subject.CanonicalID, want, why)
	}
	for _, theme := range []string{"feature_delivery", "operational", "maintenance", "quality", "risk"} {
		if math.Abs(got[theme]-want[theme]) > 1e-9 {
			f.t.Fatalf("%s: %s weighted effort = %v, want %v (%s); served mix %v", subject.CanonicalID, theme, got[theme], want[theme], why, got)
		}
	}
}

// K16a: a superseded work unit's effort reaches neither its repository nor
// the owning team -- even with no membership run recorded, when the
// membership scope reads every unit (the supersession filter is independent
// of it).
func TestCHAOS7073SupersededWorkUnitIsExcludedFromRepositoryAndTeamMix(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-superseded")
	f.repoOwnedBy("repo-a", "team-a")
	f.unit("wu-live", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-retired", "repo-a", 7, "risk", f.at)
	f.superseded("wu-retired")

	want := map[string]float64{"feature_delivery": 10}
	f.assertMix(repositorySubject("repo-a"), want, "superseded wu-retired's 7 risk must not count")
	f.assertMix(teamSubject("team-a"), want, "superseded wu-retired's 7 risk must not reach the team")
}

// K16b: only the work units of the latest complete membership run count.
func TestCHAOS7073WorkUnitOutsideCurrentMembershipRunIsExcluded(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-run")
	f.repoOwnedBy("repo-a", "team-a")
	f.unit("wu-current", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-previous", "repo-a", 7, "risk", f.at)
	// run-1 (older) grouped wu-previous; run-2 (latest complete) only
	// wu-current. run-3 has membership rows but no completion marker: an
	// incomplete run is never read.
	f.run("run-1", f.at.Add(-2*time.Hour))
	f.run("run-2", f.at.Add(-time.Hour))
	f.member("run-1", "ISS-1", "wu-previous", f.at.Add(-2*time.Hour))
	f.member("run-2", "ISS-2", "wu-current", f.at.Add(-time.Hour))
	f.member("run-3", "ISS-3", "wu-previous", f.at)
	// A pre-run (run_id = '') row naming wu-previous: read only when the
	// latest marker is the legacy one, which here it is not.
	f.member("", "ISS-9", "wu-previous", f.at.Add(-3*time.Hour))

	want := map[string]float64{"feature_delivery": 10}
	f.assertMix(repositorySubject("repo-a"), want, "wu-previous is outside the latest complete run run-2")
	f.assertMix(teamSubject("team-a"), want, "wu-previous is outside the latest complete run run-2")
}

// K16b, ops semantics: with no membership run recorded, nothing is filtered
// -- membership rows of an unmarked (incomplete) run do not narrow the read.
func TestCHAOS7073NoMembershipRunRecordedFiltersNothing(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-no-run")
	f.repoOwnedBy("repo-a", "team-a")
	f.unit("wu-1", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-2", "repo-a", 7, "risk", f.at)
	f.member("run-unmarked", "ISS-1", "wu-1", f.at)

	want := map[string]float64{"feature_delivery": 10, "risk": 7}
	f.assertMix(repositorySubject("repo-a"), want, "no complete run recorded: every unit counts")
	f.assertMix(teamSubject("team-a"), want, "no complete run recorded: every unit counts")
}

// K16b, the legacy branch: when the latest marker is ops' '__legacy__' one,
// each node's LATEST empty-run_id membership row decides; an earlier
// grouping of the same node does not.
func TestCHAOS7073LegacyMembershipRunReadsEachNodesLatestRow(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-legacy")
	f.repoOwnedBy("repo-a", "team-a")
	f.unit("wu-regrouped", "repo-a", 7, "risk", f.at)
	f.unit("wu-latest", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-other", "repo-a", 3, "maintenance", f.at)
	f.unit("wu-unmembered", "repo-a", 100, "quality", f.at)
	f.run("__legacy__", f.at)
	// ISS-1 was in wu-regrouped, then regrouped into wu-latest; ISS-2 has
	// one row (wu-other). wu-unmembered has no legacy row at all.
	f.member("", "ISS-1", "wu-regrouped", f.at.Add(-2*time.Hour))
	f.member("", "ISS-1", "wu-latest", f.at.Add(-time.Hour))
	f.member("", "ISS-2", "wu-other", f.at.Add(-2*time.Hour))
	// A row of an unmarked real run at ISS-1's latest legacy instant: the
	// legacy branch reads run_id = '' rows only.
	f.member("run-x", "ISS-1", "wu-unmembered", f.at.Add(-time.Hour))

	want := map[string]float64{"feature_delivery": 10, "maintenance": 3}
	f.assertMix(repositorySubject("repo-a"), want, "legacy marker: only each node's latest pre-run row's unit counts")
	f.assertMix(teamSubject("team-a"), want, "legacy marker: only each node's latest pre-run row's unit counts")
}

// K16c: a work unit whose LATEST row has a NULL repo_id no longer reaches the
// repository an OLDER row named.
func TestCHAOS7073NullLatestRepoIDDoesNotReviveOlderRepository(t *testing.T) {
	f := newCHAOS7073Fixture(t, "org-7073-null-repo")
	f.repoOwnedBy("repo-a", "team-a")
	f.unit("wu-anchor", "repo-a", 10, "feature_delivery", f.at)
	f.unit("wu-moved", "repo-a", 7, "risk", f.at.Add(-time.Hour))
	f.unit("wu-moved", "", 7, "risk", f.at)

	want := map[string]float64{"feature_delivery": 10}
	f.assertMix(repositorySubject("repo-a"), want, "wu-moved's latest row has no repository")
	f.assertMix(teamSubject("team-a"), want, "wu-moved's latest row has no repository")
}
