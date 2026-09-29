package directread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// modesGraph is the S3a fake edge graph plus the two lookup reads find_subjects
// needs (empty here: these tests exercise owned_by and handle) and the node
// read for handle labels.
type modesGraph struct {
	*fakeEdgeGraph
	labels map[string]string
}

func (g *modesGraph) ListSubjectsByKind(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, string, int) (LookupPage, error) {
	return LookupPage{}, nil
}
func (g *modesGraph) FindSubjectsByExactName(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, []string) (LookupPage, error) {
	return LookupPage{}, nil
}
func (g *modesGraph) ReadSubjectNodes(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]LookupNode, error) {
	var out []LookupNode
	if principal.OrgID != g.org {
		return nil, nil
	}
	for _, s := range subjects {
		if _, ok := g.nodes[graphrank.SubjectKey(s)]; ok {
			out = append(out, LookupNode{Kind: string(s.Kind), CanonicalID: s.CanonicalID, Label: g.labels[s.CanonicalID]})
		}
	}
	return out, nil
}

func newModesLookup(g *modesGraph, census graphrank.CensusFunc) *SubjectLookup {
	gate := NewSubjectGate(g, nil)
	return NewSubjectLookup(g, gate, nil).WithOwnershipAndHandles(g, census, g)
}

var workAsAttribution = subject(contractsv1.ContextFabricSubjectWorkItem, "work_item.v2:attributed")

// Team T owns repositories A (acme/a) and B (acme/b) and project Q; repository
// A is owned through two sources (two edges); a work item carries a team
// attribution edge of the same type (not ownership).
func ownershipGraph() *modesGraph {
	g := &modesGraph{fakeEdgeGraph: &fakeEdgeGraph{fakeGraph: graphOfOrgA()}}
	g.nodes[graphrank.SubjectKey(workAsAttribution)] = repos("acme/a")
	g.edges = []EdgeCandidate{
		edgeBetween("own-a-native", "OWNED_BY_TEAM", repoA, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/a"}}),
		edgeBetween("own-a-manual", "OWNED_BY_TEAM", repoA, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/a"}}),
		edgeBetween("own-b", "OWNED_BY_TEAM", repoB, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/b"}}),
		edgeBetween("own-q", "OWNED_BY_TEAM", projectQ, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/a"}}),
		edgeBetween("attr-w", "OWNED_BY_TEAM", workAsAttribution, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/a"}}),
	}
	return g
}

func foundIDs(response FindResponse) string {
	ids := make([]string, 0, len(response.Subjects))
	for _, s := range response.Subjects {
		ids = append(ids, s.CanonicalID)
	}
	return strings.Join(ids, ",")
}

// T-owned (rule 1): a restricted caller gets the owned repositories and
// projects it may read, each once, and never a work-item attribution; the
// unrestricted caller gets every owned repository and project.
func TestChaos7126_OwnedByServesOwnedSubjectsThroughTheEdgeGate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal storage.Principal
		want      string
	}{
		{"restricted to acme/a", restrictedA, "project:q,repository:a"},
		{"unrestricted", unrestricted, "project:q,repository:a,repository:b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := newModesLookup(ownershipGraph(), nil).Find(relCtx("owned-"+tc.name), tc.principal, FindRequest{OwnedBy: teamT.CanonicalID})
			if err != nil {
				t.Fatal(err)
			}
			if got := foundIDs(response); got != tc.want || response.Status != FindComplete || response.Population.TotalKnown != len(response.Subjects) {
				t.Fatalf("owned_by = %s (%s, total %d), want %s", got, response.Status, response.Population.TotalKnown, tc.want)
			}
		})
	}
}

func TestChaos7126_OwnedByKindsAndRefusals(t *testing.T) {
	lookup := newModesLookup(ownershipGraph(), nil)
	response, err := lookup.Find(relCtx("kinds"), unrestricted, FindRequest{OwnedBy: teamT.CanonicalID, Kind: "repository"})
	if err != nil || foundIDs(response) != "repository:a,repository:b" {
		t.Fatalf("kind filter: %v %s", err, foundIDs(response))
	}
	if _, err := lookup.Find(relCtx("wi"), unrestricted, FindRequest{OwnedBy: teamT.CanonicalID, Kinds: []string{"work_item"}}); !errors.Is(err, ErrFindInvalidRequest) {
		t.Fatalf("work_item kind accepted: %v", err)
	}
	// A team the restricted caller may not read (U owns only acme/b) and a
	// team that does not exist give the same empty answer.
	for _, team := range []string{teamU.CanonicalID, "team:nope"} {
		response, err := lookup.Find(relCtx("t-"+team), restrictedA, FindRequest{OwnedBy: team})
		if err != nil || response.Status != FindEmpty || len(response.Subjects) != 0 {
			t.Fatalf("%s: %v %+v", team, err, response)
		}
	}
	if _, err := lookup.Find(relCtx("two"), unrestricted, FindRequest{OwnedBy: teamT.CanonicalID, Handle: "PR 1"}); !errors.Is(err, ErrFindInvalidRequest) {
		t.Fatalf("two modes accepted: %v", err)
	}
}

func TestChaos7126_OwnedByFailsClosed(t *testing.T) {
	g := ownershipGraph()
	g.pageErr = errors.New("boom")
	if _, err := newModesLookup(g, nil).Find(relCtx("fc"), restrictedA, FindRequest{OwnedBy: teamT.CanonicalID}); !errors.Is(err, ErrFindUnavailable) {
		t.Fatalf("edge page failure: %v", err)
	}
	g2 := ownershipGraph()
	g2.reachErr = errors.New("boom")
	if _, err := newModesLookup(g2, nil).Find(relCtx("fc2"), restrictedA, FindRequest{OwnedBy: teamT.CanonicalID}); !errors.Is(err, ErrFindUnavailable) {
		t.Fatalf("gate failure: %v", err)
	}
	plain := NewSubjectLookup(ownershipGraph(), NewSubjectGate(ownershipGraph(), nil), nil)
	if _, err := plain.Find(relCtx("nc"), restrictedA, FindRequest{OwnedBy: teamT.CanonicalID}); !errors.Is(err, ErrFindUnavailable) {
		t.Fatalf("no edge graph composed: %v", err)
	}
}

var (
	prA = subject(contractsv1.ContextFabricSubjectPullRequest, "pull_request.v2:a:532")
	prB = subject(contractsv1.ContextFabricSubjectPullRequest, "pull_request.v2:b:532")
)

func handleGraph() *modesGraph {
	g := &modesGraph{fakeEdgeGraph: &fakeEdgeGraph{fakeGraph: graphOfOrgA()}, labels: map[string]string{prA.CanonicalID: "PR 532 in a", prB.CanonicalID: "PR 532 in b"}}
	g.nodes[graphrank.SubjectKey(prA)] = repos("acme/a")
	g.nodes[graphrank.SubjectKey(prB)] = repos("acme/b")
	return g
}

type censusCall struct {
	org, value string
	kind       graphrank.CensusKind
}

func fixedCensus(outcome graphrank.CensusOutcome, err error, calls *[]censusCall) graphrank.CensusFunc {
	return func(_ context.Context, org string, kind graphrank.CensusKind, value string, _ bool, _ contextfabric.SubjectKind, _ string, _ bool) (graphrank.CensusOutcome, error) {
		*calls = append(*calls, censusCall{org: org, kind: kind, value: value})
		return outcome, err
	}
}

// T-handle (rule 1 and the leak rule): the census finds PR #532 in two
// repositories. The restricted caller sees one, as a COMPLETE answer with
// total_known 1: nothing in the response says a second one exists. The
// unrestricted caller gets ambiguous with both.
func TestChaos7126_HandleServesOnlyAdmittedCandidatesAndNeverTheCensusCount(t *testing.T) {
	two := graphrank.CensusOutcome{Count: 2, SatisfierCanonicalIDs: []string{prA.CanonicalID, prB.CanonicalID}}
	var calls []censusCall
	lookup := newModesLookup(handleGraph(), fixedCensus(two, nil, &calls))
	restricted, err := lookup.Find(relCtx("h-r"), restrictedA, FindRequest{Handle: "PR #532"})
	if err != nil {
		t.Fatal(err)
	}
	if foundIDs(restricted) != prA.CanonicalID || restricted.Status != FindComplete || restricted.Population.TotalKnown != 1 || restricted.Population.Truncated {
		t.Fatalf("restricted = %+v", restricted)
	}
	if restricted.Subjects[0].Match != MatchProviderKey || restricted.Subjects[0].Label != "PR 532 in a" {
		t.Fatalf("match/label = %+v", restricted.Subjects[0])
	}
	encoded, _ := json.Marshal(restricted)
	if strings.Contains(string(encoded), prB.CanonicalID) || strings.Contains(string(encoded), `"total_known":2`) {
		t.Fatalf("leak: %s", encoded)
	}
	if len(calls) != 1 || calls[0].org != orgA || calls[0].kind != contractsv1.ContextFabricSubjectPullRequest || calls[0].value != "532" {
		t.Fatalf("census calls = %+v", calls)
	}
	open, err := lookup.Find(relCtx("h-u"), unrestricted, FindRequest{Handle: "PR #532"})
	if err != nil || open.Status != FindAmbiguous || foundIDs(open) != prA.CanonicalID+","+prB.CanonicalID {
		t.Fatalf("unrestricted = %v %+v", err, open)
	}
}

func TestChaos7126_HandleCensusStates(t *testing.T) {
	var calls []censusCall
	for _, tc := range []struct {
		name      string
		outcome   graphrank.CensusOutcome
		err       error
		status    FindStatus
		truncated bool
		wantErr   error
	}{
		{"no satisfier", graphrank.CensusOutcome{Count: 0}, nil, FindEmpty, false, nil},
		{"one satisfier", graphrank.CensusOutcome{Count: 1, SatisfierCanonicalID: prA.CanonicalID}, nil, FindComplete, false, nil},
		{"over the census budget: no ids, no number", graphrank.CensusOutcome{Count: 1500}, nil, FindPartial, true, nil},
		{"read race", graphrank.CensusOutcome{Count: 1, ClosureMismatch: true, SatisfierCanonicalID: prA.CanonicalID}, nil, FindPartial, true, nil},
		{"census error", graphrank.CensusOutcome{}, errors.New("clickhouse down"), "", false, ErrFindUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := newModesLookup(handleGraph(), fixedCensus(tc.outcome, tc.err, &calls)).Find(relCtx("hs-"+tc.name), unrestricted, FindRequest{Handle: "PR 532"})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || response.Status != tc.status || response.Population.Truncated != tc.truncated {
				t.Fatalf("%v %+v", err, response)
			}
			encoded, _ := json.Marshal(response)
			if strings.Contains(string(encoded), "1500") {
				t.Fatalf("census count leaked: %s", encoded)
			}
		})
	}
}

func TestChaos7126_HandleGrammarAndComposition(t *testing.T) {
	var calls []censusCall
	lookup := newModesLookup(handleGraph(), fixedCensus(graphrank.CensusOutcome{}, nil, &calls))
	for _, handle := range []string{"payments", "PR 1 and PR 2", "COVID-19"} {
		if _, err := lookup.Find(relCtx("g-"+handle), unrestricted, FindRequest{Handle: handle}); !errors.Is(err, ErrFindInvalidRequest) {
			t.Fatalf("%q accepted: %v", handle, err)
		}
	}
	if _, err := lookup.Find(relCtx("g-kind"), unrestricted, FindRequest{Handle: "PR 1", Kind: "pull_request"}); !errors.Is(err, ErrFindInvalidRequest) {
		t.Fatalf("handle with kind accepted: %v", err)
	}
	if len(calls) != 0 {
		t.Fatal("a refused handle reached the census")
	}
	for name, census := range map[string]graphrank.CensusFunc{"CHAOS-12": nil} {
		plain := NewSubjectLookup(handleGraph(), NewSubjectGate(handleGraph(), nil), nil).WithOwnershipAndHandles(nil, census, nil)
		if _, err := plain.Find(relCtx("nc"), unrestricted, FindRequest{Handle: name}); !errors.Is(err, ErrFindUnavailable) {
			t.Fatalf("no census composed: %v", err)
		}
	}
}
