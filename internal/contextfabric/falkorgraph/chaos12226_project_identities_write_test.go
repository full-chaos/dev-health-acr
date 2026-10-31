package falkorgraph

import (
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12226: the owned work-item entity write is the one writer of
// authorization_project_identities. It writes the stated set as a list (an
// empty list kept empty), or null (the key removed) when nothing was
// stated. No stub, no relationship endpoint and no other kind writes it.

func chaos12226WorkItemEntity(identities *[]string) contextfabric.EntityProjection {
	return contextfabric.EntityProjection{
		Subject:           contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:LIN-1", Label: "LIN-1"},
		Authorization:     contextfabric.AuthorizationScope{RepositorySlugs: []string{"acr-context-fabric:no-repository"}},
		ProjectIdentities: identities,
		EvidenceRefIDs:    []string{"evidence_12226_0001"},
		ObservedAt:        time.Unix(1, 0).UTC(),
		SourceVersion:     "ops-v1",
	}
}

func chaos12226OwnedAttrs(entity contextfabric.EntityProjection) map[string]interface{} {
	return subjectMergeAttrs(entity.Subject, entity.Authorization, entity.EvidenceRefIDs, entity.ObservedAt, entity.ValidFrom, entity.ValidTo, entity.SourceVersion, &entity, false)
}

func TestTheWorkItemWriteAssertsItsProjectIdentities(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		identities *[]string
		want       interface{}
	}{
		"not stated removes the key": {identities: nil, want: nil},
		"stated empty stays empty":   {identities: &[]string{}, want: []string{}},
		"stated set, sorted":         {identities: &[]string{"project.v2:jira:ENG", "project.v2:github:P-1"}, want: []string{"project.v2:github:P-1", "project.v2:jira:ENG"}},
	}
	for name, tc := range cases {
		attrs := chaos12226OwnedAttrs(chaos12226WorkItemEntity(tc.identities))
		got, present := attrs[propAuthzProjectIdentities]
		if !present {
			t.Fatalf("%s: %s missing from the owned write; it must be asserted either way", name, propAuthzProjectIdentities)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %s = %#v, want %#v", name, propAuthzProjectIdentities, got, tc.want)
		}
		// Repository authorization is unchanged by the new key.
		if !reflect.DeepEqual(attrs[propAuthzRepos], []string{"acr-context-fabric:no-repository"}) {
			t.Fatalf("%s: %s = %#v, want the unchanged repository sentinel", name, propAuthzRepos, attrs[propAuthzRepos])
		}
		if attrs[propAuthzProjects] != "*" {
			t.Fatalf("%s: legacy %s = %#v, want it unchanged (\"*\")", name, propAuthzProjects, attrs[propAuthzProjects])
		}
	}
}

func TestOnlyTheOwnedWorkItemWriteTouchesProjectIdentities(t *testing.T) {
	t.Parallel()
	stated := &[]string{"project.v2:github:P-1"}
	// A referenced stub of the same work item (a relationship endpoint).
	work := chaos12226WorkItemEntity(stated)
	stub := subjectMergeAttrs(work.Subject, work.Authorization, work.EvidenceRefIDs, work.ObservedAt, nil, nil, work.SourceVersion, nil, false)
	if _, present := stub[propAuthzProjectIdentities]; present {
		t.Fatalf("referenced stub wrote %s; only the owned work-item write may", propAuthzProjectIdentities)
	}
	// An owned write of another kind never writes it, stated or not.
	team := chaos12226WorkItemEntity(stated)
	team.Subject = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	team.Authorization = contextfabric.AuthorizationScope{TeamIDs: []string{"platform"}}
	if _, present := chaos12226OwnedAttrs(team)[propAuthzProjectIdentities]; present {
		t.Fatalf("owned team write wrote %s; only a work item carries it", propAuthzProjectIdentities)
	}
}
