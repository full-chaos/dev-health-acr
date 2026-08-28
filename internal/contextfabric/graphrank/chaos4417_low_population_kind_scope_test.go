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

// TestApplyLowPopulationKindScopedRescue_CrossKindTopFloorDeclines is codex
// R1 finding 1 (P1, confirmed)'s own named repro, pinned directly: a
// repository candidate at 0.80 clears LoneFloor (0.72) when evaluated
// ALONE, but a genuine 0.71 project rival visible to the SAME gate call
// makes the union fail TopFloor (0.88) -- exactly the cross-kind
// arbitration a per-kind gate call structurally cannot perform.
func TestApplyLowPopulationKindScopedRescue_CrossKindTopFloorDeclines(t *testing.T) {
	t.Parallel()
	const term = "acr"
	repoSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	projectSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1", Label: "acr-project"}
	repoNode := candidateNode(repoSubject.Kind, repoSubject.CanonicalID, repoSubject.Label, 0.80, "*")
	projectNode := candidateNode(projectSubject.Kind, projectSubject.CanonicalID, projectSubject.Label, 0.71, "*")
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
		t.Fatalf("resolution.Committed = %#v, want ZERO commits -- 0.80 repository alone clears LoneFloor (0.72) but fails TopFloor (0.88) against the real 0.71 project rival once both are visible to ONE gate call; a per-kind gate that evaluated repository in isolation would have wrongly committed it", resolution.Committed)
	}
}

// TestApplyLowPopulationKindScopedRescue_OuterPoolRivalBlocksCommit is
// codex R2's own named repro (P1, confirmed), pinned directly: a genuine
// 0.71 work_item candidate -- a kind OUTSIDE
// chaos4417LowPopulationScopedKinds entirely, found ONLY by the ORDINARY
// (resolution-wide) Search call, never by this rescue's own SearchKind
// census -- must still block a 0.80 repository candidate from committing
// via LoneFloor. R1's own union (repository ∪ project ∪ team's exhaustive
// finds) would have missed this rival completely, since work_item is
// never one of the three kinds this rescue censuses; the fix seeds the
// union from the caller's own outerPool (candidatesBySubject) FIRST, so
// every candidate the ordinary search already found -- any kind -- can
// still arbitrate.
func TestApplyLowPopulationKindScopedRescue_OuterPoolRivalBlocksCommit(t *testing.T) {
	t.Parallel()
	const term = "acr"
	repoSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	workItemSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "acr-issue"}
	repoNode := candidateNode(repoSubject.Kind, repoSubject.CanonicalID, repoSubject.Label, 0.80, "*")
	workItemNode := candidateNode(workItemSubject.Kind, workItemSubject.CanonicalID, workItemSubject.Label, 0.71, "*")
	backend := &fakeGraphBackend{
		// Both nodes reach the pool through the ORDINARY Search call --
		// work_item is never SearchKind'd by this rescue (it is not in
		// chaos4417LowPopulationScopedKinds), so the ONLY way it can ever
		// participate in this rescue's arbitration is via the outerPool
		// seed this test exists to pin.
		searchResults:    map[string][]CandidateNode{term: {repoNode, workItemNode}},
		searchTruncated:  true,
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {contextfabric.SubjectRepository: {repoNode}},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ZERO commits -- 0.80 repository alone clears LoneFloor (0.72) but fails TopFloor (0.88) against the real 0.71 work_item rival once both are visible; work_item is outside chaos4417LowPopulationScopedKinds entirely, so ONLY the outer (ordinary-search) pool can ever surface it to this rescue", resolution.Committed)
	}
}

// TestApplyLowPopulationKindScopedRescue_IncompleteSiblingAbortsWholeRescue
// is codex R1 finding 2 (P1, confirmed): a repository candidate that would
// commit cleanly on its own must NOT commit when a sibling kind
// (project/team) in chaos4417LowPopulationScopedKinds reports its own
// SearchKind census truncated -- the untested sibling kind's population
// could hide a genuine rival, so the WHOLE rescue must fail closed, not
// just decline that one sibling's own contribution.
func TestApplyLowPopulationKindScopedRescue_IncompleteSiblingAbortsWholeRescue(t *testing.T) {
	t.Parallel()
	const term = "acr"
	repoSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	repoNode := candidateNode(repoSubject.Kind, repoSubject.CanonicalID, repoSubject.Label, 0.80, "*")
	var searchKindCalls []contextfabric.SubjectKind
	// A bespoke SearchKind: repository's OWN census is clean
	// (untruncated), but project's reports truncated=true -- the ONE
	// sibling this test needs incomplete while repository stays complete.
	// Called directly (not through ResolveSubjects) to isolate this
	// function's own SearchKind usage from CHAOS-4038's unrelated
	// coverage floor, which also calls SearchKind earlier in the pipeline
	// for a different kind set.
	searchKind := func(ctx context.Context, searchTerm string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
		searchKindCalls = append(searchKindCalls, kind)
		switch kind {
		case contextfabric.SubjectRepository:
			return []CandidateNode{repoNode}, false, false, nil
		case contextfabric.SubjectProject:
			return nil, true, false, nil // truncated=true
		default:
			return nil, false, false, nil
		}
	}
	deps := ResolveDeps{SearchKind: searchKind}
	request := testRequest()
	resolution, _, _, ok, err := applyLowPopulationKindScopedRescue(
		context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, []string{term},
		nil, false, request.Options.MaxSubjectCandidates, true, DefaultCommitGatePolicy(), true, false, false,
		nil, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("applyLowPopulationKindScopedRescue() error = %v", err)
	}
	if ok || len(resolution.Committed) != 0 {
		t.Fatalf("ok=%v resolution.Committed=%#v, want ok=false -- project's own incomplete (truncated) census must abort the WHOLE rescue, even though repository's own census was clean and would have committed alone", ok, resolution.Committed)
	}
	// chaos4417LowPopulationScopedKinds is sorted lexicographically
	// (project < repository < team), so project is tried FIRST and its
	// truncation aborts before repository or team are ever attempted --
	// pins the "stop at the first incomplete kind" cost-bounding half of
	// the same fix (codex R1 P1 fan-out finding).
	if len(searchKindCalls) != 1 || searchKindCalls[0] != contextfabric.SubjectProject {
		t.Fatalf("searchKindCalls = %#v, want exactly one call, for project (the first incomplete kind in deterministic order) -- repository/team must never be attempted once an earlier sibling already failed closed", searchKindCalls)
	}
}

// TestApplyLowPopulationKindScopedRescue_VectorConfiguredSkipsEntirely is
// codex R1 finding 3 (P1, confirmed): on a deployment with a live vector
// mechanism, buildConfirmedKindScopedSnapshot returns plan_incomplete for
// EVERY kind unconditionally (chaos4154_confirmed_kind_scope.go), so this
// rescue can never succeed there -- it must detect that up front and skip
// entirely, spending ZERO SearchKind calls, rather than paying for three
// exhaustive (and, per kind, vector-census-shadowing) passes to reach a
// foregone conclusion.
func TestApplyLowPopulationKindScopedRescue_VectorConfiguredSkipsEntirely(t *testing.T) {
	t.Parallel()
	const term = "acr"
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.80, "*")
	var searchKindCalls int
	// Called directly (not through ResolveSubjects) to isolate this
	// function's own SearchKind usage from CHAOS-4038's unrelated
	// coverage floor, which also calls SearchKind for other kinds earlier
	// in the pipeline.
	deps := ResolveDeps{
		VectorMechanismConfigured: true,
		SearchKind: func(ctx context.Context, searchTerm string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
			searchKindCalls++
			return []CandidateNode{node}, false, false, nil
		},
	}
	request := testRequest()
	resolution, _, _, ok, err := applyLowPopulationKindScopedRescue(
		context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, []string{term},
		nil, false, request.Options.MaxSubjectCandidates, true, DefaultCommitGatePolicy(), true, false, false,
		nil, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("applyLowPopulationKindScopedRescue() error = %v", err)
	}
	if ok || len(resolution.Committed) != 0 {
		t.Fatalf("ok=%v resolution.Committed=%#v, want ok=false -- a live vector mechanism forecloses this rescue's completeness contract entirely", ok, resolution.Committed)
	}
	if searchKindCalls != 0 {
		t.Fatalf("searchKindCalls = %d, want ZERO -- VectorMechanismConfigured must be checked BEFORE any SearchKind call, not discovered after paying for it", searchKindCalls)
	}
}
