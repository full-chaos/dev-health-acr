package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r1 F4, the codex repro: two project-only grants {P-1, P-2} and
// {P-1} with the same repository list share the reuse key and the
// stored-result grant digest (both hash only the repository list; c2,
// CHAOS-12298, gives them a p: digest). Until then a project-only principal is
// refused every stored result and never reuses an answer, and every other
// class is unchanged.
func TestAProjectOnlyPrincipalIsRefusedEveryStoredResult(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project.v2:github:P-1", Label: "P-1"}
	wide := storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1", "project.v2:github:P-2"}}
	narrow := storage.Principal{OrgID: "org_1", ProjectScopes: []string{"project.v2:github:P-1"}}
	if StoredResultGrantDigest(wide) != StoredResultGrantDigest(narrow) || GrantScopedTimeAxisKey(wide, "k") != GrantScopedTimeAxisKey(narrow, "k") {
		t.Log("the two grants no longer share a digest and a key (c2 landed); the refusal still holds")
	}
	for _, reader := range []storage.Principal{narrow, wide, {OrgID: "org_1", RepositoryScopes: []string{"*"}, ProjectScopes: narrow.ProjectScopes}} {
		graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}
		decision := NewStoredResultGate(graph).Authorize(context.Background(), reader, StoredInvestigationResult{Result: gateResult(project), GrantDigest: StoredResultGrantDigest(wide)}, StoredResultSurfacePriorResult)
		if decision.Decision != StoredResultDenied || decision.Reason != StoredResultReasonGrantMismatch {
			t.Errorf("%v: decision %s/%s, want denied on the grant", reader.ProjectScopes, decision.Decision, decision.Reason)
		}
		if graph.calls != 0 {
			t.Errorf("%v: the gate read the graph for a refused stored result", reader.ProjectScopes)
		}
	}
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/api"}}
	graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}
	if decision := NewStoredResultGate(graph).Authorize(context.Background(), restricted, StoredInvestigationResult{Result: gateResult(project), GrantDigest: StoredResultGrantDigest(restricted)}, StoredResultSurfacePriorResult); decision.Decision == StoredResultDenied {
		t.Errorf("a restricted reader of its own grant's result was denied: %s", decision.Reason)
	}
}

// F4, the reuse side: a project-only principal's reuse lookup is never made,
// whatever the store holds; an unrestricted principal's still hits.
func TestAProjectOnlyPrincipalNeverReusesAnAnswer(t *testing.T) {
	t.Parallel()
	project, candidate := reusableCandidate()
	for _, tc := range []struct {
		principal storage.Principal
		want      bool
	}{
		{principal: storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-1"}}, want: false},
		{principal: reusePrincipal(), want: true},
	} {
		lookups := 0
		engine := mustReuseTestEngine(t, EngineDependencies{
			Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}, bases: provenCommitBases(project)},
			Results: &resultStoreStub{},
			ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
				lookups++
				return candidate, true, nil
			}),
		})
		_, hit, _, _ := engine.tryReuse(context.Background(), tc.principal, validInvestigationRequest(), TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
		if hit != tc.want || (!tc.want && lookups != 0) {
			t.Errorf("%v: hit = %v after %d lookups, want hit %v", tc.principal.ProjectScopes, hit, lookups, tc.want)
		}
	}
}

// F4, the save side: a project-only caller's result is stored with the nil
// snapshots that make a row never reusable; an unrestricted caller's keeps them.
func TestAProjectOnlyResultIsNeverStoredAsReusable(t *testing.T) {
	t.Parallel()
	watermark := SourceWatermarkSnapshot{"source": "w1"}
	one := int64(1)
	var epoch RebuildEpoch = &one
	for _, tc := range []struct {
		principal storage.Principal
		reusable  bool
	}{
		{principal: storage.Principal{OrgID: "org_reuse", ProjectScopes: []string{"project.v2:github:P-1"}}, reusable: false},
		{principal: reusePrincipal(), reusable: true},
	} {
		store := &resultStoreStub{}
		engine := mustReuseTestEngine(t, EngineDependencies{Graph: graphReaderStub{}, Results: store})
		if err := engine.saveResult(context.Background(), tc.principal, BudgetAssertDecisive, InvestigationResult{ResultID: "result_project_only"}, watermark, epoch, "", 0, "", absentSemanticState(SemanticStateAbsenceContinuationRefused)); err != nil {
			t.Fatalf("saveResult: %v", err)
		}
		if got := store.savedSnapshot != nil && store.savedEpoch != nil; got != tc.reusable {
			t.Errorf("%v: stored reusable = %v, want %v", tc.principal.ProjectScopes, got, tc.reusable)
		}
	}
}
