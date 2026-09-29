package directread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// anchoredCensus answers per anchor: the organization-wide census (no
// anchor) sees `org`; a census anchored on a repository sees perRepo[id].
func anchoredCensus(org graphrank.CensusOutcome, perRepo map[string]graphrank.CensusOutcome, calls *[]censusCall) graphrank.CensusFunc {
	return func(_ context.Context, orgID string, kind graphrank.CensusKind, value string, _ bool, _ contextfabric.SubjectKind, anchor string, bound bool) (graphrank.CensusOutcome, error) {
		*calls = append(*calls, censusCall{org: orgID, kind: kind, value: value, anchor: anchor})
		if !bound {
			return org, nil
		}
		return perRepo[anchor], nil
	}
}

// r1 P1 (permanent): for a repository-restricted caller, nothing about a
// handle depends on rows outside its grant. The reviewer's three cases:
//
//   - over budget org-wide (1500 rows, none named) must not turn "empty"
//     into "partial";
//   - a hidden candidate (a PR in acme/b) must not cost a node read (the
//     timing seam) nor change the answer;
//   - the answer, and the work done, equal the no-match case.
func TestChaos7126_R1_RestrictedHandleDependsOnlyOnTheGrant(t *testing.T) {
	cases := map[string]graphrank.CensusOutcome{
		"no match":             {Count: 0},
		"over budget org-wide": {Count: 1500},
		"hidden candidate":     {Count: 1, SatisfierCanonicalID: prB.CanonicalID},
	}
	var responses []string
	for name, org := range cases {
		t.Run(name, func(t *testing.T) {
			var calls []censusCall
			g := handleGraph()
			response, err := newModesLookup(g, anchoredCensus(org, map[string]graphrank.CensusOutcome{repoB.CanonicalID: {Count: 1, SatisfierCanonicalID: prB.CanonicalID}}, &calls)).
				Find(relCtx("r1-"+name), restrictedA, FindRequest{Handle: "PR 532"})
			if err != nil {
				t.Fatal(err)
			}
			if response.Status != FindEmpty || response.Population.Truncated || len(response.Subjects) != 0 {
				t.Fatalf("restricted answer depends on hidden rows: %+v", response)
			}
			if g.nodeReads != 0 {
				t.Fatalf("a hidden candidate cost %d node reads (timing seam)", g.nodeReads)
			}
			for _, call := range calls {
				if call.anchor != repoA.CanonicalID {
					t.Fatalf("census outside the grant: %+v", calls)
				}
			}
			if len(calls) != 1 {
				t.Fatalf("census calls = %+v, want one anchored on repository:a", calls)
			}
			encoded, _ := json.Marshal(response)
			responses = append(responses, string(encoded))
		})
	}
	if len(responses) != len(cases) {
		t.Fatalf("only %d of %d cases produced an answer", len(responses), len(cases))
	}
	for _, r := range responses[1:] {
		if r != responses[0] {
			t.Fatalf("responses differ with hidden population:\n%s\n%s", responses[0], r)
		}
	}
}

// The restricted lookup still finds what IS in the grant, and the
// unrestricted caller keeps the one organization-wide census.
func TestChaos7126_R1_HandleInsideTheGrantAndOrgWideForUnrestricted(t *testing.T) {
	var calls []censusCall
	perRepo := map[string]graphrank.CensusOutcome{repoA.CanonicalID: {Count: 1, SatisfierCanonicalID: prA.CanonicalID}}
	lookup := newModesLookup(handleGraph(), anchoredCensus(graphrank.CensusOutcome{Count: 2, SatisfierCanonicalIDs: []string{prA.CanonicalID, prB.CanonicalID}}, perRepo, &calls))
	mine, err := lookup.Find(relCtx("r1-in"), restrictedA, FindRequest{Handle: "PR 532"})
	if err != nil || mine.Status != FindComplete || foundIDs(mine) != prA.CanonicalID {
		t.Fatalf("restricted in-grant: %v %+v", err, mine)
	}
	calls = nil
	open, err := lookup.Find(relCtx("r1-org"), unrestricted, FindRequest{Handle: "PR 532"})
	if err != nil || open.Status != FindAmbiguous || len(calls) != 1 || calls[0].anchor != "" {
		t.Fatalf("unrestricted: %v %+v calls=%+v", err, open, calls)
	}
}

// Past MaxHandleGrantRepositories readable repositories, handle mode is a
// typed refusal (scope_required), never widened to the organization and
// carrying no count.
func TestChaos7126_R1_HandleScopeBound(t *testing.T) {
	g := handleGraph()
	for i := 0; i < MaxHandleGrantRepositories; i++ {
		g.nodes[graphrank.SubjectKey(subject(contractsv1.ContextFabricSubjectRepository, fmt.Sprintf("repository:extra-%02d", i)))] = repos("acme/a")
	}
	var calls []censusCall
	_, err := newModesLookup(g, anchoredCensus(graphrank.CensusOutcome{}, nil, &calls)).Find(relCtx("r1-bound"), restrictedA, FindRequest{Handle: "PR 1"})
	if !errors.Is(err, ErrFindScopeRequired) || !errors.Is(err, ErrFindInvalidRequest) || len(calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(calls))
	}
	if strings.Contains(err.Error(), "51") {
		t.Fatalf("refusal carries a count: %v", err)
	}
}

// r1 P1 (permanent): owned_by's status does not depend on how many withheld
// edges the team has: 2100 withheld ownership edges plus one visible one
// answer complete, exactly as the visible one alone.
func TestChaos7126_R1_OwnedByStatusIgnoresWithheldPopulation(t *testing.T) {
	g := ownershipGraph()
	g.edges = g.edges[:1] // own-a-native only
	for i := 0; i < 2100; i++ {
		repo := subject(contractsv1.ContextFabricSubjectRepository, fmt.Sprintf("repository:hidden-%04d", i))
		g.nodes[graphrank.SubjectKey(repo)] = repos("acme/b")
		g.edges = append(g.edges, edgeBetween(fmt.Sprintf("own-h-%04d", i), "OWNED_BY_TEAM", repo, teamT, map[string]interface{}{"authorization_repositories": []string{"acme/b"}}))
	}
	response, err := newModesLookup(g, nil).Find(relCtx("r1-owned"), restrictedA, FindRequest{OwnedBy: teamT.CanonicalID})
	if err != nil || response.Status != FindComplete || response.Population.Truncated || foundIDs(response) != repoA.CanonicalID {
		t.Fatalf("owned_by with hidden population: %v %+v", err, response)
	}
}

// An incomplete grant listing is the same typed refusal, never a widening.
func TestChaos7126_R1_HandleIncompleteGrantIsRefused(t *testing.T) {
	g := handleGraph()
	for i := 0; i <= MaxGrantedRepositories; i++ {
		g.nodes[graphrank.SubjectKey(subject(contractsv1.ContextFabricSubjectRepository, fmt.Sprintf("repository:many-%03d", i)))] = repos("acme/a")
	}
	var calls []censusCall
	_, err := newModesLookup(g, anchoredCensus(graphrank.CensusOutcome{}, nil, &calls)).Find(relCtx("r1-incomplete"), restrictedA, FindRequest{Handle: "PR 1"})
	if !errors.Is(err, ErrFindScopeRequired) || len(calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(calls))
	}
}
