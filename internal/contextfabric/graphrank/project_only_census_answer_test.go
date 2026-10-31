package graphrank

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r3, the codex repro at the answer: a question with a work-item
// handle (CHAOS-1234) whose search stalls (truncated, nothing committed)
// consults the organization-wide census only for an unscoped caller
// (resolve.go stalledForCensus, then RunShadowEvidenceRound's
// UnscopedVisibility gate). For a project-only caller, with an empty or "*"
// repository list, the census callback is never called and the committed
// result is the same for census counts 1 and 2. For an unrestricted caller
// (the control) the census is called and its count decides the commit.
func TestAnOrganizationWideCensusNeverDecidesAProjectOnlyAnswer(t *testing.T) {
	t.Parallel()
	resolve := func(principal storage.Principal, count int) (calls, committed int) {
		target := candidateNode(contextfabric.SubjectWorkItem, "work_item.v2:repo-1:CHAOS-1234", "CHAOS-1234 login bug", 0.50, "*")
		target.Attributes[AuthorizationProjectIdentitiesAttr] = []string{"project.v2:github:P-1"}
		backend := &fakeGraphBackend{
			searchResults:   map[string][]CandidateNode{"CHAOS-1234": {target}},
			searchTruncated: true,
			exactHints: map[string]CandidateNode{
				SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:repo-1:CHAOS-1234"}): target,
			},
		}
		deps := backend.deps()
		deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
			calls++
			return CensusOutcome{Count: count, CensusReadAt: time.Now().UTC(), SatisfierCanonicalID: "work_item.v2:repo-1:CHAOS-1234"}, nil
		}
		request := testRequest()
		request.Question = "why is CHAOS-1234 blocked?"
		resolution, _, err := ResolveSubjects(context.Background(), principal, request, testInterpreted("CHAOS-1234"), deps, nil, nil)
		if err != nil {
			t.Fatalf("ResolveSubjects error = %v", err)
		}
		return calls, len(resolution.Committed)
	}
	for _, scopes := range [][]string{nil, {"*"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}
		callsOne, committedOne := resolve(principal, 1)
		callsTwo, committedTwo := resolve(principal, 2)
		if callsOne != 0 || callsTwo != 0 {
			t.Errorf("%q: the census was called %d and %d times for a project-only caller, want never", scopes, callsOne, callsTwo)
		}
		if committedOne != committedTwo {
			t.Errorf("%q: committed %d with census count 1 and %d with count 2; an organization-wide count changed a project-only answer", scopes, committedOne, committedTwo)
		}
	}
	callsOne, committedOne := resolve(storage.Principal{OrgID: "org_1"}, 1)
	callsTwo, committedTwo := resolve(storage.Principal{OrgID: "org_1"}, 2)
	if callsOne != 1 || callsTwo != 1 || committedOne != 1 || committedTwo != 0 {
		t.Fatalf("control: an unrestricted caller's census calls %d/%d, committed %d/%d; want 1/1 and 1/0 (this pin lost its control)", callsOne, callsTwo, committedOne, committedTwo)
	}
}
