package graphrank

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const filterRepoCount = 26

func filterRepo(i int) string { return fmt.Sprintf("acme/repo-%02d", i) }

func filterPRID(i int) string { return fmt.Sprintf("pull_request:r-%02d:747", i) }

func filterBackend() *fakeGraphBackend {
	var nodes []CandidateNode
	hints := map[string]CandidateNode{}
	for i := 0; i < filterRepoCount; i++ {
		node := candidateNode(contextfabric.SubjectPullRequest, filterPRID(i), "PR #747", 0.5, []string{filterRepo(i)})
		nodes = append(nodes, node)
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: filterPRID(i)})] = node
	}
	return &fakeGraphBackend{
		enableAliasLookup:   true,
		aliasLookupComplete: true,
		searchResults:       map[string][]CandidateNode{"pull request 747": nodes},
		searchTruncated:     true,
		exactHints:          hints,
	}
}

type filterCensusProbe struct {
	calls   int
	filters [][]string
}

// filterStore models the source: one pull request numbered 747 in each of
// the 26 repositories. A filter-aware store applies the slug filter inside
// its read; honourFilter=false models a CensusFunc that ignores it.
func (p *filterCensusProbe) store(honourFilter bool, lie func(ids []string) []string) CensusFunc {
	return func(ctx context.Context, _ string, kind CensusKind, handleValue string, handleBound bool, _ contextfabric.SubjectKind, _ string, anchorBound bool) (CensusOutcome, error) {
		p.calls++
		slugs := CensusRepositoryFilterFrom(ctx)
		p.filters = append(p.filters, slugs)
		if kind != contextfabric.SubjectPullRequest || !handleBound || handleValue != "747" || anchorBound {
			return CensusOutcome{}, nil
		}
		var ids []string
		for i := 0; i < filterRepoCount; i++ {
			if !honourFilter || len(slugs) == 0 || filterSlugAdmits(slugs, filterRepo(i)) {
				ids = append(ids, filterPRID(i))
			}
		}
		if lie != nil {
			ids = lie(ids)
		}
		out := CensusOutcome{Count: len(ids), CensusReadAt: time.Now().UTC(), RepositoryFilterApplied: honourFilter && len(slugs) > 0}
		switch {
		case len(ids) == 1:
			out.SatisfierCanonicalID = ids[0]
		case len(ids) > 1 && len(ids) <= 999:
			out.SatisfierCanonicalIDs = ids
		}
		return out, nil
	}
}

func filterSlugAdmits(slugs []string, repo string) bool {
	for _, s := range slugs {
		if owner, ok := strings.CutSuffix(s, "/*"); ok && strings.HasPrefix(repo, owner+"/") {
			return true
		}
		if s == repo {
			return true
		}
	}
	return false
}

func resolveFiltered(t *testing.T, principal storage.Principal, slugs []string, census CensusFunc, mutate ...func(*ResolveDeps)) (contextfabric.SubjectResolution, *captureResolutionTracer) {
	t.Helper()
	deps := filterBackend().deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	deps.CensusFunc = census
	for _, m := range mutate {
		m(&deps)
	}
	request := testRequest()
	request.Question = narrowingQuestion
	request.RequestedScope.RepositorySlugs = slugs
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted("pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return resolution, tracer
}

func TestResolveSubjects_CensusCommitsWhenOneNumberLivesInMoreRepositoriesThanTheCap(t *testing.T) {
	t.Parallel()
	probe := &filterCensusProbe{}
	resolution, tracer := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, []string{filterRepo(filterRepoCount - 1)}, probe.store(true, nil))
	ids := scopeAnchorCommittedIDs(resolution)
	if len(ids) != 1 || ids[0] != filterPRID(filterRepoCount-1) {
		t.Fatalf("committed = %v, want only the pull request in the narrowed repository", ids)
	}
	if probe.calls == 0 {
		t.Fatalf("census calls = 0")
	}
	for _, f := range probe.filters {
		if len(f) != 1 || f[0] != filterRepo(filterRepoCount-1) {
			t.Fatalf("census filter = %v, want exactly the caller's requested repository", f)
		}
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowOutcome != string(ShadowWouldCommit) {
		t.Fatalf("evidence_round = %#v, want one would_commit", rounds)
	}
}

func TestResolveSubjects_CensusThatIgnoresTheFilterStillTruncatesPastTheCap(t *testing.T) {
	t.Parallel()
	probe := &filterCensusProbe{}
	resolution, tracer := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, []string{filterRepo(filterRepoCount - 1)}, probe.store(false, nil))
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none from an unfiltered 26-row census", ids)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "census_truncated" {
		t.Fatalf("evidence_round = %#v, want census_truncated", rounds)
	}
}

func TestResolveSubjects_CensusReceivesNoFilterWithoutANarrowing(t *testing.T) {
	t.Parallel()
	probe := &filterCensusProbe{}
	resolution, _ := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, nil, probe.store(true, nil))
	if probe.calls == 0 {
		t.Fatalf("census calls = 0")
	}
	for _, f := range probe.filters {
		if f != nil {
			t.Fatalf("census filter = %v, want none without a narrowing", f)
		}
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want a 26-repository number left as a clarification", ids)
	}
}

func TestResolveSubjects_CensusNeverRunsOrFiltersForARestrictedPrincipalNamingAHiddenRepository(t *testing.T) {
	t.Parallel()
	probe := &filterCensusProbe{}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{filterRepo(0)}}
	resolution, _ := resolveFiltered(t, principal, []string{filterRepo(5)}, probe.store(true, nil))
	if probe.calls != 0 {
		t.Fatalf("census calls = %d (filters %v), want 0 for a restricted principal", probe.calls, probe.filters)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none", ids)
	}
}

func TestResolveSubjects_CensusFilterNamingNoExistingRepositoryCommitsNothing(t *testing.T) {
	t.Parallel()
	probe := &filterCensusProbe{}
	resolution, tracer := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, []string{"acme/elsewhere"}, probe.store(true, nil))
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none", ids)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "narrowed_to_none" || rounds[0].ShadowOutcome != string(ShadowWouldClarify) {
		t.Fatalf("evidence_round = %#v, want narrowed_to_none and would_clarify (never would_no_match)", rounds)
	}
}

func TestResolveSubjects_FilteredCensusRowOutsideTheNarrowingIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]func([]string) []string{
		"single satisfier outside": func([]string) []string { return []string{filterPRID(0)} },
		"set with a row outside":   func(ids []string) []string { return append(ids, filterPRID(0)) },
	}
	for name, lie := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			probe := &filterCensusProbe{}
			resolution, tracer := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, []string{filterRepo(7)}, probe.store(true, lie))
			if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
				t.Fatalf("committed = %v, want a row outside the narrowing refused", ids)
			}
			if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowReason != string(ReasonCensusClosureMismatch) {
				t.Fatalf("evidence_round = %#v, want %q", rounds, ReasonCensusClosureMismatch)
			}
		})
	}
}

func TestResolveSubjects_FilteredCensusCapAppliesToTheNarrowedResult(t *testing.T) {
	t.Parallel()
	probe := &filterCensusProbe{}
	resolution, tracer := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, []string{"acme/*"}, probe.store(true, nil))
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none", ids)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowOutcome != string(ShadowWouldClarify) || rounds[0].ShadowReason == string(ReasonCensusClosureMismatch) {
		t.Fatalf("evidence_round = %#v, want an ordinary would_clarify for 26 satisfiers inside the narrowing, not a refusal", rounds)
	}
}

func TestResolveSubjects_FilteredCensusSingleSatisfierWithoutAnIdentityIsRefused(t *testing.T) {
	t.Parallel()
	census := func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, CensusReadAt: time.Now().UTC(), RepositoryFilterApplied: true}, nil
	}
	resolution, tracer := resolveFiltered(t, storage.Principal{OrgID: "org_1"}, []string{filterRepo(3)}, census)
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
		t.Fatalf("committed = %v, want none", ids)
	}
	if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowReason != string(ReasonCensusClosureMismatch) {
		t.Fatalf("evidence_round = %#v, want %q", rounds, ReasonCensusClosureMismatch)
	}
}

func TestNarrowingRankKeepsTheMostSevereOutcome(t *testing.T) {
	t.Parallel()
	order := []string{"", narrowedToOne, narrowedToMany, narrowedToNone, narrowTruncated, narrowReadFailed}
	for i := 1; i < len(order); i++ {
		if narrowingRank(order[i]) <= narrowingRank(order[i-1]) {
			t.Fatalf("rank(%q) must exceed rank(%q)", order[i], order[i-1])
		}
	}
}

func TestResolveSubjects_CallerHintRoundUsesTheCallerNarrowingVisibility(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, []string{"acme/repo-01"}),
	}}
	request := callerHintRequest(subject, "workbench")
	request.RequestedScope.RepositorySlugs = []string{"acme/repo-01"}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{}, nil
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	events := tracer.evidenceRoundEvents()
	if len(events) != 1 || events[0].ShadowReason == string(ReasonScopedVisibility) {
		t.Fatalf("evidence_round = %+v, want the round past the scoped-visibility gate for a narrowed unrestricted principal", events)
	}
}
