package directread

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func noteProvider(kind contextfabric.FactKind, note string) *stubProvider {
	provider := perSubjectProvider(kind, contractsv1.ContextFabricSubjectTeam)
	inner := provider.read
	provider.read = func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result, err := inner(query)
		result.RequestNote = note
		return result, err
	}
	return provider
}

func readNotes(t *testing.T, providers ...contextfabric.FactProvider) FactsResponse {
	t.Helper()
	team := subject(contractsv1.ContextFabricSubjectTeam, "team:a")
	kinds := make([]string, 0, len(providers))
	for _, provider := range providers {
		kinds = append(kinds, string(provider.Capability().Kind))
	}
	response, err := newTestFactsReader(t, teamGraph(team), providers...).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds:    kinds,
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: team.CanonicalID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestReadFactsServesTheRequestNoteBesideCoverage(t *testing.T) {
	response := readNotes(t, noteProvider(contextfabric.FactPullRequests, "devhealthfacts: 2 of 5 requested teams own no repository"))
	if len(response.RequestNotes) != 1 {
		t.Fatalf("request_notes = %+v, want one entry", response.RequestNotes)
	}
	got := response.RequestNotes[0]
	if got.Kind != "pull_requests" || got.Note != "2 of 5 requested teams own no repository" {
		t.Errorf("request note = %+v", got)
	}
	for _, row := range response.Coverage {
		if strings.Contains(row.Reason, "2 of 5") {
			t.Errorf("coverage row carries the request note: %q", row.Reason)
		}
	}
}

func TestReadFactsOmitsRequestNotesWhenNoKindHasOne(t *testing.T) {
	response := readNotes(t, noteProvider(contextfabric.FactPullRequests, "  "))
	if response.RequestNotes != nil {
		t.Errorf("request_notes = %+v, want none for a blank note", response.RequestNotes)
	}
	if strings.Contains(mustJSON(t, response), "request_notes") {
		t.Error("request_notes key is served although no kind has a note")
	}
}

func TestReadFactsRequestNotesNameOnlyTheKindsThatHaveOne(t *testing.T) {
	response := readNotes(t,
		noteProvider(contextfabric.FactPullRequests, ""),
		noteProvider(contextfabric.FactBlockers, "1 of 1 requested teams has no blockers read"),
	)
	var served []RequestNote
	if err := json.Unmarshal([]byte(mustJSON(t, response.RequestNotes)), &served); err != nil {
		t.Fatal(err)
	}
	if len(served) != 1 || served[0].Kind != "blockers" {
		t.Errorf("request_notes = %+v, want only blockers", served)
	}
}

func TestRequestNotesServeOnlyKindsOfTheClosedFactKindVocabulary(t *testing.T) {
	notes := func(kind contextfabric.FactKind) []RequestNote {
		return requestNotes(readPlan{kinds: []contextfabric.FactKind{kind}}, contextfabric.CanonicalFactBundle{
			Outcomes: contextfabric.FactOutcomeLedger{kind: {RequestNote: "1 of 2 requested teams"}},
		})
	}
	for _, known := range contractsv1.ContextFabricFactKindVocabulary() {
		if got := notes(contextfabric.FactKind(known)); len(got) != 1 || got[0].Kind != string(known) {
			t.Errorf("vocabulary kind %s served %+v", known, got)
		}
	}
	if got := notes("kind_outside_the_vocabulary"); got != nil {
		t.Errorf("an off-set kind was served: %+v", got)
	}
}
