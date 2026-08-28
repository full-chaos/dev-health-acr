package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveRepositorySubjectSurvivesSharedPoolTruncation is CHAOS-4417's
// own red-first pin, named verbatim in the ticket and in
// .remember/context-fabric/drafts/repo-subject-diagnosis-2026-08-28.md's
// executed repro: an org whose repository population is a small minority of
// its graph (that repro measured 11 repository nodes vs 37,001
// ci_pipeline_run + thousands of other-kind nodes) ties every lexical
// repository match at a base confidence well under any exact-label
// carve-out, so the shared, cross-kind MaxSubjectCandidates cut truncates
// the resolution-wide search BEFORE any receipt has confirmed a kind --
// turn 1's shape, where CHAOS-4132/CHAOS-4154's confirmed-kind machinery
// cannot engage at all. Pre-fix, resolution.go's commit-gate switch's `case
// searchTruncated: ambiguous = true` fires unconditionally, ahead of
// LoneFloor, and a genuinely lone, otherwise-committable repository
// candidate never gets evaluated.
//
// The fixture does not need a literal 37,001-node crowd to reproduce the
// defect: fakeGraphBackend.searchTruncated is a blanket "every Search()
// call reports truncated=true" flag (mirroring
// TestResolveSubjects_ConfirmedKindRescueTruncationBlocksALoneCandidateCommit's
// own convention for the confirmed-kind analog of this same mechanism) --
// exactly the resolution-wide signal the shared population skew produces in
// production. searchKindTruncated stays at its zero value (false): the
// isolated, kind-scoped SearchKind pass this ticket's fix adds is what
// proves the repository population itself was read completely, independent
// of what crowded out the ordinary cross-kind Search call.
//
// relevance=0.8 clears DefaultCommitGatePolicy's LoneFloor (0.72) on a
// single-mechanism (non-exact) match -- CorroboratedConfidence returns the
// base confidence unchanged below 2 distinct mechanisms. The candidate is
// deliberately NOT an exact label/term match: an exact match would commit
// via commitGate=="exact_index", which the CHAOS-3810 carve-out already
// lets survive truncation -- that path is NOT this ticket's bug and would
// make this test pass even without the fix, silently proving nothing.
// MatchLexical (term "acr" != label "acr-core") keeps this pinned to the
// LoneFloor path this ticket actually changes.
func TestResolveRepositorySubjectSurvivesSharedPoolTruncation(t *testing.T) {
	t.Parallel()
	const term = "acr"
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.8, "*")
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{term: {node}},
		searchTruncated:  true,
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {contextfabric.SubjectRepository: {node}},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchKindCalls) == 0 {
		t.Fatal("searchKindCalls is empty, want the CHAOS-4417 pre-confirmation low-population kind rescue to have queried SearchKind(repository) once the resolution-wide search truncated with nothing committed")
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution = %#v, want the repository candidate committed via the isolated, proven-complete kind-scoped census -- a shared-pool MaxSubjectCandidates truncation driven by OTHER kinds must not block a genuinely lone repository candidate before any receipt confirms a kind", resolution)
	}
}

// TestApplyLowPopulationKindScopedRescue_MultipleKindsCommitStaysAmbiguous
// pins the zero-tolerance wrong-commit discipline
// applyLowPopulationKindScopedRescue's own doc comment describes: when MORE
// THAN ONE of chaos4417LowPopulationScopedKinds independently produces an
// isolated, proven-complete commit, this mechanism must refuse to pick a
// winner (ok=false) rather than silently preferring one kind over another
// with no ranking signal between them.
func TestApplyLowPopulationKindScopedRescue_MultipleKindsCommitStaysAmbiguous(t *testing.T) {
	t.Parallel()
	const term = "acr"
	repoSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	projectSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1", Label: "acr-project"}
	repoNode := candidateNode(repoSubject.Kind, repoSubject.CanonicalID, repoSubject.Label, 0.8, "*")
	projectNode := candidateNode(projectSubject.Kind, projectSubject.CanonicalID, projectSubject.Label, 0.8, "*")
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{term: {repoNode, projectNode}},
		searchTruncated:  true,
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {
				contextfabric.SubjectRepository: {repoNode},
				contextfabric.SubjectProject:    {projectNode},
			},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ZERO commits -- two kinds independently clearing LoneFloor in isolation is cross-kind ambiguity this mechanism must not silently resolve by picking one", resolution.Committed)
	}
}
