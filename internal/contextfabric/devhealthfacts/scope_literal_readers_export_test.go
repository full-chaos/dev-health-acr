package devhealthfacts

import (
	"context"
	"fmt"
	"strconv"

	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ScopeLiteralCase is one reader that carries the membership scope (or the
// project mix pin) as a bound JSON literal: SQL takes org_id, ids (IDs) and
// Bindings.
type ScopeLiteralCase struct {
	Name     string
	SQL      string
	IDs      []string
	Bindings []readers.Binding
}

// ScopeLiteralCasesForTest renders every scope-literal reader the way a
// current read sends it, with the given scope ids as the literal: the
// repository mix, the repository and organization unit listings, the owned
// unit count, and the six project mix statements carrying pin_json. The
// project statements take their pin and link from the provider's own phase 0.
func ScopeLiteralCasesForTest(ctx context.Context, provider contextfabric.FactProvider, orgID string, scopeIDs, repoIDs, projectIDs []string) ([]ScopeLiteralCase, error) {
	p, ok := provider.(*InvestmentProvider)
	if !ok {
		return nil, fmt.Errorf("provider %T is not the investment provider", provider)
	}
	bound := factTimeBound{}
	scope := membershipScope{mode: membershipScopeIDs, ids: scopeIDs}
	timeBindings := boundReaderBindings(bound)
	withScope := func(extra ...readers.Binding) []readers.Binding {
		out := append([]readers.Binding{}, timeBindings...)
		out = append(out, extra...)
		return append(out, scope.bindings()...)
	}
	cases := []ScopeLiteralCase{
		{Name: "repo_mix", SQL: repoMixStatementScoped([]factTimeBound{bound}, scope, repoMixRowLimit(len(repoIDs), 1)), IDs: repoIDs, Bindings: withScope()},
		{Name: "repo_units", SQL: investmentUnitsStatementScoped(bound, false, scope), IDs: repoIDs, Bindings: withScope(readers.Binding{Name: unitLimitParam, Value: strconv.Itoa(101)})},
		{Name: "org_units", SQL: investmentOrgUnitsStatement(bound, false, scope), IDs: []string{}, Bindings: withScope(readers.Binding{Name: unitLimitParam, Value: strconv.Itoa(101)})},
		{Name: "owned_unit_count", SQL: ownedUnitCountStatement(bound, scope), IDs: repoIDs, Bindings: withScope()},
	}

	projectScope, err := readProjectMixScope(ctx, p.facts.client, orgID, projectIDs, bound, p)
	if err != nil {
		return nil, fmt.Errorf("project mix phase 0: %w", err)
	}
	if len(projectScope.unitIDs) == 0 {
		return nil, fmt.Errorf("project mix phase 0 pinned no work unit")
	}
	linkJSON, err := readProjectMixLinks(ctx, p.facts.client, orgID, projectIDs, bound)
	if err != nil {
		return nil, fmt.Errorf("project mix links: %w", err)
	}
	pin := projectScope.bindings()
	link := readers.Binding{Name: "link_json", Value: linkJSON}
	bugfixKey := readers.Binding{Name: "bugfix_key", Value: readers.BugfixSubcategoryKey}
	for _, c := range []struct {
		name string
		sql  string
		with []readers.Binding
	}{
		{"project_rollup_repo_themes", projectRollupRepoThemesStatement(), []readers.Binding{link}},
		{"project_rollup_repo_bugfix", projectRollupRepoBugfixStatement(), []readers.Binding{link}},
		{"project_rollup_evidence_arm", projectRollupEvidenceArmStatement(), []readers.Binding{link}},
		{"project_native_placement", projectNativePlacementStatement(), nil},
		{"project_native_theme_values", projectNativeThemeValuesStatement(), nil},
		{"project_native_bugfix_values", projectNativeBugfixValuesStatement(), []readers.Binding{bugfixKey}},
	} {
		cases = append(cases, ScopeLiteralCase{Name: c.name, SQL: c.sql, IDs: projectIDs, Bindings: append(append([]readers.Binding{}, pin...), c.with...)})
	}
	return cases, nil
}
