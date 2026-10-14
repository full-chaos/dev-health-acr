package devhealthfacts_test

// A team whose owned repositories carry work units that all have zero
// persisted effort is served: every work unit weighs 1 whatever its persisted
// effort, so the units are kept and counted. EXECUTED against a real
// ClickHouse.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTeamWithOnlyZeroEffortUnitsKeepsThemAtWeightOneAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	createPullRequestLinkTables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-zero-effort"
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	synced := end.Add(-10 * day)

	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	owns := func(team, repo string) {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), orgID, "acme/"+repo, "github", synced)
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", team, repoUUID(repo), "acme/"+repo, "exact", "native", uint8(1), uint16(100), int32(0), synced, nil, synced)
	}
	seed := func(id, repo string, effort float64) {
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, end.Add(-20*day), end.Add(-15*day), effort, map[string]float64{"feature_delivery": 1.0}, map[string]float64{},
			fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repoUUID(repo), prNumber(id)), end.Add(-15*day), orgID)
	}
	owns("team-zero", "repo-zero")
	seed("zero-1", "repo-zero", 0)
	seed("zero-2", "repo-zero", 0)
	owns("team-empty", "repo-empty")

	read := func(team string) (contextfabric.FactProviderResult, string) {
		t.Helper()
		start := end.Add(-60 * day)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
			Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result, contextfabric.CombineRequestNote(result.Reason, result.RequestNote)
	}

	zero, served := read("team-zero")
	if len(zero.Facts) != 1 {
		t.Fatalf("zero-effort team facts = %d, want 1 (the units are kept at weight 1): %q", len(zero.Facts), served)
	}
	if strings.Contains(served, "investment_team_effort_not_measurable") || strings.Contains(served, "investment_team_no_unit_in_window") {
		t.Fatalf("zero-effort team reason = %q, want no empty cause", served)
	}
	gotEffort := 0.0
	for _, row := range zero.Facts[0].Fields["theme_breakdown"].Table.Rows {
		gotEffort += *row.Fields["weighted_effort"].Number
	}
	if gotEffort != 2 {
		t.Fatalf("zero-effort team weighted effort = %v, want 2 (two units, weight 1 each)", gotEffort)
	}

	empty, emptyServed := read("team-empty")
	if len(empty.Facts) != 0 || !strings.Contains(emptyServed, "investment_team_no_unit_in_window") || strings.Contains(emptyServed, "investment_team_effort_not_measurable") {
		t.Fatalf("team with no unit: facts=%d reason=%q, want investment_team_no_unit_in_window only", len(empty.Facts), emptyServed)
	}
}

// The same property at the read_facts boundary: the zero-effort team's units
// are kept, a fact is served, and no effort-not-measurable coverage row exists.
func TestZeroEffortTeamUnitsAreServedAtTheReadFactsBoundary(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	createPullRequestLinkTables(t, ctx, direct)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-zero-effort-served"
	exec := func(stmt string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-z"), orgID, "acme/repo-z", "github", at)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", "team-z", repoUUID("repo-z"), "acme/repo-z", "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
	for _, id := range []string{"wu-z1", "wu-z2", "wu-z3"} {
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repoUUID("repo-z"), 0.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, "{}", at, orgID)
	}

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(seam), nil)
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 25, 0, 0, 0)
	team := teamSubject("team-z")
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009538"),
		storage.Principal{OrgID: orgID, Subject: "t9538", CredentialID: "t9538"}, directread.FactsRequest{
			Kinds:    []string{"investment"},
			Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: team.CanonicalID}},
			Window:   &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &end},
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	raw, _ := json.Marshal(response)
	if len(response.Facts) != 1 {
		t.Fatalf("a team whose units carry no persisted effort serves one fact (weight 1 per unit), got %d: %s", len(response.Facts), raw)
	}
	for _, row := range response.Coverage {
		if row.Subject.CanonicalID == team.CanonicalID && strings.Contains(row.Reason, "investment_team_effort_not_measurable") {
			t.Fatalf("coverage names investment_team_effort_not_measurable for a team whose units are kept: %s", raw)
		}
	}
}
