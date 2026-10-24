package devhealthfacts_test

// Evidence-gated work units (categorization_status insufficient_evidence or
// no_text_sources on the unit's LATEST row) are left out of the investment mix,
// EXECUTED against a real ClickHouse. repo-a holds two categorized units (one
// feature_delivery, one maintenance) and two gated units carrying the uniform
// prior; repo-b holds only gated units, one of them categorized on an older row.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestInvestmentLeavesEvidenceGatedUnitsOutAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-uncategorized"
	at := ts(2026, 9, 18, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	a, b := repoUUID("repo-a"), repoUUID("repo-b")
	for label, team := range map[string]string{"repo-a": "team-a", "repo-b": "team-b"} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", at)
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, orgID, "linear", uint8(1))
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", team, repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at)
	}
	prior := map[string]float64{"feature_delivery": 0.2, "operational": 0.2, "maintenance": 0.2, "quality": 0.2, "risk": 0.2}
	seed := func(id string, themes map[string]float64, repo string, pr int, status string, computed time.Time) {
		t.Helper()
		exec(`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, categorization_status, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, 1.0, themes, map[string]float64{}, fmt.Sprintf(`{"issues":[],"prs":["%s#pr%d"]}`, repo, pr), status, computed, orgID)
	}
	seed("wu1", map[string]float64{"feature_delivery": 1}, a, 1, "ok", at)
	seed("wu2", map[string]float64{"maintenance": 1}, a, 2, "ok", at)
	seed("wu3", prior, a, 3, "insufficient_evidence", at)
	seed("wu4", prior, a, 4, "no_text_sources", at)
	seed("wu5", prior, b, 5, "insufficient_evidence", at)
	seed("wu6", map[string]float64{"feature_delivery": 1}, b, 6, "ok", at.Add(-time.Hour))
	seed("wu6", prior, b, 6, "no_text_sources", at)

	read := func(ctx context.Context, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		return result
	}
	factFor := func(result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) *contextfabric.CanonicalFact {
		for i := range result.Facts {
			if _, mix := result.Facts[i].Fields["theme_feature_delivery"]; mix && contextfabric.FactSubjectKey(result.Facts[i].Subject) == contextfabric.FactSubjectKey(subject) {
				return &result.Facts[i]
			}
		}
		return nil
	}
	reasons := func(result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) string {
		return result.Reason + " | " + result.RequestNote + " | " + result.SubjectReasons[contextfabric.FactSubjectKey(subject)]
	}
	wantMix := func(label string, fact *contextfabric.CanonicalFact, gated int64, share float64) {
		t.Helper()
		if fact == nil {
			t.Fatalf("%s: no mix fact", label)
		}
		for field, want := range map[string]float64{"theme_feature_delivery": 0.5, "theme_maintenance": 0.5, "theme_operational": 0, "theme_quality": 0, "theme_risk": 0} {
			if got := *fact.Fields[field].Number; math.Abs(got-want) > 1e-9 {
				t.Errorf("%s: %s = %v, want %v (the two categorized units only, no uniform prior)", label, field, got, want)
			}
		}
		if got := *fact.Fields["uncategorized_unit_count"].Integer; got != gated {
			t.Errorf("%s: uncategorized_unit_count = %d, want %d", label, got, gated)
		}
		if got := *fact.Fields["uncategorized_unit_share"].Number; math.Abs(got-share) > 1e-9 {
			t.Errorf("%s: uncategorized_unit_share = %v, want %v", label, got, share)
		}
		if count, ok := fact.Fields["work_unit_count"]; ok && *count.Integer != 2 {
			t.Errorf("%s: work_unit_count = %d, want 2", label, *count.Integer)
		}
	}

	repoA, repoB := repositorySubject("repo-a"), repositorySubject("repo-b")
	repos := read(ctx, repoA, repoB)
	wantMix("repo-a", factFor(repos, repoA), 2, 0.5)
	if got := reasons(repos, repoA); !strings.Contains(got, "2 of 4 work units") {
		t.Errorf("repo-a reasons = %q", got)
	}
	if f := factFor(repos, repoB); f != nil {
		t.Errorf("repo-b, all gated, serves a mix: %v", f.Fields)
	}
	if got := reasons(repos, repoB); !strings.Contains(got, "2 of 2 work units") || !strings.Contains(got, "no theme mix is served") {
		t.Errorf("repo-b reasons = %q", got)
	}

	teamA, teamB := teamSubject("team-a"), teamSubject("team-b")
	teams := read(ctx, teamA, teamB)
	wantMix("team-a", factFor(teams, teamA), 2, 0.5)
	if f := factFor(teams, teamB); f != nil {
		t.Errorf("team-b, all gated, serves a mix: %v", f.Fields)
	}
	if got := reasons(teams, teamB); !strings.Contains(got, "2 of 2 work units") {
		t.Errorf("team-b reasons = %q", got)
	}

	org := organizationSubject(orgID)
	wantMix("organization", factFor(read(ctx, org), org), 4, 4.0/6.0)

	for _, subject := range []contextfabric.SubjectRef{repoA, org} {
		listing := read(contextfabric.WithInvestmentUnits(ctx, contextfabric.InvestmentUnitsRequest{Max: 50}), subject)
		var shareSum, featureSum float64
		var units []string
		for _, row := range unitFactsOf(listing, contextfabric.InvestmentUnitKind) {
			units = append(units, *row.Fields["work_unit_id"].String)
			share := *row.Fields["share_in_scope"].Number
			shareSum += share
			featureSum += share * *row.Fields["unit_theme_feature_delivery"].Number
		}
		if fmt.Sprint(units) != "[wu1 wu2]" && fmt.Sprint(units) != "[wu2 wu1]" {
			t.Errorf("%s listing units = %v, want wu1 and wu2 only", subject.CanonicalID, units)
		}
		if shareSum <= 0 || math.Abs(featureSum/shareSum-0.5) > 1e-9 {
			t.Errorf("%s listing ties to %v, want the fact's 0.5", subject.CanonicalID, featureSum/shareSum)
		}
	}
}
