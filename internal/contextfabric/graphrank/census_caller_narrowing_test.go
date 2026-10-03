package graphrank

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var narrowingRepos = []string{"acme/alpha", "acme/beta", "acme/gamma"}

func narrowingPRID(i int) string { return "pull_request:r-" + string(rune('a'+i)) + ":747" }

func narrowingBackend() *fakeGraphBackend {
	var nodes []CandidateNode
	hints := map[string]CandidateNode{}
	for i, repo := range narrowingRepos {
		node := candidateNode(contextfabric.SubjectPullRequest, narrowingPRID(i), "PR #747", 0.5, []string{repo})
		nodes = append(nodes, node)
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: narrowingPRID(i)})] = node
	}
	return &fakeGraphBackend{
		enableAliasLookup:   true,
		aliasLookupComplete: true,
		searchResults:       map[string][]CandidateNode{"pull request 747": nodes},
		searchTruncated:     true,
		exactHints:          hints,
	}
}

func narrowingCensus(calls *int, ids []string) CensusFunc {
	return func(_ context.Context, _ string, kind CensusKind, handleValue string, handleBound bool, _ contextfabric.SubjectKind, _ string, anchorBound bool) (CensusOutcome, error) {
		*calls++
		if kind == contextfabric.SubjectPullRequest && handleBound && handleValue == "747" && !anchorBound {
			out := CensusOutcome{Count: len(ids), CensusReadAt: time.Now().UTC()}
			if len(ids) == 1 {
				out.SatisfierCanonicalID = ids[0]
			} else {
				out.SatisfierCanonicalIDs = ids
			}
			return out, nil
		}
		return CensusOutcome{}, nil
	}
}

func resolveNarrowed(t *testing.T, backend *fakeGraphBackend, principal storage.Principal, slugs []string, question string, ids []string, mutate ...func(*ResolveDeps)) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
	t.Helper()
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	calls := 0
	deps.CensusFunc = narrowingCensus(&calls, ids)
	for _, m := range mutate {
		m(&deps)
	}
	request := testRequest()
	request.Question = question
	request.RequestedScope.RepositorySlugs = slugs
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted("pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return resolution, tracer, calls
}

const narrowingQuestion = "is pull request 747 ready to merge?"

func allNarrowingIDs() []string {
	return []string{narrowingPRID(0), narrowingPRID(1), narrowingPRID(2)}
}

func TestResolveSubjects_StalledHandleCensusCommitsInsideACallerRepositoryNarrowing(t *testing.T) {
	t.Parallel()
	resolution, tracer, calls := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs())
	if calls == 0 {
		t.Fatalf("census calls = 0, want the census to run for an unrestricted principal's repository narrowing")
	}
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 1 || ids[0] != narrowingPRID(1) {
		t.Fatalf("committed = %v, want only the pull request inside the narrowed repository", ids)
	}
	var commits int
	for _, e := range tracer.eventsForStage("decision") {
		if e.Outcome == "committed" && e.Subject.CanonicalID == narrowingPRID(1) && e.CommitGate == "evidence_census" && e.CommitBasis == string(contextfabric.CommitBasisStatistical) {
			commits++
		}
	}
	if commits != 1 {
		t.Fatalf("evidence_census statistical commit decisions = %d, want 1", commits)
	}
}

func narrowingEvents(tracer *captureResolutionTracer) []ResolutionTraceEvent {
	return tracer.eventsForStage("evidence_round")
}

func TestResolveSubjects_StalledHandleCensusRecordsTheNarrowingAsADecision(t *testing.T) {
	t.Parallel()
	_, tracer, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs())
	rounds := narrowingEvents(tracer)
	if len(rounds) != 1 {
		t.Fatalf("evidence_round events = %d, want 1", len(rounds))
	}
	if r := rounds[0]; r.ShadowTrigger != "stalled" || r.ShadowCallerNarrowing != "narrowed_to_one" || r.ShadowNarrowedFrom != 3 || r.ShadowNarrowedTo != 1 || r.ShadowOutcome != string(ShadowWouldCommit) {
		t.Fatalf("evidence_round = trigger %q narrowing %q %d->%d outcome %q, want stalled narrowed_to_one 3->1 would_commit", r.ShadowTrigger, r.ShadowCallerNarrowing, r.ShadowNarrowedFrom, r.ShadowNarrowedTo, r.ShadowOutcome)
	}
}

func TestResolveSubjects_StalledHandleCensusKeepsTheClarificationWhenTheNarrowingIsNotExactlyOne(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		slugs  []string
		ids    []string
		narrow string
	}{
		{"none inside", []string{"acme/elsewhere"}, allNarrowingIDs(), "narrowed_to_none"},
		{"two inside", []string{narrowingRepos[0], narrowingRepos[1]}, allNarrowingIDs(), "narrowed_to_many"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolution, tracer, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, tc.slugs, narrowingQuestion, tc.ids)
			if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
				t.Fatalf("committed = %v, want none", ids)
			}
			rounds := narrowingEvents(tracer)
			if len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != tc.narrow {
				t.Fatalf("evidence_round = %#v, want one event with narrowing %q", rounds, tc.narrow)
			}
		})
	}
}

func TestResolveSubjects_StalledHandleCensusWithoutANarrowingStaysAClarification(t *testing.T) {
	t.Parallel()
	resolution, tracer, calls := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, nil, narrowingQuestion, allNarrowingIDs())
	if calls == 0 {
		t.Fatalf("census calls = 0, want the unrestricted census to run")
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want a bare number shared by three repositories left as a clarification", ids)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "" {
		t.Fatalf("evidence_round = %#v, want no narrowing recorded", rounds)
	}
}

func TestResolveSubjects_StalledHandleCensusNeverRunsForARestrictedPrincipalWithANarrowing(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{narrowingRepos[1]}}
	resolution, _, calls := resolveNarrowed(t, narrowingBackend(), principal, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs())
	if calls != 0 {
		t.Fatalf("census calls = %d, want 0 for a repository-restricted principal", calls)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none", ids)
	}
}

func TestResolveSubjects_StalledHandleCensusDoesNotCommitASingleSatisfierOutsideTheNarrowing(t *testing.T) {
	t.Parallel()
	resolution, _, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[0]}, narrowingQuestion, []string{narrowingPRID(2)})
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none: the only match lies outside the caller's narrowing", ids)
	}
}

func TestResolveSubjects_StalledHandleCensusKeepsTheClarificationWhenAKeyedReadFails(t *testing.T) {
	t.Parallel()
	backend := narrowingBackend()
	var narrowed bool
	resolution, tracer, _ := resolveNarrowed(t, backend, storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs(), func(d *ResolveDeps) {
		inner := d.ExactHint
		d.ExactHint = func(ctx context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
			if narrowed {
				return CandidateNode{}, false, errors.New("graph unavailable")
			}
			return inner(ctx, subject)
		}
		inner2 := d.CensusFunc
		d.CensusFunc = func(ctx context.Context, org string, k CensusKind, v string, hb bool, ak contextfabric.SubjectKind, aid string, ab bool) (CensusOutcome, error) {
			narrowed = true
			return inner2(ctx, org, k, v, hb, ak, aid, ab)
		}
	})
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none", ids)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "satisfier_read_failed" {
		t.Fatalf("evidence_round = %#v, want satisfier_read_failed", rounds)
	}
}

func TestResolveSubjects_AliasLookupDecisionCarriesTermCountAndMatchedKinds(t *testing.T) {
	t.Parallel()
	backend := scopeAnchorBackend(true, -1)
	_, tracer, _ := resolveScopeAnchorQuestion(t, backend, scopeAnchorQuestion)
	events := tracer.eventsForStage("alias_lookup")
	if len(events) != 1 || events[0].AliasLookupTermCount < 1 || len(events[0].AliasLookupMatchedKinds) != 1 || events[0].AliasLookupMatchedKinds[0] != string(contextfabric.SubjectRepository) {
		t.Fatalf("alias_lookup events = %#v, want a term count and the repository kind", events)
	}
}

func manyNarrowingBackend(n int) (*fakeGraphBackend, []string) {
	var nodes []CandidateNode
	hints := map[string]CandidateNode{}
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("pull_request:r-%02d:747", i)
		node := candidateNode(contextfabric.SubjectPullRequest, id, "PR #747", 0.5, []string{fmt.Sprintf("acme/repo-%02d", i)})
		nodes = append(nodes, node)
		ids = append(ids, id)
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: id})] = node
	}
	return &fakeGraphBackend{enableAliasLookup: true, aliasLookupComplete: true, searchResults: map[string][]CandidateNode{"pull request 747": nodes}, searchTruncated: true, exactHints: hints}, ids
}

func TestResolveSubjects_StalledHandleCensusNeverNarrowsATruncatedSatisfierSet(t *testing.T) {
	t.Parallel()
	backend, ids := manyNarrowingBackend(censusNarrowingMaxSatisfiers + 1)
	resolution, tracer, _ := resolveNarrowed(t, backend, storage.Principal{OrgID: "org_1"}, []string{"acme/repo-25"}, narrowingQuestion, ids)
	if got := scopeAnchorCommittedIDs(resolution); len(got) != 0 {
		t.Fatalf("committed = %v, want none: the narrowed repository holds the last of 26 matches, beyond the cap", got)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatalf("clarification prompt is empty, want the clarification kept")
	}
	rounds := narrowingEvents(tracer)
	if len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "census_truncated" || rounds[0].ShadowNarrowedFrom != censusNarrowingMaxSatisfiers+1 {
		t.Fatalf("evidence_round = %#v, want census_truncated with the census count", rounds)
	}
}

func TestResolveSubjects_StalledHandleCensusNeverNarrowsAnIncompleteSatisfierSet(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs(), func(d *ResolveDeps) {
		inner := d.CensusFunc
		d.CensusFunc = func(ctx context.Context, org string, k CensusKind, v string, hb bool, ak contextfabric.SubjectKind, aid string, ab bool) (CensusOutcome, error) {
			out, err := inner(ctx, org, k, v, hb, ak, aid, ab)
			out.SatisfierSetClosureMismatch = true
			return out, err
		}
	})
	if got := scopeAnchorCommittedIDs(resolution); len(got) != 0 {
		t.Fatalf("committed = %v, want none", got)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "census_truncated" {
		t.Fatalf("evidence_round = %#v, want census_truncated", rounds)
	}
}

func withCensusMutation(mutate func(*CensusOutcome)) func(*ResolveDeps) {
	return func(d *ResolveDeps) {
		inner := d.CensusFunc
		d.CensusFunc = func(ctx context.Context, org string, k CensusKind, v string, hb bool, ak contextfabric.SubjectKind, aid string, ab bool) (CensusOutcome, error) {
			out, err := inner(ctx, org, k, v, hb, ak, aid, ab)
			mutate(&out)
			return out, err
		}
	}
}

func TestResolveSubjects_StalledHandleCensusNeverNarrowsASetThatDisagreesWithItsCount(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs(), withCensusMutation(func(o *CensusOutcome) {
		o.SatisfierCanonicalIDs = []string{narrowingPRID(1)}
	}))
	if got := scopeAnchorCommittedIDs(resolution); len(got) != 0 {
		t.Fatalf("committed = %v, want none: the census counted 3 but listed 1", got)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "census_truncated" {
		t.Fatalf("evidence_round = %#v, want census_truncated", rounds)
	}
}

func TestResolveSubjects_StalledHandleCensusNeverNarrowsAClosureMismatch(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, allNarrowingIDs(), withCensusMutation(func(o *CensusOutcome) {
		o.ClosureMismatch = true
	}))
	if got := scopeAnchorCommittedIDs(resolution); len(got) != 0 {
		t.Fatalf("committed = %v, want none", got)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "" {
		t.Fatalf("evidence_round = %#v, want no narrowing recorded for a closure mismatch", rounds)
	}
}

func TestResolveSubjects_StalledHandleCensusCommitsAnInsideSingleSatisfierWithoutNarrowing(t *testing.T) {
	t.Parallel()
	resolution, tracer, _ := resolveNarrowed(t, narrowingBackend(), storage.Principal{OrgID: "org_1"}, []string{narrowingRepos[1]}, narrowingQuestion, []string{narrowingPRID(1)})
	if got := scopeAnchorCommittedIDs(resolution); len(got) != 1 || got[0] != narrowingPRID(1) {
		t.Fatalf("committed = %v, want the one pull request", got)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "" {
		t.Fatalf("evidence_round = %#v, want no narrowing recorded for a census of one", rounds)
	}
}
