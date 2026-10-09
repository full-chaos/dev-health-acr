package directread

import (
	"context"
	"slices"
	"testing"
)

// A list of kind organization names exactly the caller's own organization, for
// an unrestricted and a repository-bound caller alike, without a graph read.
func TestFindOrganizationListNamesOnlyTheCallersOwnOrganization(t *testing.T) {
	for name, principal := range map[string]string{"unrestricted": "", "repository-bound": "acme/a"} {
		graph := threeRepoGraph()
		var scopes []string
		if principal != "" {
			scopes = []string{principal}
		}
		p := lookupPrincipal(orgA, scopes...)
		got, err := newLookup(graph, nil).Find(context.Background(), p, FindRequest{Kind: "organization"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(ids(got.Subjects), []string{"organization:" + orgA}) {
			t.Fatalf("%s: subjects = %v, want exactly the caller's organization", name, ids(got.Subjects))
		}
		if got.Status != FindComplete || got.Population.TotalKnown != 1 || got.Population.Kind != "organization" || !got.Page.Complete {
			t.Fatalf("%s: response = %+v", name, got)
		}
		if graph.listCalls != 0 || graph.nameCalls != 0 {
			t.Fatalf("%s: the graph was read (list=%d name=%d); the organization row names the caller's organization and reads no data", name, graph.listCalls, graph.nameCalls)
		}
	}
}
