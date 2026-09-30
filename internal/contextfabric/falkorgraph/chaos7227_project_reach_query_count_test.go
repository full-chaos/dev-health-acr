package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7227 named residual, pinned so a change is visible. For a
// repository-restricted caller, the stored-subject decision (the one
// directread.SubjectGate and so the source-row ownership route take) reads a
// project's ownership reach LAZILY (project_reach.go): only when the lookup
// returns a project node. An existing project outside the caller's reach and
// an absent project are both refused, but the existing one costs one more
// graph query. read_facts, find_subjects and run_operation share this gate,
// so the evidence route adds no new signal. The gate-wide fix (compute the
// reach for every restricted project decision) is a separate Low follow-up;
// when it lands, this test's counts change and it must be updated with it.
func TestChaos7227PinsTheLazyProjectReachQueryCount(t *testing.T) {
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/allowed"}}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "jira:PAY"}
	decide := func(present bool) (int, contextfabric.StoredSubjectOutcome) {
		project := &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "jira:PAY", propLabel: "Payments", propAuthzRepos: "*"}}
		reachQueries := 0
		fake := &fakeConn{queryFunc: func(_ context.Context, _ string, cypher string, _ map[string]interface{}, _ bool) ([]row, error) {
			switch {
			case strings.Contains(cypher, "UNWIND $targets AS t MATCH"):
				if present {
					return []row{{"n": project}}, nil
				}
				return nil, nil
			case strings.Contains(cypher, "RETURN p."+propCanonicalID+" AS id"):
				reachQueries++
				return nil, nil
			}
			return nil, nil
		}}
		outcomes, err := newFakeAdapter(t, fake).AuthorizeStoredSubjects(context.Background(), principal, contextfabric.ResolvedGraphBinding{}, []contextfabric.SubjectRef{subject})
		if err != nil || len(outcomes) != 1 {
			t.Fatalf("present=%v: outcomes %v, err %v", present, outcomes, err)
		}
		return reachQueries, outcomes[0]
	}
	existingQueries, existing := decide(true)
	absentQueries, absent := decide(false)
	if existing != contextfabric.StoredSubjectDenied || absent != contextfabric.StoredSubjectAbsent {
		t.Fatalf("outcomes: existing %v, absent %v", existing, absent)
	}
	if existingQueries != 1 || absentQueries != 0 {
		t.Fatalf("reach queries: existing %d, absent %d; the pinned residual is 1 and 0 (update CHAOS-7227 RISK-NOTES and this test if the gate now equalizes them)", existingQueries, absentQueries)
	}
}
