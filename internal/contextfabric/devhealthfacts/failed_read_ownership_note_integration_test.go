package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The health table is missing, so the health read fails while one of three
// requested teams owns no repository: read_facts serves the ownership note in
// request_notes and a partial status, not a complete one.
func TestReadFactsServesTheOwnershipNoteWhenTheHealthReadFailsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_graph_issue_pr", "git_pull_requests") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "org-read-facts-failed-kind-notes"
	seedProjectLinkedOrg(t, ctx, direct, orgID)

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("fact registry: %v", err)
	}
	gate := directread.NewSubjectGate(admitEverySubject{}, nil)
	reader := directread.NewFactsReader(gate, directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000000003"), storage.Principal{OrgID: orgID, Subject: "request-notes", CredentialID: "request-notes"}, directread.FactsRequest{
		Kinds: []string{"health"},
		Subjects: []directread.RequestSubject{
			{Kind: "team", CanonicalID: "team:team-owner"},
			{Kind: "team", CanonicalID: "team:team-both"},
			{Kind: "team", CanonicalID: "team:team-nothing"},
		},
	})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	if response.Status != directread.StatusPartial {
		t.Fatalf("status = %q, want %q: the health read failed", response.Status, directread.StatusPartial)
	}
	if len(response.RequestNotes) != 1 {
		encoded, _ := json.Marshal(response)
		t.Fatalf("request_notes = %+v, want exactly one entry for the health kind; response = %s", response.RequestNotes, encoded)
	}
	note := response.RequestNotes[0]
	if note.Kind != "health" || !strings.HasPrefix(note.Note, "1 of 3 requested teams own no repository") {
		t.Fatalf("request note = %+v, want health with the 1 of 3 sentence", note)
	}
	if strings.Contains(note.Note, "devhealthfacts:") {
		t.Fatalf("request note carries the provider package label: %q", note.Note)
	}
	for _, row := range response.Coverage {
		if strings.Contains(row.Reason, "1 of 3") {
			t.Fatalf("coverage row %s/%s carries the request note: %q", row.Kind, row.Subject.CanonicalID, row.Reason)
		}
	}
}
