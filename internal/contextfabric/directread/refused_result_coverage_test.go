package directread

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A provider result the registry refuses (here: a fact with no evidence on a
// kind that requires it) is a kind the read could not serve. Its coverage row
// says unavailable; it never reads as "read, no fact".
func TestARefusedProviderResultIsUnavailableNeverReadNoFact(t *testing.T) {
	provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: []contextfabric.CanonicalFact{{
			Kind: contextfabric.FactHealth, Subject: query.Subjects[0],
			Fields: map[string]contextfabric.FactValue{"repo_count": intValue(1)},
		}}}, nil
	}}
	if !provider.capability.RequiresEvidence {
		t.Fatal("the fixture capability does not require evidence: the result would not be refused")
	}

	response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}})

	if err != nil {
		t.Fatalf("Read() error = %v, want a response whose coverage names the refused kind", err)
	}
	row := coverageOf(t, response, "health", repoA)
	if row.Outcome != OutcomeUnavailable {
		t.Fatalf("outcome = %q, want %q", row.Outcome, OutcomeUnavailable)
	}
	if row.KindState != string(contextfabric.SourceUnavailable) || !strings.Contains(row.Reason, "rejected") {
		t.Fatalf("row = %+v, want kind state unavailable with the rejection reason", row)
	}
	if len(response.Facts) != 0 {
		t.Fatalf("facts = %d, want none served from a refused result", len(response.Facts))
	}
	if response.Status != StatusPartial {
		t.Fatalf("status = %q, want %q", response.Status, StatusPartial)
	}
}
