package directread

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func workloadCapability(t *testing.T) contextfabric.FactCapability {
	t.Helper()
	for _, candidate := range devhealthfacts.NewProviders(nil) {
		if candidate.Capability().Kind == contextfabric.FactWorkload {
			return candidate.Capability()
		}
	}
	t.Fatal("no workload provider")
	return contextfabric.FactCapability{}
}

func teamWorkloadFact(team contextfabric.SubjectRef) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{
		Kind: contextfabric.FactWorkload, Subject: team,
		Fields: map[string]contextfabric.FactValue{
			"basis":                strValue("capacity_forecast"),
			"computed_at":          strValue("2026-09-28T06:00:00Z"),
			"throughput_mean":      numValue(4.5),
			"throughput_stddev":    numValue(1.25),
			"insufficient_history": contextfabric.BooleanFactValue(false),
			"high_variance":        contextfabric.BooleanFactValue(false),
			"backlog_size":         intValue(31),
			"forecast_p50_days":    intValue(12),
		},
		EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "t")},
		SourceState:    contextfabric.SourceAvailable,
	}
}

func TestRestrictedCallerReadsTeamForecastLabelledAsAllOwnedRepositories(t *testing.T) {
	provider := &stubProvider{capability: workloadCapability(t), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		facts := []contextfabric.CanonicalFact{}
		for _, s := range query.Subjects {
			facts = append(facts, teamWorkloadFact(s))
		}
		return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable}, nil
	}}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	response, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{
		Kinds:    []string{"workload"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(response.Facts) != 1 {
		t.Fatalf("facts = %d, want 1: %s", len(response.Facts), mustJSON(t, response))
	}
	fact := response.Facts[0]
	if fact.AggregateScope != "all_owned_repositories" {
		t.Errorf("aggregate_scope = %q, want all_owned_repositories", fact.AggregateScope)
	}
	for _, name := range []string{"throughput_mean", "throughput_stddev", "backlog_size", "forecast_p50_days"} {
		if _, served := fact.Fields[name]; !served {
			t.Errorf("%s not served", name)
		}
		if !containsString(fact.AggregateFields, name) {
			t.Errorf("%s not labelled aggregate: %v", name, fact.AggregateFields)
		}
	}
	encoded := mustJSON(t, response)
	for _, leak := range []string{"acme/b", "repository:b", "acme/a", "acr:v1:repository"} {
		if strings.Contains(encoded, leak) {
			t.Errorf("response names a repository (%q): %s", leak, encoded)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

var _ = contractsv1.ContextFabricSubjectTeam
