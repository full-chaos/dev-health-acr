package v1

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func chaos7139Slugs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("acme/repo-%d", i)
	}
	return out
}

func chaos7139Entity(slugs []string) ContextFabricEntityProjection {
	return ContextFabricEntityProjection{
		Subject:        ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:big", Label: "big"},
		Authorization:  ContextFabricAuthorizationScope{TeamIDs: []string{"big"}, RepositorySlugs: slugs},
		EvidenceRefIDs: []string{"evidence_7139_1234"},
		ObservedAt:     time.Unix(1, 0).UTC(),
		SourceVersion:  "ops-v1",
	}
}

// CHAOS-7139: the entity projection's repository list is bounded at
// ContextFabricEntityAuthorizationRepositoryMax; every other scope stays 200.
func TestEntityAuthorizationRepositoryBoundIsWidenedAlone(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 200, 201, 450, ContextFabricEntityAuthorizationRepositoryMax} {
		if err := chaos7139Entity(chaos7139Slugs(n)).Validate(); err != nil {
			t.Errorf("entity with %d repositories rejected: %v", n, err)
		}
	}
	over := chaos7139Entity(chaos7139Slugs(ContextFabricEntityAuthorizationRepositoryMax + 1)).Validate()
	if !errors.Is(over, ErrEntityAuthorizationRepositoriesExceeded) {
		t.Errorf("entity above the bound: got %v, want ErrEntityAuthorizationRepositoriesExceeded (fail closed, distinct reason)", over)
	}
	// Everything else keeps the generic 200.
	if err := (ContextFabricAuthorizationScope{RepositorySlugs: chaos7139Slugs(201)}).Validate(); err == nil {
		t.Error("generic authorization scope with 201 repositories accepted; only the entity bound is widened")
	}
	if err := (ContextFabricRequestedScope{RepositorySlugs: chaos7139Slugs(201)}).Validate(); err == nil {
		t.Error("request-side forced scope with 201 repositories accepted; it must stay at 200")
	}
	teams := make([]string, 201)
	for i := range teams {
		teams[i] = fmt.Sprintf("t%d", i)
	}
	e := chaos7139Entity([]string{"acme/r"})
	e.Authorization.TeamIDs = teams
	if err := e.Validate(); err == nil {
		t.Error("entity with 201 team ids accepted; only repository_slugs is widened")
	}
}
