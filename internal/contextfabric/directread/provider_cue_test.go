package directread

import (
	"context"
	"testing"
)

func providerProject(id, label, provider string) LookupNode {
	n := projectNode(id, label)
	n.Match = MatchAlias
	n.Attributes = map[string]interface{}{"provider_" + provider: "native-" + id}
	return n
}

func TestFindNameNamesProviderWhenSameKindLabelsCollide(t *testing.T) {
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {nodes: []LookupNode{
		providerProject("project.v2:a", "CHAOS", "jira"),
		providerProject("project.v2:b", "CHAOS", "linear"),
	}}}}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Query: "CHAOS"})
	if err != nil || len(got.Subjects) != 2 {
		t.Fatalf("find = %+v, %v", got, err)
	}
	if got.Subjects[0].Provider != "jira" || got.Subjects[1].Provider != "linear" {
		t.Fatalf("providers = %q, %q; want jira, linear", got.Subjects[0].Provider, got.Subjects[1].Provider)
	}
}

func TestFindNameNamesProviderOfALoneLabel(t *testing.T) {
	a := providerProject("project.v2:a", "CHAOS", "jira")
	graph := &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {nodes: []LookupNode{a}}}}
	got, err := newLookup(graph, nil).Find(context.Background(), lookupPrincipal(orgA), FindRequest{Query: "CHAOS"})
	if err != nil || len(got.Subjects) != 1 || got.Subjects[0].Provider != "jira" {
		t.Fatalf("find = %+v, %v; want the single provider named", got, err)
	}
}
