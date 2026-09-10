package devhealthfacts_test

// CHAOS-5405 -- the work-item traversal itself: one relation, an authorized
// census, a bounded selection.
//
// WHAT THESE ARE ORACLES FOR. ExpandFactScope implements six policies and
// refuses everything else with "does not implement policy"
// (chaos4099_scope_expander.go:78-250). D-a adds the fourteen work-item
// policies with FOUR properties that a naive port of the repository hop would
// not have:
//
//   1. ONE statement per (requirement, origin) traversal supplies BOTH the
//      census and the selected targets. "A count and page obtained from
//      unrelated observations cannot constitute a complete census."
//   2. Authorization is applied INSIDE the selection relation, before content
//      projection -- not as a Go filter over rows the query already returned.
//   3. The zero-UUID repo-less population is a first-class target here (unlike
//      the repository hop, where it is a missing next hop): admitted for an
//      organization-wide principal, denied for a repository-restricted one,
//      and never turned into a fake repository.
//   4. 201 rows are read so the 201st proves truncation; the resolver, not the
//      expander, trims to 200 -- so the expander returns ALL of them.
//
// THE SELECTION RELATION'S ROW SHAPE, pinned here because it is a contract
// between this query and its scanner, is six columns:
//
//	repo_id, work_item_id, repo_slug, origin_id, attribution_source, authorized_population
//
// where authorized_population is the census -- a window aggregate over the
// SAME relation, identical on every row, so a count and a page can never come
// from two observations.
//
// RED-FIRST at 0945a53dfdad0e84ba8244c59e8e9d85a7d195f2: every work-item
// policy errors "does not implement policy". The last two tests are NEGATIVE
// CONTROLS and pass at the parent.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	chaos5405OrgID        = "org-1"
	chaos5405RepoID       = "11111111-1111-1111-1111-111111111111"
	chaos5405RepoSlug     = "example-org/widget-service"
	chaos5405ZeroRepoID   = "00000000-0000-0000-0000-000000000000"
	chaos5405OrphanRepoID = "22222222-2222-2222-2222-222222222222"
	chaos5405ProjectID    = "PROJ-1"
	// chaos5405ProjectProvider and chaos5405ProjectOriginKey mirror what the
	// project statement actually selects as origin_id: concat(p.provider, ':',
	// p.id), not the bare id (codex r2 F4). A fake that returned the bare id
	// would make every candidate's originRoot lookup miss in the fixtures
	// while succeeding in production -- a fake modelling a query the code no
	// longer sends.
	chaos5405ProjectProvider  = "linear"
	chaos5405ProjectOriginKey = chaos5405ProjectProvider + ":" + chaos5405ProjectID
	chaos5405TeamID           = "TEAM-1"
	// chaos5405SelectionMatch is the relation every work-item traversal
	// selects from. Both origin kinds land on work_items -- the project arm
	// joins projects, the team arm joins work_item_team_attributions, but the
	// TARGET rows are work items either way.
	chaos5405SelectionMatch = "work_items"
)

func chaos5405ProjectOrigin(t *testing.T) contextfabric.SubjectRef {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{chaos5405ProjectProvider, chaos5405ProjectID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project canonical id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID, Label: "Titan"}
}

func chaos5405TeamOrigin() contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + chaos5405TeamID, Label: "Platform"}
}

// chaos5405Policies is the fourteen-pair matrix as (policy, requirement,
// origin) triples, named as strings so this file compiles at the parent.
func chaos5405Policies(t *testing.T) []struct {
	policy contextfabric.FactScopePolicy
	kind   contextfabric.FactKind
	origin contextfabric.SubjectRef
} {
	t.Helper()
	project := chaos5405ProjectOrigin(t)
	team := chaos5405TeamOrigin()
	type triple = struct {
		policy contextfabric.FactScopePolicy
		kind   contextfabric.FactKind
		origin contextfabric.SubjectRef
	}
	return []triple{
		{"project_work_item_status_v1", contextfabric.FactStatus, project},
		{"project_work_item_work_v1", contextfabric.FactWork, project},
		{"project_work_item_actual_completion_v1", contextfabric.FactActualCompletion, project},
		{"project_work_item_blockers_v1", contextfabric.FactBlockers, project},
		{"project_work_item_required_children_v1", contextfabric.FactRequiredChildren, project},
		{"project_work_item_identity_v1", contextfabric.FactIdentity, project},
		{"project_work_item_membership_v1", contextfabric.FactMembership, project},
		{"team_primary_attribution_work_item_status_v1", contextfabric.FactStatus, team},
		{"team_primary_attribution_work_item_work_v1", contextfabric.FactWork, team},
		{"team_primary_attribution_work_item_actual_completion_v1", contextfabric.FactActualCompletion, team},
		{"team_primary_attribution_work_item_blockers_v1", contextfabric.FactBlockers, team},
		{"team_primary_attribution_work_item_required_children_v1", contextfabric.FactRequiredChildren, team},
		{"team_primary_attribution_work_item_identity_v1", contextfabric.FactIdentity, team},
		{"team_primary_attribution_work_item_membership_v1", contextfabric.FactMembership, team},
	}
}

// chaos5405Census is the FIVE window aggregates every selection row carries,
// named rather than positional.
//
// They were five positional uint64 arguments until codex r2 F2 added the
// fifth. Five same-typed numbers in a row is a fixture that compiles whatever
// order it is written in, and a silently transposed pair would make a test
// assert the wrong census while still passing -- the exact defect class this
// battery exists to catch, reproduced in the fixtures instead of the code.
//
// Every field is a count over the WHOLE relation, never over the returned
// page. A fixture that sets Scoped: 5000 beside a single row is not
// inconsistent; it is the normal case, and the reason these cannot be derived
// from the rows the fake hands back.
type chaos5405Census struct {
	// Scoped is the population before authorization: count() OVER ().
	Scoped uint64
	// Authorized is the caller-visible population: countIf(authorized = 1).
	Authorized uint64
	// RepoLess is the repo-less CANDIDATE population, authorized or not:
	// countIf(repo_less = 1). Added for codex r2 F2 -- see
	// workItemScopeProjection for why counting these in Go was wrong.
	RepoLess uint64
	// RepoLessDenied is its denied subset: countIf(repo_less = 1 AND
	// authorized = 0).
	RepoLessDenied uint64
	// Orphaned is countIf(orphaned = 1).
	Orphaned uint64
}

func (c chaos5405Census) columns() []any {
	return []any{c.Scoped, c.Authorized, c.RepoLess, c.RepoLessDenied, c.Orphaned}
}

// chaos5405Row builds one selection row in the TWELVE-column shape
// workItemScopeSelectionColumns documents: five masked identity columns, the
// two per-row flags, then the five window aggregates.
//
// The helper takes the AUTHORIZED case; chaos5405MaskedRow builds the denied
// one, where every identity column is ” exactly as the SQL projection makes
// it -- a test that filled those in would be testing a query this code never
// sends.
func chaos5405Row(repoID, workItemID, repoSlug, originID, source string, census chaos5405Census) []any {
	repoLess := uint8(0)
	if repoID == chaos5405ZeroRepoID {
		repoLess = 1
	}
	return append([]any{repoID, workItemID, repoSlug, originID, source, uint8(1), repoLess}, census.columns()...)
}

// chaos5405MaskedRow is a DENIED row as the projection actually returns it:
// present, counted, and carrying no identity whatsoever.
func chaos5405MaskedRow(repoLess bool, census chaos5405Census) []any {
	flag := uint8(0)
	if repoLess {
		flag = 1
	}
	return append([]any{"", "", "", "", "", uint8(0), flag}, census.columns()...)
}

func chaos5405Expand(t *testing.T, client *fakeClient, principal storage.Principal, policy contextfabric.FactScopePolicy, kind contextfabric.FactKind, origin contextfabric.SubjectRef, limit int) (contextfabric.FactScopeExpansionResult, error) {
	t.Helper()
	return devhealthfacts.NewScopeExpander(client).ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
		Principal:       principal,
		RequirementKind: kind,
		Origins:         []contextfabric.SubjectRef{origin},
		Policy:          policy,
		TargetKind:      contextfabric.SubjectWorkItem,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Limit:           limit,
	})
}

func orgWidePrincipal() storage.Principal {
	return storage.Principal{OrgID: chaos5405OrgID}
}

func repositoryRestrictedPrincipal() storage.Principal {
	return storage.Principal{OrgID: chaos5405OrgID, RepositoryScopes: []string{chaos5405RepoSlug}}
}

// TestChaos5405_EveryWorkItemPolicyIsImplemented is the activation half on the
// expander side: a policy the table enables but the expander refuses fails
// CLOSED to `failed`, which is a differently-shaped hollow answer, not a fix.
func TestChaos5405_EveryWorkItemPolicyIsImplemented(t *testing.T) {
	t.Parallel()
	for _, entry := range chaos5405Policies(t) {
		entry := entry
		t.Run(string(entry.policy), func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
				chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 1, Authorized: 1}),
			}}}}
			result, err := chaos5405Expand(t, client, orgWidePrincipal(), entry.policy, entry.kind, entry.origin, 200)
			if err != nil {
				t.Fatalf("ExpandFactScope(%s) error = %v", entry.policy, err)
			}
			if len(result.Targets) != 1 {
				t.Fatalf("targets = %d, want 1", len(result.Targets))
			}
			if result.Targets[0].Kind != contextfabric.SubjectWorkItem {
				t.Fatalf("target kind = %q, want %q", result.Targets[0].Kind, contextfabric.SubjectWorkItem)
			}
			want, omitted, deriveErr := identity.Derive(identity.KindWorkItem, []string{chaos5405RepoID, "WIDGET-101"}, nil)
			if deriveErr != nil || omitted {
				t.Fatalf("derive expected work item id: err=%v omitted=%v", deriveErr, omitted)
			}
			if result.Targets[0].CanonicalID != want {
				t.Fatalf("target canonical id = %q, want %q -- the same codec v2Index decodes with", result.Targets[0].CanonicalID, want)
			}
		})
	}
}

// TestChaos5405_OneRelationSuppliesBothTheCensusAndThePage is D-a's
// pushed-down-census rule. Two statements would be two observations, and a
// count from one cannot describe the page from the other.
func TestChaos5405_OneRelationSuppliesBothTheCensusAndThePage(t *testing.T) {
	t.Parallel()
	for _, entry := range chaos5405Policies(t) {
		entry := entry
		t.Run(string(entry.policy), func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
				chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 5000, Authorized: 5000}),
			}}}}
			if _, err := chaos5405Expand(t, client, orgWidePrincipal(), entry.policy, entry.kind, entry.origin, 200); err != nil {
				t.Fatalf("ExpandFactScope error = %v", err)
			}
			if len(client.queries) != 1 {
				statements := make([]string, 0, len(client.queries))
				for _, query := range client.queries {
					statements = append(statements, query.statement)
				}
				t.Fatalf("executed %d statements, want exactly 1 -- census and page must come from one relation:\n%s", len(client.queries), strings.Join(statements, "\n---\n"))
			}
			statement := client.queries[0].statement
			if !strings.Contains(statement, "LIMIT "+strconv.Itoa(201)) {
				t.Fatalf("statement = %q, want LIMIT 201 -- the 201st row is the only overflow evidence", statement)
			}
			if !strings.Contains(statement, "OVER ()") {
				t.Fatalf("statement = %q, want a window aggregate over the selection relation carrying the authorized population", statement)
			}
		})
	}
}

// TestChaos5405_AuthorizationIsBoundInsideTheSelectionRelation is D-c step 5.
// Filtering in Go after the fact means unauthorized content was already
// projected and crossed the boundary.
func TestChaos5405_AuthorizationIsBoundInsideTheSelectionRelation(t *testing.T) {
	t.Parallel()
	for _, entry := range chaos5405Policies(t) {
		entry := entry
		t.Run(string(entry.policy), func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
				chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 1, Authorized: 1}),
			}}}}
			if _, err := chaos5405Expand(t, client, repositoryRestrictedPrincipal(), entry.policy, entry.kind, entry.origin, 200); err != nil {
				t.Fatalf("ExpandFactScope error = %v", err)
			}
			if len(client.queries) == 0 {
				t.Fatalf("no statement executed")
			}
			var bound bool
			for _, binding := range client.queries[0].bindings {
				if binding.Name == "authorized_repository_slugs" {
					bound = true
				}
			}
			if !bound {
				t.Fatalf("statement bindings = %#v, want the principal's repository scope bound INTO the selection relation", client.queries[0].bindings)
			}
		})
	}
}

// TestChaos5405_TheRepoLessPopulationIsAdmittedOnlyOrganizationWide is D-c's
// sentinel rule, and the point on which this traversal differs most from the
// repository hop: here a zero-UUID work item IS a real target.
func TestChaos5405_TheRepoLessPopulationIsAdmittedOnlyOrganizationWide(t *testing.T) {
	t.Parallel()
	rows := [][]any{
		chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 3, Authorized: 3, RepoLess: 1, Orphaned: 1}),
		chaos5405Row(chaos5405ZeroRepoID, "linear:CHAOS-9001", "", chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 3, Authorized: 3, RepoLess: 1, Orphaned: 1}),
		chaos5405Row(chaos5405OrphanRepoID, "WIDGET-404", "", chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 3, Authorized: 3, RepoLess: 1, Orphaned: 1}),
	}
	t.Run("organization_wide_admits_the_repo_less_item", func(t *testing.T) {
		t.Parallel()
		client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: rows}}}
		result, err := chaos5405Expand(t, client, orgWidePrincipal(), "project_work_item_status_v1", contextfabric.FactStatus, chaos5405ProjectOrigin(t), 200)
		if err != nil {
			t.Fatalf("ExpandFactScope error = %v", err)
		}
		// THREE, not two. An earlier version of this pin asserted the orphan
		// is not a target; that was WRONG and it was mine. The repository
		// policies must refuse an orphaned repo_id because there is no
		// repository ENTITY to admit -- these fourteen stop AT the work item,
		// which plainly exists. Admitting it manufactures no repository,
		// which is the thing D-c actually forbids, and D-c's outcome table
		// treats both sentinels identically for AUTHORIZATION without ever
		// saying an orphan is not a target.
		if len(result.Targets) != 3 {
			t.Fatalf("targets = %d, want 3 (repo-backed + repo-less + orphaned; an orphaned work item is still a work item)", len(result.Targets))
		}
		repoLess, omitted, deriveErr := identity.Derive(identity.KindWorkItem, []string{chaos5405ZeroRepoID, "linear:CHAOS-9001"}, nil)
		if deriveErr != nil || omitted {
			t.Fatalf("derive repo-less id: err=%v omitted=%v", deriveErr, omitted)
		}
		var found bool
		for _, target := range result.Targets {
			if target.CanonicalID == repoLess {
				found = true
			}
		}
		if !found {
			t.Fatalf("the repo-less work item was dropped for an organization-wide principal: %#v", result.Targets)
		}
	})
	t.Run("repository_restricted_denies_the_repo_less_item", func(t *testing.T) {
		t.Parallel()
		// The repo-less and orphaned rows come back MASKED for a restricted
		// principal -- present so the census still counts them, carrying no
		// identity. That is the projection-mask design (D-c step 5), and a
		// fixture that returned them unmasked would be testing a query this
		// code does not send.
		restricted := [][]any{
			chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 3, Authorized: 1, RepoLess: 1, RepoLessDenied: 1, Orphaned: 1}),
			chaos5405MaskedRow(true, chaos5405Census{Scoped: 3, Authorized: 1, RepoLess: 1, RepoLessDenied: 1, Orphaned: 1}),
			chaos5405MaskedRow(false, chaos5405Census{Scoped: 3, Authorized: 1, RepoLess: 1, RepoLessDenied: 1, Orphaned: 1}),
		}
		client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: restricted}}}
		result, err := chaos5405Expand(t, client, repositoryRestrictedPrincipal(), "project_work_item_status_v1", contextfabric.FactStatus, chaos5405ProjectOrigin(t), 200)
		if err != nil {
			t.Fatalf("ExpandFactScope error = %v", err)
		}
		for _, target := range result.Targets {
			if strings.Contains(target.CanonicalID, chaos5405ZeroRepoID) {
				t.Fatalf("a repository-restricted principal reached the repo-less population: %q -- neither an authorized project nor an authorized team upgrades a restricted principal", target.CanonicalID)
			}
		}
		if result.Counts.AuthorizationDroppedCount == 0 {
			t.Fatalf("AuthorizationDroppedCount = 0, want the repo-less denial counted")
		}
	})
}

// TestChaos5405_TheOverflowRowIsReturnedNotTrimmed pins D-a's division of
// labour: the expander reads Limit+1 and returns ALL of them so the resolver
// can enforce the cap from the overflow row itself, exactly as
// projectRepositories already does for the repository hop.
func TestChaos5405_TheOverflowRowIsReturnedNotTrimmed(t *testing.T) {
	t.Parallel()
	build := func(n int) [][]any {
		rows := make([][]any, n)
		for i := 0; i < n; i++ {
			rows[i] = chaos5405Row(chaos5405RepoID, "WIDGET-"+strconv.Itoa(i), chaos5405RepoSlug, chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: uint64(n), Authorized: uint64(n)})
		}
		return rows
	}
	t.Run("exactly_the_limit_is_not_truncated", func(t *testing.T) {
		t.Parallel()
		client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: build(200)}}}
		result, err := chaos5405Expand(t, client, orgWidePrincipal(), "project_work_item_status_v1", contextfabric.FactStatus, chaos5405ProjectOrigin(t), 200)
		if err != nil {
			t.Fatalf("ExpandFactScope error = %v", err)
		}
		if result.Counts.Truncated {
			t.Fatalf("Truncated = true on an exactly-full page -- 200 targets with no overflow row is COMPLETE")
		}
		if len(result.Targets) != 200 {
			t.Fatalf("targets = %d, want 200", len(result.Targets))
		}
	})
	t.Run("the_overflow_row_reaches_the_resolver", func(t *testing.T) {
		t.Parallel()
		client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: build(201)}}}
		result, err := chaos5405Expand(t, client, orgWidePrincipal(), "project_work_item_status_v1", contextfabric.FactStatus, chaos5405ProjectOrigin(t), 200)
		if err != nil {
			t.Fatalf("ExpandFactScope error = %v", err)
		}
		if !result.Counts.Truncated {
			t.Fatalf("Truncated = false with a 201st row present")
		}
		if len(result.Targets) != 201 {
			t.Fatalf("targets = %d, want 201 -- the expander must NOT pre-trim; the resolver enforces the cap from the overflow row", len(result.Targets))
		}
	})
}

// TestChaos5405_AFullyDeniedCensusDisclosesACountAndNothingElse is D-c's
// narrowly widened existence exception: for the all-dropped case the caller
// learns HOW MANY work items exist, never WHICH.
func TestChaos5405_AFullyDeniedCensusDisclosesACountAndNothingElse(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405MaskedRow(true, chaos5405Census{Scoped: 2, RepoLess: 2, RepoLessDenied: 2}),
		chaos5405MaskedRow(true, chaos5405Census{Scoped: 2, RepoLess: 2, RepoLessDenied: 2}),
	}}}}
	result, err := chaos5405Expand(t, client, repositoryRestrictedPrincipal(), "project_work_item_status_v1", contextfabric.FactStatus, chaos5405ProjectOrigin(t), 200)
	if err != nil {
		t.Fatalf("ExpandFactScope error = %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %d, want 0 -- every candidate was denied", len(result.Targets))
	}
	if result.Counts.CandidateCount != 2 {
		t.Fatalf("CandidateCount = %d, want 2 -- a completed census is what separates matched_unauthorized from attempted_empty", result.Counts.CandidateCount)
	}
	if result.Counts.AuthorizationDroppedCount != 2 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 2", result.Counts.AuthorizationDroppedCount)
	}
	for key := range result.TargetBasis {
		t.Fatalf("a denied target leaked an identity into TargetBasis: %q", key)
	}
	for key := range result.TargetAttributionSource {
		t.Fatalf("a denied target leaked an identity into TargetAttributionSource: %q", key)
	}
}

// ---------------------------------------------------------------------------
// Negative controls -- both pass at the parent
// ---------------------------------------------------------------------------

// TestChaos5405_ControlAnUnknownPolicyStillErrors proves the refusal path this
// file's first test relies on is real, and stays real after fourteen policies
// join it.
func TestChaos5405_ControlAnUnknownPolicyStillErrors(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	_, err := chaos5405Expand(t, client, orgWidePrincipal(), "definitely_not_a_policy_v1", contextfabric.FactStatus, chaos5405ProjectOrigin(t), 200)
	if err == nil {
		t.Fatalf("ExpandFactScope accepted an unknown policy")
	}
	if !strings.Contains(err.Error(), "does not implement policy") {
		t.Fatalf("err = %v, want the unimplemented-policy refusal", err)
	}
}

// TestChaos5405_ControlTheRepositoryHopStillRefusesTheSentinels is the
// boundary in the other direction: the repository policies must keep treating
// a zero-UUID row as a MISSING NEXT HOP, never as a target, even now that the
// work-item policies treat the same population as a first-class one.
func TestChaos5405_ControlTheRepositoryHopStillRefusesTheSentinels(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_items", rows: [][]any{
		{chaos5405ZeroRepoID, "", chaos5405ProjectID},
		{chaos5405OrphanRepoID, "", chaos5405ProjectID},
	}}}}
	result, err := devhealthfacts.NewScopeExpander(client).ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
		Principal:       orgWidePrincipal(),
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos5405ProjectOrigin(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Limit:           200,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope error = %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %#v, want none -- neither sentinel may become a repository", result.Targets)
	}
	if result.Counts.MissingNextHopCount != 2 {
		t.Fatalf("MissingNextHopCount = %d, want 2", result.Counts.MissingNextHopCount)
	}
}

// TestChaos5405_TheExpandersOwnAxisRefusalCarriesTheTypedSentinel closes a gap
// the mutation battery found: the arm that strips `%w` from the expander's
// axis refusal SURVIVED.
//
// The resolver-side test asserts classifyFactScopeFailure against a HAND-BUILT
// joined error, which proves the classifier reads the sentinel and proves
// nothing about whether the expander still attaches it. That is the whole
// value of the second gate — if the first one ever stops holding, the failure
// must still read axis_unsupported rather than blaming infrastructure — so it
// is asserted here on the error the expander itself returns.
func TestChaos5405_TheExpandersOwnAxisRefusalCarriesTheTypedSentinel(t *testing.T) {
	t.Parallel()
	for _, axis := range []contractsv1.ContextFabricTemporalAxis{
		contractsv1.ContextFabricTemporalValidTime,
		contractsv1.ContextFabricTemporalObservedTime,
		contractsv1.ContextFabricTemporalRange,
	} {
		axis := axis
		t.Run(string(axis), func(t *testing.T) {
			t.Parallel()
			_, err := devhealthfacts.NewScopeExpander(&fakeClient{}).ExpandFactScope(
				context.Background(), contextfabric.FactScopeExpansionRequest{
					Principal:       orgWidePrincipal(),
					RequirementKind: contextfabric.FactStatus,
					Origins:         []contextfabric.SubjectRef{chaos5405ProjectOrigin(t)},
					Policy:          contextfabric.FactScopePolicyProjectWorkItemStatus,
					TargetKind:      contextfabric.SubjectWorkItem,
					TimeContext:     contextfabric.TimeContext{Axis: axis},
					Limit:           10,
				})
			if err == nil {
				t.Fatalf("%s: the expander returned no error -- its own boundary check is the second gate and must refuse", axis)
			}
			if !errors.Is(err, contextfabric.ErrFactScopeAxisUnsupported) {
				t.Fatalf("%s: error %v does not wrap ErrFactScopeAxisUnsupported -- an unwrapped refusal classifies as a graph-backend fault and pages an operator for a caller's historical question", axis, err)
			}
		})
	}

	// CONTROL: the current axis is NOT refused here, so the assertions above
	// are about the axis rather than about this policy erroring for any reason.
	client := &fakeClient{}
	if _, err := chaos5405Expand(t, client, orgWidePrincipal(),
		contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus,
		chaos5405ProjectOrigin(t), 10); err != nil && errors.Is(err, contextfabric.ErrFactScopeAxisUnsupported) {
		t.Fatalf("the CURRENT axis was refused as unsupported: %v", err)
	}
}

// TestChaos5405_AnUnrecognisedAttributionSourceIsRefusedNotRelabelled closes a
// gap the mutation battery found: the arm that replaces the `default:` refusal
// with `candidate.basis = attributed_primary_team` SURVIVED.
//
// The refusal was COUNTED and never ASSERTED, so admitting an unknown source
// under a label it never earned changed no test. That inverts the reason the
// per-target basis exists: a reader is supposed to be able to tell asserted
// membership from inferred membership, and a source nobody recognises is
// neither. It must be dropped and counted, never relabelled as the weaker of
// the two known answers.
func TestChaos5405_AnUnrecognisedAttributionSourceIsRefusedNotRelabelled(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405TeamID, "some_source_nobody_ruled_on", chaos5405Census{Scoped: 1, Authorized: 1}),
	}}}}
	result, err := chaos5405Expand(t, client, orgWidePrincipal(),
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus,
		contextfabric.FactStatus, chaos5405TeamOrigin(), 200)
	if err != nil {
		t.Fatalf("ExpandFactScope error = %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none -- a work item reached by a source outside the closed vocabulary is not admissible under either basis", result.Targets)
	}
	if result.Counts.UnknownAttributionSourceCount != 1 {
		t.Fatalf("unknown_attribution_source_count = %d, want 1 -- the refusal must be counted as well as made, or it is invisible", result.Counts.UnknownAttributionSourceCount)
	}
	for id, basis := range result.TargetBasis {
		t.Fatalf("target %q was given basis %q -- an unrecognised source was relabelled rather than refused", id, basis)
	}

	// CONTROL: the SAME row with a RULED source IS admitted, so the refusal
	// above is attributable to the source and to nothing else about this
	// fixture.
	ok := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405TeamID, "native_team", chaos5405Census{Scoped: 1, Authorized: 1}),
	}}}}
	control, err := chaos5405Expand(t, ok, orgWidePrincipal(),
		contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus,
		contextfabric.FactStatus, chaos5405TeamOrigin(), 200)
	if err != nil {
		t.Fatalf("control ExpandFactScope error = %v", err)
	}
	if len(control.Targets) != 1 {
		t.Fatalf("control admitted %d targets, want 1 -- without this the refusal above proves nothing about the source", len(control.Targets))
	}
	if control.Counts.UnknownAttributionSourceCount != 0 {
		t.Fatalf("control counted %d unknown sources, want 0", control.Counts.UnknownAttributionSourceCount)
	}
}

// TestChaos5405_ASuccessfulEmptySelectionIsAMeasuredZero is codex r1's first
// P1, reproduced before it was fixed and kept as the pin.
//
// `CensusComplete` was set inside the row loop, so a query that SUCCEEDED and
// returned zero rows never set it: the served census reported
// `population_measured: false` with a null count for a population that had in
// fact been measured and was genuinely zero. That is the ONE distinction D-d
// exists to carry, reported backwards in the commonest empty case — and it
// survived a 31-arm battery because every census fixture in this suite had at
// least one row.
//
// The census completed because the QUERY completed. A relation with no rows
// has a scoped population of zero and an authorized population of zero, and
// both are measurements.
func TestChaos5405_ASuccessfulEmptySelectionIsAMeasuredZero(t *testing.T) {
	t.Parallel()
	for _, entry := range chaos5405Policies(t) {
		entry := entry
		t.Run(string(entry.policy), func(t *testing.T) {
			t.Parallel()
			// No tables: the fake answers with a SUCCESSFUL, empty result,
			// which is what an org with no matching work items produces.
			client := &fakeClient{}
			result, err := chaos5405Expand(t, client, orgWidePrincipal(), entry.policy, entry.kind, entry.origin, 200)
			if err != nil {
				t.Fatalf("ExpandFactScope error = %v", err)
			}
			if result.Counts.ScopeQueryCount != 1 {
				t.Fatalf("scope_query_count = %d, want 1 -- this fixture must actually run a query, or it proves nothing about a successful empty one", result.Counts.ScopeQueryCount)
			}
			if result.Counts.ScopeRowsReturned != 0 {
				t.Fatalf("scope_rows_returned = %d, want 0", result.Counts.ScopeRowsReturned)
			}
			if !result.Counts.CensusComplete {
				t.Fatalf("census_complete = false on a SUCCESSFUL empty selection -- a measured zero is being served as an unmeasured population, which inverts the distinction the census exists to carry")
			}
			if result.Counts.AuthorizedCount != 0 {
				t.Fatalf("authorized_count = %d, want a measured 0", result.Counts.AuthorizedCount)
			}
		})
	}

	// CONTROL, in the other direction: a FAILED query must NOT claim a
	// completed census. Without this the fix above could be "always true",
	// which would be the same defect pointing the other way.
	failing := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, err: errors.New("clickhouse unavailable")}}}
	result, err := chaos5405Expand(t, failing, orgWidePrincipal(),
		contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus,
		chaos5405ProjectOrigin(t), 200)
	if err == nil {
		t.Fatalf("the failing client returned no error, so this control asserts nothing")
	}
	if result.Counts.CensusComplete {
		t.Fatalf("census_complete = true after a FAILED query -- 'we counted' must never be claimed for a traversal that did not finish")
	}
}

// TestChaos5405_ADeniedRepoLessRowIsStillACandidate is codex r1's second P1,
// reproduced before it was fixed and kept as the pin.
//
// `CandidateCount` is taken from the census, which is a PRE-authorization
// population. `RepoLessCandidateCount` was incremented after the masked-row
// `continue`, so it described a POST-authorization one: two counters reporting
// the same population disagreed, and the repo-less one under-reported exactly
// the denied rows the count-only disclosure exists to surface.
func TestChaos5405_ADeniedRepoLessRowIsStillACandidate(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405MaskedRow(true, chaos5405Census{Scoped: 1, RepoLess: 1, RepoLessDenied: 1}),
	}}}}
	result, err := chaos5405Expand(t, client, repositoryRestrictedPrincipal(),
		contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus,
		chaos5405ProjectOrigin(t), 200)
	if err != nil {
		t.Fatalf("ExpandFactScope error = %v", err)
	}
	if result.Counts.CandidateCount != 1 {
		t.Fatalf("candidate_count = %d, want 1 -- the denied row is still a candidate", result.Counts.CandidateCount)
	}
	if result.Counts.RepoLessCandidateCount != 1 {
		t.Fatalf("repo_less_candidate_count = %d, want 1 -- a subset counted after authorization cannot be compared with a total counted before it", result.Counts.RepoLessCandidateCount)
	}
	if result.Counts.RepoLessAuthorizationDroppedCount != 1 {
		t.Fatalf("repo_less_authorization_dropped_count = %d, want 1", result.Counts.RepoLessAuthorizationDroppedCount)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none -- counting a denied row must never admit it", result.Targets)
	}

	// CONTROL: an ADMITTED repo-less row is counted exactly once, so the
	// pre-gate increment did not double-count what the old post-gate one did.
	admitted := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405Row(chaos5405ZeroRepoID, "linear:CHAOS-9001", "", chaos5405ProjectOriginKey, "native_team", chaos5405Census{Scoped: 1, Authorized: 1, RepoLess: 1}),
	}}}}
	control, err := chaos5405Expand(t, admitted, orgWidePrincipal(),
		contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus,
		chaos5405ProjectOrigin(t), 200)
	if err != nil {
		t.Fatalf("control ExpandFactScope error = %v", err)
	}
	if control.Counts.RepoLessCandidateCount != 1 {
		t.Fatalf("control repo_less_candidate_count = %d, want exactly 1 -- moving the increment before the gate must not double count an admitted row", control.Counts.RepoLessCandidateCount)
	}
}
