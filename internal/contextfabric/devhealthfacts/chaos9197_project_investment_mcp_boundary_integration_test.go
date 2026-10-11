package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-9197 (D5932), the row at the MCP tool boundary: read_facts (directread.FactsReader over the
// real registry and the real provider on a real ClickHouse). A project investment answer carries
// the canonical theme mix only: no team_breakdown key, none of the legacy day fields, and
// rollup_basis is the work_unit_investments one. A project with no canonical mix is not silent:
// its coverage row names the reason (missing is not healthy).
func TestCHAOS9197ProjectInvestmentAtTheReadFactsBoundary(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-9197"
	exec := func(stmt string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	for _, p := range []string{"proj-mix", "proj-nomix"} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			p, orgID, "linear", nil, "Project "+p, uint8(1), "active", "", at)
	}
	for _, team := range []string{"team-mix", "team-nomix"} {
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, orgID, "linear", uint8(1))
	}
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-mix", "proj-mix", nil, "native", at, nil, at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-nomix", "proj-nomix", nil, "native", at, nil, at)
	for _, r := range [][2]string{{"repo-mix", "team-mix"}, {"repo-nomix", "team-nomix"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(r[0]), orgID, "acme/"+r[0], "github", at)
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", r[1], repoUUID(r[0]), "acme/"+r[0], "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
	}
	// The canonical mix exists for proj-mix only.
	exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"wu-mix", at, at, repoUUID("repo-mix"), 10.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, "{}", at, orgID)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(seam), nil)
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 25, 0, 0, 0)
	mix, noMix := projectSubject("linear", "proj-mix"), projectSubject("linear", "proj-nomix")
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009197"),
		storage.Principal{OrgID: orgID, Subject: "t9197", CredentialID: "t9197"}, directread.FactsRequest{
			Kinds: []string{"investment"},
			Subjects: []directread.RequestSubject{
				{Kind: "project", CanonicalID: mix.CanonicalID},
				{Kind: "project", CanonicalID: noMix.CanonicalID},
			},
			Window: &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &end},
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	raw, _ := json.Marshal(response)
	body := string(raw)
	for _, banned := range []string{"team_breakdown", "investment_area", "project_stream", "delivery_units", "work_items_completed", "prs_merged", "churn_loc"} {
		if strings.Contains(body, `"`+banned+`"`) {
			t.Errorf("the served answer carries the legacy key %q: %s", banned, body)
		}
	}
	var served *directread.ServedFact
	for i := range response.Facts {
		if response.Facts[i].Subject.CanonicalID == mix.CanonicalID {
			served = &response.Facts[i]
		}
		if response.Facts[i].Subject.CanonicalID == noMix.CanonicalID {
			t.Errorf("proj-nomix has no canonical mix and must serve no fact: %+v", response.Facts[i])
		}
	}
	if served == nil {
		t.Fatalf("proj-mix serves no investment fact: %s", body)
	}
	if got, _ := served.Fields["rollup_basis"].(string); got != "team_project_ownership_via_owned_repos_work_unit_investments" {
		t.Errorf("rollup_basis = %#v, want team_project_ownership_via_owned_repos_work_unit_investments", served.Fields["rollup_basis"])
	}
	if _, has := served.Tables["team_breakdown"]; has {
		t.Errorf("tables carry team_breakdown: %+v", served.Tables)
	}
	named := false
	for _, row := range response.Coverage {
		if row.Subject.CanonicalID == noMix.CanonicalID && strings.Contains(row.Reason, "investment_project_no_canonical_mix") {
			named = true
		}
	}
	if !named {
		t.Errorf("proj-nomix coverage has no named reason investment_project_no_canonical_mix (missing must not be silent): %+v", response.Coverage)
	}
	_ = time.Second
}

// The 4521b property, on the new basis (MCP's ack condition): a pseudo-project ownership row, one whose
// `projects` row does not exist, attributes to NOTHING. Executed on a real ClickHouse through the provider:
// no FactInvestment for the project and the named reason, though the owning team owns a repository with work.
func TestCHAOS9197APseudoProjectOwnershipRowAttributesToNothingOnTheCanonicalBasis(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	at := ts(2026, 9, 18, 0, 0, 0)
	const orgID = "org-9197-pseudo"
	exec := func(stmt string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-ghost", "team-ghost", at, orgID, "linear", uint8(1))
	// The ownership row names a project id that has NO row in `projects`.
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-ghost", "ghost-project", nil, "native", at, nil, at)
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-ghost"), orgID, "acme/repo-ghost", "github", at)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-ghost", repoUUID("repo-ghost"), "acme/repo-ghost", "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at)
	exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"wu-ghost", at, at, repoUUID("repo-ghost"), 10.0, map[string]float64{"feature_delivery": 1.0}, map[string]float64{}, "{}", at, orgID)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "ghost-project")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want none: a pseudo-project ownership row must attribute to nothing", result.Facts)
	}
	if !strings.Contains(result.Reason, "investment_project_no_canonical_mix") {
		t.Fatalf("reason = %q, want the named investment_project_no_canonical_mix", result.Reason)
	}
}
