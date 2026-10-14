package directread

import (
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const emptyReachedPullRequests = 100

func emptyReachedTargets() []contextfabric.SubjectRef {
	targets := make([]contextfabric.SubjectRef, 0, emptyReachedPullRequests)
	for i := 1; i <= emptyReachedPullRequests; i++ {
		targets = append(targets, contextfabric.SubjectRef{
			Kind:        contractsv1.ContextFabricSubjectPullRequest,
			CanonicalID: fmt.Sprintf("pull_request:11111111-1111-1111-1111-111111111111:%d", i),
			Label:       fmt.Sprintf("PR #%d", i),
		})
	}
	return targets
}

// Pull request facts carry a state scalar and, only where an issue link
// exists, a project_breakdown table. The first pull request holds a table.
func pullRequestStateProvider() *stubProvider {
	capability := contextfabric.FactCapability{
		Kind: contextfabric.FactPullRequests, Name: "pull_requests_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectTeam},
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields: []contextfabric.FactFieldDeclaration{
			{Name: "state", Type: contextfabric.FactFieldString},
			{Name: "project_breakdown", Type: contextfabric.FactFieldTable, Columns: []contextfabric.FactColumnDeclaration{
				{Name: "scope", Type: contextfabric.FactFieldString},
				{Name: "link_tier", Type: contextfabric.FactFieldString},
			}},
		},
	}
	return &stubProvider{capability: capability, read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, subject := range query.Subjects {
			if subject.Kind != contractsv1.ContextFabricSubjectPullRequest {
				continue
			}
			fields := map[string]contextfabric.FactValue{"state": strValue("merged")}
			if strings.HasSuffix(subject.CanonicalID, ":1") {
				fields["project_breakdown"] = contextfabric.RowsFactValue([]contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{
					"scope": strValue("project"), "link_tier": strValue("native"),
				}}})
			}
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactPullRequests, Subject: subject, Fields: fields})
		}
		return result, nil
	}}
}

func readReachedPullRequests(t *testing.T, tables string) (FactsResponse, int) {
	t.Helper()
	expander := &scriptedExpander{expand: expandTo(emptyReachedTargets()...)}
	reader := directExpandingReader(t, graphOfOrgA(), expander, nil, pullRequestStateProvider())
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"pull_requests"}, Tables: tables,
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response, len(mustJSON(t, response))
}

func emptyFacts(response FactsResponse) int {
	count := 0
	for _, fact := range response.Facts {
		if len(fact.Fields) == 0 && len(fact.Tables) == 0 && len(fact.Withheld) == 0 {
			count++
		}
	}
	return count
}

func emptyFactNote(response FactsResponse) string {
	for _, note := range response.RequestNotes {
		if note.Kind == "pull_requests" && strings.Contains(note.Note, "not listed") {
			return note.Note
		}
	}
	return ""
}

// Under tables only, 99 of 100 reached pull requests serve no field: they are
// left out and counted once; the one with a table is still served.
func TestReachedFactsServingNoFieldAreCountedNotListed(t *testing.T) {
	response, size := readReachedPullRequests(t, TablesOnly)
	if empty := emptyFacts(response); empty != 0 {
		t.Fatalf("%d empty facts served (%d facts, %d bytes)", empty, len(response.Facts), size)
	}
	if len(response.Facts) != 1 || response.Facts[0].Subject.Label != "PR #1" || len(response.Facts[0].Tables) != 1 {
		t.Fatalf("the pull request with a table is not the one served fact: %s", mustJSON(t, response.Facts))
	}
	note := emptyFactNote(response)
	if !strings.Contains(note, "99 reached pull_requests facts are not listed") || !strings.Contains(note, `tables mode "only"`) {
		t.Fatalf("count and reason missing: %q", note)
	}
	if size > 3000 {
		t.Fatalf("response %d bytes, bound 3000", size)
	}
	validateAgainst(t, "mcp_read_facts_response.v1.schema.json", []byte(mustJSON(t, response)))
}

// A mode that serves the scalar lists every fact and counts none.
func TestReachedFactsWithFieldsAreAllListed(t *testing.T) {
	response, _ := readReachedPullRequests(t, TablesInclude)
	if len(response.Facts) != emptyReachedPullRequests || emptyFactNote(response) != "" {
		t.Fatalf("%d facts, note %q", len(response.Facts), emptyFactNote(response))
	}
}
