package devhealthfacts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The remembered membership scope is bounded at 100,000 work units: a run at
// the bound is bound as one array and remembered, a run one unit above it is
// read through the scope subqueries on every request. Both give the same
// answer: a work unit outside the run never reaches the mix.
func TestThemeMixScopeAtAndAboveTheBoundServeTheSameMixAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	at := ts(2026, 9, 10, 0, 0, 0)
	repo := repoUUID("bound-repo")

	seed := func(orgID string, runUnits int) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo, orgID, "acme/bound-repo", "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		wu, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
		if err != nil {
			t.Fatal(err)
		}
		evidence := fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, repo)
		for _, u := range []struct {
			id     string
			effort float64
			theme  string
		}{{"wu-in-1", 2, "feature_delivery"}, {"wu-in-2", 2, "operational"}, {"wu-out", 100, "risk"}} {
			if err := wu.Append(u.id, at, at, nil, u.effort, map[string]float64{u.theme: 1.0}, map[string]float64{}, evidence, at, orgID); err != nil {
				t.Fatal(err)
			}
		}
		if err := wu.Send(); err != nil {
			t.Fatal(err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_unit_membership_runs (org_id, run_id, completed_at) VALUES (?,?,?)`, orgID, "run-1", at); err != nil {
			t.Fatal(err)
		}
		members, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_membership (org_id, node_type, node_id, work_unit_id, category_kind, category, computed_at, run_id)`)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{"wu-in-1", "wu-in-2"}
		for i := 0; len(ids) < runUnits; i++ {
			ids = append(ids, fmt.Sprintf("fill-%06d", i))
		}
		for i, id := range ids {
			if err := members.Append(orgID, "issue", fmt.Sprintf("issue-%06d", i), id, "theme", "feature_delivery", at, "run-1"); err != nil {
				t.Fatal(err)
			}
		}
		if err := members.Send(); err != nil {
			t.Fatal(err)
		}
	}

	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repo, Label: "bound-repo"}
	read := func(orgID string) (shares map[string]float64, membershipStatements uint64) {
		t.Helper()
		before := budgetQueryLogTotals(t, ctx, direct, "work_unit_membership")
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil {
			t.Fatalf("ReadFacts(%s): %v\nserver: %s", orgID, err, lastServerException(ctx, direct))
		}
		shares = map[string]float64{}
		for _, fact := range result.Facts {
			for _, theme := range []string{contextfabric.ThemeFeatureDelivery, contextfabric.ThemeOperational, contextfabric.ThemeRisk} {
				if v, ok := fact.Fields[contextfabric.FactFieldTheme(theme)]; ok && v.Number != nil {
					shares[theme] = *v.Number
				}
			}
		}
		return shares, budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_membership"), before).statements
	}

	for _, tc := range []struct {
		org           string
		units         int
		secondTouches bool
	}{
		{"org-bound-at", 100000, false},
		{"org-bound-above", 100001, true},
	} {
		seed(tc.org, tc.units)
		first, _ := read(tc.org)
		second, secondStatements := read(tc.org)
		for _, shares := range []map[string]float64{first, second} {
			if shares[contextfabric.ThemeFeatureDelivery] != 0.5 || shares[contextfabric.ThemeOperational] != 0.5 {
				t.Fatalf("%s: shares = %v, want feature_delivery 0.5 and operational 0.5", tc.org, shares)
			}
			if shares[contextfabric.ThemeRisk] != 0 {
				t.Fatalf("%s: shares = %v: a work unit outside the run reached the mix", tc.org, shares)
			}
		}
		if tc.secondTouches && secondStatements == 0 {
			t.Errorf("%s: a run above the bound was remembered (second read touched no membership statement)", tc.org)
		}
		if !tc.secondTouches && secondStatements != 0 {
			t.Errorf("%s: a run at the bound was not remembered (second read touched membership %d times)", tc.org, secondStatements)
		}
	}
}

// A team owning 500 repositories, read for the current and the prior window,
// fills the statement's row ceiling exactly (500 x (2 windows + span row) =
// 1500 rows, ceiling 1501): one statement serves them all and the guard that
// fails a truncated read never fires.
func TestTeamThemeMixOver500RepositoriesFillsTheRowCeilingOfOneStatementAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	const orgID = "org-ceiling"
	const repos = 500
	at := ts(2026, 9, 10, 0, 0, 0)
	repoBatch, err := direct.PrepareBatch(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	ownBatch, err := direct.PrepareBatch(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)`)
	if err != nil {
		t.Fatal(err)
	}
	wuBatch, err := direct.PrepareBatch(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id)`)
	if err != nil {
		t.Fatal(err)
	}
	current, prior := at.Add(-5*24*time.Hour), at.Add(-45*24*time.Hour)
	for i := 0; i < repos; i++ {
		label := fmt.Sprintf("ceil-%03d", i)
		if err := repoBatch.Append(repoUUID(label), orgID, "acme/"+label, "github", at); err != nil {
			t.Fatal(err)
		}
		if err := ownBatch.Append(orgID, "github", "team-ceil", repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, at); err != nil {
			t.Fatal(err)
		}
		evidence := fmt.Sprintf(`{"issues":[],"prs":["%s#pr1"]}`, repoUUID(label))
		for k, from := range []time.Time{current, prior} {
			if err := wuBatch.Append(fmt.Sprintf("wu-%s-%d", label, k), from, from, nil, 2.0, map[string]float64{"operational": 1.0}, map[string]float64{}, evidence, at, orgID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, batch := range []interface{ Send() error }{repoBatch, ownBatch, wuBatch} {
		if err := batch.Send(); err != nil {
			t.Fatal(err)
		}
	}

	start, end := at.Add(-30*24*time.Hour), at
	before := budgetQueryLogTotals(t, ctx, direct, "work_unit_investments")
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}, Kind: contextfabric.FactInvestment,
		Subjects: []contextfabric.SubjectRef{teamSubject("team-ceil")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v\nserver: %s", err, lastServerException(ctx, direct))
	}
	statements := budgetDelta(budgetQueryLogTotals(t, ctx, direct, "work_unit_investments"), before).statements
	if statements != 1 {
		t.Fatalf("statements over work_unit_investments = %d, want 1 for %d repositories", statements, repos)
	}
	var found bool
	for _, fact := range result.Facts {
		current, ok := fact.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeOperational)]
		if !ok || current.Number == nil {
			continue
		}
		found = true
		priorShare := fact.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeOperational)]
		if *current.Number != 1.0 || priorShare.Number == nil || *priorShare.Number != 1.0 {
			t.Fatalf("operational share current=%v prior=%v, want 1.0 / 1.0: a window's rows were cut", current.Number, priorShare.Number)
		}
	}
	if !found {
		t.Fatalf("no team fact carries theme fields: %#v", result.Facts)
	}
}
