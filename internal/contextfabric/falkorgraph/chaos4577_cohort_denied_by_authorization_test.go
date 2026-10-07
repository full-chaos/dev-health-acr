package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDiscoverContextSignalsWhenEntireCohortDeniedByAuthorization is
// CHAOS-4577. When an org's team_repo_ownership carries no CURRENT row for a
// team, teams_projects.go's queryTeams stamps that team's
// authorization_repositories with the CHAOS-4390 fail-closed sentinel
// (acr-context-fabric:no-team-repository-ownership) instead of a real
// repository list. A repository-scoped principal -- the only kind Ask Dev
// can construct -- then matches nothing: DiscoveredCohort returns (nil,
// authzDropped) exactly as it would if there were genuinely no such teams.
// Before this change nothing distinguished the two cases in the answer
// itself; this proves DiscoverContext now marks Coverage.Partial and records
// a "cohort_denied_by_authorization:<count>" degraded reason (the existing
// free-text vocabulary endpoint_lookup_failed/unknown_relationship_type
// already use), plus a dedicated telemetry call, whenever authorization is
// what emptied the cohort.
func TestDiscoverContextSignalsWhenEntireCohortDeniedByAuthorization(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			// No lexical match, same as the CHAOS-4395 cohort test -- the
			// question names no team by label/alias/key.
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			// chaos4348ExactNameCandidates: one real team, but its
			// authorization_repositories carries the CHAOS-4390 sentinel --
			// the exact shape queryTeams emits for a team with zero CURRENT
			// team_repo_ownership rows -- which can never match a
			// repository-scoped principal's real slug.
			teamRow := fakeSubjectNodeRow("team", "team_platform", "Platform")
			teamRow["n"].(*node).Properties["authorization_repositories"] = []string{"acr-context-fabric:no-team-repository-ownership"}
			teamRow["n"].(*node).Properties["authorization_teams"] = []string{"team_platform"}
			return []row{teamRow}, nil
		default:
			t.Fatalf("unexpected query for a subjectless cohort request with no committed origin: %s", cypher)
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %#v, want nil -- the only candidate was denied by authorization", result.Cohort)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: the whole cohort was denied by authorization, which is degradation, not an ordinary empty result")
	}
	wantReason := "cohort_denied_by_authorization:1"
	found := false
	for _, reason := range result.Coverage.DegradedReasons {
		if reason == wantReason {
			found = true
		}
	}
	if !found {
		t.Fatalf("Coverage.DegradedReasons = %v, want it to contain %q", result.Coverage.DegradedReasons, wantReason)
	}
	if telemetry.cohortDeniedByAuthorization != 1 {
		t.Fatalf("cohortDeniedByAuthorization telemetry = %d, want exactly 1", telemetry.cohortDeniedByAuthorization)
	}
	// The pre-existing CHAOS-3888 "ordinary narrowing" counter must still
	// fire too -- CHAOS-4577 adds a second, more specific signal, it does
	// not replace the general one.
	if telemetry.cohortMembersAuthzDropped != 1 {
		t.Fatalf("cohortMembersAuthzDropped telemetry = %d, want exactly 1 (CHAOS-3888 signal must still fire)", telemetry.cohortMembersAuthzDropped)
	}
}

// TestDiscoverContextCohortPartlyCutByAuthorizationIsPartial: one member
// survives and one is denied, so the cohort is served partial with one
// degrading row carrying the denied count (count only, no names).
func TestDiscoverContextCohortPartlyCutByAuthorizationIsPartial(t *testing.T) {
	result, telemetry := discoverCohortWithDenied(t, 1, 1)
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want exactly one surviving member", result.Cohort)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: a member was cut by authorization")
	}
	assertCohortDeniedRow(t, result.Coverage, 1, "1 group member excluded by authorization")
	if telemetry.cohortDeniedByAuthorization != 1 {
		t.Fatalf("cohortDeniedByAuthorization telemetry = %d, want 1", telemetry.cohortDeniedByAuthorization)
	}
	if telemetry.cohortMembersAuthzDropped != 1 {
		t.Fatalf("cohortMembersAuthzDropped telemetry = %d, want 1", telemetry.cohortMembersAuthzDropped)
	}
}

func TestDiscoverContextCohortPartlyCutByAuthorizationCarriesTheDeniedCount(t *testing.T) {
	result, _ := discoverCohortWithDenied(t, 1, 2)
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want exactly one surviving member", result.Cohort)
	}
	assertCohortDeniedRow(t, result.Coverage, 2, "2 group members excluded by authorization")
}

func TestDiscoverContextCohortWithNoDenialCarriesNoDeniedRow(t *testing.T) {
	result, telemetry := discoverCohortWithDenied(t, 2, 0)
	if result.Cohort == nil || len(result.Cohort.Members) != 2 {
		t.Fatalf("Cohort = %#v, want two members", result.Cohort)
	}
	if result.Coverage.Partial {
		t.Fatal("Coverage.Partial = true, want false: nothing was denied")
	}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
			t.Fatalf("unexpected denied row %#v", detail)
		}
	}
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			t.Fatalf("unexpected reason %q", reason)
		}
	}
	if telemetry.cohortDeniedByAuthorization != 0 || telemetry.cohortMembersAuthzDropped != 0 {
		t.Fatalf("telemetry = %d/%d, want 0/0", telemetry.cohortDeniedByAuthorization, telemetry.cohortMembersAuthzDropped)
	}
}

func discoverCohortWithDenied(t *testing.T, served, denied int) (contextfabric.GraphContext, *recordingTelemetry) {
	t.Helper()
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			var rows []row
			for i := 0; i < served; i++ {
				key := fmt.Sprintf("team_authorized_%d", i)
				r := fakeSubjectNodeRow("team", key, "Authorized")
				r["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
				r["n"].(*node).Properties["authorization_teams"] = []string{key}
				rows = append(rows, r)
			}
			for i := 0; i < denied; i++ {
				key := fmt.Sprintf("team_denied_%d", i)
				r := fakeSubjectNodeRow("team", key, "Denied")
				r["n"].(*node).Properties["authorization_repositories"] = []string{"acr-context-fabric:no-team-repository-ownership"}
				r["n"].(*node).Properties["authorization_teams"] = []string{key}
				rows = append(rows, r)
			}
			return rows, nil
		default:
			t.Fatalf("unexpected query: %s", cypher)
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	result, err := adapter.DiscoverContext(context.Background(), principal, cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	return result, telemetry
}

func assertCohortDeniedRow(t *testing.T, coverage contextfabric.Coverage, count int, label string) {
	t.Helper()
	wantReason := fmt.Sprintf("cohort_denied_by_authorization:%d", count)
	found := false
	for _, detail := range coverage.Details {
		if detail.Code != contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
			continue
		}
		found = true
		if !detail.Degrading || detail.Count == nil || *detail.Count != count || detail.Label != label || detail.Raw != wantReason {
			t.Fatalf("denied row = %#v, want degrading count %d label %q raw %q", detail, count, label, wantReason)
		}
	}
	if !found {
		t.Fatalf("no cohort_denied_by_authorization row in %#v", coverage.Details)
	}
	reasons := 0
	for _, reason := range coverage.DegradedReasons {
		if reason == wantReason {
			reasons++
		}
	}
	if reasons != 1 {
		t.Fatalf("DegradedReasons = %v, want %q once", coverage.DegradedReasons, wantReason)
	}
}

// TestDiscoverContextDoesNotSignalCohortDeniedWhenOnlyAWrongKindNodeIsDenied
// is CHAOS-4577 codex round-1 P2, reproduced then fixed. The exact-name arm
// fetches repository/project/team nodes in ONE call
// (chaos4348ExactNameCandidates' exactNameKinds); a "which teams are
// struggling" cohort request whose only denied candidate is a REPOSITORY
// (not a team) must NOT report cohort_denied_by_authorization -- there were
// zero teams in the pool at all, denied or not, so this is the genuine
// "no such teams" case, not an authorization denial of the cohort this
// question actually asked about.
func TestDiscoverContextDoesNotSignalCohortDeniedWhenOnlyAWrongKindNodeIsDenied(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			// No team node in the pool at all -- only a repository the
			// principal is not scoped to. Before the fix, DiscoveredCohort's
			// authzDropped counted this denial regardless of kind, so this
			// case wrongly reported cohort_denied_by_authorization:1 for a
			// teams question that had no team candidate, denied or not.
			deniedRepo := fakeSubjectNodeRow("repository", "repo_private", "Private Repo")
			deniedRepo["n"].(*node).Properties["authorization_repositories"] = []string{"other/private"}
			return []row{deniedRepo}, nil
		default:
			t.Fatalf("unexpected query for a subjectless cohort request with no committed origin: %s", cypher)
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %#v, want nil -- there were no team candidates at all", result.Cohort)
	}
	if result.Coverage.Partial {
		t.Fatal("Coverage.Partial = true, want false: the only denial was a repository, not a team -- this is a genuine empty census, not a cohort denial")
	}
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			t.Fatalf("Coverage.DegradedReasons = %v, must not contain a cohort_denied_by_authorization reason -- no team was ever denied", result.Coverage.DegradedReasons)
		}
	}
	if telemetry.cohortDeniedByAuthorization != 0 {
		t.Fatalf("cohortDeniedByAuthorization telemetry = %d, want 0 -- the denied node was a repository, not a team", telemetry.cohortDeniedByAuthorization)
	}
	// The pre-existing CHAOS-3888 unscoped counter still fires -- it is
	// deliberately unscoped by kind (graphrank.DiscoveredCohort's own doc
	// comment), unlike the new CHAOS-4577 signal.
	if telemetry.cohortMembersAuthzDropped != 1 {
		t.Fatalf("cohortMembersAuthzDropped telemetry = %d, want exactly 1 (the denied repository, counted by the unscoped CHAOS-3888 signal)", telemetry.cohortMembersAuthzDropped)
	}
}

// TestDiscoverContextExplicitCohortDoesNotSignalDeniedOnANonExhaustiveMiss
// is CHAOS-4577 codex round-2 P2, reproduced then fixed. ShapeExplicitCohort
// with a resolved scope anchor (the user named specific members, e.g.
// "compare the frontend and backend teams") never runs the org-wide
// exact-name census -- it resolves through the bounded fulltext/hopWalk
// candidates only. A single denied match there is NOT proof the whole named
// cohort was denied: other named members may simply never have been
// retrieved at all (a lexical miss, unrelated to authorization). The
// cohort_denied_by_authorization signal must stay reserved for a genuinely
// exhaustive census.
//
// CHAOS-4622 remainder amendment: this scenario now requires
// ScopeAnchorResolved: true explicitly -- an explicit_cohort request with
// NO resolved anchor is a different, previously-mishandled case (see
// TestDiscoverContextExplicitCohortWithoutNamedAnchorCallsExactNameCandidates,
// chaos4395_cohort_exact_name_test.go) that now DOES run the exhaustive
// census. This test stays scoped to the genuinely-named case its own doc
// comment always described.
func TestDiscoverContextExplicitCohortDoesNotSignalDeniedOnANonExhaustiveMiss(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			// runFulltextQuery's CALL db.idx.fulltext.queryNodes(...) YIELDs
			// (node, score), not (n) -- fakeSubjectNodeRow's "n" key shape
			// (used by the exact-name/hop-walk arms) is silently unreadable
			// here and would make this fixture a no-op, not a repro.
			denied := &node{Properties: map[string]interface{}{propKind: "team", propCanonicalID: "team_denied", propLabel: "Denied"}}
			denied.Properties["authorization_repositories"] = []string{"acr-context-fabric:no-team-repository-ownership"}
			return []row{{"node": denied, "score": 1.0}}, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("chaos4348ExactNameCandidates must not be called for ShapeExplicitCohort with a resolved scope anchor")
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			return nil, nil
		default:
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := cohortDiscoveryRequestWithScopeAnchor(contextfabric.ShapeExplicitCohort, true)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %#v, want nil -- the only candidate found was denied", result.Cohort)
	}
	if result.Coverage.Partial {
		t.Fatal("Coverage.Partial = true, want false: an explicit-cohort request never ran the exhaustive census, so one denied fulltext match cannot prove the whole named cohort was denied")
	}
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			t.Fatalf("Coverage.DegradedReasons = %v, must not contain a cohort_denied_by_authorization reason on a non-exhaustive retrieval", result.Coverage.DegradedReasons)
		}
	}
	if telemetry.cohortDeniedByAuthorization != 0 {
		t.Fatalf("cohortDeniedByAuthorization telemetry = %d, want 0 -- retrieval was not exhaustive", telemetry.cohortDeniedByAuthorization)
	}
}

// TestDiscoverContextDoesNotSignalCohortDeniedWhenExactNameCensusTruncated
// is CHAOS-4577 codex round-2 P2's second scenario, reproduced then fixed.
// A truncated exact-name census (over exactNameCandidateQueryLimit rows) is
// already known-incomplete -- the one member that would have survived
// authorization may be exactly the row truncation cut. Reporting
// cohort_denied_by_authorization here would claim the denial explains the
// empty cohort when truncation might; exact_name_candidates_truncated is
// the more accurate, pre-existing disclosure for this case.
func TestDiscoverContextDoesNotSignalCohortDeniedWhenExactNameCensusTruncated(t *testing.T) {
	overLimitRows := make([]row, exactNameCandidateQueryLimit+1)
	for i := range overLimitRows {
		overLimitRows[i] = fakeSubjectNodeRow("team", fmt.Sprintf("team_%d", i), fmt.Sprintf("Team %d", i))
		// Every row denied -- if even one had been authorized, Cohort would
		// be non-nil and this test would be about a different case
		// (TestDiscoverContextTruncatedExactNameForcesCohortIncomplete
		// already covers that one).
		overLimitRows[i]["n"].(*node).Properties["authorization_repositories"] = []string{"acr-context-fabric:no-team-repository-ownership"}
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			return overLimitRows, nil
		default:
			t.Fatalf("unexpected query for a subjectless cohort request with no committed origin: %s", cypher)
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)

	result, err := adapter.DiscoverContext(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort != nil {
		t.Fatalf("Cohort = %#v, want nil -- every candidate was denied", result.Cohort)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true -- the census WAS truncated, which is degradation on its own")
	}
	var sawTruncated, sawDenied bool
	for _, reason := range result.Coverage.DegradedReasons {
		if reason == "exact_name_candidates_truncated" {
			sawTruncated = true
		}
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			sawDenied = true
		}
	}
	if !sawTruncated {
		t.Fatalf("Coverage.DegradedReasons = %v, want exact_name_candidates_truncated", result.Coverage.DegradedReasons)
	}
	if sawDenied {
		t.Fatalf("Coverage.DegradedReasons = %v, must not ALSO claim cohort_denied_by_authorization -- the census was truncated, so denial is not established as the (sole) cause", result.Coverage.DegradedReasons)
	}
	if telemetry.cohortDeniedByAuthorization != 0 {
		t.Fatalf("cohortDeniedByAuthorization telemetry = %d, want 0 -- the census was truncated", telemetry.cohortDeniedByAuthorization)
	}
}

// An explicit cohort never runs the exhaustive census, so one denied hit
// cannot prove the whole named cohort was denied; but a member that was
// found and cut is a cut member, and the count row is served beside the
// surviving member.
func TestDiscoverContextExplicitCohortPartlyCutByAuthorizationIsPartial(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			served := &node{Properties: map[string]interface{}{propKind: "team", propCanonicalID: "team_served", propLabel: "Served"}}
			served.Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
			denied := &node{Properties: map[string]interface{}{propKind: "team", propCanonicalID: "team_denied", propLabel: "Denied"}}
			denied.Properties["authorization_repositories"] = []string{"acr-context-fabric:no-team-repository-ownership"}
			return []row{{"node": served, "score": 2.0}, {"node": denied, "score": 1.0}}, nil
		case strings.Contains(cypher, "$kinds"):
			t.Fatal("exact-name census must not run for an explicit cohort with a resolved scope anchor")
			return nil, nil
		default:
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	result, err := adapter.DiscoverContext(context.Background(), principal, cohortDiscoveryRequestWithScopeAnchor(contextfabric.ShapeExplicitCohort, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want exactly one surviving member", result.Cohort)
	}
	if !result.Coverage.Partial {
		t.Fatal("Coverage.Partial = false, want true: a named member was cut by authorization")
	}
	assertCohortDeniedRow(t, result.Coverage, 1, "1 group member excluded by authorization")
}
