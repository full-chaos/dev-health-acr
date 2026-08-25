package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestProjectRepositoriesRejectsUnsupportedAxis is codex xhigh review R1's
// MEDIUM finding on the CHAOS-4109 PR, fixed: before this fix,
// ScopeExpander.projectRepositories routed only TemporalValidTime/
// TemporalRange to the as-of query and fell through to the ORIGINAL
// current-column query for anything else -- including TemporalObservedTime,
// which this package explicitly does not support (no independent
// observation log for a project reassignment separate from when it
// happened). fact_scope.go's resolver already blocks ObservedTime from
// reaching here through the normal engine path, but this exported method
// is also this package's own contract boundary: a direct or future caller
// of ExpandFactScope passing TemporalObservedTime would have silently
// received TODAY's project membership under a historical axis's name --
// the exact false-historical-answer failure CHAOS-3781's H6 refusal and
// this ticket's own axis gate both exist to prevent.
//
// Uses fakeClient with NO registered tables (helpers_test.go): if the fix
// regresses to the old if/else fallthrough, the current-column query would
// actually run against the fake and return an empty (not an error) result
// -- proving this test exercises the REJECTION path, not merely "returned
// zero targets".
func TestProjectRepositoriesRejectsUnsupportedAxis(t *testing.T) {
	client := &fakeClient{}
	expander := devhealthfacts.NewScopeExpander(client)
	projectSubject, err := chaos4109AxisTestProjectSubject()
	if err != nil {
		t.Fatalf("build project subject: %v", err)
	}

	_, err = expander.ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: "org-4109-axis-rejection", RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{projectSubject},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contractsv1.ContextFabricTemporalObservedTime},
		Limit:           20,
	})
	if err == nil {
		t.Fatal("ExpandFactScope(TemporalObservedTime) = nil error, want a rejection -- this axis has no independent observation log and must never silently answer from current data")
	}
	if !strings.Contains(err.Error(), "observed_time") {
		t.Fatalf("error = %v, want it to name the unsupported axis", err)
	}
	if len(client.queries) != 0 {
		t.Fatalf("query count = %d, want 0 -- the axis must be rejected BEFORE any query runs, got: %+v", len(client.queries), client.queries)
	}
}

func chaos4109AxisTestProjectSubject() (contextfabric.SubjectRef, error) {
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", "proj-1"}, nil)
	if err != nil || omitted {
		return contextfabric.SubjectRef{}, err
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID}, nil
}
