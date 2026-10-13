package directread

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCatalogRelationshipsSayRepositoryDependenciesAreNotCollected(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	sections, _ := ParseCatalogSections("relationships")
	policy, err := DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	caller := CatalogCaller{
		PrincipalClass: ClassUnrestricted, Scopes: []string{"context:read", "evidence:read"},
		GateComposed: true, RelationshipsServable: true, GraphQL: policy,
	}
	got := BuildDataCatalog(cat, caller, sections)
	if got.Relationships == nil {
		t.Fatal("relationships section missing")
	}
	note := strings.ToLower(got.Relationships.Note)
	for _, want := range []string{"repositories", "services", "dependency", "not collected"} {
		if !strings.Contains(note, want) {
			t.Errorf("relationships note must say repository and service dependency edges are not collected; missing %q in %q", want, got.Relationships.Note)
		}
	}
	for _, rt := range got.Relationships.Types {
		if strings.Contains(strings.ToLower(rt.Type), "depend") {
			t.Errorf("no dependency edge type may be advertised, got %s", rt.Type)
		}
	}
}

func TestReadRelationshipsNamesUncollectedDependencyEdgesPerSubjectKind(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
	reader, _ := newRelReader(graph, &now)
	cases := []struct {
		name    string
		subject RelationshipsSubject
		want    []string
	}{
		{"repository with no collected dependency edges", RelationshipsSubject{Kind: "repository", CanonicalID: repoA.CanonicalID}, []string{"repository_dependency"}},
		{"team", RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, nil},
		{"work item", RelationshipsSubject{Kind: "work_item", CanonicalID: workA.CanonicalID}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := reader.Read(relCtx(tc.name), unrestricted, RelationshipsRequest{Subject: tc.subject})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var served struct {
				NotCollected []string `json:"not_collected"`
			}
			if err := json.Unmarshal(encoded, &served); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(served.NotCollected, tc.want) {
				t.Fatalf("served not_collected = %v, want %v (edges returned: %d)", served.NotCollected, tc.want, len(response.Edges))
			}
		})
	}
}
