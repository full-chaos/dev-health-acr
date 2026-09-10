package devhealthfacts

// CHAOS-5405 D-c step 5 -- the pushed-down authorization predicate agrees with
// the layer that OWNS repository authorization, in BOTH directions.
//
// WHY THIS EXISTS. The first version of this traversal bound the principal's
// repository scopes verbatim against an exact `IN` match. That silently
// disagreed with graphrank.ScopeMatch for every wildcard scope: a principal
// scoped `*` is UNRESTRICTED there, matched nothing here, and every row was
// masked — so a legitimately unrestricted caller was served an empty answer
// with no error and no disclosure. Not a refusal, not a gap: nothing.
//
// Found by codex r1, reproduced in both arms before the fix. The lesson is the
// standing one: a new evaluator CARRIES the decision of the layer that owns
// the concept, it does not re-derive it. So this test does not encode an
// opinion about what should authorize — it asks ScopeMatch, then asks the SQL
// predicate, and fails when they differ. If the ruling changes, ScopeMatch
// changes and this test follows it.
//
// The predicate is evaluated in Go here, mirroring the SQL arm for arm. That
// mirror is itself the risk, so it is kept to three lines that read the same
// order as the statement, and the real-ClickHouse arms next door execute the
// actual SQL.

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// sqlAuthorizes evaluates workItemAuthorizationExprSQL's three arms over the
// values actually bound, in the order the statement evaluates them.
func sqlAuthorizes(slugs, owners []string, repoSlug string) bool {
	if len(slugs) == 0 && len(owners) == 0 {
		return true
	}
	if repoSlug == "" {
		// The sentinels carry no slug and authorize only under the
		// unrestricted arm above.
		return false
	}
	lowered := strings.ToLower(repoSlug)
	for _, slug := range slugs {
		if slug == lowered {
			return true
		}
	}
	owner, _, _ := strings.Cut(lowered, "/")
	for _, candidate := range owners {
		if candidate == owner {
			return true
		}
	}
	return false
}

func TestChaos5405_ThePushedDownPredicateAgreesWithTheOwningLayer(t *testing.T) {
	t.Parallel()

	repositories := []string{
		"example-org/widget-service",
		"example-org/other-service",
		"Example-Org/Mixed-Case",
		"another-org/widget-service",
	}
	scopeShapes := [][]string{
		nil,                            // no scopes at all
		{},                             // an empty list
		{"example-org/widget-service"}, // one exact slug
		{"example-org/*"},              // an owner wildcard
		{"*"},                          // the global wildcard
		{"example-org/widget-service", "another-org/widget-service"}, // several exact
		{"example-org/*", "another-org/widget-service"},              // MIXED
		{"*", "example-org/widget-service"},                          // wildcard beside an exact
		{"EXAMPLE-ORG/WIDGET-SERVICE"},                               // case differs -- MUST admit on both sides
		{"nobody-org/nothing"},                                       // matches none of them
	}

	for _, scopes := range scopeShapes {
		scopes := scopes
		t.Run(strings.Join(append([]string{"scopes"}, scopes...), "_"), func(t *testing.T) {
			t.Parallel()
			principal := storage.Principal{OrgID: "org-1", RepositoryScopes: scopes}
			slugs, owners := authorizedRepositoryScopesFor(principal)
			for _, repo := range repositories {
				// The OWNING layer's answer, asked rather than assumed.
				owning := graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{},
					map[string]interface{}{"authorization_repositories": []string{repo}})
				pushed := sqlAuthorizes(slugs, owners, repo)
				if owning != pushed {
					t.Fatalf("scopes %v, repo %q: the owning layer says %v, the pushed-down predicate says %v (slugs=%v owners=%v) -- the two gates disagree, so a caller is either denied evidence it is entitled to or shown evidence it is not",
						scopes, repo, owning, pushed, slugs, owners)
				}
			}
			// The two sentinel populations carry no slug: they authorize only
			// when the principal is unrestricted, and that must hold on BOTH
			// sides too.
			sentinel := sqlAuthorizes(slugs, owners, "")
			if want := len(slugs) == 0 && len(owners) == 0; sentinel != want {
				t.Fatalf("scopes %v: a slug-less sentinel authorizes %v, want %v -- neither an authorized project nor an authorized team upgrades a repository-restricted principal", scopes, sentinel, want)
			}
		})
	}
}

// TestChaos5405_ControlTheAgreementTestCanFail is the discriminating control.
// A cross-layer agreement test that can never disagree proves nothing, so this
// asserts the comparison it makes DOES separate the two answers on a scope
// shape where a verbatim binding — the defect this replaced — would differ.
func TestChaos5405_ControlTheAgreementTestCanFail(t *testing.T) {
	t.Parallel()
	const repo = "example-org/widget-service"
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}

	owning := graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{},
		map[string]interface{}{"authorization_repositories": []string{repo}})
	if !owning {
		t.Fatalf("the owning layer denies %q for a global wildcard -- this control is built on a false premise", repo)
	}
	// The OLD behaviour: bind the scope strings verbatim and match exactly.
	if sqlAuthorizes([]string{"*"}, nil, repo) {
		t.Fatalf("a verbatim binding matched %q, so this control cannot tell the fixed predicate from the broken one", repo)
	}
	// And the CASE arm is discriminating too: a case-differing slug must be
	// admitted by the ruled rule and would NOT have been by a raw binding.
	if sqlAuthorizes([]string{"EXAMPLE-ORG/WIDGET-SERVICE"}, nil, repo) {
		t.Fatalf("a raw upper-case binding matched %q, so the case arm of this test proves nothing", repo)
	}
	if caseSlugs, caseOwners := authorizedRepositoryScopesFor(
		storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"EXAMPLE-ORG/WIDGET-SERVICE"}},
	); !sqlAuthorizes(caseSlugs, caseOwners, repo) {
		t.Fatalf("the ruled rule denies %q for a case-differing scope", repo)
	}
	// The NEW behaviour agrees with the owning layer.
	slugs, owners := authorizedRepositoryScopesFor(principal)
	if !sqlAuthorizes(slugs, owners, repo) {
		t.Fatalf("the fixed mapping denies %q for a global wildcard", repo)
	}
}
