package graphrank

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The work-item census under a requested repository scope counts the handle's
// satisfiers the scope's link walk reaches, never the satisfiers whose own
// repository is in the scope.

const (
	linkScopeQuestion = "What is the state of CHAOS-77?"
	linkScopeSlug     = "acme/svc"
	linkScopeItems    = 26
)

// linkScopeItem is one of 26 work items that carry the key CHAOS-77: item 0
// has no repository of its own, item 1 belongs to acme/other, the rest to
// repositories of their own; none belongs to the scoped acme/svc.
func linkScopeItem(i int) string { return fmt.Sprintf("work_item.v2:r-%02d:linear:CHAOS-77", i) }

func linkScopeRepos(i int) []string {
	switch i {
	case 0:
		return []string{"acr-context-fabric:no-repository"}
	case 1:
		return []string{"acme/other"}
	}
	return []string{fmt.Sprintf("acme/own-%02d", i)}
}

func linkScopeBackend() *fakeGraphBackend {
	var nodes []CandidateNode
	hints := map[string]CandidateNode{}
	for i := 0; i < linkScopeItems; i++ {
		node := candidateNode(contextfabric.SubjectWorkItem, linkScopeItem(i), "CHAOS-77", 0.5, linkScopeRepos(i))
		nodes = append(nodes, node)
		hints[SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: linkScopeItem(i)})] = node
	}
	return &fakeGraphBackend{
		enableAliasLookup:   true,
		aliasLookupComplete: true,
		searchResults:       map[string][]CandidateNode{"CHAOS-77": nodes},
		searchTruncated:     true,
		exactHints:          hints,
	}
}

type linkScopeCensusProbe struct {
	filters [][]string
}

// census lists every work item that carries the key, over the organization.
func (p *linkScopeCensusProbe) census(ctx context.Context, _ string, kind CensusKind, handleValue string, handleBound bool, _ contextfabric.SubjectKind, _ string, _ bool) (CensusOutcome, error) {
	p.filters = append(p.filters, CensusRepositoryFilterFrom(ctx))
	if kind != contextfabric.SubjectWorkItem || !handleBound || handleValue != "CHAOS-77" {
		return CensusOutcome{CensusReadAt: time.Now().UTC()}, nil
	}
	ids := make([]string, 0, linkScopeItems)
	for i := 0; i < linkScopeItems; i++ {
		ids = append(ids, linkScopeItem(i))
	}
	return CensusOutcome{Count: len(ids), CensusReadAt: time.Now().UTC(), SatisfierCanonicalIDs: ids}, nil
}

func walkedPopulation(ids []string, complete bool, err error) func(*ResolveDeps) {
	return func(deps *ResolveDeps) {
		deps.LinkScopedWorkItems = func(_ context.Context, scope contextfabric.RequestedScope) (map[string]string, bool, error) {
			if len(scope.RepositorySlugs) != 1 || scope.RepositorySlugs[0] != linkScopeSlug {
				return nil, false, fmt.Errorf("walked scope %v, want the request's", scope.RepositorySlugs)
			}
			tiers := map[string]string{}
			for _, id := range ids {
				tiers[id] = "native"
			}
			return tiers, complete, err
		}
	}
}

func resolveLinkScoped(t *testing.T, principal storage.Principal, mutate ...func(*ResolveDeps)) (contextfabric.SubjectResolution, []ResolutionTraceEvent, *linkScopeCensusProbe) {
	t.Helper()
	deps := linkScopeBackend().deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	probe := &linkScopeCensusProbe{}
	deps.CensusFunc = probe.census
	for _, m := range mutate {
		m(&deps)
	}
	request := testRequest()
	request.Question = linkScopeQuestion
	request.RequestedScope.RepositorySlugs = []string{linkScopeSlug}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted("CHAOS-77"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return resolution, narrowingEvents(tracer), probe
}

func TestAWorkItemCensusUnderARequestedScopeCountsTheLinkedSatisfiers(t *testing.T) {
	t.Parallel()
	open := storage.Principal{OrgID: "org_1"}
	for _, c := range []struct {
		name      string
		walk      func(*ResolveDeps)
		committed string
		outcome   ShadowOutcome
		reason    DegradationReason
	}{
		{"a repository-less item linked into the scope", walkedPopulation([]string{linkScopeItem(0)}, true, nil), linkScopeItem(0), ShadowWouldCommit, ""},
		{"an item of another repository linked into the scope", walkedPopulation([]string{linkScopeItem(1)}, true, nil), linkScopeItem(1), ShadowWouldCommit, ""},
		{"a walked issue that does not carry the key", walkedPopulation([]string{"work_item.v2:r-99:linear:CHAOS-78"}, true, nil), "", ShadowWouldClarify, ""},
		{"two linked items share the key", walkedPopulation([]string{linkScopeItem(0), linkScopeItem(1)}, true, nil), "", ShadowWouldClarify, ""},
		{"a cut walk", walkedPopulation([]string{linkScopeItem(0)}, false, nil), "", ShadowWouldClarify, ReasonCensusError},
		{"a failed walk", walkedPopulation(nil, false, errors.New("graph down")), "", ShadowWouldClarify, ReasonCensusError},
		{"no walk wired", func(*ResolveDeps) {}, "", ShadowWouldClarify, ReasonCensusError},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			resolution, rounds, probe := resolveLinkScoped(t, open, c.walk)
			var committed string
			for _, s := range resolution.Committed {
				if s.Kind == contextfabric.SubjectWorkItem {
					committed = s.CanonicalID
				}
			}
			if committed != c.committed {
				t.Fatalf("committed %q, want %q (rounds %#v)", committed, c.committed, rounds)
			}
			if len(rounds) != 1 || rounds[0].ShadowOutcome != string(c.outcome) || (c.reason != "" && rounds[0].ShadowReason != string(c.reason)) {
				t.Fatalf("evidence_round = %#v, want one %s %s", rounds, c.outcome, c.reason)
			}
			for _, f := range probe.filters {
				if f != nil {
					t.Fatalf("census filter %v: a work item census is never filtered on its own repository", f)
				}
			}
		})
	}
}

// TestALinkScopedSatisfierStillMeetsTheCallersGrants: the census does not run
// for a restricted caller, and the commit of a link-scoped satisfier tests the
// issue against the rest of the request.
func TestALinkScopedSatisfierStillMeetsTheCallersGrants(t *testing.T) {
	t.Parallel()
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{linkScopeSlug}}
	resolution, _, probe := resolveLinkScoped(t, restricted, walkedPopulation([]string{linkScopeItem(1)}, true, nil))
	if len(probe.filters) != 0 {
		t.Fatalf("census ran %d times for a restricted caller, want none", len(probe.filters))
	}
	for _, s := range resolution.Committed {
		if s.Kind == contextfabric.SubjectWorkItem {
			t.Fatalf("committed %s for a restricted caller", s.CanonicalID)
		}
	}
	if !AuthorizedThroughLink(storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{RepositorySlugs: []string{linkScopeSlug}}, map[string]interface{}{"authorization_repositories": []string{"acme/other"}}) {
		t.Fatal("an issue of another repository is refused under a requested scope: the scope must be tested on the link's pull request")
	}
	if AuthorizedThroughLink(storage.Principal{OrgID: "org_1", RepositoryScopes: []string{linkScopeSlug}}, contextfabric.RequestedScope{RepositorySlugs: []string{linkScopeSlug}}, map[string]interface{}{"authorization_repositories": []string{"acme/other"}}) {
		t.Fatal("an issue of an ungranted repository is admitted: the grants still apply to the issue")
	}
	if AuthorizedThroughLink(storage.Principal{OrgID: "org_1"}, contextfabric.RequestedScope{RepositorySlugs: []string{linkScopeSlug}, ProjectIDs: []string{"p-1"}}, map[string]interface{}{"authorization_repositories": []string{"acme/other"}, "authorization_projects": []string{"p-2"}}) {
		t.Fatal("an issue outside the requested project is admitted: only the repository part of the scope follows the link")
	}
}

// TestTheScopedCensusRecordsItsAdmittedSatisfiersForThisCallOnly: the
// satisfiers the link walk kept are recorded, with their tier, on the call's
// context, and nowhere else.
func TestTheScopedCensusRecordsItsAdmittedSatisfiersForThisCallOnly(t *testing.T) {
	t.Parallel()
	scope := &linkScope{read: func(context.Context) (map[string]string, bool, error) {
		return map[string]string{"a": "explicit_text", "b": "native"}, true, nil
	}}
	ctx := contextfabric.WithWorkItemCensusRepositoryScopeRecorder(context.Background())
	outcome, err := withinLinkScope(ctx, scope, CensusOutcome{Count: 2, SatisfierCanonicalIDs: []string{"a", "c"}})
	if err != nil || outcome.Count != 1 || outcome.SatisfierCanonicalID != "a" || !outcome.RepositoryFilterApplied {
		t.Fatalf("outcome %+v err %v, want the one walked satisfier", outcome, err)
	}
	if got := contextfabric.WorkItemCensusLinkedSatisfiers(ctx); len(got) != 1 || got["a"] != "explicit_text" {
		t.Fatalf("recorded %v, want only the kept satisfier with its tier", got)
	}
	if got := contextfabric.WorkItemCensusLinkedSatisfiers(contextfabric.WithWorkItemCensusRepositoryScopeRecorder(context.Background())); len(got) != 0 {
		t.Fatalf("another call's context holds %v", got)
	}
}

// TestAReuseRecheckDoesNotCarryALinkAdmission: answer reuse rechecks a stored
// answer's subjects as exact hints under the new request's scope. The hint
// path tests a work item by its own node, so a work item the scoped census
// admitted through its link is not re-admitted by the recheck: the stored
// answer is not reused and the turn runs fresh, where the census and its link
// walk run again. Nothing of the earlier admission is carried.
func TestAReuseRecheckDoesNotCarryALinkAdmission(t *testing.T) {
	t.Parallel()
	deps := linkScopeBackend().deps()
	walkedPopulation([]string{linkScopeItem(1)}, true, nil)(&deps)
	request := testRequest()
	request.Question = linkScopeQuestion
	request.RequestedScope.RepositorySlugs = []string{linkScopeSlug}
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectWorkItem, ID: linkScopeItem(1), Source: string(hintsource.AnswerReuseAuthorizationRecheck)}}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("CHAOS-77"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range resolution.Committed {
		if s.CanonicalID == linkScopeItem(1) {
			t.Fatalf("the recheck re-admitted %s by its hint: a link admission must be re-derived by the census, not carried", s.CanonicalID)
		}
	}
}

// TestALinkWalkThatOutlastsTheRoundBudgetIsNoCensus: a link walk still
// running when the evidence round's deadline passes is a census error, never
// a smaller count: nothing is committed and the round says the census is
// incomplete.
func TestALinkWalkThatOutlastsTheRoundBudgetIsNoCensus(t *testing.T) {
	t.Parallel()
	walkOutlastsTheBudget := func(deps *ResolveDeps) {
		deps.LinkScopedWorkItems = func(ctx context.Context, _ contextfabric.RequestedScope) (map[string]string, bool, error) {
			if _, ok := ctx.Deadline(); !ok {
				return nil, false, errors.New("the link walk ran without the round's deadline")
			}
			<-ctx.Done()
			return map[string]string{linkScopeItem(0): "native"}, true, ctx.Err()
		}
	}
	start := time.Now()
	resolution, rounds, _ := resolveLinkScoped(t, storage.Principal{OrgID: "org_1"}, walkOutlastsTheBudget)
	if elapsed := time.Since(start); elapsed > evidenceRoundDeadline+2*time.Second {
		t.Fatalf("resolution took %s, want the round cut at its %s budget", elapsed, evidenceRoundDeadline)
	}
	for _, s := range resolution.Committed {
		if s.Kind == contextfabric.SubjectWorkItem {
			t.Fatalf("committed %s from a walk cut by the budget", s.CanonicalID)
		}
	}
	if len(rounds) != 1 || rounds[0].ShadowOutcome != string(ShadowWouldClarify) || rounds[0].ShadowReason != string(ReasonCensusError) {
		t.Fatalf("evidence_round = %#v, want one would_clarify census_error", rounds)
	}
}
