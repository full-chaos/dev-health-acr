package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

// TestDiscoverContextCohortNarrowedByAuthorizationIsNotPartial proves the
// negative: when authorization denies SOME cohort candidates but at least
// one member survives, that is the ordinary, expected CHAOS-3888 narrowing
// case -- Coverage.Partial must stay false and no cohort_denied_by_authorization
// reason must appear, exactly as before this change.
func TestDiscoverContextCohortNarrowedByAuthorizationIsNotPartial(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "$kinds"):
			authorized := fakeSubjectNodeRow("team", "team_authorized", "Authorized")
			authorized["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
			authorized["n"].(*node).Properties["authorization_teams"] = []string{"team_authorized"}
			denied := fakeSubjectNodeRow("team", "team_denied", "Denied")
			denied["n"].(*node).Properties["authorization_repositories"] = []string{"acr-context-fabric:no-team-repository-ownership"}
			denied["n"].(*node).Properties["authorization_teams"] = []string{"team_denied"}
			return []row{authorized, denied}, nil
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
	if result.Cohort == nil || len(result.Cohort.Members) != 1 {
		t.Fatalf("Cohort = %#v, want exactly one surviving member", result.Cohort)
	}
	if result.Coverage.Partial {
		t.Fatalf("Coverage.Partial = true, want false: one member survived, so this is ordinary narrowing, not denial")
	}
	for _, reason := range result.Coverage.DegradedReasons {
		if strings.HasPrefix(reason, "cohort_denied_by_authorization") {
			t.Fatalf("Coverage.DegradedReasons = %v, must not contain a cohort_denied_by_authorization reason when the cohort is not empty", result.Coverage.DegradedReasons)
		}
	}
	if telemetry.cohortDeniedByAuthorization != 0 {
		t.Fatalf("cohortDeniedByAuthorization telemetry = %d, want 0 (narrowing, not denial)", telemetry.cohortDeniedByAuthorization)
	}
	if telemetry.cohortMembersAuthzDropped != 1 {
		t.Fatalf("cohortMembersAuthzDropped telemetry = %d, want exactly 1", telemetry.cohortMembersAuthzDropped)
	}
}
