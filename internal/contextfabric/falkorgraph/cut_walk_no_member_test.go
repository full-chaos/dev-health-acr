package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const cutBeforeMemberCode = "graph_walk_cut_before_member"

func cutBeforeMemberDetails(result contextfabric.GraphContext) []contextfabric.CoverageDetail {
	var out []contextfabric.CoverageDetail
	for _, d := range result.Coverage.Details {
		if string(d.Code) == cutBeforeMemberCode || string(d.Code) == "kind_census_truncated" {
			out = append(out, d)
		}
	}
	return out
}

func assertCutBeforeMember(t *testing.T, result contextfabric.GraphContext) {
	t.Helper()
	if result.Cohort != nil {
		t.Fatalf("cohort = %+v, want none: the read was cut before any member", result.Cohort)
	}
	details := cutBeforeMemberDetails(result)
	if len(details) != 1 {
		t.Fatalf("details = %+v, want exactly one cut detail", result.Coverage.Details)
	}
	d := details[0]
	if string(d.Code) != cutBeforeMemberCode {
		t.Fatalf("code = %q, want %q: a census that listed nothing is not a truncated census", d.Code, cutBeforeMemberCode)
	}
	if d.Kind != contextfabric.SubjectDeployment || d.Declared != nil || d.Served != nil || !d.Degrading {
		t.Fatalf("detail = %+v, want a degrading deployment detail with no census counts", d)
	}
	if want := "walk_cut_before_member:deployment"; d.Raw != want {
		t.Fatalf("raw = %q, want %q", d.Raw, want)
	}
	if !strings.Contains(d.Label, "cut before it reached any deployment") || strings.Contains(d.Label, "At least") {
		t.Fatalf("label = %q, want the sentence that names a cut read and no count", d.Label)
	}
	if !result.Coverage.Partial {
		t.Fatalf("coverage = %+v, want partial", result.Coverage)
	}
}

func TestAProjectWalkCutToNoMemberHasItsOwnCode(t *testing.T) {
	s := linkedIssues(6, 0)
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	adapter.config.MaxResults = 3
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, projectDeploymentsRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertCutBeforeMember(t, result)
}

func TestATeamWalkCutToNoMemberHasTheSameCode(t *testing.T) {
	s := projectSeed{served: map[string]string{}}
	s.nodes = append(s.nodes, seededNode{kind: "team", id: "team:anchor", label: "payments"})
	for i := 0; i < 6; i++ {
		repoID := s.repository(fmt.Sprintf("acme/owned-%02d", i), 0)
		s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", repoID, "team", "team:anchor", ""})
	}
	adapter := newFakeAdapter(t, seededGraphConn(s.nodes, s.edges))
	adapter.config.MaxResults = 3
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:anchor", Label: "payments"}
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, soleCommitRequest(anchor, contextfabric.CommitBasisStatistical))
	if err != nil {
		t.Fatal(err)
	}
	assertCutBeforeMember(t, result)
}
