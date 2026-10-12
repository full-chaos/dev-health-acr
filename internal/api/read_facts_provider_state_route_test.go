package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

type classifyingRouteProvider struct{}

func (classifyingRouteProvider) Capability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: contextfabric.FactOperationalDeficiencies, Name: "deficiencies_route_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectTeam},
		RequiresEvidence:      true,
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "rule_id", Type: contextfabric.FactFieldString}},
	}
}

func (classifyingRouteProvider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Evaluation: &contextfabric.FactEvaluationCoverage{}}
	rule := "r1"
	for _, team := range query.Subjects {
		state := "never_evaluated"
		if team.CanonicalID == "team:fired" {
			state = "fired"
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactOperationalDeficiencies, Subject: team,
				Fields:         map[string]contextfabric.FactValue{"rule_id": {String: &rule}},
				EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "fired")},
				SourceState:    contextfabric.SourceAvailable,
			})
		}
		result.Evaluation.Members = append(result.Evaluation.Members, contextfabric.FactEvaluationMember{Subject: team, State: state})
	}
	return result, nil
}

func TestFactsRouteServesProviderStatesInsideTheClosedOutputSchema(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7073Reader(t, h, reachingGraph{}, classifyingRouteProvider{})
	response := h.postFacts(token, `{"kinds":["operational_deficiencies"],"subjects":[{"kind":"team","canonical_id":"team:fired"},{"kind":"team","canonical_id":"team:never_evaluated"}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	result, err := gojsonschema.Validate(
		gojsonschema.NewReferenceLoader("file://"+repopath.Path(t, "internal", "mcp", "schemas", "mcp_read_facts_response.v1.schema.json")),
		gojsonschema.NewBytesLoader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range result.Errors() {
		t.Errorf("served JSON violates the MCP output schema: %s", problem)
	}
	var document directread.FactsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, row := range document.Coverage {
		got[row.Subject.CanonicalID] = [2]string{row.Outcome, row.ProviderState}
	}
	want := map[string][2]string{
		"team:fired":           {directread.OutcomeFactServed, "fired"},
		"team:never_evaluated": {directread.OutcomeReadNoFact, "never_evaluated"},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: served (outcome, provider_state) = %v, want %v; body %s", id, got[id], w, response.Body.String())
		}
	}
}
