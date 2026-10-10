package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// organization units columns: work unit, repository (” = unattributed), share,
// effort, theme map, from, to, prs, unresolved n, unresolved refs, scope total,
// scope rows, scope unattributed total, scope unattributed rows.
func organizationUnitsTable() []fakeTable {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	theme := map[string]float64{"feature_delivery": 1}
	return []fakeTable{{match: "scope_unattributed_rows", rows: [][]any{
		{"wu-b", repoUUID("org-units-a"), 4.0, 4.0, theme, from, from, []string{"7"}, uint64(0), []string{}, 9.0, uint64(3), 2.0, uint64(1)},
		{"wu-a", "", 2.0, 2.0, theme, from, from, []string{""}, uint64(1), []string{"unit:wu-a"}, 9.0, uint64(3), 2.0, uint64(1)},
		{"wu-c", repoUUID("org-units-a"), 3.0, 3.0, theme, from, from, []string{"8"}, uint64(0), []string{}, 9.0, uint64(3), 2.0, uint64(1)},
	}}}
}

func readOrganizationUnits(t *testing.T, client *fakeClient, principal storage.Principal, cursor *contextfabric.InvestmentUnitsCursor, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	ctx := contextfabric.WithInvestmentUnits(context.Background(), contextfabric.InvestmentUnitsRequest{Max: 2, Cursor: cursor})
	result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

func unitFactsOf(result contextfabric.FactProviderResult, kind string) []contextfabric.CanonicalFact {
	var out []contextfabric.CanonicalFact
	for _, fact := range result.Facts {
		if value, ok := fact.Fields["unit_kind"]; ok && value.String != nil && *value.String == kind {
			out = append(out, fact)
		}
	}
	return out
}

func TestInvestmentOrganizationServesItsUnitsWithUnattributedRowsMarked(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationUnitsTable()}
	result := readOrganizationUnits(t, client, storage.Principal{OrgID: "org-1"}, nil, organizationSubject("org-1"))
	pages := unitFactsOf(result, contextfabric.InvestmentUnitPageKind)
	rows := unitFactsOf(result, contextfabric.InvestmentUnitKind)
	if len(pages) != 1 || len(rows) != 2 {
		t.Fatalf("page facts %d, unit rows %d, want 1 and 2 (page size 2 of 3 rows): %#v", len(pages), len(rows), result.Facts)
	}
	page := pages[0].Fields
	if got := *page["scope_unattributed_rows"].Integer; got != 1 {
		t.Errorf("scope_unattributed_rows = %d, want 1", got)
	}
	if got := *page["scope_unattributed_total"].Number; got != 2.0 {
		t.Errorf("scope_unattributed_total = %v, want 2", got)
	}
	if page["next_cursor"].String == nil {
		t.Fatal("a page that stopped before the last row serves no next_cursor")
	}
	cursor, err := contextfabric.DecodeInvestmentUnitsCursor(*page["next_cursor"].String)
	if err != nil || cursor.WorkUnitID != "wu-a" || cursor.RepoID != contextfabric.InvestmentUnitsUnattributedRepo {
		t.Fatalf("next_cursor = %+v, %v, want the unattributed row wu-a with the unattributed word", cursor, err)
	}
	var unattributed *contextfabric.CanonicalFact
	for i := range rows {
		if _, has := rows[i].Fields["repository_id"]; !has {
			unattributed = &rows[i]
		}
	}
	if unattributed == nil {
		t.Fatalf("no unit row without a repository_id: %#v", rows)
	}
	if got := *unattributed.Fields["unit_attribution_basis"].String; got != contextfabric.InvestmentUnitAttributionUnattributed {
		t.Errorf("unit_attribution_basis = %q, want %q", got, contextfabric.InvestmentUnitAttributionUnattributed)
	}
	if got := *unattributed.Fields["share_in_scope"].Number; got != 2.0 {
		t.Errorf("share_in_scope = %v, want 2", got)
	}
	for _, ref := range unattributed.EvidenceRefIDs {
		if strings.Contains(ref, "repository") || strings.Contains(ref, "pull-request") {
			t.Errorf("an unattributed row cites %q: it reaches no repository", ref)
		}
	}
	if !hasStatementWith(client, "scope_unattributed_rows") {
		t.Error("the units statement was not issued")
	}
	for _, q := range client.queries {
		if strings.Contains(q.statement, "scope_unattributed_rows") && strings.Contains(q.statement, "{ids:Array(String)}") {
			t.Error("the organization listing filters on a repository id set")
		}
	}
}

func hasStatementWith(client *fakeClient, text string) bool {
	for _, q := range client.queries {
		if strings.Contains(q.statement, text) {
			return true
		}
	}
	return false
}

func TestInvestmentOrganizationUnitsResumeAfterAnUnattributedRow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: organizationUnitsTable()}
	cursor := &contextfabric.InvestmentUnitsCursor{Share: 2, WorkUnitID: "wu-a", RepoID: contextfabric.InvestmentUnitsUnattributedRepo}
	readOrganizationUnits(t, client, storage.Principal{OrgID: "org-1"}, cursor, organizationSubject("org-1"))
	var repoBound, found any
	for _, q := range client.queries {
		if !strings.Contains(q.statement, "scope_unattributed_rows") {
			continue
		}
		found = true
		for _, b := range q.bindings {
			if b.Name == "unit_cursor_repo" {
				repoBound = b.Value
			}
		}
	}
	if found == nil || repoBound != "" {
		t.Fatalf("unit_cursor_repo = %#v (statement issued %v), want the empty repository the unattributed row sorts under", repoBound, found)
	}
}

func TestInvestmentOrganizationUnitsAreNotServedForARepositoryBoundCallerOrAnotherOrganization(t *testing.T) {
	t.Parallel()
	bound := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{repoUUID("org-units-a")}}
	client := &fakeClient{tables: organizationUnitsTable()}
	result := readOrganizationUnits(t, client, bound, nil, organizationSubject("org-1"))
	if len(unitFactsOf(result, contextfabric.InvestmentUnitKind))+len(unitFactsOf(result, contextfabric.InvestmentUnitPageKind)) != 0 {
		t.Errorf("a repository-bound caller got organization unit facts: %#v", result.Facts)
	}
	if hasStatementWith(client, "scope_unattributed_rows") {
		t.Error("a repository-bound caller's organization units statement was issued")
	}
	other := &fakeClient{tables: organizationUnitsTable()}
	result = readOrganizationUnits(t, other, storage.Principal{OrgID: "org-1"}, nil, organizationSubject("org-2"))
	if len(unitFactsOf(result, contextfabric.InvestmentUnitKind)) != 0 || hasStatementWith(other, "scope_unattributed_rows") {
		t.Errorf("another organization's units were read: %#v", result.Facts)
	}
}
