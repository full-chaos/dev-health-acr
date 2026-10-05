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
		deps.LinkScopedWorkItems = func(_ context.Context, scope contextfabric.RequestedScope) ([]string, bool, error) {
			if len(scope.RepositorySlugs) != 1 || scope.RepositorySlugs[0] != linkScopeSlug {
				return nil, false, fmt.Errorf("walked scope %v, want the request's", scope.RepositorySlugs)
			}
			return ids, complete, err
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
